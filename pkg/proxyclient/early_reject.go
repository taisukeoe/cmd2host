// early_reject.go enforces the input-shape boundary of cmd2host's raw-argv
// transparent proxy.
//
// The raw-argv path proxies a one-shot (argv → response) execution. It
// does NOT forward stdin, file:// payloads, or TTY-bound interactive
// subcommands; users running such commands must reach the host via the
// MCP route instead. The checks here run on the container side, before
// the daemon ever sees the request, so the rejection message can name
// the user-facing command (e.g. "gh", "aws") rather than the resolved
// operation_id.

package proxyclient

import (
	"fmt"
	"strings"
)

// EarlyRejectReason wraps a structured early-reject error so the dispatch
// layer can map it to the cmd2host: <reason> exit format without
// string-matching.
type EarlyRejectReason struct {
	Kind    string // "stdin" | "file_uri" | "tty_required"
	Detail  string // free-form caller-facing detail (argv token, subcommand)
	Message string // composed message body for the cmd2host: prefix
}

func (e *EarlyRejectReason) Error() string {
	return "cmd2host: " + e.Message + "; run mcp__cmd2host__cmd2host_list_operations to discover supported operations"
}

// StdinAttached reports whether stdin appears to carry caller data.
// IsStdinPiped is injected so tests can stub the os.Stdin Stat() check;
// production callers pass proxyclient.DefaultIsStdinPiped.
type StdinDetector func() bool

// CheckEarlyReject runs the three early-reject checks against the
// container-side invocation:
//
//  1. Stdin attached: raw-argv mode is request/response; piped stdin
//     cannot be forwarded to the host process.
//  2. file:// argv value: AWS CLI's `--cli-input-json file://...` and
//     similar forms reference host filesystem paths the daemon would
//     interpret in its own filesystem context, which silently mismatches
//     the user's intent.
//  3. TTY-required subcommand: commands that need interactive terminal
//     I/O on the host (aws configure, aws sso login, aws ecs
//     execute-command) cannot run through a one-shot request/response.
//
// command is the basename of argv[0] (e.g. "gh", "git", "aws"). argv is
// the remainder. Returns a non-nil *EarlyRejectReason when any check
// trips; the dispatch layer formats the cmd2host: prefix and exits.
func CheckEarlyReject(command string, argv []string, isStdinPiped StdinDetector) *EarlyRejectReason {
	if isStdinPiped != nil && isStdinPiped() {
		return &EarlyRejectReason{
			Kind:    "stdin",
			Detail:  command,
			Message: fmt.Sprintf("raw-argv mode does not forward stdin to host; %s reads from stdin", command),
		}
	}
	for _, tok := range argv {
		// Match only URL-shaped tokens so natural-language `file://`
		// mentions inside a `--body` / `--title` / commit-message value
		// pass through unscathed. The forms we want to catch are:
		//   - standalone URL token (`file:///etc/passwd` after `--cli-input-json`)
		//   - the `flag=value` joined form (`--cli-input-json=file:///etc/passwd`)
		//   - AWS CLI's binary variant `fileb://...` (and its `=fileb://` joined form),
		//     which the AWS CLI documents as reading host-side bytes from
		//     the path and is interpreted in the daemon's filesystem context
		//     just like `file://`.
		if strings.HasPrefix(tok, "file://") || strings.Contains(tok, "=file://") ||
			strings.HasPrefix(tok, "fileb://") || strings.Contains(tok, "=fileb://") {
			return &EarlyRejectReason{
				Kind:    "file_uri",
				Detail:  tok,
				Message: fmt.Sprintf("raw-argv mode rejects file:// and fileb:// arguments (container vs host filesystem mismatch); offending token: %q", tok),
			}
		}
	}
	if sub := matchTTYRequiredSubcommand(command, argv); sub != "" {
		return &EarlyRejectReason{
			Kind:    "tty_required",
			Detail:  sub,
			Message: fmt.Sprintf("subcommand %q requires interactive TTY and is not supported via raw-argv mode", sub),
		}
	}
	return nil
}

// matchTTYRequiredSubcommand looks up command + argv tokens against the
// hard-coded TTY-required list. Leading global flags are tolerated:
// `aws --region us-east-1 configure` and `aws --debug configure` both
// match against `aws configure` because the matcher tries each plausible
// interpretation of a leading flag (flag-only vs flag-with-value) before
// giving up.
//
// The list is intentionally minimum (only the high-confidence cases
// that cannot work over a one-shot request/response) so callers do not
// lose access to less problematic commands.
//
// Returns the matched subcommand path (e.g. "aws configure") or "" when
// nothing matches.
func matchTTYRequiredSubcommand(command string, argv []string) string {
	for _, entry := range ttyRequiredSubcommands {
		if entry.command != command {
			continue
		}
		if matchSubcommandFromAnyAnchor(argv, entry.subcommand, 0) {
			return command + " " + strings.Join(entry.subcommand, " ")
		}
	}
	return ""
}

// matchSubcommandFromAnyAnchor walks argv from start, treating each
// flag-shaped token (starts with "-") as a potential global-flag prefix
// that may or may not consume the next argv token as its value. At each
// candidate non-flag anchor (or at the start when no leading flag), it
// checks whether subcommand matches consecutively from there. Returns
// true on the first match.
//
// The "-flag=value" form consumes exactly one argv slot. The "-flag" form
// is tried both ways: flag-only (advance 1) and flag-with-value
// (advance 2). The recursion depth is bounded by the number of leading
// flags in argv.
func matchSubcommandFromAnyAnchor(argv, subcommand []string, start int) bool {
	if start+len(subcommand) > len(argv) {
		return false
	}
	if !strings.HasPrefix(argv[start], "-") {
		for i, tok := range subcommand {
			if argv[start+i] != tok {
				return false
			}
		}
		return true
	}
	if strings.Contains(argv[start], "=") {
		return matchSubcommandFromAnyAnchor(argv, subcommand, start+1)
	}
	return matchSubcommandFromAnyAnchor(argv, subcommand, start+1) ||
		matchSubcommandFromAnyAnchor(argv, subcommand, start+2)
}

type ttyRequiredEntry struct {
	command    string
	subcommand []string
}

// v1 minimum list. Entries here MUST be commands whose normal usage
// requires interactive terminal I/O on the host (credential prompts,
// long-lived shell session, etc.). Expanding this list is a behaviour
// change — add cases only when the one-shot request/response shape is
// genuinely impossible, not when it is merely awkward.
var ttyRequiredSubcommands = []ttyRequiredEntry{
	{command: "aws", subcommand: []string{"configure"}},
	{command: "aws", subcommand: []string{"sso", "login"}},
	{command: "aws", subcommand: []string{"ecs", "execute-command"}},
}
