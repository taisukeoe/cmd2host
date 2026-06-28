package proxyclient

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/taisukeoe/cmd2host/pkg/operations"
)

// fakeDaemon stands up an in-process TCP listener that emits a canned
// operations.Response for each raw-argv request. Tests use it instead of
// the real daemon so Dispatch's exit-code mapping can be exercised
// without project config / token store / git wiring.
type fakeDaemon struct {
	t        *testing.T
	listener net.Listener
	// reply is the canonical response sent back to every connection.
	reply operations.Response
	// captured records the most recent decoded raw-argv request so the
	// test can assert what was forwarded to the daemon.
	captured *operations.Request
}

func newFakeDaemon(t *testing.T, reply operations.Response) *fakeDaemon {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fd := &fakeDaemon{t: t, listener: l, reply: reply}
	go fd.serve()
	return fd
}

func (fd *fakeDaemon) serve() {
	for {
		conn, err := fd.listener.Accept()
		if err != nil {
			return
		}
		go fd.handle(conn)
	}
}

func (fd *fakeDaemon) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	data, err := io.ReadAll(io.LimitReader(conn, 1<<20))
	if err != nil {
		return
	}
	var req operations.Request
	if jerr := json.Unmarshal(data, &req); jerr == nil {
		fd.captured = &req
	}
	respBytes, _ := json.Marshal(fd.reply)
	_, _ = conn.Write(respBytes)
}

func (fd *fakeDaemon) addr() (host string, port int) {
	tcp := fd.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", tcp.Port
}

func (fd *fakeDaemon) close() {
	_ = fd.listener.Close()
}

func TestDispatch_PassthroughHappyPath(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{
		ExitCode: 0,
		Stdout:   "ok\n",
		Stderr:   "",
	})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "view", "42"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})

	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if stdout.String() != "ok\n" {
		t.Errorf("stdout = %q, want ok\\n", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr nonempty: %q", stderr.String())
	}
	if fd.captured == nil {
		t.Fatal("daemon received no request")
	}
	if fd.captured.Source != "raw_argv" {
		t.Errorf("captured Source = %q, want raw_argv", fd.captured.Source)
	}
	wantArgv := []string{"gh", "pr", "view", "42"}
	if !equalStringSlice(fd.captured.RawArgv, wantArgv) {
		t.Errorf("captured RawArgv = %v, want %v", fd.captured.RawArgv, wantArgv)
	}
}

func TestDispatch_PassthroughNonZeroExitCode(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{
		ExitCode: 7,
		Stdout:   "",
		Stderr:   "command failed\n",
	})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "git",
		Argv:         []string{"fetch", "origin"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})

	if exit != 7 {
		t.Errorf("exit = %d, want 7 (passthrough)", exit)
	}
	if stderr.String() != "command failed\n" {
		t.Errorf("stderr = %q, want command failed\\n", stderr.String())
	}
}

func TestDispatch_DaemonDenialMappedTo220(t *testing.T) {
	reason := "cmd2host: no allowed operation matches argv \"gh foo bar\""
	fd := newFakeDaemon(t, operations.Response{
		ExitCode:     1,
		DeniedReason: &reason,
	})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"foo", "bar"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})

	if exit != ExitDenied {
		t.Errorf("exit = %d, want %d (denied)", exit, ExitDenied)
	}
	if !strings.Contains(stderr.String(), "cmd2host:") {
		t.Errorf("stderr = %q, want cmd2host: prefix", stderr.String())
	}
	if !strings.Contains(stderr.String(), "mcp__cmd2host__cmd2host_list_operations") {
		t.Errorf("stderr = %q, want MCP discovery hint appended", stderr.String())
	}
}

func TestDispatch_DaemonUnreachableMappedTo200(t *testing.T) {
	// Bind a listener and immediately close so the port is free but
	// nothing accepts. The dial will fail.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcp := l.Addr().(*net.TCPAddr)
	_ = l.Close()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "view", "42"},
		Client:       &Client{Host: "127.0.0.1", Port: tcp.Port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})

	if exit != ExitInfrastructure {
		t.Errorf("exit = %d, want %d (infra failure)", exit, ExitInfrastructure)
	}
	if !strings.Contains(stderr.String(), "cmd2host:") {
		t.Errorf("stderr = %q, want cmd2host: prefix", stderr.String())
	}
}

func TestDispatch_EarlyRejectStdinMappedTo230(t *testing.T) {
	// Need the fake daemon present so an accidental network call would
	// not silently succeed. captured stays nil if early-reject fires
	// before SendRawArgv.
	fd := newFakeDaemon(t, operations.Response{ExitCode: 0})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "git",
		Argv:         []string{"commit", "-F", "-"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinPiped,
	})

	if exit != ExitEarlyReject {
		t.Errorf("exit = %d, want %d (early reject)", exit, ExitEarlyReject)
	}
	if fd.captured != nil {
		t.Errorf("daemon should not have been contacted, but captured: %+v", fd.captured)
	}
	if !strings.Contains(stderr.String(), "raw-argv mode does not forward stdin") {
		t.Errorf("stderr = %q, want stdin reject message", stderr.String())
	}
}

func TestDispatch_EarlyRejectFileURIMappedTo230(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{ExitCode: 0})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "aws",
		Argv:         []string{"s3api", "list-objects", "--cli-input-json", "file:///etc/passwd"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})

	if exit != ExitEarlyReject {
		t.Errorf("exit = %d, want %d (early reject)", exit, ExitEarlyReject)
	}
	if fd.captured != nil {
		t.Errorf("daemon should not have been contacted, but captured: %+v", fd.captured)
	}
}

