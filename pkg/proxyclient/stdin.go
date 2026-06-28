// stdin.go isolates the os.Stdin Stat() check the early-reject path needs.
// Split into its own file so tests can stub the detector without
// touching CheckEarlyReject's caller-visible signature.

package proxyclient

import "os"

// DefaultIsStdinPiped reports whether os.Stdin appears to be receiving
// caller data (a pipe, a regular file, or any non-character device).
//
// The check intentionally over-flags: any non-TTY stdin is treated as
// "caller is sending data we cannot forward to the host". When the
// process is launched with stdin already redirected (e.g. via a here-
// document, pipe, or shell redirection) the Mode() bits will lack
// ModeCharDevice and the early-reject path fires. When the process is
// launched interactively from a terminal the Mode() bits include
// ModeCharDevice and stdin is considered absent.
//
// Implementation notes:
//   - Stat() failure is treated as "stdin absent" (returns false) so a
//     deliberately closed stdin does not trigger early reject. The host
//     command will see no stdin either way.
//   - ModeNamedPipe alone is not sufficient because shells often launch
//     wrappers with /dev/null on stdin; that surfaces as a regular file
//     with size 0, not a named pipe.
func DefaultIsStdinPiped() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}
