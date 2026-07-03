package daemon

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
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
	project := &config.ProjectConfig{
		Repos: []string{"owner/repo"},
	}
	target := &ExecutionTarget{Repo: "owner/repo"}
	sanitizer := NewCommandSanitizer(project, target)
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
	sanitizer := NewCommandSanitizer(nil, nil)
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
	project := &config.ProjectConfig{
		GitConfig: map[string]string{
			"user.name": "Test",
		},
	}
	sanitizer := NewCommandSanitizer(project, nil)
	env := NewSanitizedEnv()

	sanitizer.SanitizeForGit(env)

	result := env.BuildEnv()

	checks := map[string]bool{
		"GIT_TERMINAL_PROMPT=0":        false,
		"GIT_ALLOW_PROTOCOL=https:ssh": false,
		"GIT_ADVICE=0":                 false,
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

func TestCommandSanitizer_SanitizeForGitPushStrict(t *testing.T) {
	project := &config.ProjectConfig{
		GitConfig: map[string]string{
			"user.name": "Test",
		},
	}
	sanitizer := NewCommandSanitizer(project, nil)
	env := NewSanitizedEnv()

	sanitizer.SanitizeForGitPushStrict(env)

	result := env.BuildEnv()

	// Check strict push-specific environment variables
	checks := map[string]bool{
		// Base git sanitization
		"GIT_TERMINAL_PROMPT=0":        false,
		"GIT_ALLOW_PROTOCOL=https:ssh": false,
		"GIT_ADVICE=0":                 false,
		// Strict push additions
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new": false,
		"GIT_ASKPASS=":          false,
		"GIT_CONFIG_NOSYSTEM=1": false,
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

	// Check git config overrides for credential hijacking prevention
	var hasCredentialHelper, hasSubmoduleRecurse bool
	for _, e := range result {
		if strings.HasPrefix(e, "GIT_CONFIG_PARAMETERS=") {
			if strings.Contains(e, "credential.helper=") {
				hasCredentialHelper = true
			}
			if strings.Contains(e, "submodule.recurse=false") {
				hasSubmoduleRecurse = true
			}
		}
	}
	if !hasCredentialHelper {
		t.Error("Expected GIT_CONFIG_PARAMETERS with credential.helper override")
	}
	if !hasSubmoduleRecurse {
		t.Error("Expected GIT_CONFIG_PARAMETERS with submodule.recurse=false")
	}
}

func TestCommandSanitizer_PrepareCommand_GH(t *testing.T) {
	project := &config.ProjectConfig{
		Repos:     []string{"owner/repo"},
		RepoPaths: []string{"/path/to/repo"},
		Env: map[string]string{
			"CUSTOM_VAR": "value",
		},
	}
	target := &ExecutionTarget{Repo: "owner/repo", RepoPath: "/path/to/repo"}
	sanitizer := NewCommandSanitizer(project, target)

	cmd, err := sanitizer.PrepareCommand("gh", []string{"pr", "list"}, "gh")
	if err != nil {
		t.Fatalf("PrepareCommand returned error: %v", err)
	}

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
	project := &config.ProjectConfig{
		Repos:     []string{"owner/repo"},
		RepoPaths: []string{"/path/to/repo"},
		GitConfig: map[string]string{
			"user.name": "Test User",
		},
	}
	target := &ExecutionTarget{Repo: "owner/repo", RepoPath: "/path/to/repo"}
	sanitizer := NewCommandSanitizer(project, target)

	cmd, err := sanitizer.PrepareCommand("git", []string{"status"}, "git")
	if err != nil {
		t.Fatalf("PrepareCommand returned error: %v", err)
	}

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
	sanitizer := NewCommandSanitizer(nil, nil)

	// Full command path: the profile inferred from the operation template
	// must still resolve to gh sanitization.
	op := &operations.Operation{Command: "/usr/bin/gh", ArgsTemplate: []string{"--version"}}
	cmd, err := sanitizer.PrepareCommand(op.Command, []string{"--version"}, InferSanitizeProfile(op))
	if err != nil {
		t.Fatalf("PrepareCommand returned error: %v", err)
	}

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

func TestInferSanitizeProfile(t *testing.T) {
	cases := []struct {
		name string
		op   operations.Operation
		want string
	}{
		{"gh", operations.Operation{Command: "gh", ArgsTemplate: []string{"pr", "list"}}, "gh"},
		{"git push", operations.Operation{Command: "git", ArgsTemplate: []string{"push", "{expected_git_url}", "{branch}"}}, "git_push_strict"},
		{"git fetch", operations.Operation{Command: "git", ArgsTemplate: []string{"fetch", "origin"}}, "git"},
		{"git no args", operations.Operation{Command: "git", ArgsTemplate: nil}, "git"},
		{"aws", operations.Operation{Command: "aws", ArgsTemplate: []string{"s3", "ls"}}, "minimal"},
		{"absolute git push", operations.Operation{Command: "/usr/bin/git", ArgsTemplate: []string{"push"}}, "git_push_strict"},
		{"absolute gh", operations.Operation{Command: "/opt/homebrew/bin/gh", ArgsTemplate: []string{"pr", "view"}}, "gh"},
		{"windows git", operations.Operation{Command: "git.exe", ArgsTemplate: []string{"fetch"}}, "git"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InferSanitizeProfile(&tc.op); got != tc.want {
				t.Errorf("InferSanitizeProfile(%q, %v) = %q, want %q", tc.op.Command, tc.op.ArgsTemplate, got, tc.want)
			}
		})
	}
}

// TestInferSanitizeProfile_EmbeddedTemplates pins the premise that lets
// InferSanitizeProfile read the operation template instead of the runtime
// argv: in every embedded template, a git operation's args_template starts
// with a literal element (never a placeholder that could be dropped or
// substituted), and the "push" literal appears only as that first element.
// Under these two properties the template-derived profile always matches
// the argv-derived decision the previous implementation made.
func TestInferSanitizeProfile_EmbeddedTemplates(t *testing.T) {
	names, err := config.ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded templates found")
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			data, err := config.GetTemplate(name)
			if err != nil {
				t.Fatalf("GetTemplate(%q): %v", name, err)
			}
			var tmpl struct {
				Operations map[string]*operations.Operation `json:"operations"`
			}
			if err := json.Unmarshal(data, &tmpl); err != nil {
				t.Fatalf("unmarshal template %q: %v", name, err)
			}

			for opID, op := range tmpl.Operations {
				if commandBasename(op.Command) != "git" {
					continue
				}
				if len(op.ArgsTemplate) == 0 {
					t.Errorf("%s: git operation has empty args_template", opID)
					continue
				}
				if strings.Contains(op.ArgsTemplate[0], "{") {
					t.Errorf("%s: git operation args_template starts with a placeholder %q; the first element must be a literal", opID, op.ArgsTemplate[0])
				}
				for i, elem := range op.ArgsTemplate[1:] {
					if elem == "push" {
						t.Errorf("%s: args_template has \"push\" at index %d; it is only recognized at index 0", opID, i+1)
					}
				}
				wantStrict := op.ArgsTemplate[0] == "push"
				gotStrict := InferSanitizeProfile(op) == "git_push_strict"
				if gotStrict != wantStrict {
					t.Errorf("%s: InferSanitizeProfile strictness = %v, want %v", opID, gotStrict, wantStrict)
				}
			}
		})
	}
}

