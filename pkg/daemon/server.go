// Package daemon implements the cmd2host server: request dispatch,
// operation validation, sanitized execution, and the TCP/Unix transport.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/taisukeoe/cmd2host/internal/configdir"
	"github.com/taisukeoe/cmd2host/pkg/auth"
	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

const (
	readTimeout = 5 * time.Second
	maxReadSize = 65536
)

// Server handles TCP and Unix socket connections and command proxying.
// baseDir anchors per-instance directory lookups for project configs and the
// token store; all internal config / auth path resolution flows through it.
//
// inFlightSem caps concurrent handleClient goroutines. The channel's
// capacity comes from DaemonConfig.MaxInFlight; a nil channel means the
// cap is disabled (DaemonConfig.MaxInFlight < 0). acceptLoop acquires a
// slot before spawning the handler and the handler returns the slot when
// it exits, so the cap is enforced uniformly across the TCP and Unix
// transports.
type Server struct {
	daemonConfig *config.DaemonConfig
	validator    *Validator
	tokenStore   *auth.TokenStore
	tcpListener  net.Listener
	unixListener net.Listener
	baseDir      string
	inFlightSem  chan struct{}
}

// NewServer creates a new Server using the default cmd2host base directory
// resolved via configdir.Dir (honors CMD2HOST_CONFIG_DIR). Fails fast when
// the base directory cannot be determined so callers see the same diagnostic
// as the prior auth.NewTokenStore path. Callers that need explicit
// per-instance isolation should use NewServerAt instead.
func NewServer(daemonConfig *config.DaemonConfig) (*Server, error) {
	base, err := configdir.Dir()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize token store: cannot determine cmd2host config directory: %w", err)
	}
	return NewServerAt(base, daemonConfig)
}

// NewServerAt creates a new Server rooted at the given base directory.
// Project configs are loaded from <dir>/projects and tokens from
// <dir>/tokens. Multiple Servers may be constructed concurrently with
// distinct dirs without touching process-global environment state.
//
// dir must be a non-empty path (typically absolute, with the same semantics
// as the value returned by configdir.Dir). Empty dir is rejected at construct
// time so projects/ and tokens/ never resolve against the daemon CWD.
func NewServerAt(dir string, daemonConfig *config.DaemonConfig) (*Server, error) {
	if dir == "" {
		return nil, fmt.Errorf("NewServerAt: dir must be non-empty")
	}
	tokenStore := auth.NewTokenStoreAt(filepath.Join(dir, "tokens"))
	var sem chan struct{}
	if daemonConfig.MaxInFlight > 0 {
		sem = make(chan struct{}, daemonConfig.MaxInFlight)
	}
	return &Server{
		daemonConfig: daemonConfig,
		validator:    NewValidator(),
		tokenStore:   tokenStore,
		baseDir:      dir,
		inFlightSem:  sem,
	}, nil
}

