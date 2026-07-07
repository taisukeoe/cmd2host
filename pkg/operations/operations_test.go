package operations

import (
	"strings"
	"testing"
)

// intPtr returns a pointer to the int value
func intPtr(i int) *int {
	return &i
}

func TestOperation_ValidateParams(t *testing.T) {
	op := &Operation{
		Command:      "gh",
		ArgsTemplate: []string{"pr", "view", "{number}", "--json", "{fields}"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Min: intPtr(1)},
			"fields": {Type: "string", Pattern: "^[a-zA-Z,]+$"},
		},
	}
	if err := op.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	tests := []struct {
		name    string
		params  map[string]ParamValue
		wantErr bool
	}{
		{
			name: "valid params",
			params: map[string]ParamValue{
				"number": float64(123), // JSON unmarshals as float64
				"fields": "title,state",
			},
			wantErr: false,
		},
		{
			name: "invalid number (below min)",
			params: map[string]ParamValue{
				"number": float64(0),
				"fields": "title",
			},
			wantErr: true,
		},
		{
			name: "invalid fields (pattern mismatch)",
			params: map[string]ParamValue{
				"number": float64(1),
				"fields": "title;rm -rf /",
			},
			wantErr: true,
		},
		{
			name: "unknown parameter",
			params: map[string]ParamValue{
				"number":  float64(1),
				"fields":  "title",
				"unknown": "value",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := op.ValidateParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOperation_ValidateParams_WorkspacePath(t *testing.T) {
	op := &Operation{
		Command:      "aws",
		ArgsTemplate: []string{"s3", "cp", "{src}", "{dest}"},
		Params: map[string]ParamSchema{
			"src":  {Type: "string"},
			"dest": {Type: "workspace_path", MaxLength: 200, Pattern: "^[a-zA-Z0-9._/-]+$"},
		},
	}
	if err := op.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns failed: %v", err)
	}

	tests := []struct {
		name    string
		params  map[string]ParamValue
		wantErr bool
	}{
		{
			name:    "workspace_path accepts a relative string",
			params:  map[string]ParamValue{"src": "s3://bucket/log", "dest": "logs/out.log"},
			wantErr: false,
		},
		{
			name:    "workspace_path applies string Pattern to the relative input",
			params:  map[string]ParamValue{"src": "s3://bucket/log", "dest": "logs/out log"},
			wantErr: true, // space fails the pattern
		},
		{
			name:    "workspace_path applies MaxLength",
			params:  map[string]ParamValue{"src": "s3://bucket/log", "dest": strings.Repeat("a", 201)},
			wantErr: true,
		},
		{
			name:    "workspace_path rejects non-string",
			params:  map[string]ParamValue{"src": "s3://bucket/log", "dest": float64(3)},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := op.ValidateParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOperation_ValidateParams_Optional(t *testing.T) {
	op := &Operation{
		Params: map[string]ParamSchema{
			"number":   {Type: "integer", Min: intPtr(1), Optional: true},
			"required": {Type: "string"},
		},
	}

	tests := []struct {
		name    string
		params  map[string]ParamValue
		wantErr bool
	}{
		{
			name: "optional param provided",
			params: map[string]ParamValue{
				"number":   float64(123),
				"required": "value",
			},
			wantErr: false,
		},
		{
			name: "optional param omitted",
			params: map[string]ParamValue{
				"required": "value",
			},
			wantErr: false,
		},
		{
			name: "optional param invalid when provided",
			params: map[string]ParamValue{
				"number":   float64(0), // below min
				"required": "value",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := op.ValidateParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOperation_ValidateFlags(t *testing.T) {
	op := &Operation{
		AllowedFlags: []string{"--state", "--limit", "--json"},
	}

	tests := []struct {
		name    string
		flags   []string
		wantErr bool
	}{
		{
			name:    "allowed flags with equals format",
			flags:   []string{"--state=open", "--limit=10"},
			wantErr: false,
		},
		{
			name:    "allowed boolean flag",
			flags:   []string{"--json"},
			wantErr: false,
		},
		{
			name:    "separate value format rejected",
			flags:   []string{"--state", "open"},
			wantErr: true, // "open" is not a valid flag format
		},
		{
			name:    "disallowed flag",
			flags:   []string{"--format=json"},
			wantErr: true,
		},
		{
			name:    "empty flags",
			flags:   []string{},
			wantErr: false,
		},
		{
			name:    "bypass attempt blocked",
			flags:   []string{"--state", "--repo=evil/evil"},
			wantErr: true, // --repo is not allowed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := op.ValidateFlags(tt.flags)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOperation_ValidateFlags_MultiValue(t *testing.T) {
	tests := []struct {
		name            string
		allowedFlags    []string
		boolFlags       []string
		multiValueFlags []string
		flags           []string
		wantErr         bool
	}{
		{
			// A declared multi-value flag accepts a run of trailing bare
			// value tokens (the canonical form the normalizer produces).
			name:            "multi-value flag accepts bare value run",
			allowedFlags:    []string{"--rule-arns"},
			multiValueFlags: []string{"--rule-arns"},
			flags:           []string{"--rule-arns", "arn1", "arn2", "arn3"},
			wantErr:         false,
		},
		{
			name:            "multi-value flag accepts a single bare value",
			allowedFlags:    []string{"--rule-arns"},
			multiValueFlags: []string{"--rule-arns"},
			flags:           []string{"--rule-arns", "arn1"},
			wantErr:         false,
		},
		{
			// Boundary preserved: a bare token after a NON-multi-value flag
			// is still rejected.
			name:            "bare token after single-value flag still rejected",
			allowedFlags:    []string{"--state", "--rule-arns"},
			multiValueFlags: []string{"--rule-arns"},
			flags:           []string{"--state", "open"},
			wantErr:         true,
		},
		{
			// A bare token with no preceding multi-value flag is rejected.
			name:            "leading bare token rejected",
			allowedFlags:    []string{"--rule-arns"},
			multiValueFlags: []string{"--rule-arns"},
			flags:           []string{"stray", "--rule-arns", "arn1"},
			wantErr:         true,
		},
		{
			// The run closes at the next flag-shaped token; a following
			// single-value flag in `=` form is fine.
			name:            "run closes at next flag",
			allowedFlags:    []string{"--rule-arns", "--region"},
			multiValueFlags: []string{"--rule-arns"},
			flags:           []string{"--rule-arns", "arn1", "arn2", "--region=us-east-1"},
			wantErr:         false,
		},
		{
			// bool wins on contradiction: a flag in both bool and
			// multi-value is boolean, so it does NOT open a run and the
			// following bare token is rejected.
			name:            "bool wins: no run opened, trailing bare rejected",
			allowedFlags:    []string{"--flag"},
			boolFlags:       []string{"--flag"},
			multiValueFlags: []string{"--flag"},
			flags:           []string{"--flag", "value"},
			wantErr:         true,
		},
		{
			// At the ValidateFlags level the `=` form of a multi-value flag
			// does not open a run (the normalizer converts it to bare form
			// upstream on both routes). A trailing bare token after the `=`
			// form is therefore rejected.
			name:            "=-form does not open a run at validation level",
			allowedFlags:    []string{"--rule-arns"},
			multiValueFlags: []string{"--rule-arns"},
			flags:           []string{"--rule-arns=arn1", "arn2"},
			wantErr:         true,
		},
		{
			// A disallowed flag appearing as a bare-looking value is still
			// caught once the run closes: here the run is open so bare
			// tokens are values, but a flag-shaped token re-enters the
			// allow-list check.
			name:            "flag-shaped token in run re-enters allow list",
			allowedFlags:    []string{"--rule-arns"},
			multiValueFlags: []string{"--rule-arns"},
			flags:           []string{"--rule-arns", "arn1", "--evil=x"},
			wantErr:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := &Operation{
				AllowedFlags:    tt.allowedFlags,
				BoolFlags:       tt.boolFlags,
				MultiValueFlags: tt.multiValueFlags,
			}
			err := op.ValidateFlags(tt.flags)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFlags(%v) error = %v, wantErr %v", tt.flags, err, tt.wantErr)
			}
		})
	}
}

func TestOperation_BuildArgs(t *testing.T) {
	op := &Operation{
		ArgsTemplate: []string{"pr", "view", "{number}", "--json", "{fields}"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer"},
			"fields": {Type: "string"},
		},
	}

	params := map[string]ParamValue{
		"number": float64(123),
		"fields": "title,state",
	}
	flags := []string{"--web"}

	args, err := op.BuildArgs(params, flags, nil)
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}

	expected := []string{"pr", "view", "123", "--json", "title,state", "--web"}
	if len(args) != len(expected) {
		t.Fatalf("BuildArgs returned %d args, want %d", len(args), len(expected))
	}
	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, expected[i])
		}
	}
}

func TestOperation_BuildArgs_WithProfileEnv(t *testing.T) {
	op := &Operation{
		ArgsTemplate: []string{"-C", "{repo_path}", "add", "--", "{paths}"},
		Params: map[string]ParamSchema{
			"paths": {Type: "array", Items: &ItemsSchema{Type: "string"}},
		},
	}

	params := map[string]ParamValue{
		"paths": []interface{}{"file1.go", "file2.go"},
	}
	profileEnv := map[string]string{
		"repo_path": "/home/user/project",
	}

	args, err := op.BuildArgs(params, nil, profileEnv)
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}

	// Verify both profileEnv substitution and array expansion
	expected := []string{"-C", "/home/user/project", "add", "--", "file1.go", "file2.go"}
	if len(args) != len(expected) {
		t.Fatalf("BuildArgs returned %d args, want %d: got %v", len(args), len(expected), args)
	}
	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, expected[i])
		}
	}
}

func TestOperation_BuildArgs_OptionalParam(t *testing.T) {
	op := &Operation{
		ArgsTemplate: []string{"pr", "view", "{number}"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Optional: true},
		},
	}

	tests := []struct {
		name     string
		params   map[string]ParamValue
		expected []string
	}{
		{
			name:     "with optional param provided",
			params:   map[string]ParamValue{"number": float64(123)},
			expected: []string{"pr", "view", "123"},
		},
		{
			name:     "without optional param",
			params:   map[string]ParamValue{},
			expected: []string{"pr", "view"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := op.BuildArgs(tt.params, nil, nil)
			if err != nil {
				t.Fatalf("BuildArgs failed: %v", err)
			}
			if len(args) != len(tt.expected) {
				t.Fatalf("BuildArgs returned %d args, want %d: got %v", len(args), len(tt.expected), args)
			}
			for i, arg := range args {
				if arg != tt.expected[i] {
					t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, tt.expected[i])
				}
			}
		})
	}
}

