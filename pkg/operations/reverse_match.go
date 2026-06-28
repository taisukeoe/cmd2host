// reverse_match.go resolves a raw container-side argv invocation back to one
// of a project's allowed operation templates so the daemon can dispatch
// raw-argv mode requests through the same validation / sanitization path as
// explicit MCP operation requests.
//
// The algorithm stands on three pillars:
//
//  1. Injection-only placeholder skip — template tokens whose whole-arg
//     placeholder names a per-request injection value (repo / repo_path /
//     expected_git_url) never appear in user argv. They are dropped from the
//     effective template along with any immediately preceding flag literal,
//     mirroring BuildArgs' paired-drop convention. Inline occurrences inside
//     larger template tokens (e.g. "repos/{repo}/pulls/{number}/comments")
//     are handled in pillar 2 by literal substitution from the injection map.
//
//  2. Inline placeholder reversal — each remaining template token is
//     compiled into an anchored regex. Literal text is regex-escaped;
//     "{name}" placeholders for user params become capture groups whose
//     pattern derives from the param schema (integer → "\d+", otherwise a
//     non-greedy ".+?" fallback). Whole-arg placeholders bind a single
//     positional via schema typing.
//
//  3. Flag normalization — "--flag value" two-token pairs in the flag tail
//     (the argv suffix that remains after the template positions are
//     consumed) are rewritten to "--flag=value" so the downstream
//     ValidateFlags check, which rejects the separate form, sees the
//     canonical form users typically type. Value-bearing flags are
//     identified per candidate from its allowed_flags list, so the rewrite
//     is scoped to the candidate under consideration.
//
// Template literal flags (e.g. the "--body" before "{body}" or the "-R"
// before "{repo}") are matched 1-to-1 against the corresponding argv token
// rather than skipped — the user is expected to write the template's flag
// in the same token position as the template (raw-argv mode is template-
// shape-preserving). Reordering flags from the template form into a
// different position is not supported in v1.
//
// v1 limitation — optional placeholder paired-drop is NOT supported on the
// reverse-match side. BuildArgs drops `--body {body}` when {body} is
// missing, but reverse-match only tries the single fully-present template
// shape. A user-typed `gh pr edit 42` (no `--body`) therefore reports
// "no allowed operation matches argv" rather than dispatching to
// `gh_pr_edit` with body omitted. Workaround: pass an empty value
// explicitly (`gh pr edit 42 --body ""`). Supporting paired-drop would
// require trying 2^N effective-template shapes per candidate (one per
// optional placeholder subset), which inflates ambiguity surface and is
// deferred. Only `gh_pr_edit.body` is affected in the bundled templates.
//
// Ambiguity (more than one candidate matches) and unknown argv (zero
// candidates match) both surface as errors — the daemon fails loud rather
// than guessing.
package operations

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// errSchemaMissing and errUnsupportedType are sentinel errors that
// distinguish template-configuration problems from ordinary user-argv
// mismatches inside the reverse-match path. Both kinds surface as `error`
// from bindParam / compileInlineTemplateRegex; the caller (tryMatchCandidate)
// uses errors.Is to bubble misconfiguration up so reverse-match fails loud
// with an operator-actionable diagnostic, instead of silently collapsing to
// "no allowed operation matches argv". User-argv mismatches (integer parse
// failure, repeated-placeholder values disagreeing) keep returning plain
// errors so the candidate is treated as a non-match.
var (
	errSchemaMissing   = errors.New("reverse-match: placeholder has no schema in op.Params")
	errUnsupportedType = errors.New("reverse-match: placeholder has unsupported reverse-match type")
)

// isTemplateMisconfig reports whether err is a sentinel that names an
// operator-fixable template defect (as opposed to a user-argv mismatch).
func isTemplateMisconfig(err error) bool {
	return errors.Is(err, errSchemaMissing) || errors.Is(err, errUnsupportedType)
}

// InjectionOnlyParams is the set of placeholder names that the daemon
// injects from per-request execution context (see pkg/daemon/server.go
// handleOperationRequest). Reverse-match treats these specially: whole-arg
// occurrences are skipped from the effective template, and inline
// occurrences are substituted with the daemon-known value before regex
// compilation.
var InjectionOnlyParams = map[string]bool{
	"repo":             true,
	"repo_path":        true,
	"expected_git_url": true,
}

