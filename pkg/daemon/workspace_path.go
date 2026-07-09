// workspace_path.go: resolver for the `workspace_path` parameter type.
//
// A `workspace_path` param carries a caller-supplied destination for a
// host-executed operation (e.g. the local file an `aws s3 cp` writes to).
// The daemon confines the value to the session workspace (the resolved
// target.RepoPath, which oneshot-agent binds to the container's workspace)
// and stages the output through a workspace-owned identity before placing
// it at the caller-supplied path.
//
// resolveWorkspacePath turns a workspace-relative value into an absolute
// path proven to live under the workspace, or returns an error the caller
// surfaces as a denial. Every param the operation declares as
// workspace_path then flows through the staging pipeline (see
// workspace_path_staging.go):
//
//   - Direction: v1 supports "output" only. Empty resolves to "output"
//     via ParamSchema.EffectiveDirection, keeping existing project
//     configs hash-stable; "input" is reserved for a future release.
//   - Shape: single foreground file per param. Operations whose template
//     or normalized flags carry a scope-expander flag (--recursive,
//     --sync, -R) are rejected by operations.Operation.ValidateNoScopeExpanders
//     before the staging pipeline runs.
//   - Placement: the daemon allocates a staging file under a hidden
//     .cmd2host-staging/ subtree (or the operator-configured explicit
//     root), passes the staging path to the child, and finalizes the
//     file at the caller-supplied path only after the child exits
//     successfully. Every step in the finalize walk uses O_NOFOLLOW so
//     the placement stays anchored to the workspace-owned root the
//     resolver validated. Finalize uses renameat, which replaces an
//     entry already present at the caller-supplied path (regardless of
//     whether the pre-existing entry is a regular file or a symlink) —
//     the daemon does not attempt to follow such an entry, only to
//     rename over its directory slot.
//   - Multiplicity: at most one workspace_path param per operation in
//     v1. CompilePatterns rejects an operation that declares more,
//     matching the "single foreground file" contract and keeping the
//     placement step atomic.
//   - Background writers, multi-file expansions, and daemonizing
//     children are out of contract; those shapes need a different
//     primitive.

package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// resolveWorkspacePathParams confines every parameter the operation
// declares with type "workspace_path" under base (the target's workspace
// root) and rewrites its value in params to the resolved absolute path.
// Params not present are skipped (an optional workspace_path may be
// omitted). A present value must be a string; the schema validation that
// ran earlier already enforces that, so a non-string here is a config /
// pipeline defect and is reported as such.
func resolveWorkspacePathParams(op *operations.Operation, params map[string]operations.ParamValue, base string) error {
	for name, schema := range op.Params {
		if schema.Type != "workspace_path" {
			continue
		}
		raw, present := params[name]
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("workspace_path param %q: expected string, got %T", name, raw)
		}
		resolved, err := resolveWorkspacePath(base, value)
		if err != nil {
			return err
		}
		params[name] = resolved
	}
	return nil
}

// resolveWorkspacePath confines value under base and returns the resolved
// absolute path. base is the session workspace root (target.RepoPath),
// already absolute and cleaned by config load. value is the
// caller-supplied workspace-relative path.
//
// Accepted: workspace-relative paths that resolve to a location at or
// under base, including not-yet-existing nested destinations and paths
// that traverse an in-workspace symlink whose target is also inside base.
//
// Rejected: empty values, values containing a NUL byte, absolute paths,
// values with a `~` prefix, and any value whose resolved location lands
// outside base (via `..` or a symlink whose target is outside base). The
// workspace root itself (value ".") resolves to base and is accepted.
func resolveWorkspacePath(base, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("workspace_path: value is empty")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("workspace_path: value contains a NUL byte")
	}
	if strings.HasPrefix(value, "~") {
		return "", fmt.Errorf("workspace_path: value must be workspace-relative, not %q", value)
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("workspace_path: value must be workspace-relative, not absolute: %q", value)
	}

	cleaned := filepath.Clean(value)
	// After Clean, an escaping value surfaces as a leading "..". Internal
	// traversal that stays inside (e.g. "logs/../out.log" -> "out.log")
	// has already collapsed and does not trip this check.
	if escapesUpward(cleaned) {
		return "", fmt.Errorf("workspace_path: value escapes the workspace: %q", value)
	}

	// Resolve the workspace root through symlinks so the containment
	// comparison is against the canonical directory (covers a symlinked
	// base and the macOS /var -> /private/var alias).
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("workspace_path: cannot resolve workspace root: %w", err)
	}

	candidate := filepath.Join(resolvedBase, cleaned)
	resolved, err := resolveExistingPrefix(candidate)
	if err != nil {
		return "", fmt.Errorf("workspace_path: cannot resolve %q: %w", value, err)
	}

	rel, err := filepath.Rel(resolvedBase, resolved)
	if err != nil {
		return "", fmt.Errorf("workspace_path: cannot compare %q against workspace root: %w", value, err)
	}
	if escapesUpward(rel) {
		return "", fmt.Errorf("workspace_path: value escapes the workspace: %q", value)
	}

	return resolved, nil
}

// escapesUpward reports whether a filepath.Clean'd or filepath.Rel'd path
// leaves its reference directory, i.e. it is ".." itself or begins with a
// "../" component.
func escapesUpward(p string) bool {
	return p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator))
}

// resolveExistingPrefix resolves the deepest existing ancestor of an
// absolute path through symlinks, then re-appends the trailing components
// that do not yet exist. The not-yet-existing suffix cannot contain
// symlinks (it does not exist), so re-joining it onto the resolved
// ancestor yields the canonical location the path would occupy once
// created. The walk terminates because filepath.Dir reaches the volume
// root, and in practice stops at base, which always exists.
//
// The walk advances to the parent only when the current component is truly
// absent. Any other EvalSymlinks error is returned as-is: such a component
// is present but unresolvable (permission, too many symlink levels), and
// treating it lexically would skip the symlink resolution the containment
// check depends on. A NotExist result is disambiguated with os.Lstat,
// because a dangling symlink (the entry exists, its target does not) also
// reports NotExist — walking past it would let a later write follow the
// link outside the workspace. Both cases fail closed, keeping the
// resolve-then-check guarantee.
func resolveExistingPrefix(path string) (string, error) {
	cur := path
	var suffix string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if suffix == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, suffix), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		// EvalSymlinks reported NotExist. Only treat cur as absent (and walk
		// up) when the entry itself does not exist; a present-but-dangling
		// symlink is unresolvable and must fail closed.
		if _, lerr := os.Lstat(cur); lerr == nil {
			return "", fmt.Errorf("unresolvable path component %q", cur)
		} else if !errors.Is(lerr, fs.ErrNotExist) {
			return "", lerr
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor for %q", path)
		}
		name := filepath.Base(cur)
		if suffix == "" {
			suffix = name
		} else {
			suffix = filepath.Join(name, suffix)
		}
		cur = parent
	}
}