func TestOperation_BuildArgs_WithRepoInjection(t *testing.T) {
	op := &Operation{
		ArgsTemplate: []string{"pr", "view", "{number}", "-R", "{repo}"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Min: intPtr(1)},
		},
	}

	params := map[string]ParamValue{
		"number": float64(123),
	}
	// Simulate repo injection from tokenData.Repo
	profileEnv := map[string]string{
		"repo": "owner/repo-name",
	}

	args, err := op.BuildArgs(params, nil, profileEnv)
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}

	expected := []string{"pr", "view", "123", "-R", "owner/repo-name"}
	if len(args) != len(expected) {
		t.Fatalf("BuildArgs returned %d args, want %d: got %v", len(args), len(expected), args)
	}
	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, expected[i])
		}
	}
}

func TestOperation_BuildArgs_WithRepoInjectionAndFlags(t *testing.T) {
	op := &Operation{
		ArgsTemplate: []string{"pr", "list", "-R", "{repo}"},
		Params:       map[string]ParamSchema{},
		AllowedFlags: []string{"--json", "--state"},
	}

	params := map[string]ParamValue{}
	profileEnv := map[string]string{
		"repo": "taisukeoe/cmd2host",
	}
	flags := []string{"--json", "title,state", "--state", "open"}

	args, err := op.BuildArgs(params, flags, profileEnv)
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}

	expected := []string{"pr", "list", "-R", "taisukeoe/cmd2host", "--json", "title,state", "--state", "open"}
	if len(args) != len(expected) {
		t.Fatalf("BuildArgs returned %d args, want %d: got %v", len(args), len(expected), args)
	}
	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, expected[i])
		}
	}
}

