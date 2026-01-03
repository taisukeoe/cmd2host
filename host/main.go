package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	readTimeout = 5 * time.Second
	maxReadSize = 65536
)

// Request represents an incoming command request
type Request struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Token   string   `json:"token"`
}

// Server handles TCP connections and command proxying
type Server struct {
	config     *Config
	validator  *Validator
	executor   *Executor
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
		executor:   NewExecutor(config),
		tokenStore: tokenStore,
	}, nil
}

// handleClient processes a single client connection
func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()

	// Set read deadline
	conn.SetReadDeadline(time.Now().Add(readTimeout))

	// Read request
	buf := make([]byte, maxReadSize)
	n, err := conn.Read(buf)
	if err != nil {
		if err != io.EOF {
			fmt.Println("  -> ERROR reading:", err)
		}
		return
	}

	// Parse request
	var req Request
	if err := json.Unmarshal(buf[:n], &req); err != nil {
		fmt.Println("  -> Invalid JSON:", err)
		resp := ExecuteResult{
			ExitCode: 1,
			Stderr:   fmt.Sprintf("Invalid JSON: %v", err),
		}
		s.sendResponse(conn, resp)
		return
	}

	// Authenticate token and get project data
	tokenData, valid := s.tokenStore.GetTokenData(req.Token)
	if !valid {
		time.Sleep(1 * time.Second) // Delay to slow down brute force attacks
		fmt.Println("  -> AUTH FAILED")
		resp := ExecuteResult{
			ExitCode: 1,
			Stderr:   "Authentication failed",
		}
		s.sendResponse(conn, resp)
		return
	}

	// Default command
	if req.Command == "" {
		req.Command = "gh"
	}

	fmt.Printf("[%s] %s\n", req.Command, strings.Join(req.Args, " "))

	// Validate command using repo from token (not from request - prevents spoofing)
	result := s.validator.ValidateCommand(req.Command, req.Args, tokenData.Repo)
	if !result.OK {
		fmt.Printf("  -> DENIED: %s\n", result.Message)
		resp := ExecuteResult{
			ExitCode: 1,
			Stderr:   result.Message,
		}
		s.sendResponse(conn, resp)
		return
	}

	// Execute command
	resp := s.executor.Execute(req.Command, req.Args)
	fmt.Printf("  -> exit_code=%d\n", resp.ExitCode)

	s.sendResponse(conn, resp)
}

// sendResponse writes a JSON response to the connection
func (s *Server) sendResponse(conn net.Conn, resp ExecuteResult) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Println("  -> ERROR marshaling response:", err)
		return
	}
	conn.Write(data)
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
	fmt.Printf("Configured commands: %v\n", s.commandNames())
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

// commandNames returns a list of configured command names
func (s *Server) commandNames() []string {
	names := make([]string, 0, len(s.config.Commands))
	for name := range s.config.Commands {
		names = append(names, name)
	}
	return names
}

func main() {
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