// handleClient processes a single client connection.
//
// releaseSlot returns the connection's in-flight slot to dispatchConn's
// semaphore. Long-running but otherwise idle work (notably the
// authentication-failure throttle sleep) should call releaseSlot before
// blocking so the slot does not stay reserved for purely synthetic delay.
// The callback is idempotent (sync.Once) so a final defer in dispatchConn
// still safely catches paths that did not release explicitly.
func (s *Server) handleClient(conn net.Conn, releaseSlot func()) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("  -> PANIC recovered in handleClient: %v\n", r)
		}
	}()

	// Set read deadline
	conn.SetReadDeadline(time.Now().Add(readTimeout))

	// Use json.Decoder with LimitReader for robust reading:
	// - Handles TCP packet fragmentation (waits for complete JSON object)
	// - Prevents memory exhaustion via size limit
	// - Doesn't require client to close connection
	//
	// We buffer the raw bytes so we can reuse them for type detection and handler parsing
	var buf bytes.Buffer
	decoder := json.NewDecoder(io.TeeReader(io.LimitReader(conn, maxReadSize), &buf))

	// Decode into raw message map to detect request type
	var rawRequest map[string]json.RawMessage
	if err := decoder.Decode(&rawRequest); err != nil {
		if err == io.EOF {
			return // Empty request, nothing to do
		}
		// Check if request was truncated by LimitReader
		if int64(buf.Len()) >= maxReadSize {
			msg := fmt.Sprintf("Request too large (exceeded %d bytes)", maxReadSize)
			fmt.Println("  ->", msg)
			s.sendOperationResponse(conn, operations.Response{
				ExitCode:     1,
				DeniedReason: strPtr(msg),
			})
			return
		}
		fmt.Println("  -> Invalid JSON:", err)
		s.sendOperationResponse(conn, operations.Response{
			ExitCode:     1,
			DeniedReason: strPtr(fmt.Sprintf("Invalid JSON: %v", err)),
		})
		return
	}

	// Use buffered bytes for handlers (same data, no re-encoding)
	data := buf.Bytes()

	// Determine request type by checking for specific fields. The raw_argv
	// discriminator must precede the operation discriminator: raw-argv mode
	// requests carry RawArgv but resolve Operation only after reverse-match,
	// so the operation field may be empty at this point. The boolean is
	// passed to handleOperationRequest as the single source of truth for
	// raw-argv mode detection — Go's json.Unmarshal collapses `null` and
	// missing fields to a nil slice, so inspecting the unmarshaled Request
	// alone cannot distinguish `{"raw_argv":null}` (caller intent: raw-argv)
	// from a request that omits the field (caller intent: operation entry).
	_, hasRawArgv := rawRequest["raw_argv"]
	if _, hasListOps := rawRequest["list_operations"]; hasListOps {
		s.handleListOperationsRequest(conn, data, releaseSlot)
	} else if _, hasDescribeOp := rawRequest["describe_operation"]; hasDescribeOp {
		s.handleDescribeOperationRequest(conn, data, releaseSlot)
	} else if hasRawArgv {
		s.handleOperationRequest(conn, data, true, releaseSlot)
	} else if _, hasOperation := rawRequest["operation"]; hasOperation {
		s.handleOperationRequest(conn, data, false, releaseSlot)
	} else {
		fmt.Println("  -> Unknown request type (missing 'operation' or 'raw_argv' field)")
		s.sendOperationResponse(conn, operations.Response{
			ExitCode:     1,
			DeniedReason: strPtr("Unknown request type: missing 'operation' or 'raw_argv' field"),
		})
	}
}

// resolveProject resolves project config from token data.
//
// Token binding precedence:
//  1. New token with ProjectID set: ProjectID is the canonical resolver.
//     If Repo is also non-empty, it must equal project.Repos[0] as a
//     defense-in-depth check (catches token tampering after issue).
//  2. Legacy token with only Repo: NormalizeProjectID(Repo) yields the
//     project ID; Repo must equal project.Repos[0] (primary repo).
//     Legacy tokens are bound to the primary repo only; non-primary repos
//     in 1:N projects remain accessible because the target_repo is
//     authorized against project.Repos at execution time.
func (s *Server) resolveProject(tokenData auth.TokenData) (*config.ProjectConfig, string, error) {
	var projectID string
	switch {
	case tokenData.ProjectID != "":
		projectID = tokenData.ProjectID
	case tokenData.Repo != "":
		projectID = config.NormalizeProjectID(tokenData.Repo)
	default:
		return nil, "", fmt.Errorf("token does not carry a project_id or repo binding")
	}

	projectConfig, err := config.LoadProjectConfigAt(s.baseDir, projectID)
	if err != nil {
		return nil, projectID, err
	}

	primaryRepo := projectConfig.PrimaryRepo()
	if tokenData.Repo != "" && tokenData.Repo != primaryRepo {
		return nil, projectID, fmt.Errorf("token-project mismatch: token bound to repo %q but project primary repo is %q", tokenData.Repo, primaryRepo)
	}

	allowed, currentHash, err := config.IsConfigAllowedAt(s.baseDir, projectID)
	if err != nil {
		return nil, projectID, fmt.Errorf("failed to check config allowance: %w", err)
	}
	if !allowed {
		return nil, projectID, fmt.Errorf("config not allowed (hash: %s). Run: cmd2host config allow %s", currentHash[:16], projectID)
	}

	return projectConfig, projectID, nil
}

