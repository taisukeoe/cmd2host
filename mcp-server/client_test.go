package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	client := NewClient("localhost", 9876, "test-token")

	if client.host != "localhost" {
		t.Errorf("Expected host 'localhost', got '%s'", client.host)
	}
	if client.port != 9876 {
		t.Errorf("Expected port 9876, got %d", client.port)
	}
	if client.token != "test-token" {
		t.Errorf("Expected token 'test-token', got '%s'", client.token)
	}
	if client.socketPath != "" {
		t.Errorf("Expected empty socketPath, got '%s'", client.socketPath)
	}
}

func TestNewUnixClient(t *testing.T) {
	client := NewUnixClient("/var/run/cmd2host.sock", "test-token")

	if client.socketPath != "/var/run/cmd2host.sock" {
		t.Errorf("Expected socketPath '/var/run/cmd2host.sock', got '%s'", client.socketPath)
	}
	if client.token != "test-token" {
		t.Errorf("Expected token 'test-token', got '%s'", client.token)
	}
	if client.host != "" {
		t.Errorf("Expected empty host, got '%s'", client.host)
	}
	if client.port != 0 {
		t.Errorf("Expected port 0, got %d", client.port)
	}
}

func TestClient_ConnectFailure(t *testing.T) {
	// Try to connect to a port that's not listening
	client := NewClient("127.0.0.1", 59999, "test-token")
	_, err := client.connect()
	if err == nil {
		t.Error("Expected connection error, got nil")
	}
}

func TestClient_ListOperations(t *testing.T) {
	// Start a mock server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)

	// Handle connection in goroutine
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read request
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)

		var req ListOperationsRequest
		json.Unmarshal(buf[:n], &req)

		// Send response
		resp := ListOperationsResponse{
			Operations: []OperationInfo{
				{ID: "test_op", Description: "Test operation"},
			},
		}
		data, _ := json.Marshal(resp)
		conn.Write(data)
	}()

	client := NewClient("127.0.0.1", addr.Port, "test-token")
	resp, err := client.ListOperations() // No prefix - list all
	if err != nil {
		t.Fatalf("ListOperations failed: %v", err)
	}

	if len(resp.Operations) != 1 {
		t.Errorf("Expected 1 operation, got %d", len(resp.Operations))
	}
	if resp.Operations[0].ID != "test_op" {
		t.Errorf("Expected operation ID 'test_op', got '%s'", resp.Operations[0].ID)
	}
}

func TestClient_ListOperationsError(t *testing.T) {
	// Start a mock server that returns an error
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		conn.Read(buf)

		resp := ListOperationsResponse{
			Error: "Authentication failed",
		}
		data, _ := json.Marshal(resp)
		conn.Write(data)
	}()

	client := NewClient("127.0.0.1", addr.Port, "test-token")
	_, err = client.ListOperations()
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if err.Error() != "daemon error: Authentication failed" {
		t.Errorf("Expected 'daemon error: Authentication failed', got '%s'", err.Error())
	}
}

func TestClient_DescribeOperation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)

		var req DescribeOperationRequest
		json.Unmarshal(buf[:n], &req)

		resp := DescribeOperationResponse{
			Operation: &OperationInfo{
				ID:           req.DescribeOperation,
				Description:  "Test operation",
				AllowedFlags: []string{"--verbose"},
			},
		}
		data, _ := json.Marshal(resp)
		conn.Write(data)
	}()

	client := NewClient("127.0.0.1", addr.Port, "test-token")
	resp, err := client.DescribeOperation("test_op")
	if err != nil {
		t.Fatalf("DescribeOperation failed: %v", err)
	}

	if resp.Operation == nil {
		t.Fatal("Expected operation in response")
	}
	if resp.Operation.ID != "test_op" {
		t.Errorf("Expected operation ID 'test_op', got '%s'", resp.Operation.ID)
	}
}

func TestClient_RunOperation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		conn.Read(buf)

		resp := OperationResponse{
			ExitCode: 0,
			Stdout:   "hello world\n",
			Stderr:   "",
		}
		data, _ := json.Marshal(resp)
		conn.Write(data)
	}()

	client := NewClient("127.0.0.1", addr.Port, "test-token")
	resp, err := client.RunOperation("test_op", map[string]interface{}{"msg": "hello"}, nil, "")
	if err != nil {
		t.Fatalf("RunOperation failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", resp.ExitCode)
	}
	if resp.Stdout != "hello world\n" {
		t.Errorf("Expected stdout 'hello world\\n', got '%s'", resp.Stdout)
	}
}

func TestClient_ListOperationsWithPrefix(t *testing.T) {
	// Start a mock server that verifies prefix is sent
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)

	receivedPrefix := ""
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read request
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)

		var req ListOperationsRequest
		json.Unmarshal(buf[:n], &req)
		receivedPrefix = req.Prefix

		// Send response
		resp := ListOperationsResponse{
			Operations: []OperationInfo{
				{ID: "gh_pr_view", Description: "View PR"},
				{ID: "gh_pr_list", Description: "List PRs"},
			},
		}
		data, _ := json.Marshal(resp)
		conn.Write(data)
	}()

	client := NewClient("127.0.0.1", addr.Port, "test-token")
	resp, err := client.ListOperations("gh_pr")
	if err != nil {
		t.Fatalf("ListOperations failed: %v", err)
	}

	// Verify prefix was sent in request
	if receivedPrefix != "gh_pr" {
		t.Errorf("Expected prefix 'gh_pr' to be sent, got '%s'", receivedPrefix)
	}

	if len(resp.Operations) != 2 {
		t.Errorf("Expected 2 operations, got %d", len(resp.Operations))
	}
}

