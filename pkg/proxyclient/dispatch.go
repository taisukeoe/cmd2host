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
//     - 141          : caller closed our stdout (SIGPIPE equivalent;
//                      mirrors what a native host CLI would exit when
//                      its stdout reader closed early)
//     - 200          : daemon connectivity / protocol failure
//     - 201          : token file read failure
//     - 220          : daemon-side denial (unknown operation, ambiguous
//                      reverse-match, validation failure, consistency
//                      check failure)
//     - 230          : container-side early reject (stdin attached,
//                      file:// or fileb:// argv, TTY-required
//                      subcommand)
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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// Exit codes for non-passthrough cases. See file comment.
const (
	ExitInfrastructure = 200
	ExitTokenRead      = 201
	ExitDenied         = 220
	ExitEarlyReject    = 230
	// ExitSIGPIPE matches the conventional 128 + signal-number for
	// SIGPIPE (13 on Unix). Returned when the wrapper's stdout has
	// been closed by an upstream reader (`gh pr list | head -1` and
	// similar pipelines). Native host commands exit with this code
	// when they receive SIGPIPE; the proxy buffers the full daemon
	// response, so an explicit map keeps pipeline-aware tooling in
	// sync with what the host CLI would have produced.
	ExitSIGPIPE = 141
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
	// the request acts on. Empty defers resolution to the daemon's
	// cwd auto-resolve fallback (when CwdContext is non-nil) or the
	// single-repo primary default.
	TargetRepo string

	// CwdContext carries the caller's cwd-derived auto-resolve hint
	// (git toplevel + origin URL). Wired explicitly so callers can
	// inject a deterministic context in tests; production callers
	// pass CollectCwdContext() for the natural cwd-based resolve. Nil
	// disables the hint and the daemon falls back to TargetRepo or
	// the single-repo primary.
	CwdContext *operations.CwdContext

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

	resp, err := opts.Client.SendRawArgv(opts.Command, opts.Argv, opts.TargetRepo, opts.CwdContext)
	if err != nil {
		writeWrapperError(stderr, fmt.Sprintf("daemon request failed: %v", err))
		return ExitInfrastructure
	}

	if resp.DeniedReason != nil {
		writeWrapperError(stderr, *resp.DeniedReason)
		return ExitDenied
	}

	if resp.Stdout != "" {
		if _, err := io.WriteString(stdout, resp.Stdout); err != nil {
			if errors.Is(err, syscall.EPIPE) {
				// Match what a native host CLI would see when its
				// stdout reader closed early (e.g. `gh pr list | head
				// -1`): receive SIGPIPE, exit 128+13. The wrapper
				// buffered the full daemon response, so map the write
				// failure to the same exit code so pipeline-aware
				// tooling sees the same outcome it would have without
				// the proxy.
				return ExitSIGPIPE
			}
			// Other write failures (closed FD, disk full, etc.) are
			// rare and surface as infrastructure failure so the caller
			// distinguishes them from a host-command exit.
			writeWrapperError(stderr, fmt.Sprintf("write stdout: %v", err))
			return ExitInfrastructure
		}
	}
	if resp.Stderr != "" {
		// Best-effort: if stderr itself is unavailable there is nowhere
		// to surface the failure, so we deliberately drop the result.
		_, _ = io.WriteString(stderr, resp.Stderr)
	}
	// Surface daemon-side truncation flags on stderr so a caller piping
	// stdout into a streaming JSON / NDJSON consumer sees that the body
	// is incomplete. The daemon's stream bodies are now clean prefixes
	// (no synthetic suffix), so without this notice the proxy would
	// silently drop the truncation signal compared to the MCP route,
	// which emits a similar indicator outside its fenced block.
	if resp.StdoutTruncated {
		fmt.Fprintf(stderr, "cmd2host: stdout truncated by host daemon (shown %d of %d bytes)\n", len(resp.Stdout), resp.StdoutOriginalBytes)
	}
	if resp.StderrTruncated {
		fmt.Fprintf(stderr, "cmd2host: stderr truncated by host daemon (shown %d of %d bytes)\n", len(resp.Stderr), resp.StderrOriginalBytes)
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
