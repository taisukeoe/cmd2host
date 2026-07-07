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

// setupServerWithProject builds a Server rooted at fresh temp dirs without
// touching process-global env state. Returned dir is the repo path used by
// the seeded project config (callers pass it to initRepoWithOrigin when a
// real git tree is needed). The Server's baseDir is a separate temp dir so
// projects/ and tokens/ live alongside each other under one cmd2host root.
func setupServerWithProject(t *testing.T) (*Server, *auth.TokenStore, string) {
	t.Helper()

	baseDir := t.TempDir()
	repoPath := t.TempDir()

	projectID := "owner_repo"
	projectDir := filepath.Join(config.ProjectsDirAt(baseDir), projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	projectConfigContent := `{
		"repo": "owner/repo",
		"repo_path": "` + repoPath + `",
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

	if err := config.AllowConfigAt(baseDir, projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
	}

	tokenDir := filepath.Join(baseDir, "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	hash := auth.HashToken(testToken)
	if err := os.WriteFile(filepath.Join(tokenDir, hash), []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	daemonConfig := config.DefaultDaemonConfig()
	server, err := NewServerAt(baseDir, daemonConfig)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	return server, server.tokenStore, repoPath
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
			go server.handleClient(conn, func() {})
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
	baseDir := t.TempDir()
	repoPath := t.TempDir()

	// Create project directory with MISMATCHED repo in config
	projectID := "owner_repo"
	projectDir := filepath.Join(config.ProjectsDirAt(baseDir), projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	// Config has "evil/repo" but token will be for "owner/repo"
	// repo_path is required by the new 1:N schema validator (len match).
	projectConfigContent := `{
		"repo": "evil/repo",
		"repo_path": "` + repoPath + `",
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

	if err := config.AllowConfigAt(baseDir, projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
	}

	tokenDir := filepath.Join(baseDir, "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	hash := auth.HashToken(testToken)
	// Token is bound to "owner/repo" but config specifies "evil/repo"
	if err := os.WriteFile(filepath.Join(tokenDir, hash), []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	daemonConfig := config.DefaultDaemonConfig()
	server, err := NewServerAt(baseDir, daemonConfig)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

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
			go server.handleClient(conn, func() {})
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
			go server.handleClient(conn, func() {})
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
		{"above cap", "helloworld", 5, "hello", 10, true},
		{"disabled when cap is zero", "hello", 0, "hello", 5, false},
		{"disabled when cap is negative", "hello", -1, "hello", 5, false},
		{"non-empty stream reports bytes even when cap disabled", "hello", 0, "hello", 5, false},
		{"utf8 cut pulled back to rune boundary", "あい", 4, "あ", 6, true},
		{"utf8 cut on rune boundary", "あい", 3, "あ", 6, true},
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
			go server.handleClient(conn, func() {})
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
	// The truncation signal is the typed flag; the stream body must be a
	// clean prefix of the original output with no synthetic suffix mixed in.
	if strings.Contains(resp.Stdout, "... (truncated)") {
		t.Errorf("stream body must not contain a synthetic truncation marker, got %q", resp.Stdout)
	}
	if resp.Stdout != "hello" {
		t.Errorf("expected stdout prefix %q, got %q", "hello", resp.Stdout)
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
			go server.handleClient(conn, func() {})
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

// TestServer_RunOperation_RawArgvDispatch exercises the raw-argv mode end
// to end: a client sends RawArgv=["echo","value"], the server reverse-matches
// it to test_op (template ["{message}"]), and the resolved request flows
// through the same validate/sanitize/execute path as the explicit operation
// entry.
// setupServerWithMultiValueOp mirrors setupServerWithProject but seeds a
// single echo-based operation that declares a multi-value flag, so tests can
// observe the host argv shape (echo prints its arguments verbatim) after the
// shared-path flag normalization runs.
func setupServerWithMultiValueOp(t *testing.T) (*Server, string) {
	t.Helper()

	baseDir := t.TempDir()
	repoPath := t.TempDir()

	projectID := "owner_repo"
	projectDir := filepath.Join(config.ProjectsDirAt(baseDir), projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}

	projectConfigContent := `{
		"repo": "owner/repo",
		"repo_path": "` + repoPath + `",
		"allowed_operations": ["mv_echo"],
		"operations": {
			"mv_echo": {
				"command": "echo",
				"args_template": [],
				"params": {},
				"allowed_flags": ["--rule-arns", "--region"],
				"multi_value_flags": ["--rule-arns"],
				"description": "multi-value echo"
			}
		}
	}`

	configPath := filepath.Join(projectDir, "config.json")
	if err := os.WriteFile(configPath, []byte(projectConfigContent), 0644); err != nil {
		t.Fatalf("Failed to write project config: %v", err)
	}
	if err := config.AllowConfigAt(baseDir, projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
	}

	tokenDir := filepath.Join(baseDir, "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	hash := auth.HashToken(testToken)
	if err := os.WriteFile(filepath.Join(tokenDir, hash), []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	server, err := NewServerAt(baseDir, config.DefaultDaemonConfig())
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	return server, repoPath
}

// TestServer_RunOperation_MCPFlagNormalization verifies the shared-path flag
// canonicalization: on the MCP (explicit-operation) route, which does not go
// through reverse-match, a multi-value flag's `=` form is split to the bare
// list form and a single-value flag's separate value is joined — both reach
// the host (echo) in the canonical shape.
func TestServer_RunOperation_MCPFlagNormalization(t *testing.T) {
	server, tmpDir := setupServerWithMultiValueOp(t)
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
			go server.handleClient(conn, func() {})
		}
	}()

	tests := []struct {
		name       string
		flags      []string
		want       string
		wantDenied bool
	}{
		{
			name:  "multi-value =-form split to bare list",
			flags: []string{"--rule-arns=arn1", "arn2", "arn3"},
			want:  "--rule-arns arn1 arn2 arn3",
		},
		{
			name:  "multi-value already-bare list passes through",
			flags: []string{"--rule-arns", "arn1", "arn2"},
			want:  "--rule-arns arn1 arn2",
		},
		{
			name:  "single-value separate form joined",
			flags: []string{"--region", "us-east-1"},
			want:  "--region=us-east-1",
		},
		{
			// Boundary preserved end-to-end: a surplus bare token after a
			// NON-multi-value flag is not absorbed by the shared
			// normalization and never reaches the host — the request is
			// denied.
			name:       "surplus bare token after single-value flag denied",
			flags:      []string{"--region", "us-east-1", "extra"},
			wantDenied: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatalf("Failed to connect: %v", err)
			}
			defer conn.Close()

			req := operations.Request{
				Operation: "mv_echo",
				Flags:     tt.flags,
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

			if tt.wantDenied {
				if resp.DeniedReason == nil {
					t.Errorf("expected denial, got stdout=%q exit_code=%d", resp.Stdout, resp.ExitCode)
				}
				return
			}

			if resp.DeniedReason != nil {
				t.Fatalf("operation denied: %s", *resp.DeniedReason)
			}
			if resp.ExitCode != 0 {
				t.Errorf("expected exit_code=0, got %d (stderr=%q)", resp.ExitCode, resp.Stderr)
			}
			if strings.TrimSpace(resp.Stdout) != tt.want {
				t.Errorf("host argv = %q, want %q", strings.TrimSpace(resp.Stdout), tt.want)
			}
		})
	}
}

// TestServer_RunOperation_RawArgvMultiValue verifies the raw-argv route
// end-to-end: a natural `--rule-arns arn1 arn2 arn3` argv reverse-matches the
// multi-value operation and reaches the host (echo) as the original
// separate-token list, and a `=`-first form is canonicalized to the same
// shape.
func TestServer_RunOperation_RawArgvMultiValue(t *testing.T) {
	server, tmpDir := setupServerWithMultiValueOp(t)
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
			go server.handleClient(conn, func() {})
		}
	}()

	tests := []struct {
		name    string
		rawArgv []string
		want    string
	}{
		{
			name:    "bare list reaches host as separate tokens",
			rawArgv: []string{"echo", "--rule-arns", "arn1", "arn2", "arn3"},
			want:    "--rule-arns arn1 arn2 arn3",
		},
		{
			name:    "=-first form canonicalized to bare list",
			rawArgv: []string{"echo", "--rule-arns=arn1", "arn2"},
			want:    "--rule-arns arn1 arn2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatalf("Failed to connect: %v", err)
			}
			defer conn.Close()

			req := operations.Request{
				RawArgv: tt.rawArgv,
				Token:   testToken,
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
				t.Fatalf("raw-argv operation denied: %s", *resp.DeniedReason)
			}
			if resp.ExitCode != 0 {
				t.Errorf("expected exit_code=0, got %d (stderr=%q)", resp.ExitCode, resp.Stderr)
			}
			if strings.TrimSpace(resp.Stdout) != tt.want {
				t.Errorf("host argv = %q, want %q", strings.TrimSpace(resp.Stdout), tt.want)
			}
		})
	}
}

func TestServer_RunOperation_RawArgvDispatch(t *testing.T) {
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
			go server.handleClient(conn, func() {})
		}
	}()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	req := operations.Request{
		RawArgv: []string{"echo", "hello-raw"},
		Token:   testToken,
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
		t.Fatalf("raw-argv operation denied: %s", *resp.DeniedReason)
	}
	if resp.ExitCode != 0 {
		t.Errorf("expected exit_code=0, got %d (stderr=%q)", resp.ExitCode, resp.Stderr)
	}
	if strings.TrimSpace(resp.Stdout) != "hello-raw" {
		t.Errorf("expected stdout=%q, got %q", "hello-raw", resp.Stdout)
	}
}