// handleOperationRequest handles new-style operation requests for both the
// explicit operation entry (Operation field set) and the additive raw-argv
// entry (raw_argv field present in the request JSON, Operation resolved by
// reverse-match). rawArgvPresent reports JSON-level field presence detected
// in handleClient — passing it explicitly avoids ambiguity between
// `{"raw_argv":null}` and a request that omits the field (both deserialize
// to nil RawArgv slice in Go).
func (s *Server) handleOperationRequest(conn net.Conn, data []byte, rawArgvPresent bool, releaseSlot func()) {
	// Pre-resolution audit lines use [OP:?] so an operator's `grep '\[OP:'`
	// catches every operation request regardless of how far it gets through
	// auth / project / target / reverse-match. The resolved [OP:<id>] line
	// at the end carries the actual operation_id once reverse-match (or the
	// explicit operation field) has settled it.
	source := "mcp"
	if rawArgvPresent {
		source = "raw_argv"
	}

	var req operations.Request
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Printf("[OP:?] DENIED (request_parse) source=%q: %v\n", source, err)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    "",
			ExitCode:     1,
			DeniedReason: strPtr(fmt.Sprintf("Invalid request: %v", err)),
		})
		return
	}

	// Reject caller-supplied diagnostic fields (request_id / source) whose
	// character set could otherwise reach audit log format strings. The
	// per-field %q quoting on downstream log lines is a second layer.
	if err := req.Validate(); err != nil {
		fmt.Printf("[OP:?] DENIED (request_validate) source=%q: %v\n", source, err)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    "",
			ExitCode:     1,
			DeniedReason: strPtr(fmt.Sprintf("Invalid request: %v", err)),
		})
		return
	}

	// Authenticate token
	tokenData, valid := s.tokenStore.GetTokenData(req.Token)
	if !valid {
		fmt.Printf("[OP:?] AUTH FAILED source=%q request_id=%q\n", source, req.RequestID)
		// Return the slot before the synthetic delay so the cap is not
		// occupied by a goroutine that is no longer doing useful work.
		if releaseSlot != nil {
			releaseSlot()
		}
		time.Sleep(1 * time.Second) // Delay to slow down brute force attacks
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr("Authentication failed"),
		})
		return
	}

	// Resolve project config from token
	projectConfig, projectID, err := s.resolveProject(tokenData)
	if err != nil {
		fmt.Printf("[OP:?] DENIED (project) source=%q request_id=%q: %v\n", source, req.RequestID, err)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(err.Error()),
		})
		return
	}

	// Resolve the execution target: target_repo against project allow list +
	// expected git URL derivation. Empty target_repo defaults to the primary
	// repo (single-repo project ergonomics).
	//
	// Target is resolved before any per-mode branching so the raw-argv
	// reverse-match path can substitute injection-only placeholders
	// (repo / repo_path / expected_git_url) with their per-target values.
	target, resolvedSource, err := ResolveExecutionTarget(projectConfig, req.TargetRepo, req.CwdContext)
	if err != nil {
		// Surface both the requested target_repo and the cwd hint
		// (when present) so an operator grep'ing a denial line sees
		// what the caller asked for and what the auto-resolve probe
		// saw. The auto-resolve failure message itself carries the
		// detail; this log line keeps the key=value shape stable.
		// The origin URL is routed through OriginRepoForLog so a
		// credential-bearing https URL never reaches operator logs.
		var cwdSummary string
		if req.CwdContext != nil {
			cwdSummary = fmt.Sprintf(" cwd_toplevel=%q cwd_origin_repo=%q", req.CwdContext.Toplevel, OriginRepoForLog(req.CwdContext.OriginURL))
		}
		fmt.Printf("[OP:?] DENIED (target) source=%q project=%q target_repo=%q%s request_id=%q: %v\n", source, projectID, req.TargetRepo, cwdSummary, req.RequestID, err)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(err.Error()),
		})
		return
	}
	// Promote the resolved target_repo back onto the request so every
	// subsequent denial / audit log line in this function reports the
	// effective target rather than the (possibly empty) caller-supplied
	// value. cwd auto-resolve and single-repo primary-default paths
	// otherwise leave req.TargetRepo as "" — observability only, no
	// resolution semantics change.
	req.TargetRepo = target.Repo

	// Raw-argv mode: resolve Operation/Params/Flags via reverse-match
	// against the project's allowed operations before continuing through
	// the shared validate / sanitize / execute path. Triggered by the
	// presence of the raw_argv field in the request JSON (passed in via
	// rawArgvPresent so `{"raw_argv":null}` and `{"raw_argv":[]}` both
	// enter this branch and get an explicit raw-argv denial). Source is
	// normalized to "raw_argv" for logging regardless of whether the
	// caller pre-populated it.
	if rawArgvPresent {
		req.Source = "raw_argv"
		if len(req.RawArgv) == 0 {
			msg := "raw_argv field is present but empty; must contain at least the command token"
			fmt.Printf("[OP:?] DENIED (raw_argv) source=%q project=%q target_repo=%q request_id=%q: %s\n", "raw_argv", projectID, req.TargetRepo, req.RequestID, msg)
			s.sendOperationResponse(conn, operations.Response{
				RequestID:    req.RequestID,
				ExitCode:     1,
				DeniedReason: strPtr(msg),
			})
			return
		}
		if len(req.RawArgv[0]) == 0 {
			msg := "raw_argv command is empty"
			fmt.Printf("[OP:?] DENIED (raw_argv) source=%q project=%q target_repo=%q request_id=%q: %s\n", "raw_argv", projectID, req.TargetRepo, req.RequestID, msg)
			s.sendOperationResponse(conn, operations.Response{
				RequestID:    req.RequestID,
				ExitCode:     1,
				DeniedReason: strPtr(msg),
			})
			return
		}
		injection := map[string]string{
			"repo":             target.Repo,
			"repo_path":        target.RepoPath,
			"expected_git_url": target.ExpectedGitURL,
		}
		candidates := buildReverseMatchCandidates(projectConfig)
		resolved, rerr := operations.ReverseMatch(req.RawArgv[0], req.RawArgv[1:], candidates, injection)
		if rerr != nil {
			fmt.Printf("[OP:?] DENIED (reverse_match) source=%q project=%q target_repo=%q request_id=%q: %v\n", "raw_argv", projectID, req.TargetRepo, req.RequestID, rerr)
			s.sendOperationResponse(conn, operations.Response{
				RequestID:    req.RequestID,
				ExitCode:     1,
				DeniedReason: strPtr(rerr.Error()),
			})
			return
		}
		req.Operation = resolved.OperationID
		req.Params = resolved.Params
		req.Flags = resolved.Flags
	} else {
		// Explicit operation entry. Unconditionally overwrite Source so a
		// caller cannot spoof `source=raw_argv` in the audit log by
		// passing source="raw_argv" alongside an explicit operation
		// (handleClient already decided this is the MCP route via
		// rawArgvPresent == false; the log must agree).
		req.Source = "mcp"
	}

	// Log operation request (params omitted to avoid logging sensitive data
	// like PR body). source distinguishes raw-argv vs MCP entry,
	// resolved_operation_id is identical to the operation field but kept as
	// a stable log key so downstream parsers do not need to track which
	// entry shape was used, and resolved_target_source surfaces whether the
	// target_repo came from an explicit flag, the cwd auto-resolve
	// fallback, or the single-repo primary default — operators can grep on
	// `resolved_target_source=auto_resolve` to audit cwd-derived targets.
	fmt.Printf("[OP:%q] source=%q project=%q target_repo=%q resolved_target_source=%q request_id=%q resolved_operation_id=%q\n",
		req.Operation, req.Source, projectID, target.Repo, resolvedSource, req.RequestID, req.Operation)

	// Validate operation against per-target context
	op, result := s.validator.ValidateOperation(req, projectConfig, target)
	if !result.OK {
		fmt.Printf("  -> DENIED: %s\n", result.Message)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(result.Message),
		})
		return
	}

	// Misconfiguration detector: verify the target repo_path's origin remote
	// matches the resolved target_repo. This is not the primary security
	// boundary (explicit URL fixation is) but catches obvious config drift.
	if err := VerifyPathRepoConsistency(target.RepoPath, target.Repo); err != nil {
		fmt.Printf("  -> DENIED (consistency): %v\n", err)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(err.Error()),
		})
		return
	}

	// Build arguments from template.
	// Template placeholders that depend on per-request target context are
	// injected here so a single template can serve any repo in the allow list.
	projectEnv := projectConfig.GetEnvForOperation()
	projectEnv["repo"] = target.Repo
	projectEnv["repo_path"] = target.RepoPath
	projectEnv["expected_git_url"] = target.ExpectedGitURL
	args, err := op.BuildArgs(req.Params, req.Flags, projectEnv)
	if err != nil {
		fmt.Printf("  -> ARG BUILD FAILED: %v\n", err)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(fmt.Sprintf("Failed to build arguments: %v", err)),
		})
		return
	}

	// Execute with sanitized environment + per-target working directory.
	resp := s.executeWithSanitization(op.Command, args, projectConfig, target)
	fmt.Printf("  -> exit_code=%d\n", resp.ExitCode)

	s.sendOperationResponse(conn, operations.Response{
		RequestID:           req.RequestID,
		ExitCode:            resp.ExitCode,
		Stdout:              resp.Stdout,
		Stderr:              resp.Stderr,
		StdoutTruncated:     resp.StdoutTruncated,
		StderrTruncated:     resp.StderrTruncated,
		StdoutOriginalBytes: resp.StdoutOriginalBytes,
		StderrOriginalBytes: resp.StderrOriginalBytes,
	})
}

