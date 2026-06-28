package operations

import (
	"strings"
	"testing"
)

// gitGithubWriteCandidates mirrors the 13-op git_github_write.json template
// in code form so the reverse-match suite exercises the same surface the
// daemon dispatches in production. New ops added to the JSON template
// should be added here too.
func gitGithubWriteCandidates(t *testing.T) []CandidateOp {
	t.Helper()
	ops := []struct {
		id   string
		op   *Operation
	}{
		{"git_fetch", &Operation{
			Command:      "git",
			ArgsTemplate: []string{"fetch", "origin"},
			Params:       map[string]ParamSchema{},
		}},
		{"git_push", &Operation{
			Command:      "git",
			ArgsTemplate: []string{"push", "--no-verify", "{expected_git_url}", "{branch}:refs/heads/{branch}"},
			Params: map[string]ParamSchema{
				"branch": {Type: "string", Pattern: `^[a-zA-Z0-9._/-]+$`, MinLength: 1, MaxLength: 255},
			},
		}},
		{"gh_pr_view", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"pr", "view", "{number}", "-R", "{repo}"},
			Params: map[string]ParamSchema{
				"number": {Type: "integer", Min: intPtr(1)},
			},
			AllowedFlags: []string{"--json", "--comments"},
		}},
		{"gh_pr_list", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"pr", "list", "-R", "{repo}"},
			Params:       map[string]ParamSchema{},
			AllowedFlags: []string{"--json", "--state", "--limit", "--author", "--label", "--base", "--head"},
		}},
		{"gh_pr_checks", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"pr", "checks", "{number}", "-R", "{repo}"},
			Params: map[string]ParamSchema{
				"number": {Type: "integer", Min: intPtr(1)},
			},
			AllowedFlags: []string{"--json", "--required"},
		}},
		{"gh_pr_review_comments", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"api", "repos/{repo}/pulls/{number}/comments"},
			Params: map[string]ParamSchema{
				"number": {Type: "integer", Min: intPtr(1)},
			},
			AllowedFlags: []string{"--paginate", "--jq"},
		}},
		{"gh_pr_create", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"pr", "create", "-R", "{repo}", "--body", "{body}"},
			Params: map[string]ParamSchema{
				"body": {Type: "string", MinLength: 1, MaxLength: 65535, Pattern: `\S`},
			},
			AllowedFlags: []string{"--title", "--base", "--head", "--draft", "--label", "--assignee", "--reviewer"},
		}},
		{"gh_pr_edit", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"pr", "edit", "{number}", "-R", "{repo}", "--body", "{body}"},
			Params: map[string]ParamSchema{
				"number": {Type: "integer", Min: intPtr(1)},
				"body":   {Type: "string", Optional: true, MaxLength: 65535},
			},
			AllowedFlags: []string{"--title", "--add-label", "--remove-label", "--add-assignee", "--remove-assignee"},
		}},
		{"gh_pr_comment", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"pr", "comment", "{number}", "-R", "{repo}", "--body", "{body}"},
			Params: map[string]ParamSchema{
				"number": {Type: "integer", Min: intPtr(1)},
				"body":   {Type: "string", MinLength: 1, MaxLength: 65535, Pattern: `\S`},
			},
		}},
		{"gh_pr_review_comment_reply", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"api", "-X", "POST", "repos/{repo}/pulls/{number}/comments/{comment_id}/replies", "-f", "body={body}"},
			Params: map[string]ParamSchema{
				"number":     {Type: "integer", Min: intPtr(1)},
				"comment_id": {Type: "integer", Min: intPtr(1)},
				"body":       {Type: "string", MinLength: 1, MaxLength: 65535, Pattern: `\S`},
			},
		}},
		{"gh_issue_view", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"issue", "view", "{number}", "-R", "{repo}"},
			Params: map[string]ParamSchema{
				"number": {Type: "integer", Min: intPtr(1)},
			},
			AllowedFlags: []string{"--json", "--comments"},
		}},
		{"gh_issue_list", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"issue", "list", "-R", "{repo}"},
			Params:       map[string]ParamSchema{},
			AllowedFlags: []string{"--json", "--state", "--limit", "--author", "--label", "--assignee"},
		}},
		{"gh_run_view", &Operation{
			Command:      "gh",
			ArgsTemplate: []string{"run", "view", "{run_id}", "-R", "{repo}"},
			Params: map[string]ParamSchema{
				"run_id": {Type: "integer", Min: intPtr(1)},
			},
			AllowedFlags: []string{"--json", "--log", "--log-failed", "--exit-status", "--job"},
		}},
	}
	out := make([]CandidateOp, 0, len(ops))
	for _, o := range ops {
		if err := o.op.CompilePatterns(); err != nil {
			t.Fatalf("CompilePatterns(%s): %v", o.id, err)
		}
		out = append(out, CandidateOp{ID: o.id, Operation: o.op})
	}
	return out
}