// TestServer_RunOperation_RawArgvUnknownIsDenied verifies the raw-argv path
// emits a denial (with a non-nil DeniedReason) when no allowed operation
// matches the argv, rather than passing through to execution or erroring
// silently.
func TestServer_RunOperation_RawArgvUnknownIsDenied(t *testing.T) {
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
			go server.handleClient(conn, func() {})
		}
	}()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	req := operations.Request{
		RawArgv: []string{"echo", "value-a", "extra-positional"},
		Token:   testToken,
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

	if resp.DeniedReason == nil {
		t.Fatalf("expected denial for argv with no matching template, got nil DeniedReason; resp=%+v", resp)
	}
	if !strings.Contains(*resp.DeniedReason, "cmd2host:") {
		t.Errorf("expected denial reason to carry cmd2host prefix, got %q", *resp.DeniedReason)
	}
	if resp.ExitCode == 0 {
		t.Errorf("expected non-zero exit_code on denial, got 0")
	}
}

// TestServer_RunOperation_RawArgvEmptyOrNullIsDenied covers the two raw_argv
// field shapes that look "absent" to Go's json.Unmarshal but carry an
// explicit raw-argv intent in the JSON: `{"raw_argv":[]}` (non-nil empty
// slice) and `{"raw_argv":null}` (collapses to nil slice). Both must
// produce an explicit raw-argv denial rather than falling through to the
// explicit operation entry path with an empty Operation.
func TestServer_RunOperation_RawArgvEmptyOrNullIsDenied(t *testing.T) {
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
			go server.handleClient(conn, func() {})
		}
	}()

	// Raw JSON literals here because operations.Request marshals these
	// shapes ambiguously: a nil slice with `omitempty` would drop the
	// field entirely, defeating the test. We send the wire bytes verbatim
	// so the daemon sees field presence as the request would carry on
	// the wire.
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "empty array",
			payload: `{"raw_argv":[],"token":"` + testToken + `"}`,
		},
		{
			name:    "explicit null",
			payload: `{"raw_argv":null,"token":"` + testToken + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatalf("Failed to connect: %v", err)
			}
			defer conn.Close()

			writeAndCloseWrite(t, conn, []byte(tt.payload))

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

			if resp.DeniedReason == nil {
				t.Fatalf("expected explicit raw-argv denial, got nil DeniedReason; resp=%+v", resp)
			}
			if !strings.Contains(*resp.DeniedReason, "raw_argv field is present but empty") {
				t.Errorf("expected denial to name the empty-raw_argv reason, got %q", *resp.DeniedReason)
			}
			if resp.ExitCode == 0 {
				t.Errorf("expected non-zero exit_code on denial, got 0")
			}
		})
	}
}

