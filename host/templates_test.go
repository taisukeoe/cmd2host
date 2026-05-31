package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestListTemplates(t *testing.T) {
	templates, err := ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates() error = %v", err)
	}

	if len(templates) == 0 {
		t.Error("ListTemplates() returned empty list, expected at least one template")
	}

	// Check that known templates are present
	expectedTemplates := []string{"readonly", "github_write", "git_write"}
	for _, expected := range expectedTemplates {
		found := false
		for _, tmpl := range templates {
			if tmpl == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ListTemplates() missing expected template %q, got %v", expected, templates)
		}
	}

	// Check that no template names have .json extension
	for _, tmpl := range templates {
		if strings.HasSuffix(tmpl, ".json") {
			t.Errorf("ListTemplates() returned template with .json extension: %q", tmpl)
		}
	}
}

func TestGetTemplate(t *testing.T) {
	tests := []struct {
		name         string
		templateName string
		wantErr      bool
		checkContent string // substring to check in content
	}{
		{
			name:         "readonly template",
			templateName: "readonly",
			wantErr:      false,
			checkContent: "OWNER/REPO",
		},
		{
			name:         "github_write template",
			templateName: "github_write",
			wantErr:      false,
			checkContent: "gh_pr_create",
		},
		{
			name:         "git_write template",
			templateName: "git_write",
			wantErr:      false,
			checkContent: "git_push",
		},
		{
			name:         "unknown template",
			templateName: "nonexistent",
			wantErr:      true,
		},
		{
			name:         "empty name",
			templateName: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := GetTemplate(tt.templateName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetTemplate(%q) error = %v, wantErr %v", tt.templateName, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(data) == 0 {
					t.Errorf("GetTemplate(%q) returned empty content", tt.templateName)
				}
				if tt.checkContent != "" && !strings.Contains(string(data), tt.checkContent) {
					t.Errorf("GetTemplate(%q) content missing expected substring %q", tt.templateName, tt.checkContent)
				}
			}
		})
	}
}

func TestGetTemplate_ErrorWrapping(t *testing.T) {
	_, err := GetTemplate("nonexistent")
	if err == nil {
		t.Fatal("GetTemplate(nonexistent) should return error")
	}

	// Check that error message includes the template name
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error message should contain template name, got: %v", err)
	}

	// Check that it's a wrapped error (contains original fs error)
	if !strings.Contains(err.Error(), "failed to read template") {
		t.Errorf("error message should indicate read failure, got: %v", err)
	}
}

// loadTemplateOperation parses an embedded template and returns one of its
// operations with patterns compiled, mirroring LoadProjectConfig's per-op
// CompilePatterns step so pattern validation is exercised.
func loadTemplateOperation(t *testing.T, templateName, opID string) *Operation {
	t.Helper()
	data, err := GetTemplate(templateName)
	if err != nil {
		t.Fatalf("GetTemplate(%q) error = %v", templateName, err)
	}
	var config ProjectConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal template %q: %v", templateName, err)
	}
	op, ok := config.Operations[opID]
	if !ok {
		t.Fatalf("template %q has no operation %q", templateName, opID)
	}
	if err := op.CompilePatterns(); err != nil {
		t.Fatalf("CompilePatterns(%q.%q): %v", templateName, opID, err)
	}
	return op
}

// TestTemplate_RequiresNonEmptyBody is a regression guard for the
// non-interactive create contract. `gh pr create` / `gh issue create` prompt
// unless a body is supplied, and --fill is not in allowed_flags, so
// params.body is the only body channel. If body is optional, an agent that
// omits or empties it creates a PR / issue with an empty body that still
// exits 0. The two validation layers must together reject every empty-body
// shape:
//   - present-but-blank body (empty / whitespace / newline) is rejected by
//     ValidateParams via minLength + a non-whitespace pattern.
//   - a missing body is rejected by BuildArgs, because making the param
//     required disables the optional-placeholder paired-drop that would
//     otherwise silently omit --body.
func TestTemplate_RequiresNonEmptyBody(t *testing.T) {
	repoEnv := map[string]string{"repo": "owner/repo"}

	required := []struct{ templateName, opID string }{
		{"git_github_write", "gh_pr_create"},
		{"github_write", "gh_pr_create"},
		{"github_write", "gh_issue_create"},
	}
	for _, rc := range required {
		t.Run(rc.templateName+"/"+rc.opID, func(t *testing.T) {
			op := loadTemplateOperation(t, rc.templateName, rc.opID)

			// Layer 1: ValidateParams rejects present-but-blank bodies.
			blank := map[string]string{
				"empty body":            "",
				"whitespace-only body":  "   ",
				"newline/tab-only body": "\n\t ",
			}
			for name, body := range blank {
				if err := op.ValidateParams(map[string]ParamValue{"body": body}); err == nil {
					t.Errorf("%s: ValidateParams = nil, want rejection", name)
				}
			}
			if err := op.ValidateParams(map[string]ParamValue{"body": "real body"}); err != nil {
				t.Errorf("non-empty body: ValidateParams = %v, want nil", err)
			}

			// Layer 2: BuildArgs rejects a missing body (required param).
			if _, err := op.BuildArgs(map[string]ParamValue{}, nil, repoEnv); err == nil {
				t.Error("missing body: BuildArgs = nil error, want missing-required-parameter")
			}
			// With a body, BuildArgs still renders --body <body>.
			args, err := op.BuildArgs(map[string]ParamValue{"body": "real body"}, nil, repoEnv)
			if err != nil {
				t.Fatalf("BuildArgs with body: %v", err)
			}
			if !argsContainPair(args, "--body", "real body") {
				t.Errorf("BuildArgs args = %v, want --body followed by the body", args)
			}
		})
	}
}

// TestTemplate_BodyStaysOptionalForEdit guards the other side of the change:
// gh_pr_edit edits only the title / labels, so its body stays optional and
// requiring it on the create operations must not leak to it.
func TestTemplate_BodyStaysOptionalForEdit(t *testing.T) {
	op := loadTemplateOperation(t, "git_github_write", "gh_pr_edit")
	body, ok := op.Params["body"]
	if !ok {
		t.Fatal("gh_pr_edit has no body param")
	}
	if !body.Optional {
		t.Error("gh_pr_edit body.Optional = false, want true (must stay optional)")
	}
}

// argsContainPair reports whether want followed immediately by val appears in args.
func argsContainPair(args []string, want, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == want && args[i+1] == val {
			return true
		}
	}
	return false
}