var stdInjection = map[string]string{
	"repo":             "owner/repo",
	"repo_path":        "/srv/work/owner/repo",
	"expected_git_url": "git@github.com:owner/repo.git",
}

func TestReverseMatch_TemplateOps(t *testing.T) {
	candidates := gitGithubWriteCandidates(t)

	tests := []struct {
		name         string
		command      string
		argv         []string
		wantOpID     string
		wantParams   map[string]ParamValue
		wantFlags    []string
		wantMatchErr bool
	}{
		{
			name:     "git_fetch matches git fetch origin",
			command:  "git",
			argv:     []string{"fetch", "origin"},
			wantOpID: "git_fetch",
		},
		{
			name:       "git_push matches git push <branch>:refs/heads/<branch>",
			command:    "git",
			argv:       []string{"push", "main:refs/heads/main"},
			wantOpID:   "git_push",
			wantParams: map[string]ParamValue{"branch": "main"},
		},
		{
			name:       "gh_pr_view by integer positional",
			command:    "gh",
			argv:       []string{"pr", "view", "42"},
			wantOpID:   "gh_pr_view",
			wantParams: map[string]ParamValue{"number": 42},
		},
		{
			name:       "gh_pr_view with allowed boolean flag",
			command:    "gh",
			argv:       []string{"pr", "view", "42", "--comments"},
			wantOpID:   "gh_pr_view",
			wantParams: map[string]ParamValue{"number": 42},
			wantFlags:  []string{"--comments"},
		},
		{
			name:      "gh_pr_list with separate --json value normalized",
			command:   "gh",
			argv:      []string{"pr", "list", "--json", "title,state"},
			wantOpID:  "gh_pr_list",
			wantFlags: []string{"--json=title,state"},
		},
		{
			name:      "gh_pr_list with --json=... already canonical",
			command:   "gh",
			argv:      []string{"pr", "list", "--json=title,state", "--state=open"},
			wantOpID:  "gh_pr_list",
			wantFlags: []string{"--json=title,state", "--state=open"},
		},
		{
			name:       "gh_pr_checks by integer positional with allowed flag",
			command:    "gh",
			argv:       []string{"pr", "checks", "42", "--required"},
			wantOpID:   "gh_pr_checks",
			wantParams: map[string]ParamValue{"number": 42},
			wantFlags:  []string{"--required"},
		},
		{
			name:       "gh_pr_review_comments via inline placeholder reversal with repo substitution",
			command:    "gh",
			argv:       []string{"api", "repos/owner/repo/pulls/42/comments"},
			wantOpID:   "gh_pr_review_comments",
			wantParams: map[string]ParamValue{"number": 42},
		},
		{
			name:       "gh_pr_create body whole-arg",
			command:    "gh",
			argv:       []string{"pr", "create", "--body", "single line body"},
			wantOpID:   "gh_pr_create",
			wantParams: map[string]ParamValue{"body": "single line body"},
		},
		{
			name:       "gh_pr_create body multi-line",
			command:    "gh",
			argv:       []string{"pr", "create", "--body", "line one\nline two\nline three"},
			wantOpID:   "gh_pr_create",
			wantParams: map[string]ParamValue{"body": "line one\nline two\nline three"},
		},
		{
			name:       "gh_pr_create with allowed --title flag (separate value)",
			command:    "gh",
			argv:       []string{"pr", "create", "--body", "body text", "--title", "PR title"},
			wantOpID:   "gh_pr_create",
			wantParams: map[string]ParamValue{"body": "body text"},
			wantFlags:  []string{"--title=PR title"},
		},
		{
			name:       "gh_pr_edit with body provided",
			command:    "gh",
			argv:       []string{"pr", "edit", "42", "--body", "new body"},
			wantOpID:   "gh_pr_edit",
			wantParams: map[string]ParamValue{"number": 42, "body": "new body"},
		},
		{
			name:       "gh_pr_comment body whole-arg",
			command:    "gh",
			argv:       []string{"pr", "comment", "42", "--body", "comment body"},
			wantOpID:   "gh_pr_comment",
			wantParams: map[string]ParamValue{"number": 42, "body": "comment body"},
		},
		{
			name:       "gh_pr_review_comment_reply multi-injection inline + literal body=value",
			command:    "gh",
			argv:       []string{"api", "-X", "POST", "repos/owner/repo/pulls/42/comments/1234/replies", "-f", "body=hello world"},
			wantOpID:   "gh_pr_review_comment_reply",
			wantParams: map[string]ParamValue{"number": 42, "comment_id": 1234, "body": "hello world"},
		},
		{
			name:       "gh_issue_view by integer positional",
			command:    "gh",
			argv:       []string{"issue", "view", "7"},
			wantOpID:   "gh_issue_view",
			wantParams: map[string]ParamValue{"number": 7},
		},
		{
			name:     "gh_issue_list no positionals",
			command:  "gh",
			argv:     []string{"issue", "list"},
			wantOpID: "gh_issue_list",
		},
		{
			name:       "gh_run_view by run_id",
			command:    "gh",
			argv:       []string{"run", "view", "100"},
			wantOpID:   "gh_run_view",
			wantParams: map[string]ParamValue{"run_id": 100},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReverseMatch(tt.command, tt.argv, candidates, stdInjection)
			if (err != nil) != tt.wantMatchErr {
				t.Fatalf("ReverseMatch err = %v, wantErr=%v", err, tt.wantMatchErr)
			}
			if tt.wantMatchErr {
				return
			}
			if got.OperationID != tt.wantOpID {
				t.Errorf("operation_id = %q, want %q", got.OperationID, tt.wantOpID)
			}
			if !paramsEqual(got.Params, tt.wantParams) {
				t.Errorf("params = %v, want %v", got.Params, tt.wantParams)
			}
			if !stringSliceEqual(got.Flags, tt.wantFlags) {
				t.Errorf("flags = %v, want %v", got.Flags, tt.wantFlags)
			}
		})
	}
}

