package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// Executor executes validated commands
type Executor struct {
	config *Config
}

// NewExecutor creates a new Executor
func NewExecutor(config *Config) *Executor {
	return &Executor{config: config}
}

// ExecuteResult represents the result of command execution
type ExecuteResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Execute runs a command and returns the result
func (e *Executor) Execute(cmdName string, args []string) ExecuteResult {
	cmdConfig, exists := e.config.Commands[cmdName]
	if !exists {
		return ExecuteResult{
			ExitCode: 1,
			Stderr:   "Command not configured",
		}
	}

	timeout := time.Duration(cmdConfig.Timeout) * time.Second
	cmdPath := cmdConfig.Path

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if err != nil {
		// Check if it was a timeout
		if ctx.Err() == context.DeadlineExceeded {
			return ExecuteResult{
				ExitCode: 124,
				Stderr:   "Command timed out",
			}
		}

		// Check for exit error
		if exitErr, ok := err.(*exec.ExitError); ok {
			return ExecuteResult{
				ExitCode: exitErr.ExitCode(),
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
			}
		}

		// Check for command not found (exec.Error or os.ErrNotExist)
		if _, ok := err.(*exec.Error); ok {
			return ExecuteResult{
				ExitCode: 127,
				Stderr:   "Command not found: " + cmdPath,
			}
		}
		if errors.Is(err, os.ErrNotExist) {
			return ExecuteResult{
				ExitCode: 127,
				Stderr:   "Command not found: " + cmdPath,
			}
		}

		return ExecuteResult{
			ExitCode: 1,
			Stderr:   err.Error(),
		}
	}

	return ExecuteResult{
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}
}
