package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/taisukeoe/cmd2host/pkg/operations"
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
	expectedTemplates := []string{"readonly", "github_write", "git_write", "git_github_write", "aws_selected"}
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
			name:         "aws_selected template",
			templateName: "aws_selected",
			wantErr:      false,
			checkContent: "aws_sts_get_caller_identity",
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
func loadTemplateOperation(t *testing.T, templateName, opID string) *operations.Operation {
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
				if err := op.ValidateParams(map[string]operations.ParamValue{"body": body}); err == nil {
					t.Errorf("%s: ValidateParams = nil, want rejection", name)
				}
			}
			if err := op.ValidateParams(map[string]operations.ParamValue{"body": "real body"}); err != nil {
				t.Errorf("non-empty body: ValidateParams = %v, want nil", err)
			}

			// Layer 2: BuildArgs rejects a missing body (required param).
			if _, err := op.BuildArgs(map[string]operations.ParamValue{}, nil, repoEnv); err == nil {
				t.Error("missing body: BuildArgs = nil error, want missing-required-parameter")
			}
			// With a body, BuildArgs still renders --body <body>.
			args, err := op.BuildArgs(map[string]operations.ParamValue{"body": "real body"}, nil, repoEnv)
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

// TestTemplate_CommentBodyRejectsWhitespaceOnly guards the already-required
// comment bodies (gh_pr_comment / gh_pr_review_comment_reply): minLength alone
// admits a whitespace-only body, so the non-whitespace pattern must reject a
// blank comment that would otherwise be pure noise.
func TestTemplate_CommentBodyRejectsWhitespaceOnly(t *testing.T) {
	pairs := []struct{ templateName, opID string }{
		{"git_github_write", "gh_pr_comment"},
		{"git_github_write", "gh_pr_review_comment_reply"},
		{"github_write", "gh_pr_comment"},
		{"github_write", "gh_pr_review_comment_reply"},
	}
	for _, p := range pairs {
		t.Run(p.templateName+"/"+p.opID, func(t *testing.T) {
			op := loadTemplateOperation(t, p.templateName, p.opID)
			for name, body := range map[string]string{
				"whitespace-only":  "   ",
				"newline/tab-only": "\n\t ",
			} {
				if err := op.ValidateParams(map[string]operations.ParamValue{"body": body}); err == nil {
					t.Errorf("%s: ValidateParams = nil, want rejection", name)
				}
			}
			if err := op.ValidateParams(map[string]operations.ParamValue{"body": "ok"}); err != nil {
				t.Errorf("non-blank body: ValidateParams = %v, want nil", err)
			}
		})
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

// TestTemplate_GitPushBranch_RejectsLeadingDash pins the branch param
// pattern in every bundled template that exposes git_push. A leading `-`
// on `{branch}` would let `git push {expected_git_url} {branch}:refs/…`
// re-parse the branch as an option — the explicit URL fixation is the
// primary defense, but rejecting the shape at param-validate time keeps
// defense-in-depth without depending on git's argv scan.
func TestTemplate_GitPushBranch_RejectsLeadingDash(t *testing.T) {
	templates := []string{"git_write", "git_github_write"}

	rejected := []string{
		"-foo",
		"--force",
		"-",
	}
	accepted := []string{
		"main",
		"feature/bar-baz",
		"release/1.2.3",
		"topic-branch",
		".hidden",
		"_leading-underscore",
	}

	for _, tmpl := range templates {
		t.Run(tmpl, func(t *testing.T) {
			op := loadTemplateOperation(t, tmpl, "git_push")
			for _, branch := range rejected {
				if err := op.ValidateParams(map[string]operations.ParamValue{"branch": branch}); err == nil {
					t.Errorf("branch %q must be rejected by the git_push pattern", branch)
				}
			}
			for _, branch := range accepted {
				if err := op.ValidateParams(map[string]operations.ParamValue{"branch": branch}); err != nil {
					t.Errorf("branch %q must be accepted; got %v", branch, err)
				}
			}
		})
	}
}

// TestTemplates_BodyOpsMigration asserts the on-disk templates carry the
// Pattern A migration for gh_pr_create / gh_pr_edit / gh_issue_create.
// This is the regression gate: if anyone reintroduces "--body" into
// allowed_flags or drops the body schema, this test fails before the
// in-memory BuildArgs tests do.
func TestTemplates_BodyOpsMigration(t *testing.T) {
	type opCheck struct {
		template     string // template file basename (no .json)
		operation    string
		expectSuffix []string // expected trailing args_template elements
		bodyRequired bool     // create ops require a non-empty body (non-interactive contract)
	}

	checks := []opCheck{
		{template: "github_write", operation: "gh_pr_create", expectSuffix: []string{"--body", "{body}"}, bodyRequired: true},
		{template: "github_write", operation: "gh_issue_create", expectSuffix: []string{"--body", "{body}"}, bodyRequired: true},
		{template: "git_github_write", operation: "gh_pr_create", expectSuffix: []string{"--body", "{body}"}, bodyRequired: true},
		{template: "git_github_write", operation: "gh_pr_edit", expectSuffix: []string{"--body", "{body}"}},
	}

	for _, c := range checks {
		t.Run(c.template+"/"+c.operation, func(t *testing.T) {
			data, err := GetTemplate(c.template)
			if err != nil {
				t.Fatalf("GetTemplate(%q): %v", c.template, err)
			}
			var project ProjectConfig
			if err := json.Unmarshal(data, &project); err != nil {
				t.Fatalf("Unmarshal template %q: %v", c.template, err)
			}
			op, ok := project.Operations[c.operation]
			if !ok {
				t.Fatalf("operation %q not in template %q", c.operation, c.template)
			}

			// args_template ends with the expected suffix
			if len(op.ArgsTemplate) < len(c.expectSuffix) {
				t.Fatalf("args_template too short: %v", op.ArgsTemplate)
			}
			actualSuffix := op.ArgsTemplate[len(op.ArgsTemplate)-len(c.expectSuffix):]
			for i, want := range c.expectSuffix {
				if actualSuffix[i] != want {
					t.Errorf("args_template[-%d:] = %v, want suffix %v", len(c.expectSuffix), actualSuffix, c.expectSuffix)
					break
				}
			}

			// params.body declared with the expected schema
			bodySchema, ok := op.Params["body"]
			if !ok {
				t.Fatalf("params.body not declared in %q", c.operation)
			}
			if bodySchema.Type != "string" {
				t.Errorf("params.body.type = %q, want \"string\"", bodySchema.Type)
			}
			if c.bodyRequired {
				// gh pr create is non-interactive (no --fill allowed), so a
				// missing or blank body would silently create an empty-body PR.
				// Body must therefore be required (not optional) and non-blank
				// (minLength + non-whitespace pattern).
				if bodySchema.Optional {
					t.Errorf("params.body.optional = true, want false (body required)")
				}
				if bodySchema.MinLength != 1 {
					t.Errorf("params.body.minLength = %d, want 1", bodySchema.MinLength)
				}
				if bodySchema.Pattern == "" {
					t.Errorf("params.body.pattern is empty, want a non-whitespace pattern")
				}
			} else if !bodySchema.Optional {
				t.Errorf("params.body.optional = false, want true")
			}
			if bodySchema.MaxLength != 65535 {
				t.Errorf("params.body.maxLength = %d, want 65535", bodySchema.MaxLength)
			}

			// allowed_flags must not include --body (Option β: immediate break)
			for _, f := range op.AllowedFlags {
				if f == "--body" {
					t.Errorf("allowed_flags still contains --body for %q", c.operation)
				}
			}

			// ValidateFlags must reject --body=<value> form
			if err := op.ValidateFlags([]string{"--body=hello"}); err == nil {
				t.Errorf("ValidateFlags([\"--body=hello\"]) returned nil, want \"flag not allowed\" error")
			}
		})
	}
}
