package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// mkWorkspace creates a temporary directory to stand in for a session
// workspace (target.RepoPath). t.TempDir already returns an absolute,
// EvalSymlinks-resolvable path on every platform the daemon runs on.
func mkWorkspace(t *testing.T) string {
	t.Helper()
	// On macOS t.TempDir lives under /var/... which is itself a symlink to
	// /private/var. Resolving here mirrors what the resolver does to its
	// base so the test asserts against the canonical form.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(tempdir): %v", err)
	}
	return base
}

func TestResolveWorkspacePath_Accept(t *testing.T) {
	base := mkWorkspace(t)

	// Pre-create some structure the accept cases lean on.
	if err := os.MkdirAll(filepath.Join(base, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An in-workspace symlink whose target is also inside the workspace.
	if err := os.Symlink(filepath.Join(base, "logs"), filepath.Join(base, "logs-link")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   string
		want string // expected resolved absolute path
	}{
		{"relative_file", "out.log", filepath.Join(base, "out.log")},
		{"nested_existing_dir", "logs/out.log", filepath.Join(base, "logs", "out.log")},
		{"nested_nonexistent_dir", "a/b/c/out.log", filepath.Join(base, "a", "b", "c", "out.log")},
		{"dot", ".", base},
		{"dot_slash_prefix", "./out.log", filepath.Join(base, "out.log")},
		{"internal_dotdot_stays_inside", "logs/../out.log", filepath.Join(base, "out.log")},
		{"in_workspace_symlink_dir", "logs-link/out.log", filepath.Join(base, "logs", "out.log")},
		{"trailing_separator_normalized", "logs/", filepath.Join(base, "logs")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorkspacePath(base, tc.in)
			if err != nil {
				t.Fatalf("resolveWorkspacePath(%q, %q) unexpected error: %v", base, tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("resolveWorkspacePath(%q, %q) = %q, want %q", base, tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveWorkspacePath_Reject(t *testing.T) {
	base := mkWorkspace(t)

	// A directory outside the workspace, and a symlink inside the workspace
	// that points at it. Writing through the symlink must be rejected.
	outside := mkWorkspace(t)
	if err := os.Symlink(outside, filepath.Join(base, "escape-link")); err != nil {
		t.Fatal(err)
	}
	// A self-referential symlink: resolving through it fails with a
	// non-NotExist error, not "absent". The resolver must fail closed rather
	// than walk past the unresolved component and treat it lexically.
	if err := os.Symlink("selfloop", filepath.Join(base, "selfloop")); err != nil {
		t.Fatal(err)
	}
	// A dangling symlink pointing outside the workspace: the symlink entry
	// exists but its target does not, so resolving it reports NotExist even
	// though the component is present. Walking past it and treating it
	// lexically would let a later write follow the link outside the
	// workspace, so the resolver must reject it.
	if err := os.Symlink(filepath.Join(outside, "missing-target"), filepath.Join(base, "broken-link")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"nul_byte", "out\x00.log"},
		{"absolute", filepath.Join(outside, "evil.log")},
		{"absolute_root", "/etc/passwd"},
		{"dotdot_escape", "../evil.log"},
		{"dotdot_escape_nested", "a/../../evil.log"},
		{"tilde_prefix", "~/evil.log"},
		{"tilde_user_prefix", "~root/evil.log"},
		{"symlink_escape", "escape-link/evil.log"},
		{"symlink_loop", "selfloop/out.log"},
		{"broken_symlink_final", "broken-link"},
		{"broken_symlink_with_suffix", "broken-link/evil.log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorkspacePath(base, tc.in)
			if err == nil {
				t.Fatalf("resolveWorkspacePath(%q, %q) = %q, want error", base, tc.in, got)
			}
		})
	}
}

// TestResolveWorkspacePathParams covers the server-side step that rewrites
// workspace_path params in place: present values are confined and replaced
// with the resolved absolute path, non-workspace_path params are left
// untouched, an absent optional param is skipped, and an escaping value is
// rejected without mutating params.
func TestResolveWorkspacePathParams(t *testing.T) {
	base := mkWorkspace(t)
	op := &operations.Operation{
		Command:      "aws",
		ArgsTemplate: []string{"s3", "cp", "{src}", "{dest}"},
		Params: map[string]operations.ParamSchema{
			"src":  {Type: "string"},
			"dest": {Type: "workspace_path"},
			"opt":  {Type: "workspace_path", Optional: true},
		},
	}

	t.Run("present dest resolved, string param untouched, optional skipped", func(t *testing.T) {
		params := map[string]operations.ParamValue{
			"src":  "s3://bucket/log",
			"dest": "logs/out.log",
		}
		if err := resolveWorkspacePathParams(op, params, base); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if params["src"] != "s3://bucket/log" {
			t.Errorf("src mutated: %v", params["src"])
		}
		want := filepath.Join(base, "logs", "out.log")
		if params["dest"] != want {
			t.Errorf("dest = %v, want %v", params["dest"], want)
		}
		if _, ok := params["opt"]; ok {
			t.Errorf("absent optional param should not be added: %v", params["opt"])
		}
	})

	t.Run("escaping dest is rejected", func(t *testing.T) {
		params := map[string]operations.ParamValue{
			"src":  "s3://bucket/log",
			"dest": "../escape.log",
		}
		if err := resolveWorkspacePathParams(op, params, base); err == nil {
			t.Fatalf("expected error for escaping dest, got nil (params=%v)", params)
		}
	})
}

// TestResolveWorkspacePath_ContainmentIsCanonical asserts that the returned
// path is always inside the EvalSymlinks-resolved base, even when base is
// reached through a symlink (the macOS /var → /private/var shape).
func TestResolveWorkspacePath_ContainmentIsCanonical(t *testing.T) {
	real := mkWorkspace(t)
	// Build a symlink that points at the real workspace and hand THAT to the
	// resolver as base. The resolver must resolve base before containment so
	// the alias converges instead of registering as an escape.
	linkParent := mkWorkspace(t)
	aliasBase := filepath.Join(linkParent, "alias")
	if err := os.Symlink(real, aliasBase); err != nil {
		t.Fatal(err)
	}

	got, err := resolveWorkspacePath(aliasBase, "logs/out.log")
	if err != nil {
		t.Fatalf("resolveWorkspacePath(alias, ...) unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, real+string(filepath.Separator)) {
		t.Fatalf("resolved path %q is not under canonical base %q", got, real)
	}
}
