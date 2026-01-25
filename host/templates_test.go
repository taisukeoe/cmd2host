package main

import (
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
		name        string
		templateName string
		wantErr     bool
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
