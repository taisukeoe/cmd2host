// stdin.go isolates the os.Stdin Stat() check the early-reject path needs.
// Split into its own file so tests can stub the detector without
// touching CheckEarlyReject's caller-visible signature.

package proxyclient

import "os"

// DefaultIsStdinPiped reports whether os.Stdin appears to be carrying
// caller-provided data the wrapper would need to forward to the host.
//
// The proxy's request/response shape cannot forward stdin to the host
// process, so a caller that piped data in (e.g. `echo X | gh pr create
// --body-file -`) must be early-rejected. Detecting "data actually
// queued in a pipe" portably is harder than it looks — Unix `st_size`
// on pipes/FIFOs is reported as 0 regardless of buffered bytes, and a
// `FIONREAD` ioctl probe is platform-specific, racey, and not worth
// the v1 complexity. The conservative shape used here treats **any
// non-character-device stdin as piped**, which protects against silent
// stdin drop at the cost of false rejects in environments where stdin
// is structurally non-TTY but carries no semantic data (AI agent
// `Bash` tool invocations, CI step shells, systemd ExecStart). Those
// callers redirect stdin from `/dev/null` at launch
// (`gh pr view 42 < /dev/null`) — the README documents this as the
// explicit caller-side opt-out.
//
// Detection rules:
//   - Stat() failure → stdin treated as absent (likely closed fd; the
//     host command would see the same).
//   - Character device (TTY, /dev/null, /dev/zero, ...) → not piped.
//     `< /dev/null` therefore reliably bypasses the check.
//   - Everything else (pipe, FIFO, regular file, socket) → piped,
//     early reject fires.
func DefaultIsStdinPiped() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}
