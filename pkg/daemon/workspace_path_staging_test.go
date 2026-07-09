package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// mkStagingWorkspace returns an EvalSymlinks-resolved temp directory so
// tests observe the same absolute root the daemon-side pipeline computes.
// (macOS /var → /private/var alias otherwise makes relative-path
// comparisons noisy.)
func mkStagingWorkspace(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(tempdir): %v", err)
	}
	return base
}

// TestAllocateStaging_Nlink1 verifies the happy path: a brand-new file
// exists at the returned staging path, has one link, and the descriptor
// was closed (post-allocateStaging invariant so the child does the
// open).
func TestAllocateStaging_Nlink1(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	entry, err := allocateStaging(stagingRoot, "STAGE1", 0, "dest", filepath.Join(workspace, "out.log"))
	if err != nil {
		t.Fatalf("allocateStaging: %v", err)
	}
	info, err := os.Lstat(entry.stagingPath)
	if err != nil {
		t.Fatalf("lstat staging: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("staging path is a symlink, want regular file")
	}
	if info.Size() != 0 {
		t.Fatalf("staging size = %d, want 0", info.Size())
	}
	if !strings.HasPrefix(entry.stagingPath, stagingRoot) {
		t.Fatalf("staging path %q outside root %q", entry.stagingPath, stagingRoot)
	}
}

// TestAllocateStaging_RejectsExistingEntry pins that O_EXCL + O_NOFOLLOW
// makes a second allocation for the same slot fail loud — the daemon
// never overwrites an existing staging entry.
func TestAllocateStaging_RejectsExistingEntry(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	if _, err := allocateStaging(stagingRoot, "STAGE2", 0, "dest", filepath.Join(workspace, "out.log")); err != nil {
		t.Fatalf("first allocateStaging: %v", err)
	}
	// A second allocation into the same slot must not overwrite.
	if _, err := allocateStaging(stagingRoot, "STAGE2", 0, "dest", filepath.Join(workspace, "out.log")); err == nil {
		t.Fatalf("second allocateStaging accepted an existing entry")
	}
}

// TestAllocateStaging_RejectsSymlinkTarget confirms O_NOFOLLOW rejects a
// pre-existing symlink at the staging path. Simulates a scenario where
// something (an unfortunate operator, or a lingering GC gap) left a
// symlink where a fresh file is expected.
func TestAllocateStaging_RejectsSymlinkTarget(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	slotDir := filepath.Join(stagingRoot, "STAGE3", "0")
	if err := os.MkdirAll(slotDir, 0o700); err != nil {
		t.Fatalf("mkdir slot: %v", err)
	}
	stagingPath := filepath.Join(slotDir, "staged")
	if err := os.Symlink("/etc/passwd", stagingPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := allocateStaging(stagingRoot, "STAGE3", 0, "dest", filepath.Join(workspace, "out.log")); err == nil {
		t.Fatalf("allocateStaging accepted a symlink target")
	}
}

// TestFinalizeStaging_Identity places a staged file at its final path
// and verifies the file at the target is the same one allocateStaging
// created (inode compare) — nothing else moved through the target path.
func TestFinalizeStaging_Identity(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	finalPath := filepath.Join(workspace, "logs", "out.log")

	entry, err := allocateStaging(stagingRoot, "STAGE4", 0, "dest", finalPath)
	if err != nil {
		t.Fatalf("allocateStaging: %v", err)
	}
	// Write a byte so we can tell "same file moved" from "different file appeared".
	if err := os.WriteFile(entry.stagingPath, []byte{0x42}, 0o600); err != nil {
		t.Fatalf("write staging: %v", err)
	}

	stagingInfo, err := os.Stat(entry.stagingPath)
	if err != nil {
		t.Fatalf("stat staging: %v", err)
	}
	stagingSys, ok := stagingInfo.Sys().(*syscallStatT)
	if !ok {
		t.Fatalf("unexpected sys type %T", stagingInfo.Sys())
	}
	plan := &stagingPlan{
		stagingID:     "STAGE4",
		workspaceRoot: workspace,
		stagingRoot:   stagingRoot,
		entries:       []stagingEntry{entry},
	}
	registry := newStagingRegistry()
	registry.Register("STAGE4")
	if err := finalizeStaging(plan, registry); err != nil {
		t.Fatalf("finalizeStaging: %v", err)
	}

	finalInfo, err := os.Stat(finalPath)
	if err != nil {
		t.Fatalf("stat final: %v", err)
	}
	finalSys, ok := finalInfo.Sys().(*syscallStatT)
	if !ok {
		t.Fatalf("unexpected sys type %T", finalInfo.Sys())
	}
	if stagingSys.Ino != finalSys.Ino || stagingSys.Dev != finalSys.Dev {
		t.Fatalf("finalized file identity mismatch: staging (%d,%d) vs final (%d,%d)", stagingSys.Dev, stagingSys.Ino, finalSys.Dev, finalSys.Ino)
	}
	body, err := os.ReadFile(finalPath)
	if err != nil || len(body) != 1 || body[0] != 0x42 {
		t.Fatalf("final content = %v, want [0x42]", body)
	}
	// Staging subtree should be gone once finalize succeeds.
	if _, err := os.Stat(filepath.Join(stagingRoot, "STAGE4")); !os.IsNotExist(err) {
		t.Fatalf("staging subtree survived finalize: %v", err)
	}
	if registry.IsActive("STAGE4") {
		t.Fatalf("registry still active after finalize")
	}
}

// TestFinalizeStaging_RejectsSymlinkedAncestor confirms the walk aborts
// when a parent directory of the final path is a symlink — even if that
// symlink was resolved as a valid directory at plan time and swapped in
// afterwards.
func TestFinalizeStaging_RejectsSymlinkedAncestor(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	realDir := filepath.Join(workspace, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	// Swap a symlink into the ancestor slot after allocateStaging returns.
	symlinkAncestor := filepath.Join(workspace, "linkdir")
	if err := os.Symlink(realDir, symlinkAncestor); err != nil {
		t.Fatalf("symlink ancestor: %v", err)
	}
	finalPath := filepath.Join(symlinkAncestor, "out.log")

	entry, err := allocateStaging(stagingRoot, "STAGE5", 0, "dest", finalPath)
	if err != nil {
		t.Fatalf("allocateStaging: %v", err)
	}
	plan := &stagingPlan{
		stagingID:     "STAGE5",
		workspaceRoot: workspace,
		stagingRoot:   stagingRoot,
		entries:       []stagingEntry{entry},
	}
	registry := newStagingRegistry()
	registry.Register("STAGE5")
	err = finalizeStaging(plan, registry)
	if err == nil {
		t.Fatalf("finalizeStaging accepted a symlink ancestor")
	}
	// Final path must not have been created via the symlink target.
	if _, statErr := os.Stat(filepath.Join(realDir, "out.log")); statErr == nil {
		t.Fatalf("finalized file appeared through symlink ancestor")
	}
}

// TestFinalizeStaging_ReplacesExistingRegularFile documents the contract
// for a final path that already exists as a regular file: finalize
// renames over it, so the daemon's view of the resulting file is the
// staged content (and its inode). Callers that need "refuse to
// overwrite" semantics must gate that at the operation-config level;
// v1 mirrors the child command's usual `> file` behavior.
func TestFinalizeStaging_ReplacesExistingRegularFile(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	finalPath := filepath.Join(workspace, "out.log")

	// Seed a pre-existing final file with a distinct content byte.
	if err := os.WriteFile(finalPath, []byte{0x11}, 0o600); err != nil {
		t.Fatalf("seed final: %v", err)
	}

	entry, err := allocateStaging(stagingRoot, "STAGE_OVR", 0, "dest", finalPath)
	if err != nil {
		t.Fatalf("allocateStaging: %v", err)
	}
	if err := os.WriteFile(entry.stagingPath, []byte{0x22}, 0o600); err != nil {
		t.Fatalf("write staging: %v", err)
	}
	plan := &stagingPlan{
		stagingID:     "STAGE_OVR",
		workspaceRoot: workspace,
		stagingRoot:   stagingRoot,
		entries:       []stagingEntry{entry},
	}
	registry := newStagingRegistry()
	registry.Register("STAGE_OVR")
	if err := finalizeStaging(plan, registry); err != nil {
		t.Fatalf("finalizeStaging: %v", err)
	}
	body, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if len(body) != 1 || body[0] != 0x22 {
		t.Fatalf("final content = %v, want [0x22] (overwrite semantics)", body)
	}
}

// TestFinalizeStaging_ReplacesExistingSymlink documents that renameat
// replaces a symlink at the final path without following it: whatever
// the symlink pointed at outside the workspace stays untouched, and the
// entry now names the staged regular file. Compare against the ancestor
// symlink swap test — a symlink at a *parent* is rejected by the walk;
// a symlink at the final basename is renamed over.
func TestFinalizeStaging_ReplacesExistingSymlink(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	finalPath := filepath.Join(workspace, "out.log")

	outsideFile := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsideFile, []byte{0x33}, 0o600); err != nil {
		t.Fatalf("seed outside: %v", err)
	}
	// Symlink at the final basename position; the walk touches only ancestors
	// and does not follow this basename.
	if err := os.Symlink(outsideFile, finalPath); err != nil {
		t.Fatalf("symlink final: %v", err)
	}

	entry, err := allocateStaging(stagingRoot, "STAGE_OVR_SL", 0, "dest", finalPath)
	if err != nil {
		t.Fatalf("allocateStaging: %v", err)
	}
	if err := os.WriteFile(entry.stagingPath, []byte{0x44}, 0o600); err != nil {
		t.Fatalf("write staging: %v", err)
	}
	plan := &stagingPlan{
		stagingID:     "STAGE_OVR_SL",
		workspaceRoot: workspace,
		stagingRoot:   stagingRoot,
		entries:       []stagingEntry{entry},
	}
	registry := newStagingRegistry()
	registry.Register("STAGE_OVR_SL")
	if err := finalizeStaging(plan, registry); err != nil {
		t.Fatalf("finalizeStaging: %v", err)
	}
	// Final path now points at the staged regular file, and the outside
	// target was NOT written through.
	info, err := os.Lstat(finalPath)
	if err != nil {
		t.Fatalf("lstat final: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("final is still a symlink after replace")
	}
	outsideBody, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if len(outsideBody) != 1 || outsideBody[0] != 0x33 {
		t.Fatalf("outside file was written through the symlink: %v", outsideBody)
	}
}

// TestFinalizeStaging_CreatesMissingSubdir covers the "resolveWorkspacePath
// accepted a not-yet-existing suffix" case: finalize walks into a
// directory that does not yet exist, creates it via mkdirat, and places
// the staged file.
func TestFinalizeStaging_CreatesMissingSubdir(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	finalPath := filepath.Join(workspace, "new", "subdir", "out.log")

	entry, err := allocateStaging(stagingRoot, "STAGE6", 0, "dest", finalPath)
	if err != nil {
		t.Fatalf("allocateStaging: %v", err)
	}
	plan := &stagingPlan{
		stagingID:     "STAGE6",
		workspaceRoot: workspace,
		stagingRoot:   stagingRoot,
		entries:       []stagingEntry{entry},
	}
	registry := newStagingRegistry()
	registry.Register("STAGE6")
	if err := finalizeStaging(plan, registry); err != nil {
		t.Fatalf("finalizeStaging: %v", err)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final path not created: %v", err)
	}
}

// TestCleanupStaging_RemovesRequestDir confirms cleanup removes the
// per-request subtree — a failed request must leave nothing behind.
func TestCleanupStaging_RemovesRequestDir(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	entry, err := allocateStaging(stagingRoot, "STAGE7", 0, "dest", filepath.Join(workspace, "out.log"))
	if err != nil {
		t.Fatalf("allocateStaging: %v", err)
	}
	plan := &stagingPlan{
		stagingID:     "STAGE7",
		workspaceRoot: workspace,
		stagingRoot:   stagingRoot,
		entries:       []stagingEntry{entry},
	}
	cleanupStaging(plan)
	if _, err := os.Stat(filepath.Join(stagingRoot, "STAGE7")); !os.IsNotExist(err) {
		t.Fatalf("cleanup left request dir behind: %v", err)
	}
}

// TestSweepStagingRoot_SkipsActive pins the GC contract: an entry
// registered as in-flight is never removed regardless of its age.
func TestSweepStagingRoot_SkipsActive(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	stagingRoot := filepath.Join(workspace, ".cmd2host-staging")
	if err := os.MkdirAll(filepath.Join(stagingRoot, "ACTIVE1"), 0o700); err != nil {
		t.Fatalf("mkdir ACTIVE1: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(stagingRoot, "STALE1"), 0o700); err != nil {
		t.Fatalf("mkdir STALE1: %v", err)
	}
	// Backdate both entries so mtime is not a differentiating factor.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(stagingRoot, "ACTIVE1"), old, old); err != nil {
		t.Fatalf("chtimes ACTIVE1: %v", err)
	}
	if err := os.Chtimes(filepath.Join(stagingRoot, "STALE1"), old, old); err != nil {
		t.Fatalf("chtimes STALE1: %v", err)
	}
	registry := newStagingRegistry()
	registry.Register("ACTIVE1")
	sweepStagingRoot(stagingRoot, registry, time.Now(), time.Hour)
	if _, err := os.Stat(filepath.Join(stagingRoot, "ACTIVE1")); err != nil {
		t.Errorf("sweep removed active entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingRoot, "STALE1")); !os.IsNotExist(err) {
		t.Errorf("sweep kept stale entry: %v", err)
	}
}

// TestResolveStagingRoot pins the mode dispatch: workspace mode joins
// against the target repo path, explicit mode returns the operator
// root verbatim, and misconfiguration surfaces as an error.
func TestResolveStagingRoot(t *testing.T) {
	workspace := mkStagingWorkspace(t)

	cases := []struct {
		name    string
		cfg     *config.DaemonConfig
		want    string
		wantErr bool
	}{
		{
			name: "workspace mode joins under repo path",
			cfg:  &config.DaemonConfig{},
			want: filepath.Join(workspace, ".cmd2host-staging"),
		},
		{
			name: "explicit mode returns configured root",
			cfg: &config.DaemonConfig{WorkspacePathStaging: &config.StagingConfig{
				Mode: config.StagingModeExplicit,
				Root: filepath.Join(workspace, "explicit-root"),
			}},
			want: filepath.Join(workspace, "explicit-root"),
		},
		{
			name: "explicit mode with relative root rejected",
			cfg: &config.DaemonConfig{WorkspacePathStaging: &config.StagingConfig{
				Mode: config.StagingModeExplicit,
				Root: "relative/root",
			}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveStagingRoot(tc.cfg, workspace)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveStagingRoot got %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveStagingRoot err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveStagingRoot = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPlanStaging_MutatesParams covers the pipeline seam: after
// planStaging returns, the params map carries staging paths (not the
// original workspace-relative resolved paths) so BuildArgs and the child
// see the staging entry.
func TestPlanStaging_MutatesParams(t *testing.T) {
	workspace := mkStagingWorkspace(t)
	op := &operations.Operation{
		Command:      "aws",
		ArgsTemplate: []string{"s3", "cp", "src", "{dest}"},
		Params:       map[string]operations.ParamSchema{"dest": {Type: "workspace_path"}},
	}
	finalPath := filepath.Join(workspace, "logs", "out.log")
	params := map[string]operations.ParamValue{"dest": finalPath}
	registry := newStagingRegistry()
	plan, err := planStaging(&config.DaemonConfig{}, registry, op, params, workspace)
	if err != nil {
		t.Fatalf("planStaging: %v", err)
	}
	if plan == nil {
		t.Fatalf("planStaging returned nil plan for op with workspace_path")
	}
	staging, ok := params["dest"].(string)
	if !ok || staging == finalPath {
		t.Fatalf("params[dest] = %v, want staging path different from final", params["dest"])
	}
	if !strings.HasPrefix(staging, filepath.Join(workspace, ".cmd2host-staging")) {
		t.Fatalf("staging path %q not under staging root", staging)
	}
	// registry should be tracking this stagingID until finalize/cleanup runs
	if !registry.IsActive(plan.stagingID) {
		t.Fatalf("registry did not register staging ID %q", plan.stagingID)
	}
}

// TestPlanStaging_NoParam is a no-op for operations without any
// workspace_path parameter; planStaging returns nil and the request path
// bypasses the pipeline.
func TestPlanStaging_NoParam(t *testing.T) {
	op := &operations.Operation{
		Command:      "gh",
		ArgsTemplate: []string{"pr", "view", "{number}"},
		Params:       map[string]operations.ParamSchema{"number": {Type: "integer"}},
	}
	plan, err := planStaging(&config.DaemonConfig{}, newStagingRegistry(), op, map[string]operations.ParamValue{"number": 1}, "/tmp")
	if err != nil {
		t.Fatalf("planStaging: %v", err)
	}
	if plan != nil {
		t.Fatalf("planStaging returned plan for op with no workspace_path param")
	}
}

// syscallStatT names the concrete type os.FileInfo.Sys() returns on
// darwin. The alias lives in the test file so production code stays
// insulated from the platform-specific stat type.
type syscallStatT = syscall.Stat_t
