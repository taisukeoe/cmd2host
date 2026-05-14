// body.go provides body_file handling for operation requests.
//
// # Design rationale
//
// container caller (e.g. Claude Code session inside a devcontainer) often
// needs to deliver long body strings (PR descriptions, review summaries,
// multi-line content with quotes/control characters) to gh operations.
// Passing such content inline via --body=<value> forces the caller to
// escape it across the JSON-RPC + shell boundary, which has proven fragile.
// body_file lets the caller write content to a file under a daemon-known
// directory and pass only the path; the daemon reads the content and
// injects it into the existing inline body delivery path (see below).
//
// # Bind mount path
//
// Files are read from BodyFileRoot, defaulting to
// `${CMD2HOST_CONFIG_DIR:-$HOME/.cmd2host}/body` (overridable via the
// daemon.json body_file_root field). The daemon creates this directory at
// startup with mode 0700. devcontainer setups bind-mount this path into
// the container so caller-written files are visible host-side.
//
// # Effective max bytes
//
// Content size is capped via a 3-tier hierarchy computed in EffectiveMaxBytes:
//
//  1. op.Params["body"].MaxLength when the template defines it (Pattern A/B
//     param-based operations, currently 65535 for github operations).
//  2. 65535 when --body is in op.AllowedFlags (Pattern C flag-based
//     operations) — aligns the flag path with GitHub's body limit.
//  3. bodyFileSanityMaxBytes (100 MiB) as the daemon-level fallback. This
//     is a sanity cap to prevent accidentally pointing body_file at a huge
//     log file, not a DoS defense — ARG_MAX (~1 MiB on macOS) is the
//     practical inline-delivery cap, and GitHub will reject anything beyond
//     ~64 KiB anyway.
//
// # Mode detection (template-driven)
//
// The operation's body delivery mode is determined from the template,
// not from caller intent. The {body} placeholder in args_template (either
// standalone or as an inline interpolation like `body={body}`) signals
// param mode; --body in allowed_flags signals flag mode. If both are
// present the placeholder wins, since it is the actual command contract.
//
// # Identity verification (validate-then-open contract)
//
// The body root is bind-mounted into the container, so the caller can
// write — and rewrite — files there. ValidateBodyFilePath captures the
// resolved file's device + inode (and rejects files with more than one
// link) at the moment it confirms containment, and ReadBodyFile reopens
// that same path with O_NOFOLLOW and fstat-compares the descriptor's
// device + inode + link count against the recorded identity. If the
// file the caller intends to deliver is replaced between validation and
// open, the comparison fails and the read is aborted. The caller's
// resolved path is never touched between these two steps, so a
// well-behaved caller observes no behavior change.
//
// # Consume-after-success
//
// The daemon removes the resolved body_file path only when the operation
// exits with code 0. On validation failure, operation failure (non-zero
// exit), or daemon-side error, the file is preserved so the caller can
// inspect and retry without rewriting. Callers should write a unique
// filename per request to avoid races where retry creates a new file at
// the same path the previous run is about to delete.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

// bodyFileSanityMaxBytes is the daemon-level fallback cap on body_file
// content size. It protects against accidentally referencing a huge file
// (e.g. /var/log/system.log) — it is not a DoS defense, since the
// downstream argv delivery is limited by ARG_MAX (~1 MiB on macOS) and
// GitHub itself caps body fields at ~64 KiB.
const bodyFileSanityMaxBytes = 100 * 1024 * 1024

// githubBodyMaxBytes is the GitHub-API-aligned cap applied when the
// operation accepts body via the --body flag (Pattern C) and the schema
// does not provide its own MaxLength. Matches the existing
// `maxLength: 65535` constraint used on param-based body schemas.
const githubBodyMaxBytes = 65535

// ReferencesBodyPlaceholder reports whether op.ArgsTemplate references
// the {body} placeholder, either as a standalone token (e.g. "{body}")
// or inline within a literal (e.g. "body={body}"). Detection uses the
// same inlinePlaceholderPattern that BuildArgs uses for substitution.
func (op *Operation) ReferencesBodyPlaceholder() bool {
	for _, tmpl := range op.ArgsTemplate {
		for _, match := range inlinePlaceholderPattern.FindAllStringSubmatch(tmpl, -1) {
			if len(match) > 1 && match[1] == "body" {
				return true
			}
		}
	}
	return false
}

// AcceptsBodyFlag reports whether op.AllowedFlags contains "--body".
func (op *Operation) AcceptsBodyFlag() bool {
	for _, f := range op.AllowedFlags {
		if f == "--body" {
			return true
		}
	}
	return false
}

