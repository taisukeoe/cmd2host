// Package operations provides operation template definitions, typed
// request/response types, and parameter handling. Operations define
// predefined command patterns with typed parameters.
package operations

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var inlinePlaceholderPattern = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// Operation defines a predefined command template
type Operation struct {
	Command      string                 `json:"command"`       // e.g., "gh", "git"
	ArgsTemplate []string               `json:"args_template"` // e.g., ["pr", "view", "{number}"]
	Params       map[string]ParamSchema `json:"params"`        // Parameter schemas
	AllowedFlags []string               `json:"allowed_flags"` // e.g., ["--state", "--limit"]
	// BoolFlags lists entries from AllowedFlags that take no value
	// (presence-only switches such as `--draft`, `--required`,
	// `--exit-status`). The raw-argv reverse-match's flag-tail
	// normalizer consults this list so it does not splice the next argv
	// token into a boolean switch (e.g. `gh pr create --draft "title"`
	// must reach the host as two tokens, not `--draft=title`).
	BoolFlags   []string `json:"bool_flags,omitempty"`
	Description string   `json:"description"` // Human-readable description
}

// ItemsSchema defines the schema for array items
type ItemsSchema struct {
	Type string `json:"type"`
}

// ParamSchema defines validation rules for a parameter
type ParamSchema struct {
	Type      string       `json:"type"`                // "string", "integer", "array"
	Optional  bool         `json:"optional,omitempty"`  // If true, parameter can be omitted
	Pattern   string       `json:"pattern,omitempty"`   // Regex pattern for strings
	MinLength int          `json:"minLength,omitempty"` // Min length for strings
	MaxLength int          `json:"maxLength,omitempty"` // Max length for strings
	Min       *int         `json:"min,omitempty"`       // Min value for integers (pointer to distinguish unset from 0)
	Max       *int         `json:"max,omitempty"`       // Max value for integers (pointer to distinguish unset from 0)
	Items     *ItemsSchema `json:"items,omitempty"`     // For array types

	// Compiled pattern (not serialized)
	compiledPattern *regexp.Regexp
}

// ParamValue represents a parameter value that can be string, int, or []string
type ParamValue interface{}

// Request represents a request to execute an operation.
//
// Two entry shapes are supported:
//
//   - Operation entry (existing): Operation carries the resolved operation_id
//     and Params/Flags carry the typed values directly. Source is empty or
//     "mcp" depending on the caller.
//   - Raw-argv entry (additive): RawArgv carries the full [command, args...]
//     argv as the caller wrote it; the daemon resolves the operation_id and
//     extracts Params/Flags via reverse-match before dispatching through the
//     same validation / sanitization / execution path. Source is "raw_argv".
//
// Source is also surfaced in daemon log lines so operators can distinguish
// the two routes when auditing.
type Request struct {
	RequestID string                `json:"request_id,omitempty"`
	Operation string                `json:"operation,omitempty"`
	Source    string                `json:"source,omitempty"`   // "raw_argv" | "mcp" | ""
	RawArgv   []string              `json:"raw_argv,omitempty"` // [command, args...] for raw-argv entry
	Params    map[string]ParamValue `json:"params,omitempty"`
	Flags     []string              `json:"flags,omitempty"`
	Token     string                `json:"token"`
	// TargetRepo selects which repo (from the project's allow list) this
	// request acts on. Required when the project has more than one repo;
	// optional when the project has a single repo (defaults to Repos[0]).
	TargetRepo string `json:"target_repo,omitempty"`
}

