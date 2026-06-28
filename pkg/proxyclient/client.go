// Package proxyclient provides the container-side client library for the
// cmd2host transparent proxy binary (cmd/cmd2host-proxy). It wraps the
// raw-argv request protocol exposed by the cmd2host daemon
// (pkg/daemon/server.go's handleOperationRequest raw-argv branch) and
// exposes a single high-level Dispatch entry point so the binary stays a
// thin argv-to-protocol shim.
//
// The library mirrors pkg/mcpserver/client.go in spirit (TCP / Unix
// transport, JSON envelope, token authentication) but ships a smaller
// surface: only the raw-argv path the wrapper needs. MCP-style discovery
// (list_operations / describe_operation) belongs in pkg/mcpserver and is
// not duplicated here.
package proxyclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/taisukeoe/cmd2host/pkg/operations"
)

const (
	dialTimeout    = 10 * time.Second
	defaultTimeout = 60 * time.Second
	maxReadSize    = 10 * 1024 * 1024 // 10MB, matches pkg/mcpserver/client.go
)

// Client is the container-side raw-argv client for the cmd2host daemon.
//
// Connection target is one of:
//   - SocketPath (Unix socket); takes precedence when non-empty.
//   - Host + Port (TCP).
//
// Token is the session token read by the caller from -token-file or env;
// it is never accepted on argv and is not logged by the client.
type Client struct {
	Host       string
	Port       int
	SocketPath string
	Token      string

	// Timeout overrides defaultTimeout for the entire send-receive cycle.
	// Zero means use defaultTimeout.
	Timeout time.Duration
}

// SendRawArgv posts a raw-argv operation request and returns the daemon's
// typed response. command + argv form the [command, args...] pair the
// daemon reverse-matches against the project's allowed operation
// templates. targetRepo selects which repo to act on; "" defaults to the
// project's primary repo.
//
// Network / serialization / parsing errors are returned as Go errors; the
// daemon's own denial / execution reply is returned as a non-nil Response
// whose ExitCode and DeniedReason carry the outcome.
func (c *Client) SendRawArgv(command string, argv []string, targetRepo string) (*operations.Response, error) {
	req := operations.Request{
		Source:     "raw_argv",
		RawArgv:    append([]string{command}, argv...),
		Token:      c.Token,
		TargetRepo: targetRepo,
	}

	var resp operations.Response
	if err := c.sendAndReceive(req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) sendAndReceive(request interface{}, response interface{}) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	conn.SetDeadline(time.Now().Add(timeout))

	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	// Half-close the write side so the daemon's json.Decoder reaches EOF
	// promptly after the request bytes. The daemon caps reads at
	// maxReadSize (see pkg/daemon/server.go), but without this signal a
	// TCP conn would force the daemon to wait the full read deadline
	// before returning. Unix sockets benefit identically.
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	} else if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.CloseWrite()
	}

	respData, err := io.ReadAll(io.LimitReader(conn, maxReadSize))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if err := json.Unmarshal(respData, response); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

func (c *Client) connect() (net.Conn, error) {
	if c.SocketPath != "" {
		conn, err := net.DialTimeout("unix", c.SocketPath, dialTimeout)
		if err != nil {
			return nil, fmt.Errorf("connect to cmd2host daemon at %s: %w", c.SocketPath, err)
		}
		return conn, nil
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect to cmd2host daemon at %s: %w", addr, err)
	}
	return conn, nil
}
