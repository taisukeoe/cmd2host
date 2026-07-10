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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// stagingIDPattern matches the exact shape newStagingID emits (32 lowercase
// hex characters). sweepStagingRoot only considers entries whose name
// matches this pattern for removal, keeping the sweep scoped to entries
// the daemon itself could have created.
var stagingIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

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
// identifier), the daemon config the pipeline resolved staging against
// (needed by cleanup so it can re-verify the root through the same anchor
// walk allocation used), and the per-parameter entries.
type stagingPlan struct {
	daemonConfig  *config.DaemonConfig
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
		daemonConfig:  cfg,
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
		entry, err := allocateStaging(cfg, workspaceRoot, stagingRoot, stagingID, slot, name, finalPath)
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
// The subtree is created through a per-component openat/mkdirat walk from
// a trusted anchor (workspace root for `workspace` mode, an Lstat-verified
// explicit root for `explicit` mode). Every step uses O_NOFOLLOW /
// O_DIRECTORY so no intermediate component can resolve through a symlink.
// The leaf file is opened with O_NOFOLLOW|O_CREAT|O_EXCL|O_WRONLY so the
// entry must be brand new; O_CLOEXEC keeps the descriptor from leaking
// into the child, and the descriptor is closed as soon as fstat confirms
// Nlink == 1. The child does its own open() on the returned path.
func allocateStaging(cfg *config.DaemonConfig, workspaceRoot, stagingRoot, stagingID string, slot int, paramName, finalPath string) (stagingEntry, error) {
	slotFd, err := openStagingSlot(cfg, workspaceRoot, stagingID, slot)
	if err != nil {
		return stagingEntry{}, err
	}
	defer unix.Close(slotFd)

	fd, err := unix.Openat(
		slotFd,
		stagingFileBaseName,
		unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC,
		0o600,
	)
	stagingPath := filepath.Join(stagingRoot, stagingID, strconv.Itoa(slot), stagingFileBaseName)
	if err != nil {
		return stagingEntry{}, fmt.Errorf("workspace_path staging: openat %q: %w", stagingPath, err)
	}

	var st unix.Stat_t
	if ferr := unix.Fstat(fd, &st); ferr != nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(slotFd, stagingFileBaseName, 0)
		return stagingEntry{}, fmt.Errorf("workspace_path staging: fstat %q: %w", stagingPath, ferr)
	}
	if st.Nlink != 1 {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(slotFd, stagingFileBaseName, 0)
		return stagingEntry{}, fmt.Errorf("workspace_path staging: link count %d != 1 for %q", st.Nlink, stagingPath)
	}
	if cerr := unix.Close(fd); cerr != nil {
		_ = unix.Unlinkat(slotFd, stagingFileBaseName, 0)
		return stagingEntry{}, fmt.Errorf("workspace_path staging: close %q: %w", stagingPath, cerr)
	}

	return stagingEntry{
		paramName:   paramName,
		slot:        slot,
		stagingPath: stagingPath,
		finalPath:   finalPath,
	}, nil
}

// stagingFileBaseName is the fixed name every slot's staging file uses so
// finalize / cleanup can re-derive it without carrying an extra field on
// stagingEntry.
const stagingFileBaseName = "staged"

// openStagingSlot walks openat/mkdirat from a trusted anchor down to the
// slot directory (`<stagingRoot>/<stagingID>/<slot>/`) and returns the
// slot fd. The caller owns the fd and must close it.
//
// In `workspace` mode the anchor is the workspace root itself, so the walk
// covers `.cmd2host-staging/<stagingID>/<slot>`. In `explicit` mode the
// anchor is the operator-configured root, which the daemon first Lstat's
// to require a real directory, and the walk covers `<stagingID>/<slot>`.
// In both cases every step opens with O_NOFOLLOW|O_DIRECTORY so the walk
// binds the subtree to real directory entries only.
func openStagingSlot(cfg *config.DaemonConfig, workspaceRoot, stagingID string, slot int) (int, error) {
	anchorFd, walkComponents, err := openStagingAnchor(cfg, workspaceRoot)
	if err != nil {
		return -1, err
	}

	dirFd := anchorFd
	walkComponents = append(walkComponents, stagingID, strconv.Itoa(slot))
	for _, comp := range walkComponents {
		nextFd, oerr := unix.Openat(dirFd, comp, safeDirOpenFlags, 0)
		if oerr != nil && errors.Is(oerr, unix.ENOENT) {
			if merr := unix.Mkdirat(dirFd, comp, 0o700); merr != nil {
				unix.Close(dirFd)
				return -1, fmt.Errorf("workspace_path staging: mkdirat %q: %w", comp, merr)
			}
			nextFd, oerr = unix.Openat(dirFd, comp, safeDirOpenFlags, 0)
		}
		if oerr != nil {
			unix.Close(dirFd)
			return -1, fmt.Errorf("workspace_path staging: openat %q: %w", comp, oerr)
		}
		unix.Close(dirFd)
		dirFd = nextFd
	}
	return dirFd, nil
}

