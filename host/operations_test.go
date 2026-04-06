package main

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
