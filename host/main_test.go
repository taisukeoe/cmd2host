package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestHashTokenCLI(t *testing.T) {
	// Build the binary first
	buildCmd := exec.Command("go", "build", "-o", "cmd2host-test", ".")
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}
	defer exec.Command("rm", "cmd2host-test").Run()

	tests := []struct {
		name        string
		input       string
		wantErr     bool
		wantOutput  string
		checkLength bool
	}{
		{
			name:        "valid token",
			input:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantErr:     false,
			checkLength: true, // Hash output should be 64 hex chars + newline
		},
		{
			name:        "token with trailing newline is trimmed",
			input:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
			wantErr:     false,
			checkLength: true,
		},
		{
			name:       "empty token",
			input:      "",
			wantErr:    true,
			wantOutput: "Error: empty token",
		},
		{
			name:       "whitespace only",
			input:      "   \n\t",
			wantErr:    true,
			wantOutput: "Error: empty token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("./cmd2host-test", "--hash-token")
			cmd.Stdin = strings.NewReader(tt.input)

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				if tt.wantOutput != "" && !strings.Contains(stderr.String(), tt.wantOutput) {
					t.Errorf("Stderr = %q, want to contain %q", stderr.String(), tt.wantOutput)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v, stderr: %s", err, stderr.String())
				}
				if tt.checkLength {
					output := strings.TrimSpace(stdout.String())
					if len(output) != 64 {
						t.Errorf("Hash length = %d, want 64", len(output))
					}
					// Verify it's valid hex
					for _, c := range output {
						if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
							t.Errorf("Hash contains non-hex char: %c", c)
							break
						}
					}
				}
			}
		})
	}
}

func TestHashTokenDeterministic(t *testing.T) {
	// Build the binary first
	buildCmd := exec.Command("go", "build", "-o", "cmd2host-test", ".")
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}
	defer exec.Command("rm", "cmd2host-test").Run()

	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	// Run twice and verify same output
	var outputs []string
	for i := 0; i < 2; i++ {
		cmd := exec.Command("./cmd2host-test", "--hash-token")
		cmd.Stdin = strings.NewReader(token)

		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("Run %d failed: %v", i+1, err)
		}
		outputs = append(outputs, strings.TrimSpace(string(output)))
	}

	if outputs[0] != outputs[1] {
		t.Errorf("Hash is not deterministic: %s vs %s", outputs[0], outputs[1])
	}

	// Verify it matches the Go function
	expectedHash := hashToken(token)
	if outputs[0] != expectedHash {
		t.Errorf("CLI hash = %s, Go hash = %s", outputs[0], expectedHash)
	}
}
