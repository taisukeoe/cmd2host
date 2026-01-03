package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHashToken(t *testing.T) {
	// Same token should produce same hash
	token := "test-token-123"
	hash1 := hashToken(token)
	hash2 := hashToken(token)

	if hash1 != hash2 {
		t.Errorf("Same token produced different hashes: %s vs %s", hash1, hash2)
	}

	// Different tokens should produce different hashes
	hash3 := hashToken("different-token")
	if hash1 == hash3 {
		t.Error("Different tokens produced same hash")
	}

	// Hash should be hex encoded (64 chars for 256-bit hash)
	if len(hash1) != 64 {
		t.Errorf("Hash length = %d, want 64", len(hash1))
	}
}

func TestTokenStoreIsValid(t *testing.T) {
	tmpDir := t.TempDir()
	ts := &TokenStore{dir: tmpDir}

	// Must be 64 hex chars to pass format validation
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hash := hashToken(token)
	tokenPath := filepath.Join(tmpDir, hash)

	// Token file doesn't exist -> invalid
	if ts.IsValid(token) {
		t.Error("Non-existent token should be invalid")
	}

	// Create token file with JSON content
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	// Token file exists and is fresh -> valid
	if !ts.IsValid(token) {
		t.Error("Fresh token should be valid")
	}

	// Empty token should be invalid
	if ts.IsValid("") {
		t.Error("Empty token should be invalid")
	}

	// Wrong token (invalid format) should be invalid
	if ts.IsValid("wrong-token") {
		t.Error("Wrong token should be invalid")
	}

	// Wrong token (valid format but not registered) should be invalid
	wrongToken := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if ts.IsValid(wrongToken) {
		t.Error("Wrong token should be invalid")
	}
}