// CandidateOp pairs an allowed operation ID with its Operation definition.
// Callers pass the project's AllowedOperations in declaration order so the
// ambiguity error message lists candidates deterministically.
type CandidateOp struct {
	ID        string
	Operation *Operation
}

// ResolvedRequest carries the operation_id and typed params/flags that
// ReverseMatch extracted from a raw argv invocation. The daemon copies these
// into operations.Request before invoking the shared dispatch path.
type ResolvedRequest struct {
	OperationID string
	Params      map[string]ParamValue
	Flags       []string
}

// ReverseMatch resolves (command, argv) against the project's allowed
// operations.
//
//   - injection MUST carry the per-request values for the injection-only
//     placeholders the templates may reference. Missing entries cause inline
//     reversal to fail loud on candidates that need them.
//   - candidates SHOULD be in project.AllowedOperations declaration order.
//   - argv is the raw, un-normalized argv as the wrapper received it from
//     the container caller. ReverseMatch applies flag normalization per
//     candidate; callers do not pre-normalize.
//
// Returns ResolvedRequest on success; error when zero or multiple candidates
// match. Both cases are caller-visible diagnostics — the wrapper surfaces
// them on stderr so the user can adjust their invocation or project config.
func ReverseMatch(command string, argv []string, candidates []CandidateOp, injection map[string]string) (*ResolvedRequest, error) {
	if command == "" {
		return nil, fmt.Errorf("cmd2host: empty command in raw-argv request")
	}

	commandCandidates := filterByCommand(command, candidates)
	if len(commandCandidates) == 0 {
		return nil, fmt.Errorf("cmd2host: no allowed operation matches command %q", command)
	}

	type match struct {
		id     string
		params map[string]ParamValue
		flags  []string
	}
	var matches []match
	for _, c := range commandCandidates {
		params, flags, ok, terr := tryMatchCandidate(c.Operation, argv, injection)
		if terr != nil {
			// Template-level error (malformed placeholder, missing
			// injection value): bubble up so the operator can fix the
			// template. Stops further candidate evaluation.
			return nil, fmt.Errorf("cmd2host: reverse-match against operation %q failed: %w", c.ID, terr)
		}
		if !ok {
			continue
		}
		matches = append(matches, match{id: c.ID, params: params, flags: flags})
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("cmd2host: no allowed operation matches argv %q", strings.Join(append([]string{command}, argv...), " "))
	case 1:
		return &ResolvedRequest{
			OperationID: matches[0].id,
			Params:      matches[0].params,
			Flags:       matches[0].flags,
		}, nil
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.id
		}
		return nil, fmt.Errorf("cmd2host: argv resolves to multiple allowed operations: %s (cmd2host requires unambiguous templates; refine project config)", strings.Join(ids, ", "))
	}
}

// filterByCommand keeps candidates whose Operation.Command basename matches
// the requested command's basename. Both sides are basename-normalized so
// the match holds regardless of whether the caller (typically the
// container-side wrapper) sends "gh" or "/usr/bin/gh", and regardless of
// whether ResolveOperationCommands has rewritten the template Command to
// an absolute path.
func filterByCommand(command string, candidates []CandidateOp) []CandidateOp {
	want := filepath.Base(command)
	var out []CandidateOp
	for _, c := range candidates {
		if c.Operation == nil {
			continue
		}
		if filepath.Base(c.Operation.Command) == want {
			out = append(out, c)
		}
	}
	return out
}