func TestClient_Timeout(t *testing.T) {
	// Start a server that doesn't respond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Don't respond, just wait
		time.Sleep(5 * time.Second)
	}()

	// Create client with shorter timeout for testing
	client := NewClient("127.0.0.1", addr.Port, "test-token")

	// This should timeout (but we can't easily test the 60s timeout, so we rely on the deadline)
	// The test just ensures the code path works
	start := time.Now()
	_, err = client.ListOperations("gh") // Test with prefix
	elapsed := time.Since(start)

	// Should fail due to connection being closed or timeout
	if err == nil {
		t.Error("Expected error due to no response, got nil")
	}

	// Should not take too long (the mock server will close after 5s)
	if elapsed > 10*time.Second {
		t.Errorf("Request took too long: %v", elapsed)
	}
}

func TestUnixClient_ConnectFailure(t *testing.T) {
	// Try to connect to a socket that doesn't exist
	client := NewUnixClient("/tmp/nonexistent-cmd2host-test.sock", "test-token")
	_, err := client.connect()
	if err == nil {
		t.Error("Expected connection error, got nil")
	}
}

func TestUnixClient_ListOperations(t *testing.T) {
	// Create temporary directory for socket
	tmpDir, err := os.MkdirTemp("", "cmd2host-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")

	// Start a mock Unix socket server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create Unix listener: %v", err)
	}
	defer listener.Close()

	// Handle connection in goroutine
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read request
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)

		var req ListOperationsRequest
		json.Unmarshal(buf[:n], &req)

		// Send response
		resp := ListOperationsResponse{
			Operations: []OperationInfo{
				{ID: "test_unix_op", Description: "Test Unix socket operation"},
			},
		}
		data, _ := json.Marshal(resp)
		conn.Write(data)
	}()

	client := NewUnixClient(socketPath, "test-token")
	resp, err := client.ListOperations()
	if err != nil {
		t.Fatalf("ListOperations via Unix socket failed: %v", err)
	}

	if len(resp.Operations) != 1 {
		t.Errorf("Expected 1 operation, got %d", len(resp.Operations))
	}
	if resp.Operations[0].ID != "test_unix_op" {
		t.Errorf("Expected operation ID 'test_unix_op', got '%s'", resp.Operations[0].ID)
	}
}

func TestUnixClient_RunOperation(t *testing.T) {
	// Create temporary directory for socket
	tmpDir, err := os.MkdirTemp("", "cmd2host-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "test.sock")

	// Start a mock Unix socket server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create Unix listener: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		conn.Read(buf)

		resp := OperationResponse{
			ExitCode: 0,
			Stdout:   "unix socket works\n",
			Stderr:   "",
		}
		data, _ := json.Marshal(resp)
		conn.Write(data)
	}()

	client := NewUnixClient(socketPath, "test-token")
	resp, err := client.RunOperation("test_op", map[string]interface{}{"msg": "hello"}, nil, "")
	if err != nil {
		t.Fatalf("RunOperation via Unix socket failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", resp.ExitCode)
	}
	if resp.Stdout != "unix socket works\n" {
		t.Errorf("Expected stdout 'unix socket works\\n', got '%s'", resp.Stdout)
	}
}

func TestOperationResponse_LegacySchemaDecode(t *testing.T) {
	// Older daemons emit a response without the truncation indicator fields.
	// The client MUST decode such payloads cleanly and leave the new fields
	// at their zero values so a newer client running against an older daemon
	// keeps working.
	legacy := `{"request_id":"abc","exit_code":0,"stdout":"hello","stderr":"","denied_reason":null}`
	var resp OperationResponse
	if err := json.Unmarshal([]byte(legacy), &resp); err != nil {
		t.Fatalf("decode legacy schema: %v", err)
	}
	if resp.RequestID != "abc" || resp.Stdout != "hello" {
		t.Errorf("legacy fields decoded incorrectly: %+v", resp)
	}
	if resp.StdoutTruncated || resp.StderrTruncated {
		t.Errorf("new flags should default to false when missing, got Stdout=%v Stderr=%v",
			resp.StdoutTruncated, resp.StderrTruncated)
	}
	if resp.StdoutOriginalBytes != 0 || resp.StderrOriginalBytes != 0 {
		t.Errorf("new byte fields should default to 0 when missing, got Stdout=%d Stderr=%d",
			resp.StdoutOriginalBytes, resp.StderrOriginalBytes)
	}
}

func TestOperationResponse_NewSchemaRoundTrip(t *testing.T) {
	orig := OperationResponse{
		RequestID:           "req1",
		ExitCode:            0,
		Stdout:              "hello\n... (truncated)",
		StdoutTruncated:     true,
		StdoutOriginalBytes: 1500000,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var decoded OperationResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.StdoutTruncated {
		t.Errorf("StdoutTruncated lost in round-trip: %v", decoded.StdoutTruncated)
	}
	if decoded.StdoutOriginalBytes != 1500000 {
		t.Errorf("StdoutOriginalBytes lost in round-trip: %d", decoded.StdoutOriginalBytes)
	}
}
