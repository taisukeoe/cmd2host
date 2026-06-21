package daemon

// ExecuteResult represents the result of command execution.
//
// Truncation indicator fields mirror operations.Response and flow from
// executeWithSanitization to handleOperationRequest unchanged.
type ExecuteResult struct {
	ExitCode            int    `json:"exit_code"`
	Stdout              string `json:"stdout"`
	Stderr              string `json:"stderr"`
	StdoutTruncated     bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated     bool   `json:"stderr_truncated,omitempty"`
	StdoutOriginalBytes int64  `json:"stdout_original_bytes,omitempty"`
	StderrOriginalBytes int64  `json:"stderr_original_bytes,omitempty"`
}
