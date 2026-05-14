package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in cmd2hostConfigDir.

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

	// Allow the config
	if err := AllowConfig(projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
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

func TestServer_BodyFile(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
	t.Setenv("CMD2HOST_CONFIG_DIR", "")

	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	projectConfigContent := `{
		"repo": "owner/repo",
		"allowed_operations": ["body_op", "fail_body_op", "flag_body_op", "no_body_op"],
		"operations": {
			"body_op": {
				"command": "echo",
				"args_template": ["{body}"],
				"params": {"body": {"type": "string", "minLength": 1, "maxLength": 65535}},
				"description": "echo with body param (Pattern A)"
			},
			"fail_body_op": {
				"command": "false",
				"args_template": ["{body}"],
				"params": {"body": {"type": "string", "minLength": 1, "maxLength": 65535}},
				"description": "always exits 1, used to verify failure preservation"
			},
			"flag_body_op": {
				"command": "echo",
				"args_template": [],
				"allowed_flags": ["--body"],
				"description": "echo with --body flag (Pattern C)"
			},
			"no_body_op": {
				"command": "echo",
				"args_template": ["nobody"],
				"description": "no body support"
			}
		}
	}`

	configPath := filepath.Join(projectDir, "config.json")
	if err := os.WriteFile(configPath, []byte(projectConfigContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := AllowConfig(projectID); err != nil {
		t.Fatalf("allow: %v", err)
	}

	tokenDir := filepath.Join(tmpDir, ".cmd2host", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("mkdir tokens: %v", err)
	}
	tokenStore := &TokenStore{dir: tokenDir}
	hash := hashToken(testToken)
	if err := os.WriteFile(filepath.Join(tokenDir, hash), []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("token: %v", err)
	}

	// The daemon's runDaemon() creates BodyFileRoot at startup. Tests bypass
	// runDaemon and instantiate the server directly, so pre-create the root.
	bodyRoot := filepath.Join(tmpDir, ".cmd2host", "body")
	if err := os.MkdirAll(bodyRoot, 0700); err != nil {
		t.Fatalf("mkdir bodyroot: %v", err)
	}

	daemonConfig := defaultDaemonConfig()
	if daemonConfig.BodyFileRoot != bodyRoot {
		t.Fatalf("BodyFileRoot mismatch: want %s, got %s", bodyRoot, daemonConfig.BodyFileRoot)
	}
	server, err := NewServer(daemonConfig)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	server.tokenStore = tokenStore

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
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

	sendReq := func(t *testing.T, req OperationRequest) OperationResponse {
		t.Helper()
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		reqData, _ := json.Marshal(req)
		writeAndCloseWrite(t, conn, reqData)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var resp OperationResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}

	t.Run("param-mode consume-after-success", func(t *testing.T) {
		bodyPath := filepath.Join(bodyRoot, "ok-param.md")
		if err := os.WriteFile(bodyPath, []byte("hello via file"), 0600); err != nil {
			t.Fatalf("write body: %v", err)
		}
		resp := sendReq(t, OperationRequest{
			Operation: "body_op",
			BodyFile:  bodyPath,
			Token:     testToken,
		})
		if resp.ExitCode != 0 {
			t.Fatalf("expected exit 0, got %d (denied=%v stderr=%s)", resp.ExitCode, resp.DeniedReason, resp.Stderr)
		}
		if !strings.Contains(resp.Stdout, "hello via file") {
			t.Errorf("expected echo output to contain body, got %q", resp.Stdout)
		}
		if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
			t.Errorf("expected file deleted after success, stat err=%v", err)
		}
	})

	t.Run("flag-mode consume-after-success", func(t *testing.T) {
		bodyPath := filepath.Join(bodyRoot, "ok-flag.md")
		if err := os.WriteFile(bodyPath, []byte("flag-mode body"), 0600); err != nil {
			t.Fatalf("write body: %v", err)
		}
		resp := sendReq(t, OperationRequest{
			Operation: "flag_body_op",
			BodyFile:  bodyPath,
			Token:     testToken,
		})
		if resp.ExitCode != 0 {
			t.Fatalf("expected exit 0, got %d (denied=%v stderr=%s)", resp.ExitCode, resp.DeniedReason, resp.Stderr)
		}
		if !strings.Contains(resp.Stdout, "--body=flag-mode body") {
			t.Errorf("expected echo to print --body=... flag, got %q", resp.Stdout)
		}
		if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
			t.Errorf("expected file deleted after success, stat err=%v", err)
		}
	})

	t.Run("operation failure preserves file", func(t *testing.T) {
		bodyPath := filepath.Join(bodyRoot, "preserve.md")
		if err := os.WriteFile(bodyPath, []byte("retry me"), 0600); err != nil {
			t.Fatalf("write body: %v", err)
		}
		resp := sendReq(t, OperationRequest{
			Operation: "fail_body_op",
			BodyFile:  bodyPath,
			Token:     testToken,
		})
		if resp.ExitCode == 0 {
			t.Errorf("expected non-zero exit code, got 0")
		}
		if _, err := os.Stat(bodyPath); err != nil {
			t.Errorf("expected file preserved on failure, stat err=%v", err)
		}
	})

	t.Run("body + body_file conflict preserves file", func(t *testing.T) {
		bodyPath := filepath.Join(bodyRoot, "conflict.md")
		if err := os.WriteFile(bodyPath, []byte("file content"), 0600); err != nil {
			t.Fatalf("write body: %v", err)
		}
		resp := sendReq(t, OperationRequest{
			Operation: "body_op",
			Params:    map[string]ParamValue{"body": "inline"},
			BodyFile:  bodyPath,
			Token:     testToken,
		})
		if resp.DeniedReason == nil || !strings.Contains(*resp.DeniedReason, "cannot be combined") {
			t.Errorf("expected exclusivity denial, got denied=%v", resp.DeniedReason)
		}
		if _, err := os.Stat(bodyPath); err != nil {
			t.Errorf("expected file preserved on validation failure, stat err=%v", err)
		}
	})

	t.Run("path outside root denied with file preserved", func(t *testing.T) {
		outsideDir := t.TempDir()
		outsidePath := filepath.Join(outsideDir, "leak.md")
		if err := os.WriteFile(outsidePath, []byte("leak"), 0600); err != nil {
			t.Fatalf("write outside: %v", err)
		}
		resp := sendReq(t, OperationRequest{
			Operation: "body_op",
			BodyFile:  outsidePath,
			Token:     testToken,
		})
		if resp.DeniedReason == nil || !strings.Contains(*resp.DeniedReason, "outside the body root") {
			t.Errorf("expected outside-root denial, got denied=%v", resp.DeniedReason)
		}
		if _, err := os.Stat(outsidePath); err != nil {
			t.Errorf("expected file preserved on validation failure, stat err=%v", err)
		}
	})

	t.Run("operation does not accept body rejects body_file", func(t *testing.T) {
		bodyPath := filepath.Join(bodyRoot, "nobody.md")
		if err := os.WriteFile(bodyPath, []byte("ignored"), 0600); err != nil {
			t.Fatalf("write body: %v", err)
		}
		resp := sendReq(t, OperationRequest{
			Operation: "no_body_op",
			BodyFile:  bodyPath,
			Token:     testToken,
		})
		if resp.DeniedReason == nil || !strings.Contains(*resp.DeniedReason, "does not accept a body") {
			t.Errorf("expected unsupported denial, got denied=%v", resp.DeniedReason)
		}
		if _, err := os.Stat(bodyPath); err != nil {
			t.Errorf("expected file preserved on validation failure, stat err=%v", err)
		}
	})
}

func TestServer_RepoMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	// Override HOME for project config loading
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in cmd2hostConfigDir.

	// Create project directory with MISMATCHED repo in config
	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	// Config has "evil/repo" but token will be for "owner/repo"
	projectConfigContent := `{
		"repo": "evil/repo",
		"allowed_operations": ["test_op"],
		"operations": {
			"test_op": {
				"command": "echo",
				"args_template": ["test"],
				"description": "Test operation"
			}
		}
	}`

	configPath := filepath.Join(projectDir, "config.json")
	if err := os.WriteFile(configPath, []byte(projectConfigContent), 0644); err != nil {
		t.Fatalf("Failed to write project config: %v", err)
	}

	if err := AllowConfig(projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
	}

	// Create token store with token bound to "owner/repo"
	tokenDir := filepath.Join(tmpDir, ".cmd2host", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	tokenStore := &TokenStore{dir: tokenDir}

	hash := hashToken(testToken)
	tokenPath := filepath.Join(tokenDir, hash)
	// Token is bound to "owner/repo" but config specifies "evil/repo"
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	daemonConfig := defaultDaemonConfig()
	server, err := NewServer(daemonConfig)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.tokenStore = tokenStore

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

	// Should fail with repo mismatch error
	if resp.Error == "" {
		t.Error("Expected repo mismatch error, but got success")
	}
	if resp.Error != "" && !strings.Contains(resp.Error, "config repo mismatch") {
		t.Errorf("Expected error containing 'config repo mismatch', got: %s", resp.Error)
	}
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