// buildReverseMatchCandidates assembles ReverseMatch input from the project
// config in AllowedOperations declaration order so ambiguity errors list
// candidates deterministically.
func buildReverseMatchCandidates(p *config.ProjectConfig) []operations.CandidateOp {
	if p == nil {
		return nil
	}
	out := make([]operations.CandidateOp, 0, len(p.AllowedOperations))
	for _, opID := range p.AllowedOperations {
		op, ok := p.GetOperation(opID)
		if !ok {
			continue
		}
		out = append(out, operations.CandidateOp{ID: opID, Operation: op})
	}
	return out
}

// executeWithSanitization executes a command with sanitized environment
func (s *Server) executeWithSanitization(cmdName string, args []string, project *config.ProjectConfig, target *ExecutionTarget) ExecuteResult {
	// Validate command path
	if err := ValidateCommandPath(cmdName); err != nil {
		return ExecuteResult{
			ExitCode: 127,
			Stderr:   err.Error(),
		}
	}

	// Create sanitizer with project + target and prepare command once
	sanitizer := NewCommandSanitizer(project, target)
	preparedCmd := sanitizer.PrepareCommand(cmdName, args)

	timeout := time.Duration(s.daemonConfig.DefaultTimeout) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Set up command with context, preserving sanitizer configuration
	cmd := exec.CommandContext(ctx, preparedCmd.Path, preparedCmd.Args[1:]...)
	cmd.Env = preparedCmd.Env
	cmd.Dir = preparedCmd.Dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Cap stream output and capture per-stream truncation indicators. These
	// values flow through the response chain on paths that surface the actual
	// command output (success exit and *exec.ExitError). Error paths that
	// substitute a synthetic stderr message (timeout, command-not-found, generic
	// runtime error) do not surface them, so consumers must treat truncation
	// metadata as meaningful only when the response carries real command output.
	stdoutStr, stdoutBytes, stdoutTrunc := truncateOutput(stdout.String(), s.daemonConfig.MaxStdoutBytes)
	stderrStr, stderrBytes, stderrTrunc := truncateOutput(stderr.String(), s.daemonConfig.MaxStderrBytes)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ExecuteResult{
				ExitCode: 124,
				Stderr:   "Command timed out",
			}
		}

		if exitErr, ok := err.(*exec.ExitError); ok {
			return ExecuteResult{
				ExitCode:            exitErr.ExitCode(),
				Stdout:              stdoutStr,
				Stderr:              stderrStr,
				StdoutTruncated:     stdoutTrunc,
				StderrTruncated:     stderrTrunc,
				StdoutOriginalBytes: stdoutBytes,
				StderrOriginalBytes: stderrBytes,
			}
		}

		if _, ok := err.(*exec.Error); ok {
			return ExecuteResult{
				ExitCode: 127,
				Stderr:   "Command not found: " + cmdName,
			}
		}
		if errors.Is(err, os.ErrNotExist) {
			return ExecuteResult{
				ExitCode: 127,
				Stderr:   "Command not found: " + cmdName,
			}
		}

		return ExecuteResult{
			ExitCode: 1,
			Stderr:   err.Error(),
		}
	}

	return ExecuteResult{
		ExitCode:            0,
		Stdout:              stdoutStr,
		Stderr:              stderrStr,
		StdoutTruncated:     stdoutTrunc,
		StderrTruncated:     stderrTrunc,
		StdoutOriginalBytes: stdoutBytes,
		StderrOriginalBytes: stderrBytes,
	}
}

