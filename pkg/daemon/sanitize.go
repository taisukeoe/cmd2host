// sanitize.go provides execution environment sanitization.
// Ensures commands run in a controlled environment with minimal attack surface.

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// SanitizedEnv builds a sanitized environment for command execution
type SanitizedEnv struct {
	env       []string
	gitConfig map[string]string
}

// NewSanitizedEnv creates a new sanitized environment
func NewSanitizedEnv() *SanitizedEnv {
	return &SanitizedEnv{
		env:       []string{},
		gitConfig: make(map[string]string),
	}
}

// baseEnvVars returns the minimal set of environment variables to inherit
var baseEnvVars = []string{
	"PATH",
	"HOME",
	"USER",
	"LANG",
	"LC_ALL",
	"TERM",
	"SHELL",
	// macOS specific
	"TMPDIR",
	// SSH agent for git/gh
	"SSH_AUTH_SOCK",
	"SSH_AGENT_PID",
	// GPG for commit signing
	"GPG_AGENT_INFO",
	"GPG_TTY",
	// Note: GH_TOKEN/GITHUB_TOKEN are intentionally NOT inherited.
	// gh CLI should use SSH auth via SSH_AUTH_SOCK.
	// If a token is needed, set it explicitly in project's env field.
}

// BuildEnv builds the final environment array for exec.Cmd
func (s *SanitizedEnv) BuildEnv() []string {
	// Start with minimal inherited environment
	env := s.inheritBaseEnv()

	// Add explicitly set environment variables
	env = append(env, s.env...)

	// Add git config as GIT_CONFIG_PARAMETERS if any
	if len(s.gitConfig) > 0 {
		env = append(env, s.buildGitConfigEnv())
	}

	return env
}

// inheritBaseEnv copies allowed environment variables from current process
func (s *SanitizedEnv) inheritBaseEnv() []string {
	var env []string
	for _, key := range baseEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return env
}

// Set sets an environment variable
func (s *SanitizedEnv) Set(key, value string) {
	s.env = append(s.env, key+"="+value)
}

// SetFromMap sets multiple environment variables from a map
func (s *SanitizedEnv) SetFromMap(m map[string]string) {
	for k, v := range m {
		s.Set(k, v)
	}
}

// SetGitConfig sets a git config value
func (s *SanitizedEnv) SetGitConfig(key, value string) {
	s.gitConfig[key] = value
}

// SetGitConfigFromMap sets multiple git config values
func (s *SanitizedEnv) SetGitConfigFromMap(m map[string]string) {
	for k, v := range m {
		s.SetGitConfig(k, v)
	}
}

// buildGitConfigEnv builds GIT_CONFIG_PARAMETERS from gitConfig map
// Format: 'key=value' 'key2=value2'
func (s *SanitizedEnv) buildGitConfigEnv() string {
	var parts []string
	for k, v := range s.gitConfig {
		// Escape single quotes in key and value to prevent injection
		escapedKey := strings.ReplaceAll(k, "'", `'\''`)
		escapedVal := strings.ReplaceAll(v, "'", `'\''`)
		parts = append(parts, fmt.Sprintf("'%s=%s'", escapedKey, escapedVal))
	}
	return "GIT_CONFIG_PARAMETERS=" + strings.Join(parts, " ")
}

// CommandSanitizer prepares commands for safe execution.
// project carries project-level policy (env, git_config). target carries the
// resolved per-request execution context (target_repo, repo_path, expected
// git URL). Both are required for multi-repo projects; target may be nil
// only for daemon-internal probes that do not run a command.
type CommandSanitizer struct {
	project *config.ProjectConfig
	target  *ExecutionTarget
}

// NewCommandSanitizer creates a new CommandSanitizer
func NewCommandSanitizer(project *config.ProjectConfig, target *ExecutionTarget) *CommandSanitizer {
	return &CommandSanitizer{project: project, target: target}
}

// SanitizeForGH applies gh-specific sanitization
func (cs *CommandSanitizer) SanitizeForGH(env *SanitizedEnv) {
	// Disable interactive prompts
	env.Set("GH_PROMPT_DISABLED", "1")

	// Set NO_COLOR for consistent output
	env.Set("NO_COLOR", "1")

	// Bind to target repo
	if cs.target != nil && cs.target.Repo != "" {
		env.Set("GH_REPO", cs.target.Repo)
	}
}

