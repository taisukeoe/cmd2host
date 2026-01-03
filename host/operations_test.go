package main

import (
	"testing"
)

func TestOperation_ValidateParams(t *testing.T) {
	op := &Operation{
		Command:      "gh",
		ArgsTemplate: []string{"pr", "view", "{number}", "--json", "{fields}"},
		Params: map[string]ParamSchema{
			"number": {Type: "integer", Min: 1},
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

func TestOperation_ValidateFlags(t *testing.T) {
	op := &Operation{
		AllowedFlags: []string{"--state", "--limit"},
	}

	tests := []struct {
		name    string
		flags   []string
		wantErr bool
	}{
		{
			name:    "allowed flags",
			flags:   []string{"--state", "open", "--limit", "10"},
			wantErr: false,
		},
		{
			name:    "allowed flag with equals",
			flags:   []string{"--state=open"},
			wantErr: false,
		},
		{
			name:    "disallowed flag",
			flags:   []string{"--format", "json"},
			wantErr: true,
		},
		{
			name:    "empty flags",
			flags:   []string{},
			wantErr: false,
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
		ArgsTemplate: []string{"-C", "{repo_path}", "add", "--"},
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

	expected := []string{"-C", "/home/user/project", "add", "--"}
	if len(args) != len(expected) {
		t.Fatalf("BuildArgs returned %d args, want %d", len(args), len(expected))
	}
	for i, arg := range args {
		if arg != expected[i] {
			t.Errorf("BuildArgs()[%d] = %q, want %q", i, arg, expected[i])
		}
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
