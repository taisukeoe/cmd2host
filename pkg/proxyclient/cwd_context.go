// cwd_context.go: container-side collector for the cwd auto-resolve hint.
//
// CollectCwdContext shells out to `git rev-parse --show-toplevel` and
// `git remote get-url origin` in the process's current working directory
// and returns the pair as an operations.CwdContext for the daemon to
// AND-match against the project's allow list. A nil return means the
// hint is unavailable (cwd is not inside a git work tree, no `origin`
// remote, or git binary missing); callers send the request without the
// hint and the daemon falls back to the explicit flag / single-repo
// primary / error path.
//
// The collector intentionally returns nil rather than surfacing the
// underlying git error: any failure to read the cwd is exactly the case
// where the daemon's other resolution rules should take over, so a
// silent skip keeps the wrapper transparent. Diagnostic output stays
// inside the daemon's denial reason if the fallback ultimately fails.

package proxyclient

import (
	"os/exec"
	"strings"

	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// CollectCwdContext gathers the cwd's git toplevel and origin URL. Returns
// nil when either signal is unavailable so the caller can pass through to
// the daemon without an auto-resolve hint.
func CollectCwdContext() *operations.CwdContext {
	toplevel, ok := runGit("rev-parse", "--show-toplevel")
	if !ok || toplevel == "" {
		return nil
	}
	originURL, ok := runGit("remote", "get-url", "origin")
	if !ok || originURL == "" {
		// A repo without an `origin` remote cannot participate in the
		// AND-check; skip the hint entirely so the daemon sees the
		// same "no context" state as a non-git cwd.
		return nil
	}
	return &operations.CwdContext{
		Toplevel:  toplevel,
		OriginURL: originURL,
	}
}

// runGit invokes git with the given args in the current process's cwd
// and returns the trimmed stdout. The bool reports success (exit 0 and
// no exec error); stderr is discarded because every failure mode maps to
// the same "no hint" outcome.
func runGit(args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}