// BodyFileIdentity captures the resolved path and the file identity
// (device + inode) recorded at the moment ValidateBodyFilePath confirmed
// the file was a regular, single-link file contained under the body
// root. ReadBodyFile compares the opened descriptor's identity against
// this snapshot, so a caller that rewrites the path between validation
// and open cannot redirect the read at a different file.
type BodyFileIdentity struct {
	Path string
	Dev  uint64
	Ino  uint64
}

// ValidateBodyFilePath verifies that path resolves to a regular,
// single-link file safely contained under root, and returns the file's
// identity for later open-time verification. Resolution follows
// symlinks so callers cannot bypass containment via symlink chicanery.
//
// Files with more than one hard link are rejected: link count > 1
// means the inode contents are reachable from some other path on the
// same filesystem, so containment under the body root no longer
// constrains what the read will return.
//
// Root resolution runs before path resolution so a misconfigured daemon
// surfaces "resolve body_file root …" rather than masking the issue
// behind a "body_file does not exist …" message that targets the
// caller-supplied path.
func ValidateBodyFilePath(path, root string) (BodyFileIdentity, error) {
	var empty BodyFileIdentity
	if root == "" {
		return empty, fmt.Errorf("body_file root is not configured")
	}
	if path == "" {
		return empty, fmt.Errorf("body_file path is empty")
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return empty, fmt.Errorf("resolve body_file root %q: %w", root, err)
	}

	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return empty, fmt.Errorf("body_file does not exist: %s", path)
		}
		return empty, fmt.Errorf("stat body_file %q: %w", path, err)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return empty, fmt.Errorf("resolve body_file path %q: %w", path, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return empty, fmt.Errorf("stat resolved body_file %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return empty, fmt.Errorf("body_file is not a regular file: %s", resolved)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return empty, fmt.Errorf("body_file stat: unexpected filesystem metadata type")
	}
	if uint64(stat.Nlink) > 1 {
		return empty, fmt.Errorf("body_file must have exactly one link: %s", resolved)
	}

	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil {
		return empty, fmt.Errorf("compute relative path under root: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return empty, fmt.Errorf("body_file is outside the body root: %s", resolved)
	}

	return BodyFileIdentity{
		Path: resolved,
		Dev:  uint64(stat.Dev),
		Ino:  uint64(stat.Ino),
	}, nil
}

// EffectiveMaxBytes returns the operation-specific cap on body_file
// content size, falling back to sanityCap when neither the schema nor
// the allowed flags constrain the body. See the file header for the
// 3-tier rationale.
func EffectiveMaxBytes(op *Operation, sanityCap int) int {
	if schema, ok := op.Params["body"]; ok && schema.MaxLength > 0 {
		return schema.MaxLength
	}
	if op.AcceptsBodyFlag() {
		return githubBodyMaxBytes
	}
	return sanityCap
}

// ReadBodyFile opens identity.Path with O_NOFOLLOW, confirms the
// opened descriptor refers to the same regular, single-link file that
// ValidateBodyFilePath inspected (device + inode + link count match),
// and reads at most effectiveMax bytes. effectiveMax must be > 0.
//
// The open uses O_NOFOLLOW so a symlink that replaced the terminal
// path component after validation fails the open outright. The fstat
// comparison covers the non-symlink replacement case (regular file
// swapped for a different regular file at the same path).
func ReadBodyFile(identity BodyFileIdentity, effectiveMax int) (string, error) {
	if effectiveMax <= 0 {
		return "", fmt.Errorf("body_file effective max must be > 0, got %d", effectiveMax)
	}

	f, err := os.OpenFile(identity.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open body_file: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat opened body_file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("opened body_file is not a regular file")
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("body_file fstat: unexpected filesystem metadata type")
	}
	if uint64(stat.Dev) != identity.Dev || uint64(stat.Ino) != identity.Ino {
		return "", fmt.Errorf("body_file identity changed between validation and open")
	}
	if uint64(stat.Nlink) > 1 {
		return "", fmt.Errorf("body_file must have exactly one link")
	}

	limited := io.LimitReader(f, int64(effectiveMax)+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read body_file: %w", err)
	}

	if len(content) > effectiveMax {
		return "", fmt.Errorf("body_file size exceeds effective max %d bytes", effectiveMax)
	}

	if bytes.IndexByte(content, 0) >= 0 {
		return "", fmt.Errorf("body_file contains null bytes")
	}

	if !utf8.Valid(content) {
		return "", fmt.Errorf("body_file is not valid UTF-8")
	}

	return string(content), nil
}

// bodyDeliveryMode identifies how the operation receives body content.
type bodyDeliveryMode int

const (
	bodyModeUnsupported bodyDeliveryMode = iota
	bodyModeParam
	bodyModeFlag
)

// determineBodyMode resolves the template-driven delivery mode (see
// file header). The {body} placeholder wins over the --body flag when
// both are present, because the placeholder is the actual command
// contract that BuildArgs substitutes.
func determineBodyMode(op *Operation) bodyDeliveryMode {
	if op.ReferencesBodyPlaceholder() {
		return bodyModeParam
	}
	if op.AcceptsBodyFlag() {
		return bodyModeFlag
	}
	return bodyModeUnsupported
}

// checkBodyExclusivity reports whether req already carries a body value
// that would conflict with body_file injection in the given mode. The
// check runs as a preflight so unsupported operations and conflicts
// fail before ReadBodyFile pulls content into memory.
func checkBodyExclusivity(req *OperationRequest, mode bodyDeliveryMode) error {
	switch mode {
	case bodyModeParam:
		if _, exists := req.Params["body"]; exists {
			return fmt.Errorf("body_file cannot be combined with body param")
		}
	case bodyModeFlag:
		for _, f := range req.Flags {
			if f == "--body" || strings.HasPrefix(f, "--body=") {
				return fmt.Errorf("body_file cannot be combined with --body flag")
			}
		}
	}
	return nil
}

// InjectBodyIntoRequest writes content into req's body slot based on
// the operation's template-defined delivery mode. Returns an error when
// the operation does not accept body, when an existing body / --body
// value already occupies the slot, or when content cannot be injected.
//
// Mode resolution (template-driven, see file header):
//   - op.ReferencesBodyPlaceholder() → param mode (req.Params["body"] = content)
//   - op.AcceptsBodyFlag()           → flag mode (append "--body=<content>" to req.Flags)
//   - neither                        → unsupported
//
// Exclusivity: param mode rejects any req.Params["body"] entry; flag
// mode rejects any req.Flags entry equal to "--body" or starting with
// "--body=".
func InjectBodyIntoRequest(req *OperationRequest, op *Operation, content string) error {
	mode := determineBodyMode(op)
	if mode == bodyModeUnsupported {
		return fmt.Errorf("operation %s does not accept a body parameter", req.Operation)
	}
	if err := checkBodyExclusivity(req, mode); err != nil {
		return err
	}
	switch mode {
	case bodyModeParam:
		if req.Params == nil {
			req.Params = map[string]ParamValue{}
		}
		req.Params["body"] = content
	case bodyModeFlag:
		req.Flags = append(req.Flags, "--body="+content)
	}
	return nil
}

// processBodyFile resolves req.BodyFile against bodyFileRoot, reads and
// validates the content, and injects it into req. Returns the resolved
// (absolute, symlink-resolved) path so the caller can remove it after a
// successful operation. When req.BodyFile is empty the function is a
// no-op and returns ("", nil).
//
// Order matters for I/O efficiency and DoS safety: mode detection and
// exclusivity are preflighted before any file read, so unsupported
// operations and body / body_file conflicts fail without consuming the
// effective max (up to 100 MiB sanity cap for non-body operations).
func processBodyFile(req *OperationRequest, project *ProjectConfig, bodyFileRoot string) (string, error) {
	if req.BodyFile == "" {
		return "", nil
	}

	op, exists := project.GetOperation(req.Operation)
	if !exists {
		return "", fmt.Errorf("unknown operation: %s", req.Operation)
	}
	if !project.HasOperation(req.Operation) {
		return "", fmt.Errorf("operation %s not allowed", req.Operation)
	}

	mode := determineBodyMode(op)
	if mode == bodyModeUnsupported {
		return "", fmt.Errorf("operation %s does not accept a body parameter", req.Operation)
	}
	if err := checkBodyExclusivity(req, mode); err != nil {
		return "", err
	}

	identity, err := ValidateBodyFilePath(req.BodyFile, bodyFileRoot)
	if err != nil {
		return "", err
	}

	effMax := EffectiveMaxBytes(op, bodyFileSanityMaxBytes)
	content, err := ReadBodyFile(identity, effMax)
	if err != nil {
		return "", err
	}

	if err := InjectBodyIntoRequest(req, op, content); err != nil {
		return "", err
	}

	return identity.Path, nil
}
