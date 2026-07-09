// workspace_path_staging.go: single-foreground-file staging for
// workspace_path parameters.
//
// A workspace_path parameter carries a caller-supplied destination for a
// host command's single-foreground-file output. This file adds the daemon
// side of the staged pipeline:
//
//  1. allocateStaging opens a fresh file under a daemon-managed staging
//     root, using O_NOFOLLOW|O_CREAT|O_EXCL|O_WRONLY|O_CLOEXEC so the
//     created path is a brand-new regular file. An fstat with Nlink == 1
//     confirms the entry has no other names before the daemon closes the
//     descriptor and hands the staging path to the child.
//  2. finalizeStaging walks from the workspace root down to the final
//     parent one component at a time using openat with O_NOFOLLOW, and
//     places the staged file at the final path with unix.Renameat. The
//     per-component openat rejects a symlink at any ancestor level.
//  3. cleanupStaging tears down staged files best-effort when the child
//     fails, the finalize step fails, or the guard rejects an operation.
//
// A per-request internal stagingID (crypto/rand hex) identifies the
// staging subtree so two requests with the same caller-supplied
// request_id cannot collide on the filesystem, and so periodic GC has a
// stable name to key on. Params are placed in ordinal slot directories
// (0, 1, ...) derived from sorted param name order rather than the raw
// param name, because param names come from operator-authored project
// config that is not currently shape-validated the way template
// placeholders are.
//
// Contract: single-foreground-file output only. Operations that spawn a
// background writer, expand to multiple files, or fold both input and
// output into the same argv are rejected upstream by
// operations.ValidateNoScopeExpanders (v1) and by the ParamSchema
// direction check ("input" unsupported in v1). Long-running or
// daemonizing children are out of contract.
package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// stagingRootDirName is the hidden directory name the "workspace" staging
// mode places under target.RepoPath. Kept private so callers cannot
// override the on-disk name in a way that would drift from
// workspace_path.go's docstring.
const stagingRootDirName = ".cmd2host-staging"

// stagingGCMinAge is the mtime cutoff the periodic GC uses when sweeping
// staging subtrees at runtime. Staging IDs still tracked in the active
// registry are always skipped; the cutoff only decides whether an
// unregistered (crash-orphaned) subtree is old enough to remove.
const stagingGCMinAge = time.Hour

// safeDirOpenFlags is the flag combination the staging pipeline uses
// every time it opens a directory during the finalize walk. Kept as one
// constant so the three call sites (workspace root, one-component-per
// walk step, staging parent) cannot drift in what they accept.
const safeDirOpenFlags = unix.O_NOFOLLOW | unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC

// stagingPlan is the daemon-side view of every workspace_path parameter
// the current request will stage. It carries the resolved workspace root,
// the internal stagingID (so periodic GC and cleanup key on the same
// identifier), and the per-parameter entries.
type stagingPlan struct {
	stagingID     string
	workspaceRoot string
	stagingRoot   string
	entries       []stagingEntry
}

// stagingEntry records the parameters the daemon needs to finalize a
// single staged output file. paramName is retained for audit logging;
// slot is the ordinal directory used on disk.
type stagingEntry struct {
	paramName   string
	slot        int
	stagingPath string
	finalPath   string
}

// stagingRegistry tracks in-flight stagingIDs so the periodic GC does not
// remove a subtree that a live request is about to finalize. Guarded by a
// plain mutex — the map is touched only at allocate / finalize / cleanup
// / GC boundaries so contention is negligible.
type stagingRegistry struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func newStagingRegistry() *stagingRegistry {
	return &stagingRegistry{active: make(map[string]struct{})}
}

// Register marks stagingID as in-flight. Callers must pair a successful
// Register with a Release regardless of the request outcome.
func (r *stagingRegistry) Register(stagingID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[stagingID] = struct{}{}
}

// Release removes stagingID from the in-flight set.
func (r *stagingRegistry) Release(stagingID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, stagingID)
}