func TestReverseMatch_RejectsBadInputs(t *testing.T) {
	candidates := gitGithubWriteCandidates(t)

	tests := []struct {
		name      string
		command   string
		argv      []string
		errSubstr string
	}{
		{
			name:      "empty command",
			command:   "",
			argv:      []string{"pr", "view", "42"},
			errSubstr: "empty command",
		},
		{
			name:      "command outside allowed set",
			command:   "aws",
			argv:      []string{"s3", "ls"},
			errSubstr: "no allowed operation matches command",
		},
		{
			name:      "argv does not match any gh op",
			command:   "gh",
			argv:      []string{"repo", "view", "owner/repo"},
			errSubstr: "no allowed operation matches argv",
		},
		{
			name:      "gh_pr_view number is non-integer",
			command:   "gh",
			argv:      []string{"pr", "view", "abc"},
			errSubstr: "no allowed operation matches argv",
		},
		{
			name:      "gh_pr_view rejects disallowed --format flag",
			command:   "gh",
			argv:      []string{"pr", "view", "42", "--format=json"},
			errSubstr: "no allowed operation matches argv",
		},
		{
			name:      "git_push branch missing refspec form is not a template match",
			command:   "git",
			argv:      []string{"push", "origin", "main"},
			errSubstr: "no allowed operation matches argv",
		},
		{
			name:      "gh api with unknown sub-path is not a template match",
			command:   "gh",
			argv:      []string{"api", "repos/owner/repo/issues"},
			errSubstr: "no allowed operation matches argv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReverseMatch(tt.command, tt.argv, candidates, stdInjection)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.errSubstr)
			}
		})
	}
}

