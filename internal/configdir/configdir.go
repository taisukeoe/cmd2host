// Package configdir resolves the base directory for cmd2host's mutable
// state: daemon config, per-project config, token store, and the default
// UDS socket.
package configdir

import (
	"os"
	"path/filepath"
)

// Dir returns the cmd2host base directory.
//
// Resolution order:
//  1. $CMD2HOST_CONFIG_DIR (per-session override)
//  2. $HOME/.cmd2host (legacy default)
//
// Returns an error only when os.UserHomeDir fails AND no env override is
// set, so callers can preserve their original diagnostics. Callers that
// prefer the legacy "empty path → treated as missing config" semantics
// collapse the error themselves.
func Dir() (string, error) {
	if dir := os.Getenv("CMD2HOST_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cmd2host"), nil
}