// seedAllowedProjectAt writes a minimal allowed project config under baseDir
// and stores a token file bound to its primary repo. tokenStr is the caller-
// supplied raw token to bind (the helper does not generate one).
func seedAllowedProjectAt(t *testing.T, baseDir, repo, tokenStr string) {
	t.Helper()
	projectID := config.NormalizeProjectID(repo)
	projectDir := filepath.Join(config.ProjectsDirAt(baseDir), projectID)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project dir: %v", err)
	}
	cfg := `{
		"repo": "` + repo + `",
		"repo_path": "` + projectDir + `",
		"allowed_operations": [],
		"operations": {}
	}`
	if err := os.WriteFile(filepath.Join(projectDir, "config.json"), []byte(cfg), 0644); err != nil {
		t.Fatalf("Failed to write project config: %v", err)
	}
	if err := config.AllowConfigAt(baseDir, projectID); err != nil {
		t.Fatalf("Failed to allow config: %v", err)
	}
	tokenDir := filepath.Join(baseDir, "tokens")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		t.Fatalf("Failed to create token dir: %v", err)
	}
	hash := auth.HashToken(tokenStr)
	body := []byte(`{"repo":"` + repo + `"}`)
	if err := os.WriteFile(filepath.Join(tokenDir, hash), body, 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}
}