func TestDispatch_TargetRepoForwarded(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{ExitCode: 0})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	_ = Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "view", "42"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		TargetRepo:   "owner/sub-repo",
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})

	if fd.captured == nil {
		t.Fatal("daemon received no request")
	}
	if fd.captured.TargetRepo != "owner/sub-repo" {
		t.Errorf("captured TargetRepo = %q, want owner/sub-repo", fd.captured.TargetRepo)
	}
}

func TestDispatch_MissingCommandReturnsInfra(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "",
		Argv:         []string{"foo"},
		Client:       &Client{Host: "127.0.0.1", Port: 1, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})
	if exit != ExitInfrastructure {
		t.Errorf("exit = %d, want %d", exit, ExitInfrastructure)
	}
}

func TestDispatch_MissingClientReturnsInfra(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "view", "42"},
		Client:       nil,
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})
	if exit != ExitInfrastructure {
		t.Errorf("exit = %d, want %d", exit, ExitInfrastructure)
	}
}

// TestDispatch_SurfacesStdoutTruncationOnStderr pins the indicator the
// proxy now writes to stderr when the daemon flags stdout truncation.
// The exit code is the host command's actual exit (passthrough). The
// stdout body is the prefix the daemon returned; the indicator goes to
// stderr so it does not pollute a pipe consumer of stdout.
func TestDispatch_SurfacesStdoutTruncationOnStderr(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{
		ExitCode:            0,
		Stdout:              "hello",
		StdoutTruncated:     true,
		StdoutOriginalBytes: 1500000,
	})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "view", "42"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})
	if exit != 0 {
		t.Errorf("exit = %d, want 0 (passthrough)", exit)
	}
	if stdout.String() != "hello" {
		t.Errorf("stdout = %q, want prefix only", stdout.String())
	}
	want := "cmd2host: stdout truncated by host daemon (shown 5 of 1500000 bytes)"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
}

// TestDispatch_SurfacesStderrTruncationOnStderr mirrors the stdout
// case for the stderr stream. Both indicators land on stderr so a
// stream-aware caller still distinguishes "host produced stderr" from
// "host produced more stderr than we surface".
func TestDispatch_SurfacesStderrTruncationOnStderr(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{
		ExitCode:            1,
		Stderr:              "errlog",
		StderrTruncated:     true,
		StderrOriginalBytes: 70000,
	})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "view", "42"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})
	if exit != 1 {
		t.Errorf("exit = %d, want 1 (passthrough)", exit)
	}
	want := "cmd2host: stderr truncated by host daemon (shown 6 of 70000 bytes)"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
	if !strings.Contains(stderr.String(), "errlog") {
		t.Errorf("stderr = %q, want it to still carry the host stderr body", stderr.String())
	}
}

// epipeWriter always returns syscall.EPIPE on Write, simulating a
// caller-side pipe that was closed by an upstream reader.
type epipeWriter struct{}

func (epipeWriter) Write(p []byte) (int, error) {
	return 0, syscall.EPIPE
}

// TestDispatch_StdoutBrokenPipeMapsToSIGPIPE pins the SIGPIPE-equivalent
// exit code: when the wrapper's own stdout is a broken pipe (the
// upstream reader closed early, e.g. `gh pr list --json ... | head -1`)
// the proxy must exit 141 so pipeline-aware tooling sees the same
// signal-driven outcome a native host CLI would produce. The proxy
// buffers the full daemon response, so a write failure at the wrapper
// boundary is the only signal we have to convey the pipe closure.
func TestDispatch_StdoutBrokenPipeMapsToSIGPIPE(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{
		ExitCode: 0,
		Stdout:   "hello world\n",
	})
	defer fd.close()
	host, port := fd.addr()

	var stderr bytes.Buffer
	exit := Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "list"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       epipeWriter{},
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})
	if exit != ExitSIGPIPE {
		t.Errorf("exit = %d, want %d (SIGPIPE equivalent)", exit, ExitSIGPIPE)
	}
}

// TestDispatch_NoTruncationNoIndicator pins the negative case: when
// the daemon does not flag truncation, the proxy must not synthesize
// an indicator out of thin air.
func TestDispatch_NoTruncationNoIndicator(t *testing.T) {
	fd := newFakeDaemon(t, operations.Response{
		ExitCode: 0,
		Stdout:   "ok\n",
	})
	defer fd.close()
	host, port := fd.addr()

	var stdout, stderr bytes.Buffer
	Dispatch(Options{
		Command:      "gh",
		Argv:         []string{"pr", "view", "42"},
		Client:       &Client{Host: host, Port: port, Token: "tok"},
		Stdout:       &stdout,
		Stderr:       &stderr,
		IsStdinPiped: stdinAbsent,
	})
	if strings.Contains(stderr.String(), "truncated") {
		t.Errorf("stderr = %q, must not contain a truncation indicator", stderr.String())
	}
}

func TestCommandFromArg0(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"gh", "gh"},
		{"/usr/local/bin/gh", "gh"},
		{"./bin/git", "git"},
		{"/usr/local/bin/cmd2host-proxy", "cmd2host-proxy"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := CommandFromArg0(tt.in); got != tt.want {
				t.Errorf("CommandFromArg0(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
