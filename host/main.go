package main

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
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

const (
	readTimeout = 5 * time.Second
	maxReadSize = 65536
)

// Server handles TCP connections and command proxying
type Server struct {
	config     *Config
	validator  *Validator
	tokenStore *TokenStore
	listener   net.Listener
}

// NewServer creates a new Server
func NewServer(config *Config) (*Server, error) {
	tokenStore, err := NewTokenStore()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize token store: %w", err)
	}
	return &Server{
		config:     config,
		validator:  NewValidator(config),
		tokenStore: tokenStore,
	}, nil
}

// handleClient processes a single client connection
func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()

	// Set read deadline
	conn.SetReadDeadline(time.Now().Add(readTimeout))

	// Read request until EOF (client closes connection after sending)
	// Use LimitReader to prevent memory exhaustion
	buf, err := io.ReadAll(io.LimitReader(conn, maxReadSize))
	if err != nil {
		fmt.Println("  -> ERROR reading:", err)
		return
	}
	if len(buf) == 0 {
		return // Empty request, nothing to do
	}

	// Try to detect request type by peeking at JSON
	var rawRequest map[string]json.RawMessage
	if err := json.Unmarshal(buf, &rawRequest); err != nil {
		fmt.Println("  -> Invalid JSON:", err)
		s.sendOperationResponse(conn, OperationResponse{
			ExitCode:     1,
			DeniedReason: strPtr(fmt.Sprintf("Invalid JSON: %v", err)),
		})
		return
	}

	// Determine request type by checking for specific fields
	if _, hasListOps := rawRequest["list_operations"]; hasListOps {
		s.handleListOperationsRequest(conn, buf)
	} else if _, hasDescribeOp := rawRequest["describe_operation"]; hasDescribeOp {
		s.handleDescribeOperationRequest(conn, buf)
	} else if _, hasOperation := rawRequest["operation"]; hasOperation {
		s.handleOperationRequest(conn, buf)
	} else {
		fmt.Println("  -> Unknown request type (missing 'operation' field)")
		s.sendOperationResponse(conn, OperationResponse{
			ExitCode:     1,
			DeniedReason: strPtr("Unknown request type: missing 'operation' field"),
		})
	}
}