func TestExecutionProfile(t *testing.T) {
	cases := []struct {
		name string
		op   operations.Operation
		args []string
		want string
	}{
		{"git literal push", operations.Operation{Command: "git", ArgsTemplate: []string{"push", "{expected_git_url}"}}, []string{"push", "git@github.com:owner/repo.git"}, "git_push_strict"},
		{"git placeholder expanding to push", operations.Operation{Command: "git", ArgsTemplate: []string{"{subcommand}", "origin"}}, []string{"push", "origin"}, "git_push_strict"},
		{"git placeholder expanding to fetch", operations.Operation{Command: "git", ArgsTemplate: []string{"{subcommand}", "origin"}}, []string{"fetch", "origin"}, "git"},
		{"git fetch stays base profile", operations.Operation{Command: "git", ArgsTemplate: []string{"fetch", "origin"}}, []string{"fetch", "origin"}, "git"},
		{"gh unaffected by push arg", operations.Operation{Command: "gh", ArgsTemplate: []string{"{subcommand}"}}, []string{"push"}, "gh"},
		{"non-git unaffected by push arg", operations.Operation{Command: "aws", ArgsTemplate: []string{"{subcommand}"}}, []string{"push"}, "minimal"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExecutionProfile(&tc.op, tc.args); got != tc.want {
				t.Errorf("ExecutionProfile(%q, %v) = %q, want %q", tc.op.Command, tc.args, got, tc.want)
			}
		})
	}
}

func TestPrepareCommand_UnknownProfileFailsClosed(t *testing.T) {
	sanitizer := NewCommandSanitizer(nil, nil)

	cmd, err := sanitizer.PrepareCommand("gh", []string{"pr", "list"}, "no_such_profile")
	if err == nil {
		t.Fatal("Expected error for unknown profile name")
	}
	if cmd != nil {
		t.Error("Expected nil cmd when the profile name is rejected")
	}
	if !strings.Contains(err.Error(), "no_such_profile") {
		t.Errorf("Expected error to name the rejected profile, got: %v", err)
	}
}

