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
	"time"

	"github.com/taisukeoe/cmd2host/pkg/auth"
	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
)

const (
	readTimeout = 5 * time.Second
	maxReadSize = 65536
)

// Server handles TCP and Unix socket connections and command proxying
type Server struct {
	daemonConfig *config.DaemonConfig
	validator    *Validator
	tokenStore   *auth.TokenStore
	tcpListener  net.Listener
	unixListener net.Listener
}

// NewServer creates a new Server
func NewServer(daemonConfig *config.DaemonConfig) (*Server, error) {
	tokenStore, err := auth.NewTokenStore()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize token store: %w", err)
	}
	return &Server{
		daemonConfig: daemonConfig,
		validator:    NewValidator(),
		tokenStore:   tokenStore,
	}, nil
}

// handleClient processes a single client connection
func (s *Server) handleClient(conn net.Conn) {
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

	// Determine request type by checking for specific fields
	if _, hasListOps := rawRequest["list_operations"]; hasListOps {
		s.handleListOperationsRequest(conn, data)
	} else if _, hasDescribeOp := rawRequest["describe_operation"]; hasDescribeOp {
		s.handleDescribeOperationRequest(conn, data)
	} else if _, hasOperation := rawRequest["operation"]; hasOperation {
		s.handleOperationRequest(conn, data)
	} else {
		fmt.Println("  -> Unknown request type (missing 'operation' field)")
		s.sendOperationResponse(conn, operations.Response{
			ExitCode:     1,
			DeniedReason: strPtr("Unknown request type: missing 'operation' field"),
		})
	}
}

// resolveProject resolves project config from token data
func (s *Server) resolveProject(tokenData auth.TokenData) (*config.ProjectConfig, string, error) {
	if tokenData.Repo == "" {
		return nil, "", fmt.Errorf("token does not have a repository bound")
	}

	projectID := config.NormalizeProjectID(tokenData.Repo)

	// Load project config
	projectConfig, err := config.LoadProjectConfig(projectID)
	if err != nil {
		return nil, projectID, err
	}

	// Verify projectConfig.Repo matches tokenData.Repo to prevent config tampering
	if projectConfig.Repo != tokenData.Repo {
		return nil, projectID, fmt.Errorf("config repo mismatch: token bound to %q but config specifies %q", tokenData.Repo, projectConfig.Repo)
	}

	// Verify config is allowed
	allowed, currentHash, err := config.IsConfigAllowed(projectID)
	if err != nil {
		return nil, projectID, fmt.Errorf("failed to check config allowance: %w", err)
	}
	if !allowed {
		return nil, projectID, fmt.Errorf("config not allowed (hash: %s). Run: cmd2host config allow %s", currentHash[:16], projectID)
	}

	return projectConfig, projectID, nil
}

// handleOperationRequest handles new-style operation requests
func (s *Server) handleOperationRequest(conn net.Conn, data []byte) {
	var req operations.Request
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Println("  -> Invalid operation request:", err)
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
		time.Sleep(1 * time.Second) // Delay to slow down brute force attacks
		fmt.Println("  -> AUTH FAILED")
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
		fmt.Printf("  -> %v\n", err)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(err.Error()),
		})
		return
	}

	// Log operation request (params omitted to avoid logging sensitive data like PR body)
	fmt.Printf("[OP:%s] project=%s request_id=%s\n", req.Operation, projectID, req.RequestID)

	// Validate operation
	op, result := s.validator.ValidateOperation(req, projectConfig)
	if !result.OK {
		fmt.Printf("  -> DENIED: %s\n", result.Message)
		s.sendOperationResponse(conn, operations.Response{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(result.Message),
		})
		return
	}

	// Build arguments from template
	projectEnv := projectConfig.GetEnvForOperation()
	// Inject token's repo into template expansion
	if tokenData.Repo != "" {
		projectEnv["repo"] = tokenData.Repo
	}
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

	// Execute with sanitized environment
	resp := s.executeWithSanitization(op.Command, args, projectConfig)
	fmt.Printf("  -> exit_code=%d\n", resp.ExitCode)

	s.sendOperationResponse(conn, operations.Response{
		RequestID: req.RequestID,
		ExitCode:  resp.ExitCode,
		Stdout:    resp.Stdout,
		Stderr:    resp.Stderr,
	})
}

// executeWithSanitization executes a command with sanitized environment
func (s *Server) executeWithSanitization(cmdName string, args []string, project *config.ProjectConfig) ExecuteResult {
	// Validate command path
	if err := ValidateCommandPath(cmdName); err != nil {
		return ExecuteResult{
			ExitCode: 127,
			Stderr:   err.Error(),
		}
	}

	// Create sanitizer with project and prepare command once
	sanitizer := NewCommandSanitizer(project)
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

	// Truncate output if needed
	stdoutStr := truncateOutput(stdout.String(), s.daemonConfig.MaxStdoutBytes)
	stderrStr := truncateOutput(stderr.String(), s.daemonConfig.MaxStderrBytes)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ExecuteResult{
				ExitCode: 124,
				Stderr:   "Command timed out",
			}
		}

		if exitErr, ok := err.(*exec.ExitError); ok {
			return ExecuteResult{
				ExitCode: exitErr.ExitCode(),
				Stdout:   stdoutStr,
				Stderr:   stderrStr,
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
		ExitCode: 0,
		Stdout:   stdoutStr,
		Stderr:   stderrStr,
	}
}

// truncateOutput truncates output to maxBytes
func truncateOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n... (truncated)"
}

// handleListOperationsRequest handles requests to list available operations
func (s *Server) handleListOperationsRequest(conn net.Conn, data []byte) {
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
		time.Sleep(1 * time.Second)
		fmt.Println("  -> AUTH FAILED (list_operations)")
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
func (s *Server) handleDescribeOperationRequest(conn net.Conn, data []byte) {
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
		time.Sleep(1 * time.Second)
		fmt.Println("  -> AUTH FAILED (describe_operation)")
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

	// List configured projects
	projects, _ := config.ListProjects()
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

// acceptLoop handles incoming connections on a listener
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
		go s.handleClient(conn)
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