// IsActive reports whether stagingID is currently in-flight.
func (r *stagingRegistry) IsActive(stagingID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[stagingID]
	return ok
}

// newStagingID returns a 16-byte hex-encoded identifier for a staging
// subtree. Uses crypto/rand so an operator running two requests
// concurrently under the same caller-supplied request_id (which is
// optional and not required to be unique) still gets disjoint directory
// names on disk.
func newStagingID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("workspace_path staging: crypto/rand failed: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// resolveStagingRoot returns the absolute path the daemon uses as the
// staging root for the current request. The workspace mode places the
// root inside the target's repo path (giving same-device rename by
// default). The explicit mode uses the operator-supplied absolute path
// verbatim.
func resolveStagingRoot(cfg *config.DaemonConfig, workspaceRoot string) (string, error) {
	switch cfg.EffectiveStagingMode() {
	case config.StagingModeWorkspace:
		if workspaceRoot == "" {
			return "", fmt.Errorf("workspace_path staging: workspace root is empty")
		}
		return filepath.Join(workspaceRoot, stagingRootDirName), nil
	case config.StagingModeExplicit:
		root := cfg.EffectiveStagingRoot()
		if root == "" {
			return "", fmt.Errorf("workspace_path staging: explicit mode requires a configured root")
		}
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("workspace_path staging: explicit mode root %q must be absolute", root)
		}
		return root, nil
	default:
		return "", fmt.Errorf("workspace_path staging: unknown mode %q", cfg.EffectiveStagingMode())
	}
}

// planStaging allocates a staging file for every workspace_path
// parameter the operation declares and rewrites the params map so
// BuildArgs sees the staging path. The returned plan carries every
// artifact the finalize / cleanup helpers need; a nil plan means the
// operation had no workspace_path params and the pipeline reduces to a
// no-op for this request.
//
// On any error, planStaging cleans up whatever it has already allocated
// so callers do not have to handle partial state.
func planStaging(
	cfg *config.DaemonConfig,
	registry *stagingRegistry,
	op *operations.Operation,
	params map[string]operations.ParamValue,
	workspaceRoot string,
) (*stagingPlan, error) {
	names := workspacePathParamNames(op)
	if len(names) == 0 {
		return nil, nil
	}

	stagingRoot, err := resolveStagingRoot(cfg, workspaceRoot)
	if err != nil {
		return nil, err
	}

	stagingID, err := newStagingID()
	if err != nil {
		return nil, err
	}

	plan := &stagingPlan{
		stagingID:     stagingID,
		workspaceRoot: workspaceRoot,
		stagingRoot:   stagingRoot,
	}

	registry.Register(stagingID)

	for slot, name := range names {
		raw, present := params[name]
		if !present {
			continue
		}
		finalPath, ok := raw.(string)
		if !ok {
			cleanupStaging(plan)
			registry.Release(stagingID)
			return nil, fmt.Errorf("workspace_path staging: param %q resolved to non-string %T", name, raw)
		}
		entry, err := allocateStaging(stagingRoot, stagingID, slot, name, finalPath)
		if err != nil {
			cleanupStaging(plan)
			registry.Release(stagingID)
			return nil, err
		}
		plan.entries = append(plan.entries, entry)
		params[name] = entry.stagingPath
	}

	if len(plan.entries) == 0 {
		registry.Release(stagingID)
		return nil, nil
	}

	return plan, nil
}