// NormalizeFlagTail rewrites "--flag value" two-token pairs into
// "--flag=value" one-token form when the flag name is in the candidate's
// value-bearing allow list (allowedFlags minus boolFlags) and the
// following token does not itself start with "-". Boolean flags
// (presence-only switches such as `--draft`) and unrecognized
// flag-shaped tokens pass through verbatim. Operates only on the flag
// tail produced by the per-candidate template walk, so template-literal
// flags (which were matched 1:1 against the template) are not in scope.
//
// boolFlags MUST be a subset of allowedFlags. Entries in boolFlags that
// are not also in allowedFlags are ignored (they cannot reach this code
// path anyway — ValidateFlags would reject them).
func NormalizeFlagTail(tail, allowedFlags, boolFlags []string) []string {
	if len(tail) == 0 {
		return tail
	}
	boolSet := make(map[string]struct{}, len(boolFlags))
	for _, f := range boolFlags {
		boolSet[f] = struct{}{}
	}
	valueBearing := make(map[string]struct{}, len(allowedFlags))
	for _, f := range allowedFlags {
		if _, isBool := boolSet[f]; isBool {
			continue
		}
		valueBearing[f] = struct{}{}
	}

	out := make([]string, 0, len(tail))
	for i := 0; i < len(tail); i++ {
		tok := tail[i]
		if !strings.HasPrefix(tok, "--") || strings.Contains(tok, "=") {
			out = append(out, tok)
			continue
		}
		if _, ok := valueBearing[tok]; !ok {
			out = append(out, tok)
			continue
		}
		if i+1 >= len(tail) || strings.HasPrefix(tail[i+1], "-") {
			out = append(out, tok)
			continue
		}
		out = append(out, tok+"="+tail[i+1])
		i++
	}
	return out
}

// tryMatchCandidate attempts to bind argv against op's template.
// Template tokens are walked 1-to-1 against argv prefix; any remaining argv
// tokens form the flag tail and must satisfy op.AllowedFlags after flag
// normalization.
//
// Returns (params, flags, true, nil) on match, (nil, nil, false, nil) on
// mismatch (caller continues to next candidate), and (nil, nil, false, err)
// on a template-level configuration error that should fail loud.
func tryMatchCandidate(op *Operation, argv []string, injection map[string]string) (map[string]ParamValue, []string, bool, error) {
	effective := effectiveTemplate(op.ArgsTemplate)

	if len(argv) < len(effective) {
		return nil, nil, false, nil
	}

	params := make(map[string]ParamValue)
	for i, tmpl := range effective {
		pos := argv[i]

		switch {
		case isWholeArgPlaceholder(tmpl):
			name := tmpl[1 : len(tmpl)-1]
			if InjectionOnlyParams[name] {
				return nil, nil, false, fmt.Errorf("internal: injection-only placeholder %q reached match step", name)
			}
			if err := bindParam(op, name, pos, params); err != nil {
				if isTemplateMisconfig(err) {
					return nil, nil, false, err
				}
				return nil, nil, false, nil
			}

		case strings.ContainsAny(tmpl, "{}"):
			re, captureNames, err := compileInlineTemplateRegex(tmpl, op.Params, injection)
			if err != nil {
				return nil, nil, false, err
			}
			m := re.FindStringSubmatch(pos)
			if m == nil {
				return nil, nil, false, nil
			}
			for j, name := range captureNames {
				if name == "" {
					continue
				}
				if err := bindParam(op, name, m[j+1], params); err != nil {
					if isTemplateMisconfig(err) {
						return nil, nil, false, err
					}
					return nil, nil, false, nil
				}
			}

		default:
			if tmpl != pos {
				return nil, nil, false, nil
			}
		}
	}

	flagTail := argv[len(effective):]
	flags := NormalizeFlagTail(flagTail, op.AllowedFlags, op.BoolFlags)

	// Flag validation: ValidateOperation re-runs this later, but rejecting
	// here treats out-of-policy flags as "this candidate does not match"
	// rather than failing the whole request. With ambiguous templates this
	// can change the rejection candidate set, but unique templates produce
	// the same outcome either way.
	if err := op.ValidateFlags(flags); err != nil {
		return nil, nil, false, nil
	}

	return params, flags, true, nil
}