// truncateOutput caps s at maxBytes. When truncation happens, the returned
// string is the byte prefix at the rune boundary at or before maxBytes and
// wasTruncated is true; the daemon does not mix any synthetic marker into
// the stream body. Consumers signal truncation to their downstream via the
// typed StdoutTruncated / StderrTruncated and StdoutOriginalBytes /
// StderrOriginalBytes fields on the response, which keeps the daemon's
// stream output a clean prefix of the original command output suitable for
// streaming JSON parsers.
//
// originalBytes is always the byte length of the input regardless of
// truncation, so consumers can report how much of the original stream was
// actually surfaced even when truncation is disabled (maxBytes <= 0).
//
// When the cap falls inside a multi-byte UTF-8 sequence, the cut point is
// pulled back to the previous rune boundary so the daemon never marshals an
// invalid UTF-8 string. Without this, encoding/json would silently replace
// the trailing partial rune with U+FFFD, making the indicator's "shown N of
// M bytes" count drift from what the consumer actually sees.
func truncateOutput(s string, maxBytes int) (out string, originalBytes int64, wasTruncated bool) {
	originalBytes = int64(len(s))
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, originalBytes, false
	}
	cut := runeBoundaryBefore(s, maxBytes)
	return s[:cut], originalBytes, true
}