// openStagingAnchor returns an fd for the directory that anchors the
// staging walk plus the extra components the caller must openat/mkdirat
// through to reach the request subtree. In workspace mode that means
// opening the workspace root and prepending the hidden `.cmd2host-staging`
// component; in explicit mode the anchor is the operator's root and no
// extra components are prepended.
func openStagingAnchor(cfg *config.DaemonConfig, workspaceRoot string) (int, []string, error) {
	switch cfg.EffectiveStagingMode() {
	case config.StagingModeWorkspace:
		if workspaceRoot == "" {
			return -1, nil, fmt.Errorf("workspace_path staging: workspace root is empty")
		}
		anchorFd, err := unix.Open(workspaceRoot, safeDirOpenFlags, 0)
		if err != nil {
			return -1, nil, fmt.Errorf("workspace_path staging: open workspace root %q: %w", workspaceRoot, err)
		}
		return anchorFd, []string{stagingRootDirName}, nil
	case config.StagingModeExplicit:
		root := cfg.EffectiveStagingRoot()
		if root == "" {
			return -1, nil, fmt.Errorf("workspace_path staging: explicit mode requires a configured root")
		}
		if !filepath.IsAbs(root) {
			return -1, nil, fmt.Errorf("workspace_path staging: explicit mode root %q must be absolute", root)
		}
		if err := verifyStagingRootIsRealDir(root); err != nil {
			return -1, nil, err
		}
		anchorFd, err := unix.Open(root, safeDirOpenFlags, 0)
		if err != nil {
			return -1, nil, fmt.Errorf("workspace_path staging: open explicit staging root %q: %w", root, err)
		}
		return anchorFd, nil, nil
	default:
		return -1, nil, fmt.Errorf("workspace_path staging: unknown mode %q", cfg.EffectiveStagingMode())
	}
}

// verifyStagingRootIsRealDir Lstat's an explicit-mode staging root and
// returns an error if the entry is a symlink or not a directory. Nothing
// is created here — an operator supplying the explicit root is expected to
// pre-create it as a real directory, and the daemon refuses to auto-mkdir
// under a path it cannot anchor.
func verifyStagingRootIsRealDir(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("workspace_path staging: lstat explicit staging root %q: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace_path staging: explicit staging root %q is a symlink; must be a real directory", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace_path staging: explicit staging root %q is not a directory", root)
	}
	return nil
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
		if err := finalizePlacement(plan, entry); err != nil {
			return fmt.Errorf("workspace_path staging: finalize param %q: %w", entry.paramName, err)
		}
	}

	// Sweep the per-request subtree once every entry is in place. The
	// helper re-anchors through the openat walk allocation used so the
	// teardown stays bound to the same directory identities.
	cleanupStaging(plan)
	return nil
}