func TestTokenStoreGetTokenData(t *testing.T) {
	tmpDir := t.TempDir()
	ts := &TokenStore{dir: tmpDir}

	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hash := hashToken(token)
	tokenPath := filepath.Join(tmpDir, hash)

	// Token file doesn't exist -> invalid
	data, valid := ts.GetTokenData(token)
	if valid {
		t.Error("Non-existent token should be invalid")
	}

	// Create token file with JSON content
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	// Token file exists -> valid with repo data
	data, valid = ts.GetTokenData(token)
	if !valid {
		t.Error("Fresh token should be valid")
	}
	if data.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want %q", data.Repo, "owner/repo")
	}

	// Empty repo in JSON
	token2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hash2 := hashToken(token2)
	tokenPath2 := filepath.Join(tmpDir, hash2)
	if err := os.WriteFile(tokenPath2, []byte(`{"repo":""}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	data, valid = ts.GetTokenData(token2)
	if !valid {
		t.Error("Token with empty repo should be valid")
	}
	if data.Repo != "" {
		t.Errorf("Repo = %q, want empty", data.Repo)
	}

	// Malformed JSON should be treated as invalid
	token3 := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	hash3 := hashToken(token3)
	tokenPath3 := filepath.Join(tmpDir, hash3)
	if err := os.WriteFile(tokenPath3, []byte(`{invalid json}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	data, valid = ts.GetTokenData(token3)
	if valid {
		t.Error("Token with malformed JSON should be invalid")
	}
	if data.Repo != "" {
		t.Errorf("Repo = %q, want empty for malformed JSON", data.Repo)
	}

	// Empty file should be treated as invalid
	token4 := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	hash4 := hashToken(token4)
	tokenPath4 := filepath.Join(tmpDir, hash4)
	if err := os.WriteFile(tokenPath4, []byte(``), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	data, valid = ts.GetTokenData(token4)
	if valid {
		t.Error("Token with empty file should be invalid")
	}
	if data.Repo != "" {
		t.Errorf("Repo = %q, want empty for empty file", data.Repo)
	}
}

func TestTokenStoreExpiredToken(t *testing.T) {
	tmpDir := t.TempDir()
	ts := &TokenStore{dir: tmpDir}

	// Must be 64 hex chars to pass format validation
	token := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	hash := hashToken(token)
	tokenPath := filepath.Join(tmpDir, hash)

	// Create token file with JSON content
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	// Set mtime to 25 hours ago (past TTL)
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(tokenPath, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to set mtime: %v", err)
	}

	// Expired token should be invalid
	if ts.IsValid(token) {
		t.Error("Expired token should be invalid")
	}
}

func TestTokenStoreCleanupExpired(t *testing.T) {
	tmpDir := t.TempDir()
	ts := &TokenStore{dir: tmpDir}

	// Create fresh token (must be 64 hex chars)
	freshToken := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	freshHash := hashToken(freshToken)
	freshPath := filepath.Join(tmpDir, freshHash)
	if err := os.WriteFile(freshPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create fresh token file: %v", err)
	}

	// Create expired token (must be 64 hex chars)
	expiredToken := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	expiredHash := hashToken(expiredToken)
	expiredPath := filepath.Join(tmpDir, expiredHash)
	if err := os.WriteFile(expiredPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create expired token file: %v", err)
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(expiredPath, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to set mtime: %v", err)
	}

	// Run cleanup
	if err := ts.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired failed: %v", err)
	}

	// Fresh token should still exist
	if _, err := os.Stat(freshPath); os.IsNotExist(err) {
		t.Error("Fresh token should not be removed")
	}

	// Expired token should be removed
	if _, err := os.Stat(expiredPath); !os.IsNotExist(err) {
		t.Error("Expired token should be removed")
	}
}

func TestTokenStoreCleanupNonExistentDir(t *testing.T) {
	ts := &TokenStore{dir: "/non/existent/path"}

	// Should not error on non-existent directory
	if err := ts.CleanupExpired(); err != nil {
		t.Errorf("CleanupExpired on non-existent dir should not error: %v", err)
	}
}

func TestTokenStoreBruteForceProtection(t *testing.T) {
	// 256-bit token (64 hex chars) is cryptographically secure
	// This test just verifies the hash function produces the expected length
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hash := hashToken(token)

	// BLAKE3-256 produces 32 bytes = 64 hex chars
	if len(hash) != 64 {
		t.Errorf("Hash length = %d, want 64 (256 bits)", len(hash))
	}

	// Verify hash is deterministic
	hash2 := hashToken(token)
	if hash != hash2 {
		t.Error("Hash function should be deterministic")
	}
}

func TestIsValidTokenFormat(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"valid lowercase", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},
		{"valid uppercase", "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", true},
		{"valid mixed case", "0123456789abcDEF0123456789ABCdef0123456789abcdef0123456789ABCDEF", true},
		{"too short", "0123456789abcdef", false},
		{"too long", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef00", false},
		{"empty", "", false},
		{"contains space", "0123456789abcdef 123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"contains newline", "0123456789abcdef\n123456789abcdef0123456789abcdef0123456789abcde", false},
		{"contains non-hex char g", "0123456789abcdefg123456789abcdef0123456789abcdef0123456789abcde", false},
		{"contains special char", "0123456789abcdef!123456789abcdef0123456789abcdef0123456789abcde", false},
		{"contains unicode", "0123456789abcdef日23456789abcdef0123456789abcdef0123456789abcde", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidTokenFormat(tt.token); got != tt.want {
				t.Errorf("isValidTokenFormat(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestTokenStoreIsValidMalformedTokens(t *testing.T) {
	tmpDir := t.TempDir()
	ts := &TokenStore{dir: tmpDir}

	// Create a valid token file with JSON content
	validToken := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hash := hashToken(validToken)
	tokenPath := filepath.Join(tmpDir, hash)
	if err := os.WriteFile(tokenPath, []byte(`{"repo":"owner/repo"}`), 0600); err != nil {
		t.Fatalf("Failed to create token file: %v", err)
	}

	// Valid token should work
	if !ts.IsValid(validToken) {
		t.Error("Valid token should be valid")
	}

	// Malformed tokens should be rejected (even if they might hash to the same value)
	malformedTokens := []string{
		"short",
		"contains-non-hex-chars-0123456789abcdef0123456789abcdef01234567",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
		"",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef ",
	}

	for _, token := range malformedTokens {
		if ts.IsValid(token) {
			t.Errorf("Malformed token %q should be invalid", token)
		}
	}
}