// SanitizeForGit applies git-specific sanitization
func (cs *CommandSanitizer) SanitizeForGit(env *SanitizedEnv) {
	// Disable terminal prompts (credential, password, etc.)
	env.Set("GIT_TERMINAL_PROMPT", "0")

	// Restrict allowed protocols
	env.Set("GIT_ALLOW_PROTOCOL", "https:ssh")

	// Disable advice messages
	env.Set("GIT_ADVICE", "0")

	// Apply project git config
	if cs.project != nil && cs.project.GitConfig != nil {
		env.SetGitConfigFromMap(cs.project.GitConfig)
	}
}

// SanitizeForGitRemoteStrict applies strict sanitization for git
// subcommands that communicate with a remote via an explicit URL argument
// (push / fetch / clone / pull / ls-remote). Explicit URL fixation (the
// remote target URL is passed as an explicit argument by the operation
// template) is the primary defense; the env hardening below removes
// secondary channels (system / global git config, credential helpers,
// Git hooks, SSH command override, recursive submodule operations) that
// could redirect or hijack the remote-communicating invocation.
func (cs *CommandSanitizer) SanitizeForGitRemoteStrict(env *SanitizedEnv) {
	// Apply base git sanitization first
	cs.SanitizeForGit(env)

	// Strict: Disable SSH command injection via environment
	env.Set("GIT_SSH_COMMAND", "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")

	// Strict: Clear any askpass helpers
	env.Set("GIT_ASKPASS", "")

	// Strict: Ignore system git config
	env.Set("GIT_CONFIG_NOSYSTEM", "1")

	// Strict: Ignore global git config ($HOME/.gitconfig and $XDG_CONFIG_HOME/git/config).
	// /dev/null is the documented form to skip global config entirely.
	env.Set("GIT_CONFIG_GLOBAL", "/dev/null")

	// Strict: Override git config at runtime so repo-local .git/config cannot
	// redirect credentials, hook execution, ssh command, or submodule recursion.
	env.SetGitConfig("credential.helper", "")
	env.SetGitConfig("core.hooksPath", "/dev/null")
	env.SetGitConfig("core.sshCommand", "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")
	env.SetGitConfig("submodule.recurse", "false")
}

// sanitizeProfile is a named, registry-managed sanitization behavior.
// apply mutates the environment the way the profile's target CLI expects.
type sanitizeProfile struct {
	apply func(cs *CommandSanitizer, env *SanitizedEnv)
}

// sanitizeProfiles is the registry of daemon-side sanitization profiles.
// PrepareCommand only executes profiles present in this registry; a name
// outside it is rejected rather than silently mapped to a weaker profile.
var sanitizeProfiles = map[string]sanitizeProfile{
	"minimal":           {apply: func(cs *CommandSanitizer, env *SanitizedEnv) {}},
	"gh":                {apply: func(cs *CommandSanitizer, env *SanitizedEnv) { cs.SanitizeForGH(env) }},
	"git":               {apply: func(cs *CommandSanitizer, env *SanitizedEnv) { cs.SanitizeForGit(env) }},
	"git_remote_strict": {apply: func(cs *CommandSanitizer, env *SanitizedEnv) { cs.SanitizeForGitRemoteStrict(env) }},
}

// commandBasename returns the command's basename with a trailing ".exe"
// suffix removed (Windows), the shape sanitization profiles key on.
func commandBasename(cmdPath string) string {
	name := strings.TrimSuffix(cmdPath, ".exe") // Handle Windows
	return name[strings.LastIndex(name, "/")+1:]
}

// gitRemoteSubcommands lists the top-level git subcommand names that
// communicate with a remote via a URL passed as an argv token and therefore
// run under git_remote_strict. The strict profile pairs with URL fixation
// through the operation template's {expected_git_url} placeholder; every
// subcommand named here is a shape whose operation template can bind the
// remote destination by supplying that URL in argv.
//
// Excluded on purpose:
//   - `remote`: `git remote add / set-url / update` operate on the
//     repo-local remote configuration (or, for `remote update`, on all
//     configured remotes). Their destination is resolved from
//     `.git/config`, not from an argv URL, so URL fixation cannot bind
//     them and routing them to strict would advertise a contract this
//     profile does not enforce.
//   - Sub-subcommand trees (`git lfs fetch`, `git submodule update
//     --remote`, ...): the first args_template element is `lfs` /
//     `submodule`, not the remote-communicating verb, so operators that
//     template those shapes keep the base "git" profile unless a future
//     entry names them explicitly here.
var gitRemoteSubcommands = map[string]struct{}{
	"push":      {},
	"fetch":     {},
	"clone":     {},
	"pull":      {},
	"ls-remote": {},
}

