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
}

// Server handles TCP connections and command proxying
type Server struct {
	config    *Config
	validator *Validator
	executor  *Executor
	listener  net.Listener
}

// NewServer creates a new Server
func NewServer(config *Config) *Server {
	return &Server{
		config:    config,
		validator: NewValidator(config),
		executor:  NewExecutor(config),
	}
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

	// Default command
	if req.Command == "" {
		req.Command = "gh"
	}

	fmt.Printf("[%s] %s\n", req.Command, strings.Join(req.Args, " "))

	// Validate command
	result := s.validator.ValidateCommand(req.Command, req.Args)
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
	addr := fmt.Sprintf("%s:%d", s.config.ListenAddress, s.config.ListenPort)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.listener = listener

	fmt.Printf("cmd2host listening on %s\n", addr)
	fmt.Printf("Configured commands: %v\n", s.commandNames())
	if len(s.config.AllowedRepositories) > 0 {
		fmt.Printf("Allowed repositories: %v\n", s.config.AllowedRepositories)
	} else {
		fmt.Println("Allowed repositories: all")
	}
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
	configPath := DefaultConfigPath()
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Run: curl -fsSL https://raw.githubusercontent.com/taisukeoe/cmd2host/main/host/install.sh | bash")
		os.Exit(1)
	}

	server := NewServer(config)

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
