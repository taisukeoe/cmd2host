package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testToken must be 64 hex chars to pass format validation
const testToken = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

// writeAndCloseWrite writes data to conn and closes the write side to signal EOF to the server.
// This ensures clean connection handling and signals the server that no more data will be sent.
func writeAndCloseWrite(t *testing.T, conn net.Conn, data []byte) {
	t.Helper()
	_, err := conn.Write(data)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}
	// Close write side to signal EOF to server
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if err := tcpConn.CloseWrite(); err != nil {
			t.Fatalf("Failed to CloseWrite: %v", err)
		}
	} else {
		// Non-TCP connection (shouldn't happen in tests, but handle gracefully)
		t.Log("Warning: connection is not TCP, cannot half-close")
	}
}

// setupServerWithProject creates a test server with project-based configuration
func setupServerWithProject(t *testing.T) (*Server, *TokenStore, string) {
	t.Helper()

	tmpDir := t.TempDir()

	// Override HOME for project config loading
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// Create project directory and config
	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	projectConfigContent := `{
		"repo": "owner/repo",
		"allowed_operations": ["test_op"],
		"operations": {
			"test_op": {
				"command": "echo",
				"args_template": ["{message}"],
				"params": {
					"message": {"type": "string"}
				},
				"allowed_flags": ["--verbose"],
				"description": "Test operation"
			},
			"other_op": {
				"command": "echo",
				"args_template": ["other"],
				"description": "Other operation"
			}
		}
	}`

	configPath := filepath.Join(projectDir, "config.json")
	if err := os.WriteFile(configPath, []byte(projectConfigContent), 0644); err != nil {
		t.Fatalf("Failed to write project config: %v", err)
	}

	// Approve the config
	if err := ApproveConfig(projectID); err != nil {
		t.Fatalf("Failed to approve config: %v", err)
	}

	// Create token store in temp directory
	tokenDir := filepath.Join(tmpDir, ".cmd2host", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	tokenStore := &TokenStore{dir: tokenDir}

	// Create a test token with repo assigned
	hash := hashToken(testToken)
	tokenPath := filepath.Join(tokenDir, hash)
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	// Create daemon config
	daemonConfig := defaultDaemonConfig()

	server, err := NewServer(daemonConfig)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.tokenStore = tokenStore

	return server, tokenStore, tmpDir
}

func TestServer_ListOperations(t *testing.T) {
	server, _, _ := setupServerWithProject(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handleClient(conn)
		}
	}()

	t.Run("successful list operations", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		req := ListOperationsRequest{
			ListOperations: true,
			Token:          testToken,
		}
		reqData, _ := json.Marshal(req)
		writeAndCloseWrite(t, conn, reqData)

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp ListOperationsResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Error != "" {
			t.Errorf("Expected no error, got: %s", resp.Error)
		}

		// Should only return test_op (from allowed_operations)
		if len(resp.Operations) != 1 {
			t.Errorf("Expected 1 operation, got %d", len(resp.Operations))
		}
		if len(resp.Operations) > 0 && resp.Operations[0].ID != "test_op" {
			t.Errorf("Expected operation ID 'test_op', got '%s'", resp.Operations[0].ID)
		}
	})

	t.Run("auth failure", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		req := ListOperationsRequest{
			ListOperations: true,
			Token:          "invalid-token",
		}
		reqData, _ := json.Marshal(req)
		writeAndCloseWrite(t, conn, reqData)

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp ListOperationsResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Error != "Authentication failed" {
			t.Errorf("Expected 'Authentication failed', got: %s", resp.Error)
		}
	})
}

func TestServer_DescribeOperation(t *testing.T) {
	server, _, _ := setupServerWithProject(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handleClient(conn)
		}
	}()

	t.Run("successful describe operation", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		req := DescribeOperationRequest{
			DescribeOperation: "test_op",
			Token:             testToken,
		}
		reqData, _ := json.Marshal(req)
		writeAndCloseWrite(t, conn, reqData)

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp DescribeOperationResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Error != "" {
			t.Errorf("Expected no error, got: %s", resp.Error)
		}
		if resp.Operation == nil {
			t.Fatal("Expected operation in response")
		}
		if resp.Operation.ID != "test_op" {
			t.Errorf("Expected operation ID 'test_op', got '%s'", resp.Operation.ID)
		}
		if resp.Operation.Description != "Test operation" {
			t.Errorf("Expected description 'Test operation', got '%s'", resp.Operation.Description)
		}
	})

	t.Run("operation not allowed", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		// other_op exists but is not in allowed_operations
		req := DescribeOperationRequest{
			DescribeOperation: "other_op",
			Token:             testToken,
		}
		reqData, _ := json.Marshal(req)
		writeAndCloseWrite(t, conn, reqData)

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp DescribeOperationResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Error != "Operation not allowed: other_op" {
			t.Errorf("Expected 'Operation not allowed: other_op', got: %s", resp.Error)
		}
	})

	t.Run("auth failure", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		req := DescribeOperationRequest{
			DescribeOperation: "test_op",
			Token:             "invalid-token",
		}
		reqData, _ := json.Marshal(req)
		writeAndCloseWrite(t, conn, reqData)

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp DescribeOperationResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Error != "Authentication failed" {
			t.Errorf("Expected 'Authentication failed', got: %s", resp.Error)
		}
	})
}