// isGitRemoteSubcommand reports whether a git subcommand name identifies
// an invocation that communicates with a remote.
func isGitRemoteSubcommand(name string) bool {
	_, ok := gitRemoteSubcommands[name]
	return ok
}

// InferSanitizeProfile returns the sanitization profile for an operation
// derived from its command and args template:
//
//   - command basename "gh" → "gh"
//   - command basename "git" with an args_template whose first element is
//     a remote-communicating subcommand (isGitRemoteSubcommand) →
//     "git_remote_strict"
//   - any other "git" operation → "git"
//   - everything else → "minimal"
//
// The decision reads the operation template rather than the built argv, so
// the profile is a property of the declared operation shape.
func InferSanitizeProfile(op *operations.Operation) string {
	switch commandBasename(op.Command) {
	case "gh":
		return "gh"
	case "git":
		if len(op.ArgsTemplate) > 0 && isGitRemoteSubcommand(op.ArgsTemplate[0]) {
			return "git_remote_strict"
		}
		return "git"
	default:
		return "minimal"
	}
}

// ExecutionProfile returns the sanitization profile for an operation about
// to execute with the given argv. It starts from the template-declarative
// InferSanitizeProfile and applies a strengthen-only correction: a git
// invocation whose first runtime argument names a remote-communicating
// subcommand always executes under "git_remote_strict", even when the
// operation template reaches that subcommand through a placeholder rather
// than a literal head. This keeps the runtime invariant that a git remote
// operation never runs under a weaker profile.
func ExecutionProfile(op *operations.Operation, args []string) string {
	profile := InferSanitizeProfile(op)
	if profile == "git" && len(args) > 0 && isGitRemoteSubcommand(args[0]) {
		return "git_remote_strict"
	}
	return profile
}

// PrepareCommand creates an exec.Cmd with sanitized environment. profile
// selects the sanitization behavior from sanitizeProfiles; a name outside
// the registry is an error and no command is prepared.
//
// When profile is "git_remote_strict", PrepareCommand additionally
// enforces the URL-fixation contract at runtime: the daemon-derived
// target.ExpectedGitURL must appear at args[1], immediately after the
// subcommand and before any option or refspec. Placing the URL at the
// first positional slot binds it to the "<repository>" argument git
// resolves as the connection destination for push / fetch / clone / pull /
// ls-remote. Any other layout (URL absent, URL as an option value like
// `--upload-pack=<URL>`, URL after another positional that git would
// treat as the repository) is rejected fail-closed so a custom operation
// cannot advertise the strict profile while resolving the remote through
// `.git/config`.
func (cs *CommandSanitizer) PrepareCommand(cmdPath string, args []string, profile string) (*exec.Cmd, error) {
	p, ok := sanitizeProfiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown sanitize profile %q", profile)
	}

	if profile == "git_remote_strict" {
		if cs.target == nil || cs.target.ExpectedGitURL == "" {
			return nil, fmt.Errorf("sanitize profile %q requires target.ExpectedGitURL to be resolved", profile)
		}
		if len(args) < 2 || args[1] != cs.target.ExpectedGitURL {
			// Deliberately omit the resolved argv from the error: the
			// message reaches the caller via stderr, and a custom
			// operation whose argv carries a token would otherwise
			// surface that token in the failure text. The profile name
			// and the required expected_git_url identify the violated
			// contract sufficiently.
			return nil, fmt.Errorf("sanitize profile %q requires the daemon-derived expected_git_url %q at args[1] (immediately after the subcommand, so it binds the git <repository> positional)", profile, cs.target.ExpectedGitURL)
		}
	}

	cmd := exec.Command(cmdPath, args...)

	env := NewSanitizedEnv()

	// Apply project environment
	if cs.project != nil {
		env.SetFromMap(cs.project.Env)
	}

	// Apply profile-specific sanitization
	p.apply(cs, env)

	cmd.Env = env.BuildEnv()

	// Set working directory to the target's repo path
	if cs.target != nil && cs.target.RepoPath != "" {
		cmd.Dir = cs.target.RepoPath
	}

	return cmd, nil
}

// ValidateCommandPath ensures the command path is safe
func ValidateCommandPath(path string) error {
	// Reject obviously dangerous patterns
	if strings.Contains(path, "..") {
		return fmt.Errorf("command path contains '..': %s", path)
	}
	if strings.HasPrefix(path, "-") {
		return fmt.Errorf("command path starts with '-': %s", path)
	}

	// Ensure the command exists and is executable
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("command not found: %s", path)
	}

	return nil
}