// handleOperationRequest handles new-style operation requests
func (s *Server) handleOperationRequest(conn net.Conn, data []byte) {
	var req OperationRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Println("  -> Invalid operation request:", err)
		s.sendOperationResponse(conn, OperationResponse{
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
		s.sendOperationResponse(conn, OperationResponse{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr("Authentication failed"),
		})
		return
	}

	// Resolve profile from token (with fallback to default)
	profile, profileName, err := s.resolveProfile(tokenData)
	if err != nil {
		fmt.Printf("  -> %v\n", err)
		s.sendOperationResponse(conn, OperationResponse{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(err.Error()),
		})
		return
	}

	// Log operation request (params omitted to avoid logging sensitive data like PR body)
	fmt.Printf("[OP:%s] profile=%s request_id=%s\n", req.Operation, profileName, req.RequestID)

	// Validate operation
	op, result := s.validator.ValidateOperation(req, profile)
	if !result.OK {
		fmt.Printf("  -> DENIED: %s\n", result.Message)
		s.sendOperationResponse(conn, OperationResponse{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(result.Message),
		})
		return
	}

	// Build arguments from template
	profileEnv := profile.GetEnvForOperation()
	// Inject token's repo into template expansion
	if tokenData.Repo != "" {
		profileEnv["repo"] = tokenData.Repo
	}
	args, err := op.BuildArgs(req.Params, req.Flags, profileEnv)
	if err != nil {
		fmt.Printf("  -> ARG BUILD FAILED: %v\n", err)
		s.sendOperationResponse(conn, OperationResponse{
			RequestID:    req.RequestID,
			ExitCode:     1,
			DeniedReason: strPtr(fmt.Sprintf("Failed to build arguments: %v", err)),
		})
		return
	}

	// Execute with sanitized environment
	resp := s.executeWithSanitization(op.Command, args, profile)
	fmt.Printf("  -> exit_code=%d\n", resp.ExitCode)

	s.sendOperationResponse(conn, OperationResponse{
		RequestID: req.RequestID,
		ExitCode:  resp.ExitCode,
		Stdout:    resp.Stdout,
		Stderr:    resp.Stderr,
	})
}

// executeWithSanitization executes a command with sanitized environment
func (s *Server) executeWithSanitization(cmdName string, args []string, profile *Profile) ExecuteResult {
	// Validate command path
	if err := ValidateCommandPath(cmdName); err != nil {
		return ExecuteResult{
			ExitCode: 127,
			Stderr:   err.Error(),
		}
	}

	// Create sanitizer with profile and prepare command once
	sanitizer := NewCommandSanitizer(profile)
	preparedCmd := sanitizer.PrepareCommand(cmdName, args)

	timeout := time.Duration(s.config.DefaultTimeout) * time.Second
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
	stdoutStr := truncateOutput(stdout.String(), s.config.MaxStdoutBytes)
	stderrStr := truncateOutput(stderr.String(), s.config.MaxStderrBytes)

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
	var req ListOperationsRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.sendListOperationsResponse(conn, ListOperationsResponse{
			Error: fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Authenticate token
	tokenData, valid := s.tokenStore.GetTokenData(req.Token)
	if !valid {
		time.Sleep(1 * time.Second)
		fmt.Println("  -> AUTH FAILED (list_operations)")
		s.sendListOperationsResponse(conn, ListOperationsResponse{
			Error: "Authentication failed",
		})
		return
	}

	// Resolve profile from token (with fallback to default)
	profile, profileName, err := s.resolveProfile(tokenData)
	if err != nil {
		s.sendListOperationsResponse(conn, ListOperationsResponse{
			Error: err.Error(),
		})
		return
	}

	fmt.Printf("[LIST_OPERATIONS] profile=%s\n", profileName)

	// Build list of operations available to this profile
	var ops []OperationInfo
	for _, opID := range profile.Operations {
		op, exists := s.config.GetOperation(opID)
		if !exists {
			continue
		}
		ops = append(ops, OperationInfo{
			ID:           opID,
			Command:      op.Command,
			Description:  op.Description,
			Params:       op.Params,
			AllowedFlags: op.AllowedFlags,
		})
	}

	s.sendListOperationsResponse(conn, ListOperationsResponse{
		Operations: ops,
	})
}

// handleDescribeOperationRequest handles requests to describe a specific operation
func (s *Server) handleDescribeOperationRequest(conn net.Conn, data []byte) {
	var req DescribeOperationRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.sendDescribeOperationResponse(conn, DescribeOperationResponse{
			Error: fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Authenticate token
	tokenData, valid := s.tokenStore.GetTokenData(req.Token)
	if !valid {
		time.Sleep(1 * time.Second)
		fmt.Println("  -> AUTH FAILED (describe_operation)")
		s.sendDescribeOperationResponse(conn, DescribeOperationResponse{
			Error: "Authentication failed",
		})
		return
	}

	// Resolve profile from token (with fallback to default)
	profile, profileName, err := s.resolveProfile(tokenData)
	if err != nil {
		s.sendDescribeOperationResponse(conn, DescribeOperationResponse{
			Error: err.Error(),
		})
		return
	}

	// Check if operation is allowed for this profile
	if !profile.HasOperation(req.DescribeOperation) {
		s.sendDescribeOperationResponse(conn, DescribeOperationResponse{
			Error: fmt.Sprintf("Operation not allowed: %s", req.DescribeOperation),
		})
		return
	}

	op, exists := s.config.GetOperation(req.DescribeOperation)
	if !exists {
		s.sendDescribeOperationResponse(conn, DescribeOperationResponse{
			Error: fmt.Sprintf("Operation not found: %s", req.DescribeOperation),
		})
		return
	}

	fmt.Printf("[DESCRIBE_OPERATION] profile=%s operation=%s\n", profileName, req.DescribeOperation)

	s.sendDescribeOperationResponse(conn, DescribeOperationResponse{
		Operation: &OperationInfo{
			ID:           req.DescribeOperation,
			Command:      op.Command,
			Description:  op.Description,
			Params:       op.Params,
			AllowedFlags: op.AllowedFlags,
		},
	})
}

// sendListOperationsResponse writes a list operations response to the connection
func (s *Server) sendListOperationsResponse(conn net.Conn, resp ListOperationsResponse) {
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
func (s *Server) sendDescribeOperationResponse(conn net.Conn, resp DescribeOperationResponse) {
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
func (s *Server) sendOperationResponse(conn net.Conn, resp OperationResponse) {
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

// resolveProfile resolves the profile for a token, using default_profile as fallback
func (s *Server) resolveProfile(tokenData TokenData) (*Profile, string, error) {
	profileName := tokenData.Profile
	if profileName == "" {
		profileName = s.config.DefaultProfile
	}
	if profileName == "" {
		return nil, "", fmt.Errorf("token does not have a profile assigned and no default_profile configured")
	}
	profile, exists := s.config.GetProfile(profileName)
	if !exists {
		return nil, "", fmt.Errorf("profile not found: %s", profileName)
	}
	return profile, profileName, nil
}

// Run starts the TCP server
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

	addr := fmt.Sprintf("%s:%d", s.config.ListenAddress, s.config.ListenPort)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.listener = listener

	fmt.Printf("cmd2host listening on %s\n", addr)
	if names := s.profileNames(); len(names) > 0 {
		fmt.Printf("Profiles: %v\n", names)
	}
	if s.config.DefaultProfile != "" {
		fmt.Printf("Default profile: %s\n", s.config.DefaultProfile)
	}
	fmt.Println("Repository restriction: bound to token (set at session init)")
	fmt.Println()

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
	if s.listener != nil {
		s.listener.Close()
	}
}

// profileNames returns a list of configured profile names
func (s *Server) profileNames() []string {
	names := make([]string, 0, len(s.config.Profiles))
	for name := range s.config.Profiles {
		names = append(names, name)
	}
	return names
}

func main() {
	// Handle --version flag
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("cmd2host version %s\n", version)
		return
	}

	// Handle --hash-token for generating token hashes (used by init scripts)
	// Token is read from stdin to avoid exposure in process list (ps aux)
	if len(os.Args) == 2 && os.Args[1] == "--hash-token" {
		token, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading token from stdin: %v\n", err)
			os.Exit(1)
		}
		tokenStr := strings.TrimSpace(string(token))
		if tokenStr == "" {
			fmt.Fprintln(os.Stderr, "Error: empty token")
			os.Exit(1)
		}
		fmt.Println(hashToken(tokenStr))
		return
	}

	configPath := DefaultConfigPath()
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Run: curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/scripts/install.sh | bash")
		os.Exit(1)
	}

	server, err := NewServer(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Server initialization error: %v\n", err)
		os.Exit(1)
	}

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		server.Shutdown()
	}()

	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
