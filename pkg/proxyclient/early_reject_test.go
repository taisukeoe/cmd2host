package proxyclient

import (
	"strings"
	"testing"
)

func stdinPiped() bool   { return true }
func stdinAbsent() bool  { return false }
func stdinNilCheck() bool { return false }

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