// TestNewServerAt_ConcurrentInstances verifies that two Servers constructed
// via NewServerAt with distinct base dirs hold independent token stores and
// resolve project configs from their own dirs, without any env mutation.
// This is the load-bearing case for callers that need to run multiple
// cmd2host instances in the same process.
func TestNewServerAt_ConcurrentInstances(t *testing.T) {
	baseA := t.TempDir()
	baseB := t.TempDir()
	tokenA := strings.Repeat("a", 64)
	tokenB := strings.Repeat("b", 64)

	seedAllowedProjectAt(t, baseA, "owner/alpha", tokenA)
	seedAllowedProjectAt(t, baseB, "owner/beta", tokenB)

	daemonConfig := config.DefaultDaemonConfig()
	serverA, err := NewServerAt(baseA, daemonConfig)
	if err != nil {
		t.Fatalf("NewServerAt(baseA) failed: %v", err)
	}
	serverB, err := NewServerAt(baseB, daemonConfig)
	if err != nil {
		t.Fatalf("NewServerAt(baseB) failed: %v", err)
	}

	// Each Server's token store sees only its own token.
	if _, ok := serverA.tokenStore.GetTokenData(tokenA); !ok {
		t.Error("serverA must accept tokenA")
	}
	if _, ok := serverA.tokenStore.GetTokenData(tokenB); ok {
		t.Error("serverA must NOT accept tokenB (cross-instance bleed)")
	}
	if _, ok := serverB.tokenStore.GetTokenData(tokenB); !ok {
		t.Error("serverB must accept tokenB")
	}
	if _, ok := serverB.tokenStore.GetTokenData(tokenA); ok {
		t.Error("serverB must NOT accept tokenA (cross-instance bleed)")
	}

	// Each Server resolves project configs from its own baseDir.
	cfgA, _, err := serverA.resolveProject(auth.TokenData{Repo: "owner/alpha"})
	if err != nil {
		t.Fatalf("serverA.resolveProject(owner/alpha) failed: %v", err)
	}
	if cfgA.PrimaryRepo() != "owner/alpha" {
		t.Errorf("serverA primary repo = %q, want owner/alpha", cfgA.PrimaryRepo())
	}
	if _, _, err := serverA.resolveProject(auth.TokenData{Repo: "owner/beta"}); err == nil {
		t.Error("serverA must NOT resolve owner/beta (lives only under baseB)")
	}
	cfgB, _, err := serverB.resolveProject(auth.TokenData{Repo: "owner/beta"})
	if err != nil {
		t.Fatalf("serverB.resolveProject(owner/beta) failed: %v", err)
	}
	if cfgB.PrimaryRepo() != "owner/beta" {
		t.Errorf("serverB primary repo = %q, want owner/beta", cfgB.PrimaryRepo())
	}

	if serverA.baseDir != baseA || serverB.baseDir != baseB {
		t.Errorf("baseDir not preserved: A=%q (want %q) B=%q (want %q)",
			serverA.baseDir, baseA, serverB.baseDir, baseB)
	}
}

// TestNewServerAt_RejectsEmptyDir verifies the empty-dir guard so callers
// never construct a Server whose projects/ and tokens/ resolve against the
// daemon CWD.
func TestNewServerAt_RejectsEmptyDir(t *testing.T) {
	if _, err := NewServerAt("", config.DefaultDaemonConfig()); err == nil {
		t.Fatal("NewServerAt(\"\") must return error")
	}
}

// TestServer_DispatchConn_ClosesAtCapacity pins the in-flight cap: when
// the semaphore is full, dispatchConn must close the connection
// immediately instead of spawning another handleClient goroutine that
// would stack another auth-failure delay.
func TestServer_DispatchConn_ClosesAtCapacity(t *testing.T) {
	server, _, _ := setupServerWithProject(t)
	// Replace the default cap with a single slot and pre-fill it so the
	// next dispatch is forced into the at-capacity branch.
	server.inFlightSem = make(chan struct{}, 1)
	server.inFlightSem <- struct{}{}

	a, b := net.Pipe()
	defer b.Close()
	server.dispatchConn(a)

	// dispatchConn closed `a`; the peer end `b` must see EOF without any
	// payload being written, confirming handleClient never ran.
	b.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	n, err := b.Read(buf)
	if err == nil {
		t.Fatalf("expected EOF on rejected connection, got n=%d err=nil", n)
	}
}