// finalizePlacement covers a single staged file: it opens the staging
// parent and the final parent through the safe walks (anchor-rooted
// openat / mkdirat), then invokes Renameat. Both descriptors are closed
// before returning even on the success path so caller code does not have
// to handle fd lifetime.
func finalizePlacement(plan *stagingPlan, entry stagingEntry) error {
	rel, err := workspaceRelative(plan.workspaceRoot, entry.finalPath)
	if err != nil {
		return err
	}

	finalParentFd, closeFinal, err := openWorkspaceSubtree(plan.workspaceRoot, filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer closeFinal()

	stagingParentFd, err := openStagingSlot(plan.daemonConfig, plan.workspaceRoot, plan.stagingID, entry.slot)
	if err != nil {
		return err
	}
	defer unix.Close(stagingParentFd)

	if rerr := unix.Renameat(stagingParentFd, stagingFileBaseName, finalParentFd, filepath.Base(entry.finalPath)); rerr != nil {
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

// cleanupStaging removes the per-request staging subtree through the
// same fd-anchored walk allocation used. Called on any pipeline failure
// so the daemon does not accumulate on-disk state, and on caller-visible
// finalize failure so the child's staged output does not linger.
// Registry release is idempotent when finalizeStaging succeeded (already
// released via its defer).
//
// The removal is fd-relative: the helper opens the anchor and walks down
// to the staging root directory, then unlinks the request's slot files
// and slot directories via `Unlinkat` against those fds. `os.RemoveAll`
// on the path is not used, so a directory that stops resolving through
// its verified fds — for example a component that no longer opens with
// `O_NOFOLLOW|O_DIRECTORY` — is simply left alone by cleanup and reaped
// on a later GC cycle.
func cleanupStaging(plan *stagingPlan) {
	if plan == nil {
		return
	}
	if plan.daemonConfig == nil {
		return
	}
	rootFd, err := openStagingRootDirFd(plan.daemonConfig, plan.workspaceRoot)
	if err != nil {
		return
	}
	defer unix.Close(rootFd)

	requestFd, oerr := unix.Openat(rootFd, plan.stagingID, safeDirOpenFlags, 0)
	if oerr != nil {
		return
	}
	// unlink every recorded entry via fd-relative Unlinkat, then remove
	// the slot directory it lived in. Missing entries are ignored so
	// finalizeStaging's success path (which has already renamed the file
	// away) still gets a clean subtree teardown.
	for _, entry := range plan.entries {
		slotName := strconv.Itoa(entry.slot)
		slotFd, sferr := unix.Openat(requestFd, slotName, safeDirOpenFlags, 0)
		if sferr == nil {
			_ = unix.Unlinkat(slotFd, stagingFileBaseName, 0)
			unix.Close(slotFd)
		}
		_ = unix.Unlinkat(requestFd, slotName, unix.AT_REMOVEDIR)
	}
	unix.Close(requestFd)
	_ = unix.Unlinkat(rootFd, plan.stagingID, unix.AT_REMOVEDIR)
}

// openStagingRootDirFd returns the fd for the staging root directory
// itself, produced by walking from the anchor through the "extra"
// components that separate the anchor from the root. Workspace mode
// walks the anchor (workspace root fd) through `.cmd2host-staging`;
// explicit mode returns the anchor unchanged because the operator's
// root IS the anchor. Caller owns the fd.
func openStagingRootDirFd(cfg *config.DaemonConfig, workspaceRoot string) (int, error) {
	anchorFd, extra, err := openStagingAnchor(cfg, workspaceRoot)
	if err != nil {
		return -1, err
	}
	dirFd := anchorFd
	for _, comp := range extra {
		nextFd, oerr := unix.Openat(dirFd, comp, safeDirOpenFlags, 0)
		if oerr != nil {
			unix.Close(dirFd)
			return -1, fmt.Errorf("workspace_path staging: openat %q: %w", comp, oerr)
		}
		unix.Close(dirFd)
		dirFd = nextFd
	}
	return dirFd, nil
}

// sweepStagingRoot removes staging subtrees at or below stagingRoot that
// no request is still working on and whose mtime is older than the GC
// cutoff. Two conditions bound the removal:
//
//   - stagingRoot itself is Lstat'd; a symlink or non-directory root is
//     skipped with a warning so the sweep only walks a real directory
//     under the daemon's ownership.
//   - each direct child must match stagingIDPattern (32 lowercase hex),
//     the exact shape newStagingID emits, so an explicit-mode root that
//     carries unrelated entries has them left alone.
//
// Only direct children of the root are removed; the daemon owns every
// entry it creates there.
func sweepStagingRoot(stagingRoot string, registry *stagingRegistry, now time.Time, minAge time.Duration) {
	info, err := os.Lstat(stagingRoot)
	if err != nil {
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		fmt.Printf("Warning: staging root %q is a symlink; sweep skipped\n", stagingRoot)
		return
	}
	if !info.IsDir() {
		fmt.Printf("Warning: staging root %q is not a directory; sweep skipped\n", stagingRoot)
		return
	}

	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !stagingIDPattern.MatchString(name) {
			continue
		}
		if registry.IsActive(name) {
			continue
		}
		einfo, ierr := entry.Info()
		if ierr != nil {
			continue
		}
		if now.Sub(einfo.ModTime()) < minAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(stagingRoot, name))
	}
}
