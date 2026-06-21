package daemon

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/taisukeoe/cmd2host/pkg/auth"
	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/operations"
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
func setupServerWithProject(t *testing.T) (*Server, *auth.TokenStore, string) {
	t.Helper()

	tmpDir := t.TempDir()

	// Override HOME for project config loading
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in configdir.Dir.

	// Create project directory and config
	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	projectConfigContent := `{
		"repo": "owner/repo",
		"repo_path": "` + tmpDir + `",
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
	if err := config.AllowConfig(projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
	}

	// Create token store in temp directory
	tokenDir := filepath.Join(tmpDir, ".cmd2host", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	tokenStore := auth.NewTokenStoreAt(tokenDir)

	// Create a test token with repo assigned
	hash := auth.HashToken(testToken)
	tokenPath := filepath.Join(tokenDir, hash)
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	// Create daemon config
	daemonConfig := config.DefaultDaemonConfig()

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

		req := operations.ListOperationsRequest{
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

		var resp operations.ListOperationsResponse
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

		req := operations.ListOperationsRequest{
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

		var resp operations.ListOperationsResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Error != "Authentication failed" {
			t.Errorf("Expected 'Authentication failed', got: %s", resp.Error)
		}
	})
}

func TestServer_RepoMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	// Override HOME for project config loading
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
	t.Setenv("CMD2HOST_CONFIG_DIR", "") // Exercise the legacy HOME-based fallback in configdir.Dir.

	// Create project directory with MISMATCHED repo in config
	projectID := "owner_repo"
	projectDir := filepath.Join(tmpDir, ".cmd2host", "projects", projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	// Config has "evil/repo" but token will be for "owner/repo"
	// repo_path is required by the new 1:N schema validator (len match).
	projectConfigContent := `{
		"repo": "evil/repo",
		"repo_path": "` + tmpDir + `",
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

	if err := config.AllowConfig(projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
	}

	// Create token store with token bound to "owner/repo"
	tokenDir := filepath.Join(tmpDir, ".cmd2host", "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	tokenStore := auth.NewTokenStoreAt(tokenDir)

	hash := auth.HashToken(testToken)
	tokenPath := filepath.Join(tokenDir, hash)
	// Token is bound to "owner/repo" but config specifies "evil/repo"
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	daemonConfig := config.DefaultDaemonConfig()
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

	req := operations.ListOperationsRequest{
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

	var resp operations.ListOperationsResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Should fail with repo mismatch error
	if resp.Error == "" {
		t.Error("Expected repo mismatch error, but got success")
	}
	if resp.Error != "" && !strings.Contains(resp.Error, "token-project mismatch") {
		t.Errorf("Expected error containing 'token-project mismatch', got: %s", resp.Error)
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

		req := operations.DescribeOperationRequest{
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

		var resp operations.DescribeOperationResponse
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
		req := operations.DescribeOperationRequest{
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

		var resp operations.DescribeOperationResponse
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

		req := operations.DescribeOperationRequest{
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

		var resp operations.DescribeOperationResponse
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.Error != "Authentication failed" {
			t.Errorf("Expected 'Authentication failed', got: %s", resp.Error)
		}
	})
}

func TestTruncateOutput(t *testing.T) {
	// "あい" is 2 runes / 6 bytes ("あ" = E3 81 82, "い" = E3 81 84).
	// A byte-only cut at maxBytes=4 would slice "い" mid-sequence and leave
	// invalid UTF-8 in the stream. The implementation must pull the cut back
	// to the previous rune boundary (index 3) so the surfaced string is
	// "あ" + truncation suffix.
	cases := []struct {
		name          string
		input         string
		maxBytes      int
		wantOut       string
		wantOrigBytes int64
		wantTruncated bool
	}{
		{"empty", "", 100, "", 0, false},
		{"below cap", "hello", 100, "hello", 5, false},
		{"equal to cap", "hello", 5, "hello", 5, false},
		{"above cap", "helloworld", 5, "hello\n... (truncated)", 10, true},
		{"disabled when cap is zero", "hello", 0, "hello", 5, false},
		{"disabled when cap is negative", "hello", -1, "hello", 5, false},
		{"non-empty stream reports bytes even when cap disabled", "hello", 0, "hello", 5, false},
		{"utf8 cut pulled back to rune boundary", "あい", 4, "あ\n... (truncated)", 6, true},
		{"utf8 cut on rune boundary", "あい", 3, "あ\n... (truncated)", 6, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, origBytes, truncated := truncateOutput(tc.input, tc.maxBytes)
			if out != tc.wantOut {
				t.Errorf("out = %q, want %q", out, tc.wantOut)
			}
			if origBytes != tc.wantOrigBytes {
				t.Errorf("originalBytes = %d, want %d", origBytes, tc.wantOrigBytes)
			}
			if truncated != tc.wantTruncated {
				t.Errorf("wasTruncated = %v, want %v", truncated, tc.wantTruncated)
			}
			// Round-trip through encoding/json must preserve byte length:
			// if truncateOutput returned an invalid UTF-8 prefix, json
			// would substitute U+FFFD on marshal, drifting len(out).
			if truncated {
				data, err := json.Marshal(out)
				if err != nil {
					t.Fatalf("marshal truncated output: %v", err)
				}
				var back string
				if err := json.Unmarshal(data, &back); err != nil {
					t.Fatalf("unmarshal truncated output: %v", err)
				}
				if back != out {
					t.Errorf("json round-trip drifted: pre=%q post=%q", out, back)
				}
			}
		})
	}
}

// initRepoWithOrigin initialises tmpDir as a git work tree whose origin
// matches expectedRepo so VerifyPathRepoConsistency passes during operation
// execution tests.
func initRepoWithOrigin(t *testing.T, tmpDir, expectedRepo string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "git@github.com:" + expectedRepo + ".git"},
	} {
		cmd := exec.Command("git", append([]string{"-C", tmpDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
}

func TestServer_RunOperation_TruncatesStdoutWithTypedFlag(t *testing.T) {
	server, _, tmpDir := setupServerWithProject(t)
	initRepoWithOrigin(t, tmpDir, "owner/repo")
	// Force the stdout cap below the message length so test_op (echo {message})
	// trips the truncate path. Stderr cap stays at the default because echo
	// writes nothing to stderr.
	server.daemonConfig.MaxStdoutBytes = 5

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

	req := operations.Request{
		Operation: "test_op",
		Params:    map[string]operations.ParamValue{"message": "helloworld"},
		Token:     testToken,
	}
	reqData, _ := json.Marshal(req)
	writeAndCloseWrite(t, conn, reqData)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read: %v", err)
	}

	var resp operations.Response
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.DeniedReason != nil {
		t.Fatalf("operation denied: %s", *resp.DeniedReason)
	}
	if !resp.StdoutTruncated {
		t.Errorf("expected StdoutTruncated=true, got false; stdout=%q", resp.Stdout)
	}
	// echo "helloworld" produces 11 bytes (10 + newline).
	if resp.StdoutOriginalBytes != 11 {
		t.Errorf("expected StdoutOriginalBytes=11, got %d", resp.StdoutOriginalBytes)
	}
	if !strings.HasSuffix(resp.Stdout, "\n... (truncated)") {
		t.Errorf("expected legacy truncation suffix preserved in stdout, got %q", resp.Stdout)
	}
	if resp.StderrTruncated {
		t.Errorf("expected StderrTruncated=false, got true")
	}
}

func TestServer_RunOperation_NoTruncationLeavesFlagsZero(t *testing.T) {
	server, _, tmpDir := setupServerWithProject(t)
	initRepoWithOrigin(t, tmpDir, "owner/repo")

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

	req := operations.Request{
		Operation: "test_op",
		Params:    map[string]operations.ParamValue{"message": "hi"},
		Token:     testToken,
	}
	reqData, _ := json.Marshal(req)
	writeAndCloseWrite(t, conn, reqData)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read: %v", err)
	}

	var resp operations.Response
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.DeniedReason != nil {
		t.Fatalf("operation denied: %s", *resp.DeniedReason)
	}
	if resp.StdoutTruncated || resp.StderrTruncated {
		t.Errorf("expected truncation flags=false, got Stdout=%v Stderr=%v",
			resp.StdoutTruncated, resp.StderrTruncated)
	}
	// echo "hi" produces 3 bytes (2 + newline).
	if resp.StdoutOriginalBytes != 3 {
		t.Errorf("expected StdoutOriginalBytes=3, got %d", resp.StdoutOriginalBytes)
	}
}