// effectiveTemplate drops whole-arg injection-only placeholders and any
// immediately preceding flag literal (mirroring BuildArgs' paired-drop).
// Inline placeholders inside non-whole-arg tokens are NOT removed here;
// they are handled by compileInlineTemplateRegex via literal substitution.
func effectiveTemplate(tmpl []string) []string {
	skip := make(map[int]bool, len(tmpl))
	for i, t := range tmpl {
		if !isWholeArgPlaceholder(t) {
			continue
		}
		name := t[1 : len(t)-1]
		if !InjectionOnlyParams[name] {
			continue
		}
		skip[i] = true
		if i > 0 && isFlagLiteral(tmpl[i-1]) {
			skip[i-1] = true
		}
	}
	out := make([]string, 0, len(tmpl))
	for i, t := range tmpl {
		if skip[i] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// bindParam stores a captured value under name with the schema's typing.
// When the same name appears twice in a template (e.g. git_push's
// "{branch}:refs/heads/{branch}"), the second bind must produce the same
// value as the first; otherwise the candidate is rejected so the daemon
// does not silently rewrite mismatched user input via BuildArgs (which
// only renders one canonical value per parameter).
//
// Returns a non-nil error when the raw value cannot be coerced to the
// declared type or conflicts with a prior bind. The caller treats any
// error as "candidate did not match" so reverse-match continues to the
// next candidate or reports zero matches.
func bindParam(op *Operation, name, raw string, params map[string]ParamValue) error {
	schema, hasSchema := op.Params[name]
	if !hasSchema {
		return fmt.Errorf("%w: placeholder %q", errSchemaMissing, name)
	}
	var newVal ParamValue
	switch schema.Type {
	case "integer":
		n, err := strconv.Atoi(raw)
		if err != nil {
			return err
		}
		newVal = n
	case "string", "":
		newVal = raw
	default:
		return fmt.Errorf("%w: placeholder %q has type %q", errUnsupportedType, name, schema.Type)
	}
	if existing, ok := params[name]; ok {
		if existing != newVal {
			return fmt.Errorf("placeholder %q bound twice with mismatched values (%v vs %v)", name, existing, newVal)
		}
		return nil
	}
	params[name] = newVal
	return nil
}

// compileInlineTemplateRegex builds an anchored regex matching the entire
// template token. Injection-only placeholders are substituted with their
// daemon-known value (regex-escaped). User placeholders become capture
// groups whose inner pattern is chosen from the param schema. The "(?s)"
// flag is set so multi-line values (e.g. a PR comment body) can be captured
// inside a single positional token.
//
// Returns the compiled regex and, for each capture group in order, the
// param name it binds.
func compileInlineTemplateRegex(tmpl string, schemas map[string]ParamSchema, injection map[string]string) (*regexp.Regexp, []string, error) {
	var b strings.Builder
	b.WriteString("(?s)^")
	var captureNames []string

	i := 0
	for i < len(tmpl) {
		ch := tmpl[i]
		if ch != '{' {
			b.WriteString(regexp.QuoteMeta(string(ch)))
			i++
			continue
		}
		end := strings.IndexByte(tmpl[i:], '}')
		if end == -1 {
			return nil, nil, fmt.Errorf("unterminated placeholder in template token %q", tmpl)
		}
		name := tmpl[i+1 : i+end]
		if name == "" {
			return nil, nil, fmt.Errorf("empty placeholder in template token %q", tmpl)
		}
		if InjectionOnlyParams[name] {
			val, ok := injection[name]
			if !ok || val == "" {
				return nil, nil, fmt.Errorf("missing injection value for placeholder %q", name)
			}
			b.WriteString(regexp.QuoteMeta(val))
		} else {
			// Fail loud here when an inline placeholder names a param
			// that the op did not declare. The fallback ".+?" would
			// otherwise let the regex match opportunistically, and the
			// downstream bindParam would convert the misconfig into a
			// candidate mismatch — hiding the operator-fixable defect
			// behind a generic "no allowed operation matches argv".
			schema, hasSchema := schemas[name]
			if !hasSchema {
				return nil, nil, fmt.Errorf("%w: placeholder %q in template token %q", errSchemaMissing, name, tmpl)
			}
			pattern := capturePatternForSchema(schema)
			b.WriteString("(")
			b.WriteString(pattern)
			b.WriteString(")")
			captureNames = append(captureNames, name)
		}
		i += end + 1
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, nil, fmt.Errorf("compile reverse-match regex for token %q: %w", tmpl, err)
	}
	return re, captureNames, nil
}

// capturePatternForSchema picks the inner regex for an inline placeholder
// capture group. The fallback is non-greedy ".+?" so a trailing literal
// (e.g. the "/comments" suffix in "repos/{repo}/pulls/{number}/comments")
// can pin the right boundary. Integer params use "\d+" for stricter typing.
func capturePatternForSchema(schema ParamSchema) string {
	if schema.Type == "integer" {
		return `\d+`
	}
	return `.+?`
}
