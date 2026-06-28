// dispatch.go is the high-level entry point for the cmd2host-proxy binary.
// It composes (a) early-reject checks, (b) the daemon client call, and
// (c) exit-code mapping so the binary's main() stays a thin
// flag/env/exit wrapper.
//
// Exit code policy (v1):
//
//   Passthrough (host command's actual exit, full Unix 0..255 range):
//     - 0..127       : command-defined success / failure surfaced by
//                      the underlying gh / aws / ... (whichever
//                      auth-heavy CLI the project config exposes)
//     - 128..143     : process killed by signal n (128+n; e.g. a host
//                      command killed by SIGPIPE exits 141 = 128+13)
//     - 144..255     : other command-defined exits in the upper Unix
//                      range (CLIs commonly use 254/255 for transport
//                      or generic failure)
//
//   Proxy-originated (reserved high codes):
//     - 200          : daemon connectivity / protocol failure
//     - 201          : token file read failure
//     - 220          : daemon-side denial (unknown operation, ambiguous
//                      reverse-match, validation failure, consistency
//                      check failure)
//     - 230          : container-side early reject (stdin attached,
//                      file:// argv, TTY-required subcommand)
//
// Numeric collision: a host command that explicitly exits with a code
// the proxy also reserves (e.g. a custom CLI returns 200) surfaces as
// the same integer the proxy uses. The proxy distinguishes its own
// outcomes from passthrough by always writing a `cmd2host:` prefix on
// stderr (carrying the daemon's DeniedReason or a local diagnostic),
// while genuine host command output passes through with whatever stderr
// the host CLI wrote. Callers that need a robust contract should
// inspect stderr or treat the `cmd2host:` prefix as the authoritative
// signal for proxy-originated outcomes.

package proxyclient

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Exit codes for non-passthrough cases. See file comment.
const (
	ExitInfrastructure = 200
	ExitTokenRead      = 201
	ExitDenied         = 220
	ExitEarlyReject    = 230
)

// Options bundles the inputs the cmd2host-proxy binary collects from
// argv, env, and flags into a single struct so Dispatch can be called
// from tests without mocking process state.
type Options struct {
	// Command is the basename of argv[0] (e.g. "gh", "git", "aws"). The
	// dispatcher does not basename the path itself so callers can
	// inject any name they want during tests.
	Command string

	// Argv is argv[1:] — the arguments the user typed, in order. Empty
	// is valid (e.g. invoking "git" alone).
	Argv []string

	// Client is the configured daemon client. Caller resolves
	// host/port/socket from env or flags before invoking Dispatch.
	Client *Client

	// TargetRepo selects which repo (from the project's allow list)
	// the request acts on. Empty defaults to the project's primary
	// repo on the daemon side.
	TargetRepo string

	// Stdout / Stderr default to os.Stdout / os.Stderr when nil.
	// Wired explicitly for testability.
	Stdout io.Writer
	Stderr io.Writer

	// IsStdinPiped overrides the default os.Stdin Stat() check. Nil
	// uses DefaultIsStdinPiped.
	IsStdinPiped StdinDetector
}

// Dispatch runs the early-reject checks, sends the raw-argv request to
// the daemon, copies the response streams onto the caller's stdout /
// stderr, and returns the exit code per the v1 policy in the file
// comment. It never panics; all error paths funnel through here.
func Dispatch(opts Options) int {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	stdinCheck := opts.IsStdinPiped
	if stdinCheck == nil {
		stdinCheck = DefaultIsStdinPiped
	}

	if opts.Command == "" {
		// Should not happen — main() always passes filepath.Base(argv[0]).
		writeWrapperError(stderr, "internal: empty command (caller bug)")
		return ExitInfrastructure
	}
	if opts.Client == nil {
		writeWrapperError(stderr, "internal: missing daemon client (caller bug)")
		return ExitInfrastructure
	}

	if reason := CheckEarlyReject(opts.Command, opts.Argv, stdinCheck); reason != nil {
		fmt.Fprintln(stderr, reason.Error())
		return ExitEarlyReject
	}

	resp, err := opts.Client.SendRawArgv(opts.Command, opts.Argv, opts.TargetRepo)
	if err != nil {
		writeWrapperError(stderr, fmt.Sprintf("daemon request failed: %v", err))
		return ExitInfrastructure
	}

	if resp.DeniedReason != nil {
		writeWrapperError(stderr, *resp.DeniedReason)
		return ExitDenied
	}

	if resp.Stdout != "" {
		_, _ = io.WriteString(stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = io.WriteString(stderr, resp.Stderr)
	}
	return resp.ExitCode
}

// writeWrapperError formats wrapper-originated diagnostic text with the
// canonical "cmd2host: ..." prefix and the MCP discovery hint. Daemon
// denial reasons that already carry the cmd2host: prefix (from
// reverse-match) are passed through verbatim.
func writeWrapperError(w io.Writer, msg string) {
	body := msg
	if !strings.HasPrefix(body, "cmd2host:") {
		body = "cmd2host: " + body
	}
	suffix := "; run mcp__cmd2host__cmd2host_list_operations to discover supported operations"
	if !strings.Contains(body, "mcp__cmd2host__cmd2host_list_operations") {
		body += suffix
	}
	fmt.Fprintln(w, body)
}

// CommandFromArg0 normalizes an argv[0] (which may be an absolute path
// or a relative path from execve) to a bare basename suitable for the
// command argument of Dispatch / Client.SendRawArgv. The daemon-side
// reverse-match basename-normalizes both sides too, so this is a small
// belt to the daemon's suspenders.
func CommandFromArg0(arg0 string) string {
	return filepath.Base(arg0)
}
