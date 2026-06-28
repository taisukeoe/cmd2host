package proxyclient

import (
	"strings"
	"testing"
)

func stdinPiped() bool  { return true }
func stdinAbsent() bool { return false }

func TestCheckEarlyReject_AllowsCleanInvocation(t *testing.T) {
	tests := []struct {
		name    string
		command string
		argv    []string
	}{
		{name: "gh pr view", command: "gh", argv: []string{"pr", "view", "42"}},
		{name: "git push refspec", command: "git", argv: []string{"push", "main:refs/heads/main"}},
		{name: "aws s3 ls", command: "aws", argv: []string{"s3", "ls"}},
		{name: "empty argv", command: "git", argv: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r := CheckEarlyReject(tt.command, tt.argv, stdinAbsent); r != nil {
				t.Errorf("expected no early reject, got %q", r.Error())
			}
		})
	}
}

func TestCheckEarlyReject_StdinPipedTrips(t *testing.T) {
	r := CheckEarlyReject("aws", []string{"s3", "cp", "-", "s3://bucket/key"}, stdinPiped)
	if r == nil {
		t.Fatal("expected early reject when stdin is piped")
	}
	if r.Kind != "stdin" {
		t.Errorf("Kind = %q, want stdin", r.Kind)
	}
	if !strings.Contains(r.Error(), "raw-argv mode does not forward stdin") {
		t.Errorf("error %q does not name the stdin reason", r.Error())
	}
}

func TestCheckEarlyReject_FileURIInArgvTrips(t *testing.T) {
	tests := []struct {
		name    string
		command string
		argv    []string
		want    string // expected substring in error
	}{
		{
			name:    "aws --cli-input-json file://",
			command: "aws",
			argv:    []string{"s3api", "list-objects", "--cli-input-json", "file:///etc/passwd"},
			want:    "file://",
		},
		{
			name:    "embedded file:// in --template-body=...",
			command: "aws",
			argv:    []string{"cloudformation", "deploy", "--template-body=file:///tmp/cf.yaml"},
			want:    "file://",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := CheckEarlyReject(tt.command, tt.argv, stdinAbsent)
			if r == nil {
				t.Fatalf("expected file:// reject, got nil")
			}
			if r.Kind != "file_uri" {
				t.Errorf("Kind = %q, want file_uri", r.Kind)
			}
			if !strings.Contains(r.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", r.Error(), tt.want)
			}
		})
	}
}

// TestCheckEarlyReject_FileURIInNaturalTextPassesThrough verifies the
// narrowed file:// detector lets through tokens whose `file://` is part
// of natural-language prose (PR body, issue title, commit message) and
// not a URL-shaped value. The earlier substring match used to fire on
// these and break common gh / aws invocations.
func TestCheckEarlyReject_FileURIInNaturalTextPassesThrough(t *testing.T) {
	tests := []struct {
		name    string
		command string
		argv    []string
	}{
		{
			name:    "gh pr comment body mentions file:// in prose",
			command: "gh",
			argv:    []string{"pr", "comment", "42", "--body", "See the file:// scheme docs for details"},
		},
		{
			name:    "gh pr create body mentions file:// in prose",
			command: "gh",
			argv:    []string{"pr", "create", "--body", "Refactored file:// URL handling on the host side"},
		},
		{
			name:    "gh pr edit title contains file:// mention",
			command: "gh",
			argv:    []string{"pr", "edit", "42", "--title", "fix file:// scheme handling"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r := CheckEarlyReject(tt.command, tt.argv, stdinAbsent); r != nil {
				t.Errorf("expected pass-through for prose containing file://, got reject: %q", r.Error())
			}
		})
	}
}

func TestCheckEarlyReject_TTYRequiredSubcommandTrips(t *testing.T) {
	tests := []struct {
		name    string
		command string
		argv    []string
		wantSub string
	}{
		{name: "aws configure", command: "aws", argv: []string{"configure"}, wantSub: "aws configure"},
		{name: "aws configure with extra args", command: "aws", argv: []string{"configure", "set", "region", "us-east-1"}, wantSub: "aws configure"},
		{name: "aws sso login", command: "aws", argv: []string{"sso", "login"}, wantSub: "aws sso login"},
		{name: "aws ecs execute-command", command: "aws", argv: []string{"ecs", "execute-command", "--cluster", "x"}, wantSub: "aws ecs execute-command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := CheckEarlyReject(tt.command, tt.argv, stdinAbsent)
			if r == nil {
				t.Fatalf("expected TTY-required reject, got nil")
			}
			if r.Kind != "tty_required" {
				t.Errorf("Kind = %q, want tty_required", r.Kind)
			}
			if r.Detail != tt.wantSub {
				t.Errorf("Detail = %q, want %q", r.Detail, tt.wantSub)
			}
		})
	}
}

