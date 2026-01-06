package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	defaultTimeout = 60 * time.Second
	maxReadSize    = 10 * 1024 * 1024 // 10MB max response
)

// Client communicates with the cmd2host daemon
type Client struct {
	host  string
	port  int
	token string
}

// NewClient creates a new cmd2host client
func NewClient(host string, port int, token string) *Client {
	return &Client{
		host:  host,
		port:  port,
		token: token,
	}
}

// connect establishes a TCP connection to the daemon
func (c *Client) connect() (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", c.host, c.port)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to cmd2host daemon at %s: %w", addr, err)
	}
	return conn, nil
}

// sendAndReceive sends a request and receives a response
func (c *Client) sendAndReceive(request interface{}, response interface{}) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Set deadline for the entire operation
	conn.SetDeadline(time.Now().Add(defaultTimeout))

	// Send request
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Read response
	respData, err := io.ReadAll(io.LimitReader(conn, maxReadSize))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := json.Unmarshal(respData, response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	return nil
}

// ListOperations returns the list of available operations for the token.
// Optional prefix parameter filters operations by ID prefix (e.g., "gh", "gh_pr").
func (c *Client) ListOperations(prefix ...string) (*ListOperationsResponse, error) {
	p := ""
	if len(prefix) > 0 {
		p = prefix[0]
	}
	req := ListOperationsRequest{
		ListOperations: true,
		Prefix:         p,
		Token:          c.token,
	}

	var resp ListOperationsResponse
	if err := c.sendAndReceive(req, &resp); err != nil {
		return nil, err
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("daemon error: %s", resp.Error)
	}

	return &resp, nil
}

// DescribeOperation returns details about a specific operation
func (c *Client) DescribeOperation(operationID string) (*DescribeOperationResponse, error) {
	req := DescribeOperationRequest{
		DescribeOperation: operationID,
		Token:             c.token,
	}

	var resp DescribeOperationResponse
	if err := c.sendAndReceive(req, &resp); err != nil {
		return nil, err
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("daemon error: %s", resp.Error)
	}

	return &resp, nil
}

// RunOperation executes an operation with the given parameters
func (c *Client) RunOperation(operationID string, params map[string]interface{}, flags []string) (*OperationResponse, error) {
	req := OperationRequest{
		Operation: operationID,
		Params:    params,
		Flags:     flags,
		Token:     c.token,
	}

	var resp OperationResponse
	if err := c.sendAndReceive(req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
