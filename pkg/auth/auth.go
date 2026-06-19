// Package auth provides session token authentication for cmd2host.
// Tokens are BLAKE3 hashed and stored as JSON files under the cmd2host
// base dir's tokens/ subdirectory (see internal/configdir).
// Token validity is determined by file mtime (24-hour TTL).
package auth

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/taisukeoe/cmd2host/internal/configdir"
	"github.com/zeebo/blake3"
)

// TokenData contains project-specific data stored with the token.
// This struct is extensible for future use cases beyond repository restriction.
type TokenData struct {
	// ProjectID is the canonical project identifier this token is bound to
	// (NormalizeProjectID(primary_repo)). When present, the daemon resolves
	// the project config directly from ProjectID and uses Repo (if non-empty)
	// as a defense-in-depth check against project.Repos[0].
	ProjectID string `json:"project_id,omitempty"`

	// Repo is the GitHub repository (owner/repo) bound to this token.
	// For new tokens that carry ProjectID, Repo equals the project's primary
	// repo (Repos[0]) and acts as a defense-in-depth check.
	// For legacy tokens without ProjectID, Repo is the sole project resolver:
	// the project ID is computed via NormalizeProjectID(Repo).
	// Empty string means repo could not be detected at token creation time;
	// in that case, commands that target a specific repo are denied while
	// repo-agnostic commands stay allowed. See pkg/daemon resolveProject.
	Repo string `json:"repo"`

	// Profile is deprecated and unused. Project-based config is now used instead.
	// Kept for backwards compatibility with existing token files.
	Profile string `json:"profile,omitempty"`
}

const (
	tokenTTL      = 24 * time.Hour
	cleanupBuffer = 5 * time.Minute // Extra time before cleanup to prevent race conditions
	// tokenDirName is the tokens subdirectory name relative to the base dir
	// resolved by configdir.Dir. Joined onto the resolved base dir at runtime.
	tokenDirName = "tokens"
)

// TokenStore manages session tokens
type TokenStore struct {
	dir string
}

// NewTokenStore creates a new TokenStore.
// Honors CMD2HOST_CONFIG_DIR via configdir.Dir, while preserving the
// pre-existing diagnostic when the underlying home-dir lookup fails.
func NewTokenStore() (*TokenStore, error) {
	base, err := configdir.Dir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine cmd2host config directory: %w", err)
	}
	return NewTokenStoreAt(filepath.Join(base, tokenDirName)), nil
}

// NewTokenStoreAt creates a TokenStore backed by the given token directory,
// bypassing the default base-dir resolution. Callers that manage per-session
// state can point it at an explicit directory.
func NewTokenStoreAt(dir string) *TokenStore {
	return &TokenStore{dir: dir}
}

// HashToken computes the BLAKE3 hash of a token
func HashToken(token string) string {
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
	_, valid := ts.GetTokenData(token)
	return valid
}

// GetTokenData validates the token and returns associated project data.
// Returns the token data and true if valid, empty TokenData and false otherwise.
func (ts *TokenStore) GetTokenData(token string) (TokenData, bool) {
	if token == "" || !isValidTokenFormat(token) {
		return TokenData{}, false
	}

	hashStr := HashToken(token)
	path := filepath.Join(ts.dir, hashStr)

	info, err := os.Stat(path)
	if err != nil {
		return TokenData{}, false // File does not exist
	}

	// Check TTL
	if time.Since(info.ModTime()) >= tokenTTL {
		return TokenData{}, false
	}

	// Read and parse JSON content
	content, err := os.ReadFile(path)
	if err != nil {
		// Log file system errors for debugging (permission denied, I/O errors, etc.)
		// Only show first 8 chars of hash to avoid leaking full token hash in logs
		log.Printf("Warning: failed to read token file %s...: %v", hashStr[:8], err)
		return TokenData{}, false
	}

	var data TokenData
	if err := json.Unmarshal(content, &data); err != nil {
		return TokenData{}, false
	}

	return data, true
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