// TestCheckEarlyReject_TTYRequiredTolerantOfLeadingGlobalFlags pins
// the fix for flag-prefixed invocations. `aws --region us-east-1
// configure` and `aws --debug configure` were anchored on argv[0] in
// v1 and slipped past the TTY-required reject; the matcher now tries
// both interpretations of each leading flag (with-value / no-value)
// so common AWS global-flag patterns still trip the reject.
func TestCheckEarlyReject_TTYRequiredTolerantOfLeadingGlobalFlags(t *testing.T) {
	tests := []struct {
		name    string
		command string
		argv    []string
		wantSub string
	}{
		{
			name:    "aws --region us-east-1 configure",
			command: "aws",
			argv:    []string{"--region", "us-east-1", "configure"},
			wantSub: "aws configure",
		},
		{
			name:    "aws --profile foo sso login",
			command: "aws",
			argv:    []string{"--profile", "foo", "sso", "login"},
			wantSub: "aws sso login",
		},
		{
			name:    "aws --output=text configure",
			command: "aws",
			argv:    []string{"--output=text", "configure"},
			wantSub: "aws configure",
		},
		{
			name:    "aws --debug configure (bool global flag)",
			command: "aws",
			argv:    []string{"--debug", "configure"},
			wantSub: "aws configure",
		},
		{
			name:    "aws --debug --output text configure (chained flags)",
			command: "aws",
			argv:    []string{"--debug", "--output", "text", "configure"},
			wantSub: "aws configure",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := CheckEarlyReject(tt.command, tt.argv, stdinAbsent)
			if r == nil {
				t.Fatalf("expected TTY-required reject for %v, got nil", tt.argv)
			}
			if r.Kind != "tty_required" {
				t.Errorf("Kind = %q, want tty_required", r.Kind)
			}
			if r.Detail != tt.wantSub {
				t.Errorf("Detail = %q, want %q", r.Detail, tt.wantSub)
			}
		})
	}
}

// TestCheckEarlyReject_FilebURITrips pins the fileb:// detector. AWS
// CLI uses fileb://path to read raw bytes from a host filesystem path
// (e.g. `aws lambda invoke --payload fileb:///tmp/payload.bin`), so it
// needs the same container-vs-host filesystem reject as file://.
func TestCheckEarlyReject_FilebURITrips(t *testing.T) {
	tests := []struct {
		name    string
		command string
		argv    []string
	}{
		{
			name:    "aws lambda invoke --payload fileb://",
			command: "aws",
			argv:    []string{"lambda", "invoke", "--payload", "fileb:///tmp/payload.bin", "out.json"},
		},
		{
			name:    "aws kms encrypt --plaintext=fileb://",
			command: "aws",
			argv:    []string{"kms", "encrypt", "--plaintext=fileb:///etc/passwd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := CheckEarlyReject(tt.command, tt.argv, stdinAbsent)
			if r == nil {
				t.Fatalf("expected fileb:// reject, got nil")
			}
			if r.Kind != "file_uri" {
				t.Errorf("Kind = %q, want file_uri", r.Kind)
			}
			if !strings.Contains(r.Error(), "fileb://") && !strings.Contains(r.Error(), "file://") {
				t.Errorf("error %q does not mention the rejected scheme", r.Error())
			}
		})
	}
}

func TestCheckEarlyReject_TTYRequiredOnNonAWSPassesThrough(t *testing.T) {
	// `gh configure` (a hypothetical subcommand) is not in the
	// TTY-required list, so the early-reject path must not block it.
	if r := CheckEarlyReject("gh", []string{"configure"}, stdinAbsent); r != nil {
		t.Errorf("expected non-AWS configure to pass, got %q", r.Error())
	}
}

func TestCheckEarlyReject_NilStdinDetectorTreatsAsAbsent(t *testing.T) {
	if r := CheckEarlyReject("gh", []string{"pr", "view", "42"}, nil); r != nil {
		t.Errorf("expected nil detector to pass, got %q", r.Error())
	}
}

func TestEarlyRejectReason_ErrorCarriesMCPHint(t *testing.T) {
	r := &EarlyRejectReason{Kind: "stdin", Detail: "git", Message: "raw-argv mode does not forward stdin to host"}
	got := r.Error()
	if !strings.HasPrefix(got, "cmd2host:") {
		t.Errorf("expected cmd2host: prefix, got %q", got)
	}
	if !strings.Contains(got, "mcp__cmd2host__cmd2host_list_operations") {
		t.Errorf("expected MCP discovery hint, got %q", got)
	}
}
