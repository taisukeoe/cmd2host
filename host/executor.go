package main

// ExecuteResult represents the result of command execution
type ExecuteResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}
