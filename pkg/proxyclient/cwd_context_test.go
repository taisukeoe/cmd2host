package proxyclient

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestRunGit_TimesOutOnHungGit installs a fake `git` binary that blocks
// past cwdGitTimeout and confirms runGit returns the silent-skip pair
// (empty string, false) well inside the deadline. Guards the C2
// hardening — without exec.CommandContext + cwdGitTimeout the whole
// wrapper startup (CollectCwdContext) would stall on any hung git.
func TestRunGit_TimesOutOnHungGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git shim assumes POSIX shell semantics")
	}

	fakeDir := t.TempDir()
	fakePath := filepath.Join(fakeDir, "git")
	// The stub blocks forever so any successful return from runGit
	// definitely came from the deadline, not from the subprocess exiting.
	const script = "#!/bin/sh\nsleep 60\n"
	if err := os.WriteFile(fakePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", fakeDir)

	prev := cwdGitTimeout
	cwdGitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { cwdGitTimeout = prev })

	start := time.Now()
	out, ok := runGit("rev-parse", "--show-toplevel")
	elapsed := time.Since(start)

	if ok {
		t.Errorf("runGit must return ok=false when git hangs; got ok=true out=%q", out)
	}
	if out != "" {
		t.Errorf("runGit must return empty stdout on timeout; got %q", out)
	}
	// Margin generous enough for goroutine scheduling on loaded CI but far
	// below the 60s the stub would otherwise wait.
	if elapsed > 5*time.Second {
		t.Errorf("runGit returned but took %v; deadline (%v) did not cut off the hung subprocess", elapsed, cwdGitTimeout)
	}
}

// TestRunGit_ReturnsSubprocessOutputWithinDeadline is the positive
// counterpart: a fast fake git that exits with the expected stdout must
// still succeed with the shortened deadline, so the timeout hardening
// does not regress the happy path.
func TestRunGit_ReturnsSubprocessOutputWithinDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git shim assumes POSIX shell semantics")
	}

	fakeDir := t.TempDir()
	fakePath := filepath.Join(fakeDir, "git")
	const script = "#!/bin/sh\nprintf '/tmp/toplevel\\n'\n"
	if err := os.WriteFile(fakePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", fakeDir)

	prev := cwdGitTimeout
	cwdGitTimeout = 500 * time.Millisecond
	t.Cleanup(func() { cwdGitTimeout = prev })

	out, ok := runGit("rev-parse", "--show-toplevel")
	if !ok {
		t.Fatalf("runGit ok=false on a fast fake git; wanted ok=true")
	}
	if out != "/tmp/toplevel" {
		t.Errorf("runGit stdout = %q, want %q", out, "/tmp/toplevel")
	}
}