// TestServer_AuthFailure_ReleasesInFlightSlotBeforeThrottleSleep pins
// the cap-amplification fix: when a connection authenticates with a
// bogus token, the handler must release its in-flight slot before the
// 1-second throttle sleep so the cap does not stay reserved by a
// goroutine that has no remaining work. Otherwise a stream of failed
// auth attempts can exhaust max_in_flight for the entire duration of
// the synthetic delay and block legitimate clients via the
// dispatchConn drop branch.
func TestServer_AuthFailure_ReleasesInFlightSlotBeforeThrottleSleep(t *testing.T) {
	server, _, _ := setupServerWithProject(t)
	server.inFlightSem = make(chan struct{}, 1)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.dispatchConn(conn)
		}
	}()

	// Fire a request with a bogus token. The handler should log
	// AUTH FAILED, release the slot, then sleep ~1s before sending
	// the response. We do not wait for the response here.
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	req := operations.Request{
		Operation: "test_op",
		Token:     "this-is-not-a-valid-token-just-bytes-bytes-bytes-bytes-bytes-x",
	}
	reqData, _ := json.Marshal(req)
	writeAndCloseWrite(t, conn, reqData)

	// Within the 1-second throttle window, the slot must already be
	// free. Poll with a 500ms ceiling — well under the 1s sleep and
	// well above the time the handler needs to reach the release call.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		select {
		case server.inFlightSem <- struct{}{}:
			// Acquired — release immediately and return clean.
			<-server.inFlightSem
			return
		default:
			if time.Now().After(deadline) {
				t.Fatal("slot stayed reserved through the throttle window; release must happen before time.Sleep")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestServer_DispatchConn_NoCapPassesThrough verifies the disabled-cap
// path (MaxInFlight < 0 → inFlightSem == nil): dispatchConn must still
// TestServer_RejectsControlCharsInRequestID pins the daemon's early rejection
// of caller-supplied diagnostic fields whose payload could otherwise reach
// the audit log format strings intact. When the request_id carries a
// character outside the allowed set (newline, carriage return, quote,
// space, ...), the daemon must respond with a validation-shaped
// DeniedReason and must not proceed to auth. Anchoring this at the
// transport layer (rather than only at the pure operations.Validate unit
// test) confirms handleOperationRequest wires Validate() into the request
// path before token check.
func TestServer_RejectsControlCharsInRequestID(t *testing.T) {
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
			go server.handleClient(conn, func() {})
		}
	}()

	cases := []struct {
		name      string
		requestID string
	}{
		{name: "embedded newline (log-line spoof shape)", requestID: "INJECTED\n[OP:git_push] source=mcp"},
		{name: "carriage return", requestID: "abc\rdef"},
		{name: "tab", requestID: "abc\tdef"},
		{name: "NUL byte", requestID: "abc\x00def"},
		{name: "quote", requestID: "abc\"def"},
		{name: "space", requestID: "abc def"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatalf("Failed to connect: %v", err)
			}
			defer conn.Close()

			// Note: token is intentionally invalid. The validation must
			// fire before auth so callers cannot spoof audit lines via a
			// bare unauthenticated request.
			req := map[string]any{
				"request_id": tc.requestID,
				"operation":  "test_op",
				"token":      "invalid",
			}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}
			writeAndCloseWrite(t, conn, data)

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

			if resp.DeniedReason == nil {
				t.Fatalf("Expected DeniedReason for control-char request_id, got nil")
			}
			if !strings.Contains(*resp.DeniedReason, "request_id") {
				t.Errorf("Expected DeniedReason to mention request_id, got: %s", *resp.DeniedReason)
			}
			// The daemon must not report auth failure — validation must
			// short-circuit before the token check runs.
			if strings.Contains(*resp.DeniedReason, "Authentication failed") {
				t.Errorf("Validation should run before auth; got auth-failure DeniedReason: %s", *resp.DeniedReason)
			}
		})
	}
}

