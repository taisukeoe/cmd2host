// stdin.go isolates the os.Stdin Stat() check the early-reject path needs.
// Split into its own file so tests can stub the detector without
// touching CheckEarlyReject's caller-visible signature.

package proxyclient

import "os"

// DefaultIsStdinPiped reports whether os.Stdin appears to be carrying
// caller-provided data the wrapper would need to forward to the host.
//
// The proxy's request/response shape cannot forward stdin to the host
// process, so a caller that deliberately piped data in (e.g. `echo X |
// gh pr create --body-file -`) must be early-rejected. The detector is
// designed to **not** fire on the more common non-interactive launches
// where stdin is structurally non-TTY but carries no data — AI agent
// `Bash` tool invocations, CI step shells, systemd ExecStart, and
// `< /dev/null` redirects all fit that shape and should pass through to
// the host.
//
// Detection rules:
//   - Stat() failure → stdin treated as absent (likely closed fd; the
//     host command would see the same).
//   - Character device (TTY, /dev/null, /dev/zero, ...) → not piped data.
//   - Named pipe with Size() == 0 → empty pipe attached structurally by
//     the parent shell with no buffered data; treated as not-piped.
//     Real piped data (e.g. `echo X | proxy`) reports a non-zero Size()
//     after the kernel buffers the producer's write, so this rule
//     covers the AI-agent / CI / `< /dev/null` cases without missing
//     the genuine pipe case.
//   - Regular file with Size() == 0 → empty redirect (`< /dev/null` on
//     systems that surface it as a regular file); treated as not-piped.
//   - Everything else (non-empty pipe, non-empty regular file, socket,
//     etc.) → piped, early reject fires.
//
// Callers that explicitly want to bypass the check (for tooling that
// drives the proxy from a context where stdin is structurally pipe-like
// but carries no semantic input) should redirect stdin from `/dev/null`
// at launch; that is also documented in the README.
func DefaultIsStdinPiped() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	mode := info.Mode()
	if mode&os.ModeCharDevice != 0 {
		return false
	}
	if mode&os.ModeNamedPipe != 0 {
		return info.Size() > 0
	}
	if mode.IsRegular() {
		return info.Size() > 0
	}
	return true
}
