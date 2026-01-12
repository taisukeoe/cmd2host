package main

import (
	"os"
	"strings"
	"testing"
)

func TestSanitizedEnv_Set(t *testing.T) {
	env := NewSanitizedEnv()
	env.Set("FOO", "bar")
	env.Set("BAZ", "qux")

	result := env.BuildEnv()

	// Check that our custom vars are present
	found := map[string]bool{"FOO": false, "BAZ": false}
	for _, e := range result {
		if e == "FOO=bar" {
			found["FOO"] = true
		}
		if e == "BAZ=qux" {
			found["BAZ"] = true
		}
	}

	if !found["FOO"] {
		t.Error("Expected FOO=bar in environment")
	}
	if !found["BAZ"] {
		t.Error("Expected BAZ=qux in environment")
	}
}

func TestSanitizedEnv_SetFromMap(t *testing.T) {
	env := NewSanitizedEnv()
	env.SetFromMap(map[string]string{
		"KEY1": "value1",
		"KEY2": "value2",
	})

	result := env.BuildEnv()

	found := map[string]bool{"KEY1": false, "KEY2": false}
	for _, e := range result {
		if e == "KEY1=value1" {
			found["KEY1"] = true
		}
		if e == "KEY2=value2" {
			found["KEY2"] = true
		}
	}

	if !found["KEY1"] || !found["KEY2"] {
		t.Error("Expected both keys in environment")
	}
}

func TestSanitizedEnv_SetGitConfig(t *testing.T) {
	env := NewSanitizedEnv()
	env.SetGitConfig("user.name", "Test User")
	env.SetGitConfig("user.email", "test@example.com")

	result := env.BuildEnv()

	var gitConfigParam string
	for _, e := range result {
		if strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS=") {
			gitConfigParam = e
			break
		}
	}

	if gitConfigParam == "" {
		t.Fatal("Expected GIT_CONFIG_PARAMETERS in environment")
	}

	// Verify format contains expected config
	if !strings.Contains(gitConfigParam, "user.name=Test User") {
		t.Error("Expected user.name in GIT_CONFIG_PARAMETERS")
	}
	if !strings.Contains(gitConfigParam, "user.email=test@example.com") {
		t.Error("Expected user.email in GIT_CONFIG_PARAMETERS")
	}
}

func TestSanitizedEnv_SetGitConfig_EscapesSingleQuotes(t *testing.T) {
	env := NewSanitizedEnv()
	env.SetGitConfig("commit.message", "It's a test")

	result := env.BuildEnv()

	var gitConfigParam string
	for _, e := range result {
		if strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS=") {
			gitConfigParam = e
			break
		}
	}

	if gitConfigParam == "" {
		t.Fatal("Expected GIT_CONFIG_PARAMETERS in environment")
	}

	// Verify single quotes are escaped
	if !strings.Contains(gitConfigParam, `'\''`) {
		t.Error("Expected escaped single quotes in GIT_CONFIG_PARAMETERS")
	}
}

func TestSanitizedEnv_InheritsBaseEnvVars(t *testing.T) {
	// Set a base env var temporarily
	originalPath := os.Getenv("PATH")
	if originalPath == "" {
		t.Skip("PATH not set, skipping test")
	}

	env := NewSanitizedEnv()
	result := env.BuildEnv()

	// Check that PATH is inherited
	var hasPath bool
	for _, e := range result {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
			break
		}
	}

	if !hasPath {
		t.Error("Expected PATH to be inherited in environment")
	}
}

func TestCommandSanitizer_SanitizeForGH(t *testing.T) {
	project := &ProjectConfig{
		Repo: "owner/repo",
	}
	sanitizer := NewCommandSanitizer(project)
	env := NewSanitizedEnv()

	sanitizer.SanitizeForGH(env)

	result := env.BuildEnv()

	checks := map[string]bool{
		"GH_PROMPT_DISABLED=1": false,
		"NO_COLOR=1":           false,
		"GH_REPO=owner/repo":   false,
	}

	for _, e := range result {
		for check := range checks {
			if e == check {
				checks[check] = true
			}
		}
	}

	for check, found := range checks {
		if !found {
			t.Errorf("Expected %s in environment", check)
		}
	}
}

func TestCommandSanitizer_SanitizeForGH_NoRepo(t *testing.T) {
	sanitizer := NewCommandSanitizer(nil)
	env := NewSanitizedEnv()

	sanitizer.SanitizeForGH(env)

	result := env.BuildEnv()

	// Should have GH_PROMPT_DISABLED but not GH_REPO
	var hasPromptDisabled, hasRepo bool
	for _, e := range result {
		if e == "GH_PROMPT_DISABLED=1" {
			hasPromptDisabled = true
		}
		if strings.HasPrefix(e, "GH_REPO=") {
			hasRepo = true
		}
	}

	if !hasPromptDisabled {
		t.Error("Expected GH_PROMPT_DISABLED=1")
	}
	if hasRepo {
		t.Error("Did not expect GH_REPO when profile is nil")
	}
}

