// workspace_path.go: resolver for the `workspace_path` parameter type.
//
// A `workspace_path` param carries a caller-supplied destination for a
// host-executed operation (e.g. the local file an `aws s3 cp` writes to).
// Because the daemon runs the command on the host, the value must be
// confined to the session workspace (the resolved target.RepoPath, which
// oneshot-agent binds to the container's workspace). resolveWorkspacePath
// turns a workspace-relative value into an absolute path proven to live
// under the workspace, or returns an error the caller surfaces as a
// denial.
//
// The guarantee is confinement computed from the filesystem state at
// resolution time: the value's deepest existing ancestor is resolved
// through symlinks and checked against the resolved workspace root, and
// the remaining (not-yet-existing) suffix is lexical-only. The resolver
// does not hand the running command a file descriptor, so it governs the
// path the command is given, not the command's own write behavior.

package daemon

import (
	"fmt"
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
// at or above base (via `..` or a symlink whose target is outside base).
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
func resolveExistingPrefix(path string) (string, error) {
	cur := path
	var suffix string
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if suffix == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, suffix), nil
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