// legacyPrepareEnv reproduces the environment construction PrepareCommand
// performed before profiles were introduced: project env first, then the
// sanitize method selected by command basename (with the git push special
// case keyed on the first runtime arg). It fixes the pre-profile behavior
// as the comparison baseline for TestPrepareCommand_GoldenEnvParity.
func legacyPrepareEnv(cs *CommandSanitizer, cmdPath string, args []string) []string {
	env := NewSanitizedEnv()

	if cs.project != nil {
		env.SetFromMap(cs.project.Env)
	}

	cmdName := strings.TrimSuffix(cmdPath, ".exe")
	cmdName = cmdName[strings.LastIndex(cmdName, "/")+1:]

	switch cmdName {
	case "gh":
		cs.SanitizeForGH(env)
	case "git":
		if len(args) > 0 && args[0] == "push" {
			cs.SanitizeForGitPushStrict(env)
		} else {
			cs.SanitizeForGit(env)
		}
	}

	return env.BuildEnv()
}

// normalizeGitConfigEntry makes GIT_CONFIG_PARAMETERS entries comparable:
// the value is assembled from a map, so the 'key=value' parts appear in
// nondeterministic order in both the legacy and the profile-based path.
// All other entries are returned unchanged.
func normalizeGitConfigEntry(entry string) string {
	const prefix = "GIT_CONFIG_PARAMETERS="
	if !strings.HasPrefix(entry, prefix) {
		return entry
	}
	value := strings.TrimPrefix(entry, prefix)
	value = strings.TrimPrefix(value, "'")
	value = strings.TrimSuffix(value, "'")
	parts := strings.Split(value, "' '")
	sort.Strings(parts)
	return prefix + "'" + strings.Join(parts, "' '") + "'"
}

// TestPrepareCommand_GoldenEnvParity pins backward compatibility for
// operations without a declared profile: the environment produced by the
// profile-based PrepareCommand (via InferSanitizeProfile) must be
// identical, entry for entry and in order, to the environment the
// pre-profile implementation produced. Covers all four legacy branches:
// gh, git non-push, git push, and a command with no dedicated handling.
func TestPrepareCommand_GoldenEnvParity(t *testing.T) {
	project := &config.ProjectConfig{
		Repos:     []string{"owner/repo"},
		RepoPaths: []string{"/path/to/repo"},
		Env:       map[string]string{"CUSTOM_VAR": "value"},
		GitConfig: map[string]string{"user.name": "tester"},
	}
	target := &ExecutionTarget{
		Repo:           "owner/repo",
		RepoPath:       "/path/to/repo",
		ExpectedGitURL: "git@github.com:owner/repo.git",
	}

	cases := []struct {
		name string
		op   operations.Operation
		args []string
	}{
		{"gh", operations.Operation{Command: "gh", ArgsTemplate: []string{"pr", "list"}}, []string{"pr", "list"}},
		{"git fetch", operations.Operation{Command: "git", ArgsTemplate: []string{"fetch", "origin"}}, []string{"fetch", "origin"}},
		{"git push", operations.Operation{Command: "git", ArgsTemplate: []string{"push", "{expected_git_url}", "{branch}"}}, []string{"push", "git@github.com:owner/repo.git", "main"}},
		{"git placeholder push", operations.Operation{Command: "git", ArgsTemplate: []string{"{subcommand}", "origin"}}, []string{"push", "origin"}},
		{"unknown command", operations.Operation{Command: "aws", ArgsTemplate: []string{"s3", "ls"}}, []string{"s3", "ls"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sanitizer := NewCommandSanitizer(project, target)

			want := legacyPrepareEnv(sanitizer, tc.op.Command, tc.args)

			cmd, err := sanitizer.PrepareCommand(tc.op.Command, tc.args, ExecutionProfile(&tc.op, tc.args))
			if err != nil {
				t.Fatalf("PrepareCommand returned error: %v", err)
			}
			got := cmd.Env

			if len(got) != len(want) {
				t.Fatalf("env length mismatch: got %d entries, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
			}
			for i := range want {
				g := normalizeGitConfigEntry(got[i])
				w := normalizeGitConfigEntry(want[i])
				if g != w {
					t.Errorf("env[%d] mismatch:\ngot:  %s\nwant: %s", i, g, w)
				}
			}

			if cmd.Dir != target.RepoPath {
				t.Errorf("Expected Dir %q, got %q", target.RepoPath, cmd.Dir)
			}
		})
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
