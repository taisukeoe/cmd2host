package operations

import (
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