func TestOperation_BuildArgs_WithInlinePlaceholders(t *testing.T) {
	op := &Operation{
		ArgsTemplate: []string{"api", "repos/{repo}/pulls/{number}/comments"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Min: intPtr(1)},
		},
	}

	params := map[string]ParamValue{
		"number": float64(82),
	}
	profileEnv := map[string]string{
		"repo": "taisukeoe/dotfiles",
	}

	args, err := op.BuildArgs(params, []string{"--paginate"}, profileEnv)
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}

	expected := []string{"api", "repos/taisukeoe/dotfiles/pulls/82/comments", "--paginate"}
	if len(args) != len(expected) {
		t.Fatalf("BuildArgs returned %d args, want %d: got %v", len(args), len(expected), args)
	}
	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, expected[i])
		}
	}
}

func TestOperation_BuildArgs_WithRepeatedInlinePlaceholder(t *testing.T) {
	// An arg of the form "{x}...{x}" must be treated as inline interpolation,
	// not as a single whole-arg placeholder named "x}...{x". This shape comes
	// up in git_push's refspec "{branch}:refs/heads/{branch}".
	op := &Operation{
		ArgsTemplate: []string{"push", "{branch}:refs/heads/{branch}"},
		Params: map[string]ParamSchema{
			"branch": {Type: "string"},
		},
	}

	args, err := op.BuildArgs(
		map[string]ParamValue{"branch": "fix/whole-arg"},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("BuildArgs failed: %v", err)
	}

	expected := []string{"push", "fix/whole-arg:refs/heads/fix/whole-arg"}
	if len(args) != len(expected) {
		t.Fatalf("BuildArgs returned %d args, want %d: got %v", len(args), len(expected), args)
	}
	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, expected[i])
		}
	}
}

