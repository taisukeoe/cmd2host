// auth.go provides session token authentication for cmd2host.
// Tokens are BLAKE3 hashed and stored as empty files in ~/.cmd2host/tokens/.
// Token validity is determined by file mtime (24-hour TTL).
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zeebo/blake3"
)

const (
	tokenTTL          = 24 * time.Hour
	cleanupBuffer     = 5 * time.Minute // Extra time before cleanup to prevent race conditions
	tokenDir          = ".cmd2host/tokens"
)

// TokenStore manages session tokens
type TokenStore struct {
	dir string
}

// NewTokenStore creates a new TokenStore
func NewTokenStore() (*TokenStore, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &TokenStore{
		dir: filepath.Join(homeDir, tokenDir),
	}, nil
}

// hashToken computes the BLAKE3 hash of a token
func hashToken(token string) string {
	hash := blake3.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// isValidTokenFormat checks if the token has the expected format (64 hex chars)
func isValidTokenFormat(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, c := range token {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// IsValid checks if the given token is valid and not expired
func (ts *TokenStore) IsValid(token string) bool {
	if token == "" || !isValidTokenFormat(token) {
		return false
	}

	hashStr := hashToken(token)
	path := filepath.Join(ts.dir, hashStr)

	info, err := os.Stat(path)
	if err != nil {
		return false // File does not exist
	}

	return time.Since(info.ModTime()) < tokenTTL
}

// CleanupExpired removes expired token files
func (ts *TokenStore) CleanupExpired() error {
	entries, err := os.ReadDir(ts.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Directory doesn't exist yet, nothing to clean
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Use tokenTTL + cleanupBuffer to avoid race conditions where a token
		// could be validated as valid but cleaned up before the request completes
		if time.Since(info.ModTime()) > tokenTTL+cleanupBuffer {
			path := filepath.Join(ts.dir, entry.Name())
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				fmt.Printf("Warning: failed to remove expired token %s: %v\n", entry.Name(), err)
			}
		}
	}

	return nil
}