func TestCommandSanitizer_SanitizeForGit(t *testing.T) {
	project := &ProjectConfig{
		GitConfig: map[string]string{
			"user.name": "Test",
		},
	}
	sanitizer := NewCommandSanitizer(project)
	env := NewSanitizedEnv()

	sanitizer.SanitizeForGit(env)

	result := env.BuildEnv()

	checks := map[string]bool{
		"GIT_TERMINAL_PROMPT=0":    false,
		"GIT_ALLOW_PROTOCOL=https:ssh": false,
		"GIT_ADVICE=0":             false,
	}

	for _, e := range result {
		for check := range checks {
			if e == check {
				checks[check] = true
			}
		}
	}

	for check, found := range checks {
		if !found {
			t.Errorf("Expected %s in environment", check)
		}
	}

	// Check git config is applied
	var hasGitConfig bool
	for _, e := range result {
		if strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS=") && strings.Contains(e, "user.name=Test") {
			hasGitConfig = true
			break
		}
	}
	if !hasGitConfig {
		t.Error("Expected GIT_CONFIG_PARAMETERS with user.name")
	}
}

func TestCommandSanitizer_PrepareCommand_GH(t *testing.T) {
	project := &ProjectConfig{
		Repo:     "owner/repo",
		RepoPath: "/path/to/repo",
		Env: map[string]string{
			"CUSTOM_VAR": "value",
		},
	}
	sanitizer := NewCommandSanitizer(project)

	cmd := sanitizer.PrepareCommand("gh", []string{"pr", "list"})

	// Check working directory
	if cmd.Dir != "/path/to/repo" {
		t.Errorf("Expected Dir to be /path/to/repo, got %s", cmd.Dir)
	}

	// Check environment contains expected vars
	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["GH_PROMPT_DISABLED"] != "1" {
		t.Error("Expected GH_PROMPT_DISABLED=1")
	}
	if envMap["NO_COLOR"] != "1" {
		t.Error("Expected NO_COLOR=1")
	}
	if envMap["GH_REPO"] != "owner/repo" {
		t.Error("Expected GH_REPO=owner/repo")
	}
	if envMap["CUSTOM_VAR"] != "value" {
		t.Error("Expected CUSTOM_VAR=value from profile env")
	}
}

func TestCommandSanitizer_PrepareCommand_Git(t *testing.T) {
	project := &ProjectConfig{
		RepoPath: "/path/to/repo",
		GitConfig: map[string]string{
			"user.name": "Test User",
		},
	}
	sanitizer := NewCommandSanitizer(project)

	cmd := sanitizer.PrepareCommand("git", []string{"status"})

	// Check working directory
	if cmd.Dir != "/path/to/repo" {
		t.Errorf("Expected Dir to be /path/to/repo, got %s", cmd.Dir)
	}

	// Check environment contains git-specific vars
	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["GIT_TERMINAL_PROMPT"] != "0" {
		t.Error("Expected GIT_TERMINAL_PROMPT=0")
	}
	if envMap["GIT_ALLOW_PROTOCOL"] != "https:ssh" {
		t.Error("Expected GIT_ALLOW_PROTOCOL=https:ssh")
	}
}

func TestCommandSanitizer_PrepareCommand_ExtractsBasename(t *testing.T) {
	sanitizer := NewCommandSanitizer(nil)

	// Test with full path
	cmd := sanitizer.PrepareCommand("/usr/bin/gh", []string{"--version"})

	// Should still apply gh sanitization
	envMap := make(map[string]string)
	for _, e := range cmd.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["GH_PROMPT_DISABLED"] != "1" {
		t.Error("Expected GH_PROMPT_DISABLED=1 even with full path")
	}
}

func TestValidateCommandPath_RejectsDotDot(t *testing.T) {
	err := ValidateCommandPath("../../../bin/sh")
	if err == nil {
		t.Error("Expected error for path containing '..'")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("Expected error message to mention '..', got: %v", err)
	}
}

func TestValidateCommandPath_RejectsDashPrefix(t *testing.T) {
	err := ValidateCommandPath("-rf")
	if err == nil {
		t.Error("Expected error for path starting with '-'")
	}
	if !strings.Contains(err.Error(), "-") {
		t.Errorf("Expected error message to mention '-', got: %v", err)
	}
}

func TestValidateCommandPath_RejectsNonexistent(t *testing.T) {
	err := ValidateCommandPath("nonexistent-command-12345")
	if err == nil {
		t.Error("Expected error for nonexistent command")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Expected error message to mention 'not found', got: %v", err)
	}
}

func TestValidateCommandPath_AcceptsValidCommand(t *testing.T) {
	// Use a command that exists on all systems
	err := ValidateCommandPath("sh")
	if err != nil {
		t.Errorf("Expected no error for valid command 'sh', got: %v", err)
	}
}