// Response represents the response from an operation.
//
// Truncation indicator fields (additive, optional):
//   - StdoutTruncated / StderrTruncated: true when the corresponding stream
//     exceeded the configured cap. When the flag is true, the Stdout / Stderr
//     string holds a clean prefix of the original output cut at a UTF-8 rune
//     boundary. The daemon does not mix any synthetic marker into the stream
//     body so that streaming JSON parsers downstream see only the original
//     command output and do not have to filter trailing daemon-supplied text.
//   - StdoutOriginalBytes / StderrOriginalBytes: the byte length of the
//     original (pre-truncation) output. Populated for streams that carry
//     actual command output (success exit and exec exit-code paths). Error
//     paths that substitute a synthetic stderr message (timeout,
//     command-not-found, generic runtime error) leave these fields at zero
//     even when the corresponding stream string is non-empty. The `Original`
//     prefix distinguishes these fields from the daemon-side
//     `MaxStdoutBytes` / `MaxStderrBytes` caps defined in pkg/config.
type Response struct {
	RequestID           string  `json:"request_id,omitempty"`
	ExitCode            int     `json:"exit_code"`
	Stdout              string  `json:"stdout"`
	Stderr              string  `json:"stderr"`
	DeniedReason        *string `json:"denied_reason"`
	StdoutTruncated     bool    `json:"stdout_truncated,omitempty"`
	StderrTruncated     bool    `json:"stderr_truncated,omitempty"`
	StdoutOriginalBytes int64   `json:"stdout_original_bytes,omitempty"`
	StderrOriginalBytes int64   `json:"stderr_original_bytes,omitempty"`
}

// ListOperationsRequest requests the list of available operations
type ListOperationsRequest struct {
	ListOperations bool   `json:"list_operations"`
	Prefix         string `json:"prefix,omitempty"` // Filter by operation ID prefix (e.g., "gh", "gh_pr")
	Token          string `json:"token"`
}

// ListOperationsResponse contains the list of available operations
type ListOperationsResponse struct {
	Operations []OperationInfo `json:"operations"`
	Error      string          `json:"error,omitempty"`
}

// DescribeOperationRequest requests details about a specific operation
type DescribeOperationRequest struct {
	DescribeOperation string `json:"describe_operation"`
	Token             string `json:"token"`
}

