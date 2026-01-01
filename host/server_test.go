package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupServerConfig(t *testing.T, port int) *Config {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
		"listen_address": "127.0.0.1",
		"listen_port": ` + string(rune('0'+port%10)) + `,
		"allowed_repositories": ["owner/repo"],
		"commands": {
			"echo": {
				"path": "echo",
				"timeout": 5,
				"allowed": [".*"]
			}
		}
	}`

	// Use a dynamic port for testing
	configContent = `{
		"listen_address": "127.0.0.1",
		"listen_port": 0,
		"allowed_repositories": ["owner/repo"],
		"commands": {
			"echo": {
				"path": "echo",
				"timeout": 5,
				"allowed": [".*"]
			}
		}
	}`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	return config
}

func TestServer_HandleClient(t *testing.T) {
	config := setupServerConfig(t, 0)
	config.ListenPort = 0 // Use dynamic port

	server := NewServer(config)

	// Start listener on dynamic port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	// Accept connections in goroutine
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handleClient(conn)
		}
	}()

	// Test successful request
	t.Run("successful request", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		req := Request{
			Command: "echo",
			Args:    []string{"hello", "world"},
		}
		reqData, _ := json.Marshal(req)

		_, err = conn.Write(reqData)
		if err != nil {
			t.Fatalf("Failed to write: %v", err)
		}

		// Read response
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp ExecuteResult
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0", resp.ExitCode)
		}
		if resp.Stdout != "hello world\n" {
			t.Errorf("Stdout = %q, want %q", resp.Stdout, "hello world\n")
		}
	})

	// Test unconfigured command
	t.Run("unconfigured command", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		req := Request{
			Command: "unknown",
			Args:    []string{},
		}
		reqData, _ := json.Marshal(req)

		_, err = conn.Write(reqData)
		if err != nil {
			t.Fatalf("Failed to write: %v", err)
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp ExecuteResult
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1", resp.ExitCode)
		}
	})

	// Test invalid JSON
	t.Run("invalid JSON", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		_, err = conn.Write([]byte("invalid json"))
		if err != nil {
			t.Fatalf("Failed to write: %v", err)
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		var resp ExecuteResult
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}

		if resp.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1", resp.ExitCode)
		}
		if resp.Stderr == "" {
			t.Error("Stderr should contain error message")
		}
	})
}

func TestServer_DefaultCommand(t *testing.T) {
	config := setupServerConfig(t, 0)
	config.ListenPort = 0
	config.Commands["gh"] = CommandConfig{
		Path:            "echo",
		Timeout:         5,
		allowedPatterns: nil,
	}

	server := NewServer(config)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		server.handleClient(conn)
	}()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Request without command field (should default to "gh")
	_, err = conn.Write([]byte(`{"args": ["test"]}`))
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read: %v", err)
	}

	var resp ExecuteResult
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (should use default 'gh' command)", resp.ExitCode)
	}
}
