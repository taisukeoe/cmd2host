// origin_url.go: parser for the `git remote get-url origin` output forms
// the cwd auto-resolve path needs to normalize into a canonical
// "owner/repo" identifier. Used only by the cwd auto-resolve fallback in
// ResolveExecutionTarget; the explicit-flag and primary-default paths do
// not consult origin URLs.
//
// Supported forms (covers every shape `git remote get-url` emits for the
// hosts cmd2host expects to proxy):
//
//   - scp-style:     git@github.com:owner/repo[.git]
//   - https:         https://github.com/owner/repo[.git]
//   - https + user:  https://user@github.com/owner/repo[.git]
//   - ssh url:       ssh://git@github.com/owner/repo[.git]
//   - ssh + port:    ssh://git@github.com:22/owner/repo[.git]
//
// The parser returns "" for any URL that does not normalize cleanly to a
// single "owner/repo" pair. Callers treat "" as "auto-resolve unavailable"
// and fall through to the next resolution step.

package daemon

import (
	"strings"
)

// ParseOriginOwnerRepo extracts the "owner/repo" identifier from a git
// remote URL. Returns "" when the URL does not match any supported form
// or does not yield a clean owner/repo pair.
//
// The function is intentionally lenient with input formatting (leading /
// trailing whitespace, optional `.git` suffix, optional userinfo prefix)
// but strict about the resulting shape: exactly one "/" separator
// between owner and repo, both non-empty, no embedded path segments.
func ParseOriginOwnerRepo(url string) string {
	s := strings.TrimSpace(url)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, ".git")

	// URL form: <scheme>://[userinfo@]host[:port]/owner/repo
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		// Strip userinfo prefix (user@).
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		// host[:port]/owner/repo → split at first "/".
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return ""
		}
		path := rest[slash+1:]
		return canonicalOwnerRepo(path)
	}

	// scp-style: [user@]host:owner/repo. The first ":" separates the host
	// from the path. We require a "@" prefix so a plain literal that
	// happens to contain ":" (e.g. accidental Windows path) does not
	// silently parse as a URL.
	if at := strings.Index(s, "@"); at >= 0 {
		afterUser := s[at+1:]
		if colon := strings.Index(afterUser, ":"); colon >= 0 {
			path := afterUser[colon+1:]
			return canonicalOwnerRepo(path)
		}
	}

	return ""
}

// canonicalOwnerRepo accepts a path fragment (owner/repo or owner/repo/...)
// and returns "owner/repo" only when the input has exactly two non-empty
// segments. Deeper paths (e.g. owner/repo/branch) are rejected to avoid
// confusing a sub-path with a different repo.
func canonicalOwnerRepo(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		return ""
	}
	if parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
