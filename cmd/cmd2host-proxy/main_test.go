package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/taisukeoe/cmd2host/pkg/proxyclient"
)

// TestRun_DirectVersionExitsZero pins the documented `cmd2host-proxy --version`
// contract: when the wrapper is invoked directly with the version flag, it
// must print the version to stdout and exit 0 ahead of the missing-host-command
// check.
func TestRun_DirectVersionExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run([]string{"cmd2host-proxy", "--version"}, &stdout, &stderr)

	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if !strings.Contains(stdout.String(), "cmd2host-proxy version") {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), "cmd2host-proxy version")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on --version, got %q", stderr.String())
	}
}

// TestRun_DirectMissingCommandReturnsInfra pins the existing direct-form
// "no host command supplied" path: still exits with the infrastructure
// band (200) and writes the usage message to stderr, even after the
// --version short-circuit was added above.
func TestRun_DirectMissingCommandReturnsInfra(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run([]string{"cmd2host-proxy"}, &stdout, &stderr)

	if exit != proxyclient.ExitInfrastructure {
		t.Errorf("exit = %d, want %d", exit, proxyclient.ExitInfrastructure)
	}
	if !strings.Contains(stderr.String(), "missing host command") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "missing host command")
	}
}

// TestRun_SymlinkInvocationDoesNotConsumeHostVersionFlag pins symlink form:
// when invoked as `gh --version` (typical PATH layout has /usr/local/bin/gh
// → cmd2host-proxy), the wrapper must NOT print its own version — `--version`
// belongs to the host command and must reach the daemon (or fail with a
// host-side outcome). The proxy version string must be absent from stdout.
//
// This test depends on the token file path being unreadable in the test
// environment (no daemon, no token), which causes dispatch to exit with
// ExitTokenRead (201) before any network call. That confirms control
// reached the dispatch path rather than the wrapper's own --version
// short-circuit.
func TestRun_SymlinkInvocationDoesNotConsumeHostVersionFlag(t *testing.T) {
	// Force the token file lookup to fail by pointing at a non-existent
	// path so dispatch deterministically exits ExitTokenRead before any
	// network attempt, without depending on machine-local state.
	t.Setenv("HOST_CMD_PROXY_TOKEN_FILE", filepath.Join(t.TempDir(), "no-such-token"))

	var stdout, stderr bytes.Buffer
	exit := run([]string{"/usr/local/bin/gh", "--version"}, &stdout, &stderr)

	if exit != proxyclient.ExitTokenRead {
		t.Errorf("exit = %d, want %d (symlink form should reach dispatch's token-load path)", exit, proxyclient.ExitTokenRead)
	}
	if strings.Contains(stdout.String(), "cmd2host-proxy version") {
		t.Errorf("stdout = %q, must not contain proxy version on symlink invocation", stdout.String())
	}
	if !strings.Contains(stderr.String(), "cmd2host:") {
		t.Errorf("stderr = %q, want a cmd2host: prefix from the token-read failure", stderr.String())
	}
}

// Ensure the package compiles its os import — Go's unused-import rule
// would otherwise hide a future stray edit. The import survives because
// it is used in run()'s real callers; this is a defensive no-op assert.
var _ = os.Stdout
