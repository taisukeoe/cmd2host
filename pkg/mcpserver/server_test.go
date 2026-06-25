package mcpserver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveToken(t *testing.T) {
	tokenFile := func(t *testing.T, contents string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "token")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		return path
	}

	tests := []struct {
		name      string
		opts      Options
		env       string // value to set CMD2HOST_TOKEN to ("" means unset)
		fileBody  string // when non-empty, opts.TokenFile is set to a file containing this body
		wantToken string
		wantErr   error
	}{
		{
			name:      "Token wins over TokenFile and env",
			opts:      Options{Token: "raw-token"},
			env:       "env-token",
			fileBody:  "file-token\n",
			wantToken: "raw-token",
		},
		{
			name:      "TokenFile wins over env when Token is empty",
			env:       "env-token",
			fileBody:  "file-token\n",
			wantToken: "file-token",
		},
		{
			name:      "TokenFile is trimmed",
			fileBody:  "  trimmed-token \n",
			wantToken: "trimmed-token",
		},
		{
			name:      "TokenFile contents whitespace-only falls through to env",
			env:       "env-token",
			fileBody:  "   \n\t\n",
			wantToken: "env-token",
		},
		{
			name:      "Env used when Token and TokenFile empty",
			env:       "env-token",
			wantToken: "env-token",
		},
		{
			name:    "Empty everywhere returns ErrTokenRequired",
			wantErr: ErrTokenRequired,
		},
		{
			name:    "TokenFile path that does not exist returns wrapped error",
			opts:    Options{TokenFile: filepath.Join(t.TempDir(), "missing")},
			wantErr: os.ErrNotExist,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(tokenEnvVar, tc.env)
			} else {
				t.Setenv(tokenEnvVar, "")
			}
			opts := tc.opts
			if tc.fileBody != "" {
				opts.TokenFile = tokenFile(t, tc.fileBody)
			}

			got, err := resolveToken(opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("resolveToken err = %v, want errors.Is %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveToken err = %v, want nil", err)
			}
			if got != tc.wantToken {
				t.Errorf("resolveToken token = %q, want %q", got, tc.wantToken)
			}
		})
	}
}

func TestNewClientFromOptions(t *testing.T) {
	tests := []struct {
		name           string
		opts           Options
		wantSocketPath string // when non-empty, expect Unix client at this path
		wantHost       string
		wantPort       int
	}{
		{
			name:           "Socket path selects Unix client",
			opts:           Options{SocketPath: "/tmp/cmd2host.sock", DaemonHost: "ignored", DaemonPort: 1234},
			wantSocketPath: "/tmp/cmd2host.sock",
		},
		{
			name:     "Zero-valued host and port fall back to defaults",
			opts:     Options{},
			wantHost: defaultDaemonHost,
			wantPort: defaultDaemonPort,
		},
		{
			name:     "Explicit host overrides default, default port stays",
			opts:     Options{DaemonHost: "127.0.0.1"},
			wantHost: "127.0.0.1",
			wantPort: defaultDaemonPort,
		},
		{
			name:     "Explicit port overrides default, default host stays",
			opts:     Options{DaemonPort: 12345},
			wantHost: defaultDaemonHost,
			wantPort: 12345,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(tc.opts, "tok")
			if client == nil {
				t.Fatal("newClient returned nil")
			}
			if client.token != "tok" {
				t.Errorf("client.token = %q, want %q", client.token, "tok")
			}
			if tc.wantSocketPath != "" {
				if client.socketPath != tc.wantSocketPath {
					t.Errorf("client.socketPath = %q, want %q", client.socketPath, tc.wantSocketPath)
				}
				if client.host != "" || client.port != 0 {
					t.Errorf("Unix client should leave host/port zero, got host=%q port=%d", client.host, client.port)
				}
				return
			}
			if client.socketPath != "" {
				t.Errorf("TCP client should leave socketPath empty, got %q", client.socketPath)
			}
			if client.host != tc.wantHost {
				t.Errorf("client.host = %q, want %q", client.host, tc.wantHost)
			}
			if client.port != tc.wantPort {
				t.Errorf("client.port = %d, want %d", client.port, tc.wantPort)
			}
		})
	}
}