// workspacePathParamNames returns the workspace_path parameter names in
// deterministic (sorted) order. Sorting gives every request a stable
// slot-to-name mapping so audit logs and tests read the same way across
// runs.
func workspacePathParamNames(op *operations.Operation) []string {
	var names []string
	for name, schema := range op.Params {
		if schema.Type == "workspace_path" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// allocateStaging creates the staging file for one workspace_path
// parameter under `<stagingRoot>/<stagingID>/<slot>/staged`. It returns
// an entry describing the on-disk paths so the finalize / cleanup steps
// can operate without re-deriving them.
//
// The staging file is created with O_NOFOLLOW|O_CREAT|O_EXCL|O_WRONLY so
// the entry must be brand new; O_CLOEXEC keeps the descriptor from
// leaking into the child, and the descriptor is closed as soon as fstat
// confirms Nlink == 1. The child does its own open() on the returned
// path.
func allocateStaging(stagingRoot, stagingID string, slot int, paramName, finalPath string) (stagingEntry, error) {
	slotDir := filepath.Join(stagingRoot, stagingID, strconv.Itoa(slot))
	if err := os.MkdirAll(slotDir, 0o700); err != nil {
		return stagingEntry{}, fmt.Errorf("workspace_path staging: mkdir slot %d: %w", slot, err)
	}

	stagingPath := filepath.Join(slotDir, "staged")
	fd, err := unix.Open(
		stagingPath,
		unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return stagingEntry{}, fmt.Errorf("workspace_path staging: open %q: %w", stagingPath, err)
	}

	var st unix.Stat_t
	if ferr := unix.Fstat(fd, &st); ferr != nil {
		_ = unix.Close(fd)
		_ = os.Remove(stagingPath)
		return stagingEntry{}, fmt.Errorf("workspace_path staging: fstat %q: %w", stagingPath, ferr)
	}
	if st.Nlink != 1 {
		_ = unix.Close(fd)
		_ = os.Remove(stagingPath)
		return stagingEntry{}, fmt.Errorf("workspace_path staging: link count %d != 1 for %q", st.Nlink, stagingPath)
	}
	if cerr := unix.Close(fd); cerr != nil {
		_ = os.Remove(stagingPath)
		return stagingEntry{}, fmt.Errorf("workspace_path staging: close %q: %w", stagingPath, cerr)
	}

	return stagingEntry{
		paramName:   paramName,
		slot:        slot,
		stagingPath: stagingPath,
		finalPath:   finalPath,
	}, nil
}

// finalizeStaging places every staged file at its final path via
// unix.Renameat, opening the final parent directory through a fresh walk
// from workspaceRoot using per-component openat with O_NOFOLLOW. Any
// component that resolves through a symlink is rejected because a walk
// step with O_NOFOLLOW on a symlink fails at the kernel level.
func finalizeStaging(plan *stagingPlan, registry *stagingRegistry) error {
	if plan == nil || len(plan.entries) == 0 {
		return nil
	}
	defer registry.Release(plan.stagingID)

	for _, entry := range plan.entries {
		if err := finalizePlacement(plan.workspaceRoot, entry); err != nil {
			return fmt.Errorf("workspace_path staging: finalize param %q: %w", entry.paramName, err)
		}
	}

	// Sweep the per-request subtree once every entry is in place.
	requestDir := filepath.Join(plan.stagingRoot, plan.stagingID)
	_ = os.RemoveAll(requestDir)
	return nil
}

// finalizePlacement covers a single staged file: it opens the staging
// parent and the final parent through the safe walk, then invokes
// Renameat. Both descriptors are closed before returning even on the
// success path so caller code does not have to handle fd lifetime.
func finalizePlacement(workspaceRoot string, entry stagingEntry) error {
	rel, err := workspaceRelative(workspaceRoot, entry.finalPath)
	if err != nil {
		return err
	}

	finalParentFd, closeFinal, err := openWorkspaceSubtree(workspaceRoot, filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer closeFinal()

	stagingParentDir := filepath.Dir(entry.stagingPath)
	stagingParentFd, err := unix.Open(
		stagingParentDir,
		safeDirOpenFlags,
		0,
	)
	if err != nil {
		return fmt.Errorf("open staging parent %q: %w", stagingParentDir, err)
	}
	defer unix.Close(stagingParentFd)

	if rerr := unix.Renameat(stagingParentFd, filepath.Base(entry.stagingPath), finalParentFd, filepath.Base(entry.finalPath)); rerr != nil {
		if errors.Is(rerr, unix.EXDEV) {
			return fmt.Errorf("cross-device rename to %q not permitted; set workspace_path_staging.mode to %q with a same-device root: %w", entry.finalPath, config.StagingModeExplicit, rerr)
		}
		return fmt.Errorf("renameat -> %q: %w", entry.finalPath, rerr)
	}
	return nil
}

// workspaceRelative computes finalPath relative to workspaceRoot. The
// upstream resolver already guaranteed the value stays under the root;
// this helper is the seam that turns the absolute output into the
// component list the walk consumes.
func workspaceRelative(workspaceRoot, finalPath string) (string, error) {
	rel, err := filepath.Rel(workspaceRoot, finalPath)
	if err != nil {
		return "", fmt.Errorf("workspace_path staging: relative %q against %q: %w", finalPath, workspaceRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace_path staging: final path %q escapes workspace root %q", finalPath, workspaceRoot)
	}
	return rel, nil
}

// openWorkspaceSubtree opens the directory named by relDir under
// workspaceRoot through a per-component openat walk. Missing intermediate
// directories are created with mkdirat so a resolver that admitted a
// not-yet-existing suffix still succeeds. Every openat and mkdirat step
// uses O_NOFOLLOW / non-symlink semantics so an intermediate component
// that turns into a symlink between resolve time and finalize time is
// rejected.
//
// Returns the target directory fd plus a close callback the caller must
// invoke when the fd is no longer needed.
func openWorkspaceSubtree(workspaceRoot, relDir string) (int, func(), error) {
	rootFd, err := unix.Open(
		workspaceRoot,
		safeDirOpenFlags,
		0,
	)
	if err != nil {
		return -1, nil, fmt.Errorf("open workspace root %q: %w", workspaceRoot, err)
	}

	if relDir == "" || relDir == "." {
		return rootFd, func() { unix.Close(rootFd) }, nil
	}

	dirFd := rootFd
	components := strings.Split(filepath.ToSlash(relDir), "/")
	for _, comp := range components {
		if comp == "" {
			continue
		}
		nextFd, ferr := unix.Openat(
			dirFd,
			comp,
			unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC,
			0,
		)
		if ferr != nil && errors.Is(ferr, unix.ENOENT) {
			if merr := unix.Mkdirat(dirFd, comp, 0o700); merr != nil {
				unix.Close(dirFd)
				return -1, nil, fmt.Errorf("mkdirat component %q: %w", comp, merr)
			}
			nextFd, ferr = unix.Openat(
				dirFd,
				comp,
				unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC,
				0,
			)
		}
		if ferr != nil {
			unix.Close(dirFd)
			return -1, nil, fmt.Errorf("openat component %q: %w", comp, ferr)
		}
		unix.Close(dirFd)
		dirFd = nextFd
	}

	final := dirFd
	return final, func() { unix.Close(final) }, nil
}

// cleanupStaging removes the per-request staging subtree best-effort.
// Called on any pipeline failure so the daemon does not leak on-disk
// state, and on caller-visible finalize failure so the child's staged
// output does not linger. Registry release is idempotent when
// finalizeStaging succeeded (already released via its defer).
func cleanupStaging(plan *stagingPlan) {
	if plan == nil {
		return
	}
	requestDir := filepath.Join(plan.stagingRoot, plan.stagingID)
	_ = os.RemoveAll(requestDir)
}

// sweepStagingRoot removes staging subtrees at or below stagingRoot that
// no request is still working on and whose mtime is older than the GC
// cutoff. Nothing happens when the root does not exist (the pipeline
// creates it lazily). The sweep only removes direct children of the
// root; the daemon owns every entry there so no user-authored data
// mingles with the staging subtrees.
func sweepStagingRoot(stagingRoot string, registry *stagingRegistry, now time.Time, minAge time.Duration) {
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if registry.IsActive(name) {
			continue
		}
		info, ierr := entry.Info()
		if ierr != nil {
			continue
		}
		if now.Sub(info.ModTime()) < minAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(stagingRoot, name))
	}
}
