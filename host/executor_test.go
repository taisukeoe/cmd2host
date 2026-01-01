package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setupExecutorConfig(t *testing.T) *Config {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configContent := `{
		"commands": {
			"echo": {
				"path": "echo",
				"timeout": 5
			},
			"sleep": {
				"path": "sleep",
				"timeout": 1
			},
			"nonexistent": {
				"path": "/nonexistent/command",
				"timeout": 5
			},
			"false": {
				"path": "false",
				"timeout": 5
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

func TestExecute_Success(t *testing.T) {
	config := setupExecutorConfig(t)
	executor := NewExecutor(config)

	result := executor.Execute("echo", []string{"hello", "world"})

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello world") {
		t.Errorf("Stdout = %q, want to contain 'hello world'", result.Stdout)
	}
}

func TestExecute_CommandNotFound(t *testing.T) {
	config := setupExecutorConfig(t)
	executor := NewExecutor(config)

	result := executor.Execute("nonexistent", []string{})

	if result.ExitCode != 127 {
		t.Errorf("ExitCode = %d, want 127 (command not found)", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "Command not found") {
		t.Errorf("Stderr = %q, want to contain 'Command not found'", result.Stderr)
	}
}

func TestExecute_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("false command not available on Windows")
	}

	config := setupExecutorConfig(t)
	executor := NewExecutor(config)

	result := executor.Execute("false", []string{})

	if result.ExitCode == 0 {
		t.Error("ExitCode should be non-zero for 'false' command")
	}
}

func TestExecute_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep command not available on Windows")
	}

	config := setupExecutorConfig(t)
	executor := NewExecutor(config)

	// sleep command configured with 1 second timeout
	result := executor.Execute("sleep", []string{"10"})

	if result.ExitCode != 124 {
		t.Errorf("ExitCode = %d, want 124 (timeout)", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "timed out") {
		t.Errorf("Stderr = %q, want to contain 'timed out'", result.Stderr)
	}
}

func TestExecute_UnconfiguredCommand(t *testing.T) {
	config := setupExecutorConfig(t)
	executor := NewExecutor(config)

	result := executor.Execute("unconfigured", []string{})

	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "not configured") {
		t.Errorf("Stderr = %q, want to contain 'not configured'", result.Stderr)
	}
}