// DescribeOperationResponse contains detailed operation info
type DescribeOperationResponse struct {
	Operation *OperationInfo `json:"operation,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// OperationInfo provides information about an operation for API responses
type OperationInfo struct {
	ID           string                 `json:"id"`
	Command      string                 `json:"command"`
	Description  string                 `json:"description"`
	Params       map[string]ParamSchema `json:"params,omitempty"`
	AllowedFlags []string               `json:"allowed_flags,omitempty"`
}

// CompilePatterns compiles regex patterns in parameter schemas
func (op *Operation) CompilePatterns() error {
	for name, schema := range op.Params {
		if schema.Pattern != "" {
			re, err := regexp.Compile(schema.Pattern)
			if err != nil {
				return fmt.Errorf("invalid pattern for param %s: %w", name, err)
			}
			schema.compiledPattern = re
			op.Params[name] = schema
		}
	}
	return nil
}

// ValidateParams validates parameters against the operation schema
func (op *Operation) ValidateParams(params map[string]ParamValue) error {
	// Check for unknown parameters
	for name := range params {
		if _, exists := op.Params[name]; !exists {
			return fmt.Errorf("unknown parameter: %s", name)
		}
	}

	// Validate each defined parameter
	for name, schema := range op.Params {
		value, exists := params[name]
		if !exists {
			// Parameter is optional if not provided (template may have defaults)
			continue
		}

		if err := validateParamValue(name, value, schema); err != nil {
			return err
		}
	}

	return nil
}

// validateParamValue validates a single parameter value against its schema
func validateParamValue(name string, value ParamValue, schema ParamSchema) error {
	switch schema.Type {
	case "string":
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("param %s: expected string, got %T", name, value)
		}
		if schema.MinLength > 0 && len(str) < schema.MinLength {
			return fmt.Errorf("param %s: length %d below minimum %d", name, len(str), schema.MinLength)
		}
		if schema.MaxLength > 0 && len(str) > schema.MaxLength {
			return fmt.Errorf("param %s: length %d exceeds maximum %d", name, len(str), schema.MaxLength)
		}
		if schema.compiledPattern != nil && !schema.compiledPattern.MatchString(str) {
			return fmt.Errorf("param %s: value does not match pattern %s", name, schema.Pattern)
		}

	case "integer":
		var intVal int
		switch v := value.(type) {
		case int:
			intVal = v
		case float64:
			intVal = int(v) // JSON unmarshals numbers as float64
		default:
			return fmt.Errorf("param %s: expected integer, got %T", name, value)
		}
		if schema.Min != nil && intVal < *schema.Min {
			return fmt.Errorf("param %s: value %d below minimum %d", name, intVal, *schema.Min)
		}
		if schema.Max != nil && intVal > *schema.Max {
			return fmt.Errorf("param %s: value %d exceeds maximum %d", name, intVal, *schema.Max)
		}

	case "array":
		arr, ok := value.([]interface{})
		if !ok {
			// Also accept []string - type already validated by Go type system
			if _, ok := value.([]string); ok {
				// ItemsSchema currently only has Type field (no Pattern)
				// For []string, items are already strings, so validation passes
				return nil
			}
			return fmt.Errorf("param %s: expected array, got %T", name, value)
		}
		// Validate []interface{} items against schema
		if schema.Items != nil && schema.Items.Type == "string" {
			for i, item := range arr {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("param %s[%d]: expected string, got %T", name, i, item)
				}
			}
		}

	default:
		return fmt.Errorf("param %s: unknown type %s", name, schema.Type)
	}

	return nil
}

// ValidateFlags validates that all provided flags are in the allowed list.
// Flags must be in --flag or --flag=value format. Separate value arguments
// (e.g., --state open) are not supported - use --state=open instead.
func (op *Operation) ValidateFlags(flags []string) error {
	if len(op.AllowedFlags) == 0 && len(flags) > 0 {
		return fmt.Errorf("no flags allowed for this operation")
	}

	allowedSet := make(map[string]bool)
	for _, f := range op.AllowedFlags {
		allowedSet[f] = true
	}

	for _, flag := range flags {
		// Every element must be a flag (start with -)
		if !strings.HasPrefix(flag, "-") {
			return fmt.Errorf("invalid flag format: %s (use --flag=value, not --flag value)", flag)
		}

		// Extract flag name (handle --flag=value)
		flagName := flag
		if idx := strings.Index(flag, "="); idx > 0 {
			flagName = flag[:idx]
		}

		if !allowedSet[flagName] {
			return fmt.Errorf("flag not allowed: %s", flagName)
		}
	}

	return nil
}

// BuildArgs builds the final argument list by expanding the template.
//
// Template language convention: standalone optional flag-value pairs.
// When a whole-argument placeholder "{name}" references a parameter
// declared with Optional: true and the parameter is not present in
// params (and not injected via profileEnv), the placeholder is dropped.
// If the immediately preceding template element is a flag literal
// (starts with "-" and contains no placeholder syntax), it is dropped
// together with the placeholder. This is what allows templates like
// `["pr", "create", "-R", "{repo}", "--body", "{body}"]` to omit both
// `--body` and `{body}` cleanly when body is not supplied.
//
// The convention applies only to whole-argument placeholders. Inline
// placeholders embedded inside literal strings (e.g. `body={body}`)
// are processed by interpolation and do not participate in paired-drop.
func (op *Operation) BuildArgs(params map[string]ParamValue, flags []string, profileEnv map[string]string) ([]string, error) {
	// First pass: identify template indices to skip for optional-placeholder paired-drop.
	skipIdx := make(map[int]bool)
	for i, tmpl := range op.ArgsTemplate {
		if !isWholeArgPlaceholder(tmpl) {
			continue
		}
		paramName := tmpl[1 : len(tmpl)-1]
		// profileEnv-injected values always resolve, so they are never paired-dropped.
		if profileEnv != nil {
			if _, ok := profileEnv[paramName]; ok {
				continue
			}
		}
		if _, exists := params[paramName]; exists {
			continue
		}
		schema, hasSchema := op.Params[paramName]
		if !hasSchema || !schema.Optional {
			continue
		}
		// Optional placeholder is missing: drop it, and the preceding flag literal if any.
		skipIdx[i] = true
		if i > 0 && isFlagLiteral(op.ArgsTemplate[i-1]) {
			skipIdx[i-1] = true
		}
	}

	var args []string

	for i, tmpl := range op.ArgsTemplate {
		if skipIdx[i] {
			continue
		}
		if isWholeArgPlaceholder(tmpl) {
			// Template placeholder
			paramName := tmpl[1 : len(tmpl)-1]

			// Check for special profile-injected values
			if profileEnv != nil {
				if val, ok := profileEnv[paramName]; ok {
					args = append(args, val)
					continue
				}
			}

			value, exists := params[paramName]
			if !exists {
				// Optional placeholders with missing values are handled by the
				// first-pass skipIdx above. Reaching here means the parameter
				// is required.
				return nil, fmt.Errorf("missing required parameter: %s", paramName)
			}

			expanded, err := expandParamValue(value)
			if err != nil {
				return nil, fmt.Errorf("param %s: %w", paramName, err)
			}
			args = append(args, expanded...)
		} else if strings.Contains(tmpl, "{") && strings.Contains(tmpl, "}") {
			expanded, err := interpolateTemplateArg(tmpl, params, profileEnv, op.Params)
			if err != nil {
				return nil, err
			}
			args = append(args, expanded)
		} else {
			// Literal argument
			args = append(args, tmpl)
		}
	}

	// Append validated flags
	args = append(args, flags...)

	return args, nil
}

// isWholeArgPlaceholder reports whether the template element is a single
// placeholder occupying the entire argument (e.g. "{body}"). Inline
// templated args like "body={body}" or "{branch}:refs/heads/{branch}" are
// not whole-arg placeholders — the inner-brace check distinguishes them
// from a true single placeholder whose name happens to contain "}...{".
func isWholeArgPlaceholder(tmpl string) bool {
	if len(tmpl) <= 2 || !strings.HasPrefix(tmpl, "{") || !strings.HasSuffix(tmpl, "}") {
		return false
	}
	return !strings.ContainsAny(tmpl[1:len(tmpl)-1], "{}")
}

// isFlagLiteral reports whether the template element is a literal flag
// argument (starts with "-", contains no placeholder syntax). Used by
// the optional-placeholder paired-drop convention; see BuildArgs.
func isFlagLiteral(tmpl string) bool {
	if !strings.HasPrefix(tmpl, "-") {
		return false
	}
	if strings.Contains(tmpl, "{") || strings.Contains(tmpl, "}") {
		return false
	}
	return true
}

func interpolateTemplateArg(tmpl string, params map[string]ParamValue, profileEnv map[string]string, schemas map[string]ParamSchema) (string, error) {
	var interpolationErr error

	result := inlinePlaceholderPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		if interpolationErr != nil {
			return ""
		}

		paramName := match[1 : len(match)-1]

		if profileEnv != nil {
			if val, ok := profileEnv[paramName]; ok {
				return val
			}
		}

		value, exists := params[paramName]
		if !exists {
			if schema, hasSchema := schemas[paramName]; hasSchema && schema.Optional {
				return ""
			}
			interpolationErr = fmt.Errorf("missing required parameter: %s", paramName)
			return ""
		}

		rendered, err := stringifyInlineParamValue(value)
		if err != nil {
			interpolationErr = fmt.Errorf("param %s: %w", paramName, err)
			return ""
		}
		return rendered
	})

	if interpolationErr != nil {
		return "", interpolationErr
	}

	return result, nil
}

func stringifyInlineParamValue(value ParamValue) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case int:
		return strconv.Itoa(v), nil
	case float64:
		if math.Trunc(v) != v {
			return "", fmt.Errorf("inline placeholder requires an integer-compatible number, got %v", v)
		}
		return strconv.Itoa(int(v)), nil
	default:
		return "", fmt.Errorf("inline placeholder requires scalar value, got %T", value)
	}
}

// expandParamValue converts a parameter value to string arguments
func expandParamValue(value ParamValue) ([]string, error) {
	switch v := value.(type) {
	case string:
		return []string{v}, nil
	case int:
		return []string{fmt.Sprintf("%d", v)}, nil
	case float64:
		return []string{fmt.Sprintf("%d", int(v))}, nil
	case []interface{}:
		var result []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			} else {
				return nil, fmt.Errorf("array item is not a string: %T", item)
			}
		}
		return result, nil
	case []string:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported value type: %T", value)
	}
}