// runeBoundaryBefore returns the largest index i in [0, max] such that
// s[:i] ends on a UTF-8 rune boundary. ASCII-only inputs always return max.
func runeBoundaryBefore(s string, max int) int {
	for i := max; i > 0; i-- {
		// A byte is a continuation byte iff (b & 0xC0) == 0x80. The cut
		// point is valid when the byte AT i is either past the end (i == len)
		// or starts a new rune (top two bits != 0b10).
		if i == len(s) || (s[i]&0xC0) != 0x80 {
			return i
		}
	}
	return 0
}

// handleListOperationsRequest handles requests to list available operations
func (s *Server) handleListOperationsRequest(conn net.Conn, data []byte, releaseSlot func()) {
	var req operations.ListOperationsRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.sendListOperationsResponse(conn, operations.ListOperationsResponse{
			Error: fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Authenticate token
	tokenData, valid := s.tokenStore.GetTokenData(req.Token)
	if !valid {
		fmt.Println("  -> AUTH FAILED (list_operations)")
		if releaseSlot != nil {
			releaseSlot()
		}
		time.Sleep(1 * time.Second)
		s.sendListOperationsResponse(conn, operations.ListOperationsResponse{
			Error: "Authentication failed",
		})
		return
	}

	// Resolve project config from token
	projectConfig, projectID, err := s.resolveProject(tokenData)
	if err != nil {
		s.sendListOperationsResponse(conn, operations.ListOperationsResponse{
			Error: err.Error(),
		})
		return
	}

	fmt.Printf("[LIST_OPERATIONS] project=%s\n", projectID)

	// Build list of operations available to this project
	var ops []operations.OperationInfo
	for _, opID := range projectConfig.AllowedOperations {
		// Apply prefix filter if specified
		if req.Prefix != "" && !strings.HasPrefix(opID, req.Prefix) {
			continue
		}
		op, exists := projectConfig.GetOperation(opID)
		if !exists {
			continue
		}
		ops = append(ops, operations.OperationInfo{
			ID:           opID,
			Command:      op.Command,
			Description:  op.Description,
			Params:       op.Params,
			AllowedFlags: op.AllowedFlags,
		})
	}

	s.sendListOperationsResponse(conn, operations.ListOperationsResponse{
		Operations: ops,
	})
}

// handleDescribeOperationRequest handles requests to describe a specific operation
func (s *Server) handleDescribeOperationRequest(conn net.Conn, data []byte, releaseSlot func()) {
	var req operations.DescribeOperationRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.sendDescribeOperationResponse(conn, operations.DescribeOperationResponse{
			Error: fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Authenticate token
	tokenData, valid := s.tokenStore.GetTokenData(req.Token)
	if !valid {
		fmt.Println("  -> AUTH FAILED (describe_operation)")
		if releaseSlot != nil {
			releaseSlot()
		}
		time.Sleep(1 * time.Second)
		s.sendDescribeOperationResponse(conn, operations.DescribeOperationResponse{
			Error: "Authentication failed",
		})
		return
	}

	// Resolve project config from token
	projectConfig, projectID, err := s.resolveProject(tokenData)
	if err != nil {
		s.sendDescribeOperationResponse(conn, operations.DescribeOperationResponse{
			Error: err.Error(),
		})
		return
	}

	// Check if operation is allowed for this project
	if !projectConfig.HasOperation(req.DescribeOperation) {
		s.sendDescribeOperationResponse(conn, operations.DescribeOperationResponse{
			Error: fmt.Sprintf("Operation not allowed: %s", req.DescribeOperation),
		})
		return
	}

	op, exists := projectConfig.GetOperation(req.DescribeOperation)
	if !exists {
		s.sendDescribeOperationResponse(conn, operations.DescribeOperationResponse{
			Error: fmt.Sprintf("Operation not found: %s", req.DescribeOperation),
		})
		return
	}

	fmt.Printf("[DESCRIBE_OPERATION] project=%s operation=%s\n", projectID, req.DescribeOperation)

	s.sendDescribeOperationResponse(conn, operations.DescribeOperationResponse{
		Operation: &operations.OperationInfo{
			ID:           req.DescribeOperation,
			Command:      op.Command,
			Description:  op.Description,
			Params:       op.Params,
			AllowedFlags: op.AllowedFlags,
		},
	})
}

// sendListOperationsResponse writes a list operations response to the connection
func (s *Server) sendListOperationsResponse(conn net.Conn, resp operations.ListOperationsResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Println("  -> ERROR marshaling response:", err)
		return
	}
	if _, err := conn.Write(data); err != nil {
		fmt.Println("  -> ERROR writing response:", err)
	}
}

// sendDescribeOperationResponse writes a describe operation response to the connection
func (s *Server) sendDescribeOperationResponse(conn net.Conn, resp operations.DescribeOperationResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Println("  -> ERROR marshaling response:", err)
		return
	}
	if _, err := conn.Write(data); err != nil {
		fmt.Println("  -> ERROR writing response:", err)
	}
}

// sendOperationResponse writes an operation response to the connection
func (s *Server) sendOperationResponse(conn net.Conn, resp operations.Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Println("  -> ERROR marshaling response:", err)
		return
	}
	if _, err := conn.Write(data); err != nil {
		fmt.Println("  -> ERROR writing response:", err)
	}
}

// strPtr returns a pointer to the string
func strPtr(s string) *string {
	return &s
}

// Run starts the server based on listen mode
func (s *Server) Run() error {
	// Cleanup expired tokens on startup
	if err := s.tokenStore.CleanupExpired(); err != nil {
		fmt.Printf("Warning: failed to cleanup expired tokens: %v\n", err)
	}

	// Periodic cleanup for long-running daemons
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := s.tokenStore.CleanupExpired(); err != nil {
				fmt.Printf("Warning: periodic token cleanup failed: %v\n", err)
			}
		}
	}()

	// List configured projects from this Server's base dir so the startup
	// banner reflects the same dir as resolveProject (NewServerAt callers
	// need the per-instance view, not the env-resolved one).
	projects, _ := config.ListProjectsAt(s.baseDir)
	if len(projects) > 0 {
		fmt.Printf("Projects: %v\n", projects)
	}
	fmt.Println()

	switch s.daemonConfig.ListenMode {
	case "tcp":
		return s.runTCP()
	case "unix":
		return s.runUnix()
	case "both":
		return s.runBoth()
	default:
		return fmt.Errorf("invalid listen_mode: %s (must be tcp, unix, or both)", s.daemonConfig.ListenMode)
	}
}