func TestOperation_BuildArgs_WithInlinePlaceholders_RejectsNonIntegralFloat(t *testing.T) {
	op := &Operation{
		ArgsTemplate: []string{"api", "repos/{repo}/pulls/{number}/comments"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Min: intPtr(1)},
		},
	}

	params := map[string]ParamValue{
		"number": float64(1.5),
	}
	profileEnv := map[string]string{
		"repo": "taisukeoe/dotfiles",
	}

	_, err := op.BuildArgs(params, nil, profileEnv)
	if err == nil {
		t.Fatal("BuildArgs should fail for non-integral float placeholder values")
	}
}

func TestOperation_ValidateArrayParams(t *testing.T) {
	op := &Operation{
		Params: map[string]ParamSchema{
			"paths": {Type: "array", Items: &ItemsSchema{Type: "string"}},
		},
	}

	tests := []struct {
		name    string
		params  map[string]ParamValue
		wantErr bool
	}{
		{
			name: "valid string array",
			params: map[string]ParamValue{
				"paths": []interface{}{"file1.go", "file2.go"},
			},
			wantErr: false,
		},
		{
			name: "valid []string",
			params: map[string]ParamValue{
				"paths": []string{"file1.go", "file2.go"},
			},
			wantErr: false,
		},
		{
			name: "invalid array item type",
			params: map[string]ParamValue{
				"paths": []interface{}{"file1.go", 123},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := op.ValidateParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOperation_StringParams(t *testing.T) {
	op := &Operation{
		Params: map[string]ParamSchema{
			"message": {Type: "string", MinLength: 1, MaxLength: 10},
		},
	}

	tests := []struct {
		name    string
		params  map[string]ParamValue
		wantErr bool
	}{
		{
			name:    "valid length",
			params:  map[string]ParamValue{"message": "hello"},
			wantErr: false,
		},
		{
			name:    "too short",
			params:  map[string]ParamValue{"message": ""},
			wantErr: true,
		},
		{
			name:    "too long",
			params:  map[string]ParamValue{"message": "hello world!"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := op.ValidateParams(tt.params)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOperation_BuildArgs_PairedDrop(t *testing.T) {
	tests := []struct {
		name     string
		template []string
		schemas  map[string]ParamSchema
		params   map[string]ParamValue
		expected []string
	}{
		{
			name:     "optional placeholder absent + preceding flag literal drops both",
			template: []string{"pr", "create", "-R", "owner/repo", "--body", "{body}"},
			schemas: map[string]ParamSchema{
				"body": {Type: "string", Optional: true, MaxLength: 65535},
			},
			params:   map[string]ParamValue{},
			expected: []string{"pr", "create", "-R", "owner/repo"},
		},
		{
			name:     "optional placeholder present + preceding flag literal keeps both (with control chars)",
			template: []string{"pr", "create", "-R", "owner/repo", "--body", "{body}"},
			schemas: map[string]ParamSchema{
				"body": {Type: "string", Optional: true, MaxLength: 65535},
			},
			params: map[string]ParamValue{
				"body": "multi\nline\nwith \"quote\" and \x01 control byte",
			},
			expected: []string{"pr", "create", "-R", "owner/repo", "--body", "multi\nline\nwith \"quote\" and \x01 control byte"},
		},
		{
			name:     "optional placeholder absent + preceding non-flag literal drops only placeholder",
			template: []string{"pr", "view", "{number}"},
			schemas: map[string]ParamSchema{
				"number": {Type: "integer", Optional: true, Min: intPtr(1)},
			},
			params:   map[string]ParamValue{},
			expected: []string{"pr", "view"},
		},
		{
			name:     "multiple adjacent paired drops: both absent",
			template: []string{"cmd", "--body", "{body}", "--title", "{title}", "--draft"},
			schemas: map[string]ParamSchema{
				"body":  {Type: "string", Optional: true, MaxLength: 65535},
				"title": {Type: "string", Optional: true, MaxLength: 255},
			},
			params:   map[string]ParamValue{},
			expected: []string{"cmd", "--draft"},
		},
		{
			name:     "multiple adjacent paired drops: body present, title absent",
			template: []string{"cmd", "--body", "{body}", "--title", "{title}", "--draft"},
			schemas: map[string]ParamSchema{
				"body":  {Type: "string", Optional: true, MaxLength: 65535},
				"title": {Type: "string", Optional: true, MaxLength: 255},
			},
			params: map[string]ParamValue{
				"body": "hello",
			},
			expected: []string{"cmd", "--body", "hello", "--draft"},
		},
		{
			name:     "inline placeholder Pattern B style is not paired-dropped (body provided)",
			template: []string{"-f", "body={body}"},
			schemas: map[string]ParamSchema{
				"body": {Type: "string", Optional: true, MaxLength: 65535},
			},
			params: map[string]ParamValue{
				"body": "hello",
			},
			expected: []string{"-f", "body=hello"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := &Operation{
				ArgsTemplate: tt.template,
				Params:       tt.schemas,
			}
			args, err := op.BuildArgs(tt.params, nil, nil)
			if err != nil {
				t.Fatalf("BuildArgs failed: %v", err)
			}
			if len(args) != len(tt.expected) {
				t.Fatalf("BuildArgs returned %d args, want %d: got %v, want %v", len(args), len(tt.expected), args, tt.expected)
			}
			for i, arg := range args {
				if arg != tt.expected[i] {
					t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, tt.expected[i])
				}
			}
		})
	}
}

func TestOperation_BuildArgs_MigratedBodyOps(t *testing.T) {
	// Mirrors the body-param handling in pkg/config/templates/*.json. gh_pr_create
	// and gh_issue_create now require a non-empty body (rejection of a missing
	// body is covered by TestTemplate_RequiresNonEmptyBody), so only their
	// body-present cases are modelled here; gh_pr_edit keeps an optional body
	// that paired-drops --body when absent.
	longBody := "title line\n\nparagraph with \"quotes\" and `backticks`\n\nline with control \x01 char\n\n" +
		"multibyte: 日本語の本文 — こんにちは"

	tests := []struct {
		name        string
		op          *Operation
		params      map[string]ParamValue
		profileEnv  map[string]string
		expectedArg []string
	}{
		{
			name: "gh_pr_create body present (long content)",
			op: &Operation{
				Command:      "gh",
				ArgsTemplate: []string{"pr", "create", "-R", "{repo}", "--body", "{body}"},
				Params: map[string]ParamSchema{
					"body": {Type: "string", MinLength: 1, MaxLength: 65535, Pattern: "\\S"},
				},
			},
			params:      map[string]ParamValue{"body": longBody},
			profileEnv:  map[string]string{"repo": "owner/repo"},
			expectedArg: []string{"pr", "create", "-R", "owner/repo", "--body", longBody},
		},
		{
			name: "gh_pr_edit body present",
			op: &Operation{
				Command:      "gh",
				ArgsTemplate: []string{"pr", "edit", "{number}", "-R", "{repo}", "--body", "{body}"},
				Params: map[string]ParamSchema{
					"number": {Type: "integer", Min: intPtr(1)},
					"body":   {Type: "string", Optional: true, MaxLength: 65535},
				},
			},
			params:      map[string]ParamValue{"number": float64(42), "body": longBody},
			profileEnv:  map[string]string{"repo": "owner/repo"},
			expectedArg: []string{"pr", "edit", "42", "-R", "owner/repo", "--body", longBody},
		},
		{
			name: "gh_pr_edit body absent",
			op: &Operation{
				Command:      "gh",
				ArgsTemplate: []string{"pr", "edit", "{number}", "-R", "{repo}", "--body", "{body}"},
				Params: map[string]ParamSchema{
					"number": {Type: "integer", Min: intPtr(1)},
					"body":   {Type: "string", Optional: true, MaxLength: 65535},
				},
			},
			params:      map[string]ParamValue{"number": float64(42)},
			profileEnv:  map[string]string{"repo": "owner/repo"},
			expectedArg: []string{"pr", "edit", "42", "-R", "owner/repo"},
		},
		{
			name: "gh_issue_create body present",
			op: &Operation{
				Command:      "gh",
				ArgsTemplate: []string{"issue", "create", "-R", "{repo}", "--body", "{body}"},
				Params: map[string]ParamSchema{
					"body": {Type: "string", MinLength: 1, MaxLength: 65535, Pattern: "\\S"},
				},
			},
			params:      map[string]ParamValue{"body": longBody},
			profileEnv:  map[string]string{"repo": "owner/repo"},
			expectedArg: []string{"issue", "create", "-R", "owner/repo", "--body", longBody},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := tt.op.BuildArgs(tt.params, nil, tt.profileEnv)
			if err != nil {
				t.Fatalf("BuildArgs failed: %v", err)
			}
			if len(args) != len(tt.expectedArg) {
				t.Fatalf("BuildArgs returned %d args, want %d: got %v, want %v", len(args), len(tt.expectedArg), args, tt.expectedArg)
			}
			for i, arg := range args {
				if arg != tt.expectedArg[i] {
					t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, tt.expectedArg[i])
				}
			}
		})
	}
}

func TestRequest_Validate(t *testing.T) {
	// Values chosen so a downstream `grep '\[OP:'` parser cannot be
	// tricked into treating a caller-supplied diagnostic field as the
	// start of a new audit log line. The upper-length case bounds log
	// width so a single request cannot produce an unbounded line.
	longID := ""
	for i := 0; i < MaxRequestIDLength+1; i++ {
		longID += "a"
	}

	tests := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{name: "empty", req: Request{}, wantErr: false},
		{name: "hyphen-uuid-ish", req: Request{RequestID: "req-1234.abcd_XYZ"}, wantErr: false},
		{name: "source mcp", req: Request{Source: "mcp"}, wantErr: false},
		{name: "source raw_argv", req: Request{Source: "raw_argv"}, wantErr: false},

		{name: "embedded newline", req: Request{RequestID: "INJECTED\n[OP:git_push]"}, wantErr: true},
		{name: "embedded carriage return", req: Request{RequestID: "abc\rdef"}, wantErr: true},
		{name: "embedded tab", req: Request{RequestID: "abc\tdef"}, wantErr: true},
		{name: "embedded NUL", req: Request{RequestID: "abc\x00def"}, wantErr: true},
		{name: "embedded space", req: Request{RequestID: "abc def"}, wantErr: true},
		{name: "embedded quote", req: Request{RequestID: "abc\"def"}, wantErr: true},
		{name: "utf8 letters not allowed", req: Request{RequestID: "日本語"}, wantErr: true},
		{name: "over length cap", req: Request{RequestID: longID}, wantErr: true},

		{name: "source unknown enum", req: Request{Source: "attacker"}, wantErr: true},
		{name: "source with newline", req: Request{Source: "mcp\ninjected"}, wantErr: true},

		{name: "operation valid template shape", req: Request{Operation: "gh_pr_view"}, wantErr: false},
		{name: "operation numeric suffix", req: Request{Operation: "gh_pr_view_2"}, wantErr: false},
		{name: "operation embedded newline", req: Request{Operation: "gh_pr_view\n[OP:git_push]"}, wantErr: true},
		{name: "operation embedded CRLF", req: Request{Operation: "gh_pr_view\r\n[OP:git_push]"}, wantErr: true},
		{name: "operation embedded NUL", req: Request{Operation: "gh_pr_view\x00"}, wantErr: true},
		{name: "operation leading digit", req: Request{Operation: "1st_op"}, wantErr: true},
		{name: "operation uppercase", req: Request{Operation: "GH_PR_VIEW"}, wantErr: true},
		{name: "operation hyphen", req: Request{Operation: "gh-pr-view"}, wantErr: true},
		{name: "operation dot", req: Request{Operation: "gh.pr.view"}, wantErr: true},
		{name: "operation over length", req: Request{Operation: "a" + strings.Repeat("b", 64)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