func TestReverseMatch_AmbiguousFailsLoud(t *testing.T) {
	// Two ops with identical effective templates: reverse-match cannot pick.
	opA := &Operation{
		Command:      "gh",
		ArgsTemplate: []string{"pr", "view", "{number}"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Min: intPtr(1)},
		},
	}
	opB := &Operation{
		Command:      "gh",
		ArgsTemplate: []string{"pr", "view", "{number}"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Min: intPtr(1)},
		},
	}
	candidates := []CandidateOp{
		{ID: "first_op", Operation: opA},
		{ID: "second_op", Operation: opB},
	}
	_, err := ReverseMatch("gh", []string{"pr", "view", "42"}, candidates, stdInjection)
	if err == nil {
		t.Fatalf("expected ambiguous error, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error %q does not signal ambiguity", err.Error())
	}
	if !strings.Contains(err.Error(), "first_op") || !strings.Contains(err.Error(), "second_op") {
		t.Errorf("error %q does not list both candidate IDs", err.Error())
	}
}

func TestReverseMatch_InjectionMismatchIsNotMatch(t *testing.T) {
	// gh_pr_review_comments needs the inline {repo} to substitute exactly
	// to "owner/repo". A request for a different repo path must not match.
	candidates := gitGithubWriteCandidates(t)

	_, err := ReverseMatch(
		"gh",
		[]string{"api", "repos/other/team/pulls/42/comments"},
		candidates,
		stdInjection, // repo = "owner/repo"
	)
	if err == nil {
		t.Fatalf("expected no-match error when repo segment differs from injection, got nil")
	}
}

func TestNormalizeFlagTail(t *testing.T) {
	tests := []struct {
		name         string
		tail         []string
		allowedFlags []string
		want         []string
	}{
		{
			name:         "no-op when empty",
			tail:         nil,
			allowedFlags: []string{"--json"},
			want:         nil,
		},
		{
			name:         "joins --flag value when allowed",
			tail:         []string{"--state", "open"},
			allowedFlags: []string{"--state"},
			want:         []string{"--state=open"},
		},
		{
			name:         "leaves already-joined flag alone",
			tail:         []string{"--state=open"},
			allowedFlags: []string{"--state"},
			want:         []string{"--state=open"},
		},
		{
			name:         "leaves boolean flag alone (next token is also a flag)",
			tail:         []string{"--draft", "--state", "open"},
			allowedFlags: []string{"--draft", "--state"},
			want:         []string{"--draft", "--state=open"},
		},
		{
			name:         "passes through unknown flag verbatim (validator rejects later)",
			tail:         []string{"--unknown", "value"},
			allowedFlags: []string{"--state"},
			want:         []string{"--unknown", "value"},
		},
		{
			name:         "does not join when next token starts with -",
			tail:         []string{"--state", "--limit", "5"},
			allowedFlags: []string{"--state", "--limit"},
			want:         []string{"--state", "--limit=5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeFlagTail(tt.tail, tt.allowedFlags)
			if !stringSliceEqual(got, tt.want) {
				t.Errorf("NormalizeFlagTail = %v, want %v", got, tt.want)
			}
		})
	}
}

func paramsEqual(a, b map[string]ParamValue) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		// Treat int and float64 (JSON) equivalence; reverse-match emits int.
		switch av := va.(type) {
		case int:
			if bv, ok := vb.(int); !ok || av != bv {
				return false
			}
		case string:
			if bv, ok := vb.(string); !ok || av != bv {
				return false
			}
		default:
			if va != vb {
				return false
			}
		}
	}
	return true
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