// runTCP starts only the TCP listener
func (s *Server) runTCP() error {
	addr := net.JoinHostPort(s.daemonConfig.ListenAddress, strconv.Itoa(s.daemonConfig.ListenPort))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on TCP %s: %w", addr, err)
	}
	s.tcpListener = listener
	fmt.Printf("cmd2host listening on %s (TCP)\n", addr)
	return s.acceptLoop(listener)
}

// runUnix starts only the Unix socket listener
func (s *Server) runUnix() error {
	listener, err := s.createUnixListener()
	if err != nil {
		return err
	}
	s.unixListener = listener
	fmt.Printf("cmd2host listening on %s (Unix socket)\n", s.daemonConfig.SocketPath)
	return s.acceptLoop(listener)
}

// runBoth starts both TCP and Unix socket listeners
func (s *Server) runBoth() error {
	// Start TCP listener
	tcpAddr := net.JoinHostPort(s.daemonConfig.ListenAddress, strconv.Itoa(s.daemonConfig.ListenPort))
	tcpListener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on TCP %s: %w", tcpAddr, err)
	}
	s.tcpListener = tcpListener

	// Start Unix listener
	unixListener, err := s.createUnixListener()
	if err != nil {
		tcpListener.Close()
		return err
	}
	s.unixListener = unixListener

	fmt.Printf("cmd2host listening on %s (TCP) and %s (Unix socket)\n", tcpAddr, s.daemonConfig.SocketPath)

	// Run both accept loops concurrently
	errCh := make(chan error, 2)
	go func() { errCh <- s.acceptLoop(tcpListener) }()
	go func() { errCh <- s.acceptLoop(unixListener) }()

	// Return first error (usually from shutdown)
	return <-errCh
}

