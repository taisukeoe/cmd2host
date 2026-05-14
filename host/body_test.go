package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// writeBodyFile writes content into name under the given root and returns
// the absolute path. fails the test on error.
func writeBodyFile(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	return path
}

// identityOf builds a BodyFileIdentity for path by reading the file's
// dev/ino via os.Stat. ReadBodyFile-only tests use this helper to skip
// the ValidateBodyFilePath plumbing while still exercising the
// validate-then-open contract.
func identityOf(t *testing.T, path string) BodyFileIdentity {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected filesystem metadata type for %s", path)
	}
	return BodyFileIdentity{
		Path: path,
		Dev:  uint64(stat.Dev),
		Ino:  uint64(stat.Ino),
	}
}

func TestReferencesBodyPlaceholder(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"standalone {body}", []string{"pr", "comment", "--body", "{body}"}, true},
		{"inline body={body}", []string{"api", "-f", "body={body}"}, true},
		{"no body placeholder", []string{"pr", "create", "-R", "{repo}"}, false},
		{"unrelated placeholders only", []string{"pr", "view", "{number}", "{repo}"}, false},
		{"body suffix in other word should not match", []string{"pr", "comment", "-f", "somebody={somebody}"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := &Operation{ArgsTemplate: tt.args}
			if got := op.ReferencesBodyPlaceholder(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAcceptsBodyFlag(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
		want  bool
	}{
		{"has --body", []string{"--title", "--body", "--draft"}, true},
		{"no --body", []string{"--title", "--state"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := &Operation{AllowedFlags: tt.flags}
			if got := op.AcceptsBodyFlag(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveMaxBytes(t *testing.T) {
	// Pattern A/B: body param with MaxLength → schema wins
	opA := &Operation{
		Params: map[string]ParamSchema{
			"body": {Type: "string", MaxLength: 65535},
		},
	}
	if got := EffectiveMaxBytes(opA, bodyFileSanityMaxBytes); got != 65535 {
		t.Errorf("Pattern A: got %d, want 65535", got)
	}

	// Pattern C: --body flag, no body param → 65535
	opC := &Operation{
		AllowedFlags: []string{"--title", "--body"},
	}
	if got := EffectiveMaxBytes(opC, bodyFileSanityMaxBytes); got != githubBodyMaxBytes {
		t.Errorf("Pattern C: got %d, want %d", got, githubBodyMaxBytes)
	}

	// No body support: falls back to sanity cap
	opNone := &Operation{
		AllowedFlags: []string{"--json"},
	}
	if got := EffectiveMaxBytes(opNone, bodyFileSanityMaxBytes); got != bodyFileSanityMaxBytes {
		t.Errorf("Fallback: got %d, want %d", got, bodyFileSanityMaxBytes)
	}

	// Schema with MaxLength 0 is treated as unset (sanity fallback when no flag either)
	opUnset := &Operation{
		Params: map[string]ParamSchema{
			"body": {Type: "string"},
		},
	}
	if got := EffectiveMaxBytes(opUnset, 4096); got != 4096 {
		t.Errorf("Unset MaxLength: got %d, want 4096", got)
	}
}

func TestValidateBodyFilePath(t *testing.T) {
	root := t.TempDir()
	inside := writeBodyFile(t, root, "good.md", "hello")

	t.Run("valid file under root", func(t *testing.T) {
		got, err := ValidateBodyFilePath(inside, root)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Compare resolved paths to handle macOS /var → /private/var symlinks
		wantResolved, err := filepath.EvalSymlinks(inside)
		if err != nil {
			t.Fatalf("eval inside: %v", err)
		}
		if got.Path != wantResolved {
			t.Errorf("got path %q, want %q", got.Path, wantResolved)
		}
		if got.Ino == 0 {
			t.Errorf("expected non-zero inode in identity, got %+v", got)
		}
	})

	t.Run("non-existing path", func(t *testing.T) {
		_, err := ValidateBodyFilePath(filepath.Join(root, "missing.md"), root)
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("expected non-existing error, got %v", err)
		}
	})

	t.Run("path traversal escapes root", func(t *testing.T) {
		// create file in a sibling directory and try ../sibling/foo.md
		outsideRoot := t.TempDir()
		outsideFile := writeBodyFile(t, outsideRoot, "leak.md", "leak")
		_, err := ValidateBodyFilePath(outsideFile, root)
		if err == nil || !strings.Contains(err.Error(), "outside the body root") {
			t.Errorf("expected outside-root error, got %v", err)
		}
	})

	t.Run("symlink to outside root is rejected", func(t *testing.T) {
		outsideRoot := t.TempDir()
		target := writeBodyFile(t, outsideRoot, "target.md", "target")
		link := filepath.Join(root, "escape.md")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		_, err := ValidateBodyFilePath(link, root)
		if err == nil || !strings.Contains(err.Error(), "outside the body root") {
			t.Errorf("expected outside-root error after symlink resolve, got %v", err)
		}
	})

	t.Run("sibling-prefix attack rejected", func(t *testing.T) {
		// /tmpdir/root holds the legitimate root; /tmpdir/rootEvil shares a string prefix
		// but is a separate directory. filepath.Rel must reject it.
		base := t.TempDir()
		legitRoot := filepath.Join(base, "body")
		evilRoot := filepath.Join(base, "body-evil")
		if err := os.MkdirAll(legitRoot, 0700); err != nil {
			t.Fatalf("mkdir legit: %v", err)
		}
		if err := os.MkdirAll(evilRoot, 0700); err != nil {
			t.Fatalf("mkdir evil: %v", err)
		}
		evilFile := writeBodyFile(t, evilRoot, "x.md", "evil")
		_, err := ValidateBodyFilePath(evilFile, legitRoot)
		if err == nil || !strings.Contains(err.Error(), "outside the body root") {
			t.Errorf("expected sibling-prefix rejection, got %v", err)
		}
	})

	t.Run("directory not regular file", func(t *testing.T) {
		dir := filepath.Join(root, "sub")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		_, err := ValidateBodyFilePath(dir, root)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("expected not-regular error, got %v", err)
		}
	})

	t.Run("empty root rejected", func(t *testing.T) {
		_, err := ValidateBodyFilePath(inside, "")
		if err == nil || !strings.Contains(err.Error(), "root is not configured") {
			t.Errorf("expected root-empty error, got %v", err)
		}
	})

	t.Run("empty path rejected", func(t *testing.T) {
		_, err := ValidateBodyFilePath("", root)
		if err == nil || !strings.Contains(err.Error(), "path is empty") {
			t.Errorf("expected path-empty error, got %v", err)
		}
	})
}

func TestReadBodyFile(t *testing.T) {
	root := t.TempDir()

	t.Run("valid utf-8", func(t *testing.T) {
		path := writeBodyFile(t, root, "ok.md", "こんにちは body")
		got, err := ReadBodyFile(identityOf(t, path), 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "こんにちは body" {
			t.Errorf("got %q, want %q", got, "こんにちは body")
		}
	})

	t.Run("size exceeds effective max", func(t *testing.T) {
		path := writeBodyFile(t, root, "big.md", strings.Repeat("a", 100))
		_, err := ReadBodyFile(identityOf(t, path), 50)
		if err == nil || !strings.Contains(err.Error(), "exceeds effective max") {
			t.Errorf("expected size error, got %v", err)
		}
	})

	t.Run("null byte rejected", func(t *testing.T) {
		path := writeBodyFile(t, root, "nul.md", "hello\x00world")
		_, err := ReadBodyFile(identityOf(t, path), 1024)
		if err == nil || !strings.Contains(err.Error(), "null") {
			t.Errorf("expected null byte error, got %v", err)
		}
	})

	t.Run("invalid utf-8 rejected", func(t *testing.T) {
		path := filepath.Join(root, "bad.md")
		// 0xff is not valid UTF-8
		if err := os.WriteFile(path, []byte{0xff, 0xfe, 0xfd}, 0600); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := ReadBodyFile(identityOf(t, path), 1024)
		if err == nil || !strings.Contains(err.Error(), "UTF-8") {
			t.Errorf("expected UTF-8 error, got %v", err)
		}
	})

	t.Run("effective max zero rejected", func(t *testing.T) {
		path := writeBodyFile(t, root, "zero.md", "x")
		_, err := ReadBodyFile(identityOf(t, path), 0)
		if err == nil || !strings.Contains(err.Error(), "effective max") {
			t.Errorf("expected effective-max error, got %v", err)
		}
	})

	t.Run("size exactly at limit accepted", func(t *testing.T) {
		path := writeBodyFile(t, root, "exact.md", "12345")
		got, err := ReadBodyFile(identityOf(t, path), 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "12345" {
			t.Errorf("got %q, want 12345", got)
		}
	})
}

func TestInjectBodyIntoRequest(t *testing.T) {
	t.Run("param mode injects body param", func(t *testing.T) {
		op := &Operation{ArgsTemplate: []string{"pr", "comment", "--body", "{body}"}}
		req := &OperationRequest{Operation: "gh_pr_comment"}
		if err := InjectBodyIntoRequest(req, op, "hello"); err != nil {
			t.Fatalf("inject: %v", err)
		}
		if req.Params["body"] != "hello" {
			t.Errorf("got %v, want hello", req.Params["body"])
		}
	})

	t.Run("param mode rejects existing body param", func(t *testing.T) {
		op := &Operation{ArgsTemplate: []string{"pr", "comment", "--body", "{body}"}}
		req := &OperationRequest{
			Operation: "gh_pr_comment",
			Params:    map[string]ParamValue{"body": "inline"},
		}
		err := InjectBodyIntoRequest(req, op, "fromfile")
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Errorf("expected exclusivity error, got %v", err)
		}
	})

	t.Run("flag mode appends --body= flag", func(t *testing.T) {
		op := &Operation{AllowedFlags: []string{"--title", "--body"}}
		req := &OperationRequest{Operation: "gh_pr_create"}
		if err := InjectBodyIntoRequest(req, op, "hello world"); err != nil {
			t.Fatalf("inject: %v", err)
		}
		found := false
		for _, f := range req.Flags {
			if f == "--body=hello world" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected --body=hello world in %v", req.Flags)
		}
	})

	t.Run("flag mode rejects existing --body= flag", func(t *testing.T) {
		op := &Operation{AllowedFlags: []string{"--body"}}
		req := &OperationRequest{
			Operation: "gh_pr_create",
			Flags:     []string{"--body=inline"},
		}
		err := InjectBodyIntoRequest(req, op, "fromfile")
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Errorf("expected exclusivity error, got %v", err)
		}
	})

	t.Run("flag mode rejects bare --body flag", func(t *testing.T) {
		op := &Operation{AllowedFlags: []string{"--body"}}
		req := &OperationRequest{
			Operation: "gh_pr_create",
			Flags:     []string{"--body"},
		}
		err := InjectBodyIntoRequest(req, op, "fromfile")
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Errorf("expected exclusivity error, got %v", err)
		}
	})

	t.Run("placeholder wins over flag (template-driven priority)", func(t *testing.T) {
		// Hypothetical mixed-mode operation: both {body} placeholder and --body
		// in allowed_flags. The placeholder should win because it is the
		// actual command contract that BuildArgs will substitute.
		op := &Operation{
			ArgsTemplate: []string{"pr", "comment", "--body", "{body}"},
			AllowedFlags: []string{"--body"},
		}
		req := &OperationRequest{Operation: "mixed_op"}
		if err := InjectBodyIntoRequest(req, op, "content"); err != nil {
			t.Fatalf("inject: %v", err)
		}
		if req.Params["body"] != "content" {
			t.Errorf("expected param-mode injection, params=%v flags=%v", req.Params, req.Flags)
		}
		// Flag must NOT also be appended
		for _, f := range req.Flags {
			if strings.HasPrefix(f, "--body=") {
				t.Errorf("flag-mode also injected: %s", f)
			}
		}
	})

	t.Run("unsupported operation rejected", func(t *testing.T) {
		op := &Operation{ArgsTemplate: []string{"pr", "view", "{number}"}}
		req := &OperationRequest{Operation: "gh_pr_view"}
		err := InjectBodyIntoRequest(req, op, "content")
		if err == nil || !strings.Contains(err.Error(), "does not accept a body") {
			t.Errorf("expected unsupported error, got %v", err)
		}
	})
}

func TestValidateBodyFilePathRejectsHardlink(t *testing.T) {
	root := t.TempDir()
	target := writeBodyFile(t, root, "target.md", "shared content")
	link := filepath.Join(root, "link.md")
	if err := os.Link(target, link); err != nil {
		t.Fatalf("os.Link: %v", err)
	}

	// Both endpoints of the hard link must be rejected — link count > 1
	// means the inode is reachable from more than one path, so containment
	// under the body root no longer constrains what the read returns.
	for _, name := range []string{"target.md", "link.md"} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateBodyFilePath(filepath.Join(root, name), root)
			if err == nil || !strings.Contains(err.Error(), "exactly one link") {
				t.Errorf("expected hardlink rejection, got %v", err)
			}
		})
	}
}

func TestReadBodyFileDetectsReplacementAfterValidation(t *testing.T) {
	t.Run("regular file replaced with different regular file", func(t *testing.T) {
		root := t.TempDir()
		path := writeBodyFile(t, root, "swap.md", "original content")
		identity, err := ValidateBodyFilePath(path, root)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		// Simulate the caller rewriting the file under the same path between
		// validation and read. os.Remove + os.WriteFile yields a new inode,
		// which ReadBodyFile must notice via fstat.
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove original: %v", err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			t.Fatalf("write replacement: %v", err)
		}
		_, err = ReadBodyFile(identity, 1024)
		if err == nil || !strings.Contains(err.Error(), "identity changed") {
			t.Errorf("expected identity-changed rejection, got %v", err)
		}
	})

	t.Run("regular file replaced with symlink", func(t *testing.T) {
		root := t.TempDir()
		path := writeBodyFile(t, root, "swap2.md", "original content")
		identity, err := ValidateBodyFilePath(path, root)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		// External target the caller would prefer the daemon to read.
		outside := t.TempDir()
		outsideFile := writeBodyFile(t, outside, "secret.md", "outside content")
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove original: %v", err)
		}
		if err := os.Symlink(outsideFile, path); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		_, err = ReadBodyFile(identity, 1024)
		if err == nil {
			t.Fatal("expected open failure or identity rejection, got nil")
		}
		// Accept either branch:
		//   - O_NOFOLLOW fails the open with ELOOP-equivalent "open body_file"
		//   - or, if some FS lets the open succeed, fstat catches the dev/ino mismatch
		if !strings.Contains(err.Error(), "open body_file") &&
			!strings.Contains(err.Error(), "identity changed") {
			t.Errorf("unexpected error shape: %v", err)
		}
	})
}
