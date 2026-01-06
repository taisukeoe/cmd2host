package main

import (
	"encoding/json"
	"net"
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
	resp, err := client.RunOperation("test_op", map[string]interface{}{"msg": "hello"}, nil)
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