// createUnixListener creates and configures a Unix domain socket listener
func (s *Server) createUnixListener() (net.Listener, error) {
	path := s.daemonConfig.SocketPath

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Remove stale socket file if exists
	if info, err := os.Stat(path); err == nil {
		if info.Mode()&os.ModeSocket != 0 {
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("failed to remove stale socket %s: %w", path, err)
			}
		} else {
			return nil, fmt.Errorf("path %s exists but is not a socket", path)
		}
	}

	// Create listener
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("failed to create unix socket %s: %w", path, err)
	}

	// Set permissions
	if err := os.Chmod(path, os.FileMode(s.daemonConfig.SocketMode)); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	return listener, nil
}

// acceptLoop handles incoming connections on a listener.
//
// When the in-flight cap is set (inFlightSem != nil) the loop tries to
// acquire a slot before spawning a handler. If the cap is reached the
// connection is closed immediately without reading a request; this keeps
// the daemon's authentication-failure delay from amplifying a burst of
// connections into a long backlog of stacked sleeps.
func (s *Server) acceptLoop(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			// Check if listener was closed
			if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
				return nil
			}
			fmt.Println("Accept error:", err)
			continue
		}
		s.dispatchConn(conn)
	}
}

// dispatchConn launches handleClient under the in-flight cap. When the cap
// channel is non-nil and at capacity the connection is dropped immediately
// (no response read, no response written) so the caller sees a clean EOF.
//
// The release callback handed to handleClient is wrapped in sync.Once so
// the handler can return the slot early (e.g. before the
// authentication-failure throttle sleep) without racing the goroutine's
// final defer, which catches any path that did not release explicitly.
func (s *Server) dispatchConn(conn net.Conn) {
	if s.inFlightSem == nil {
		go s.handleClient(conn, func() {})
		return
	}
	select {
	case s.inFlightSem <- struct{}{}:
		var once sync.Once
		release := func() {
			once.Do(func() { <-s.inFlightSem })
		}
		go func() {
			defer release()
			s.handleClient(conn, release)
		}()
	default:
		fmt.Printf("  -> connection dropped: max_in_flight=%d reached\n", cap(s.inFlightSem))
		conn.Close()
	}
}

// Shutdown gracefully stops the server
func (s *Server) Shutdown() {
	if s.tcpListener != nil {
		s.tcpListener.Close()
	}
	if s.unixListener != nil {
		s.unixListener.Close()
		// Clean up socket file
		os.Remove(s.daemonConfig.SocketPath)
	}
}