// TestServer_RejectsControlCharsInOperation mirrors the request_id
// rejection test for the caller-supplied operation field. An operation
// name carrying a newline (or any character outside the operation
// template naming shape) must be denied before it reaches the
// `[OP:%q]` / `Unknown operation: %q` audit log format strings — even
// a `%q` second layer would still emit the escaped payload, so the
// character-set check runs first.
func TestServer_RejectsControlCharsInOperation(t *testing.T) {
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
			go server.handleClient(conn, func() {})
		}
	}()

	cases := []struct {
		name      string
		operation string
	}{
		{name: "embedded newline (log-line spoof shape)", operation: "gh_pr_view\n[OP:git_push] source=\"mcp\" exit_code=0"},
		{name: "CRLF", operation: "gh_pr_view\r\n[OP:git_push]"},
		{name: "NUL byte", operation: "gh_pr_view\x00"},
		{name: "uppercase", operation: "GH_PR_VIEW"},
		{name: "hyphen", operation: "gh-pr-view"},
		{name: "leading digit", operation: "1gh"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatalf("Failed to connect: %v", err)
			}
			defer conn.Close()

			req := map[string]any{
				"operation": tc.operation,
				"token":     "invalid",
			}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}
			writeAndCloseWrite(t, conn, data)

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

			if resp.DeniedReason == nil {
				t.Fatalf("Expected DeniedReason for bad operation, got nil")
			}
			if !strings.Contains(*resp.DeniedReason, "operation") {
				t.Errorf("Expected DeniedReason to mention operation, got: %s", *resp.DeniedReason)
			}
			// Validation must short-circuit before the auth check.
			if strings.Contains(*resp.DeniedReason, "Authentication failed") {
				t.Errorf("Validation should run before auth; got auth-failure DeniedReason: %s", *resp.DeniedReason)
			}
		})
	}
}

// TestServer_ValidatorRejectsEmptyOperation locks the downstream contract
// that Request.Validate documents: an MCP entry with an empty Operation
// passes the pre-dispatch shape check (Validate) but is rejected
// downstream by Validator.ValidateOperation as "Unknown operation".
// Anchoring this at the transport layer confirms the two-layer split
// (shape validation early, presence enforcement at validator) survives
// future edits to either side.
func TestServer_ValidatorRejectsEmptyOperation(t *testing.T) {
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
			go server.handleClient(conn, func() {})
		}
	}()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Send raw JSON with an explicit empty operation field so handleClient's
	// discriminator (field presence, not value) routes to
	// handleOperationRequest. operations.Request has json omitempty on the
	// Operation tag; marshalling a zero-valued struct would drop the key and
	// hit the "Unknown request type" branch instead of the validator.
	req := map[string]any{
		"operation": "",
		"token":     testToken,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}
	writeAndCloseWrite(t, conn, data)

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

	if resp.DeniedReason == nil {
		t.Fatalf("Expected DeniedReason for empty operation, got nil")
	}
	if !strings.Contains(*resp.DeniedReason, "Unknown operation") {
		t.Errorf("Expected DeniedReason to mention Unknown operation, got: %s", *resp.DeniedReason)
	}
}

// TestServer_RejectsUnknownSource pins the enum-restriction on the
// caller-supplied source field. Any value outside {"", "mcp", "raw_argv"}
// must be denied before it reaches audit log format strings.
func TestServer_RejectsUnknownSource(t *testing.T) {
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
			go server.handleClient(conn, func() {})
		}
	}()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	req := map[string]any{
		"request_id": "req-1",
		"operation":  "test_op",
		"source":     "attacker",
		"token":      "invalid",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}
	writeAndCloseWrite(t, conn, data)

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

	if resp.DeniedReason == nil {
		t.Fatalf("Expected DeniedReason for unknown source enum, got nil")
	}
	if !strings.Contains(*resp.DeniedReason, "source") {
		t.Errorf("Expected DeniedReason to mention source, got: %s", *resp.DeniedReason)
	}
}

// dispatch the connection to handleClient with no semaphore handshake.
func TestServer_DispatchConn_NoCapPassesThrough(t *testing.T) {
	server, _, _ := setupServerWithProject(t)
	server.inFlightSem = nil

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// handleClient will block on the decoder for up to readTimeout (5s)
	// since we never write a request. We only need to confirm dispatchConn
	// returned without panic and without closing `a` before handleClient
	// ran. Write a single byte from the peer so a non-piped read on `a`
	// inside handleClient would unblock — even if handleClient discards
	// it as invalid JSON, the dispatch path itself is what we are
	// pinning here.
	done := make(chan struct{})
	go func() {
		server.dispatchConn(a)
		close(done)
	}()
	select {
	case <-done:
		// dispatchConn returns immediately after launching the handler.
	case <-time.After(time.Second):
		t.Fatal("dispatchConn did not return within 1s")
	}
}
