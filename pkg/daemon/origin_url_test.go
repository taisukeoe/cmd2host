package daemon

import "testing"

func TestParseOriginOwnerRepo(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"scp_style", "git@github.com:owner/repo.git", "owner/repo"},
		{"scp_style_no_suffix", "git@github.com:owner/repo", "owner/repo"},
		{"https", "https://github.com/owner/repo.git", "owner/repo"},
		{"https_no_suffix", "https://github.com/owner/repo", "owner/repo"},
		{"https_with_userinfo", "https://x-access-token@github.com/owner/repo.git", "owner/repo"},
		{"ssh_url", "ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"ssh_with_port", "ssh://git@github.com:22/owner/repo.git", "owner/repo"},
		{"trailing_newline", "git@github.com:owner/repo.git\n", "owner/repo"},
		{"hyphen_owner", "git@github.com:my-owner/repo.git", "my-owner/repo"},
		{"dot_in_repo", "git@github.com:owner/repo.name.git", "owner/repo.name"},
		// Negative cases — must return "" so callers skip auto-resolve cleanly.
		{"empty", "", ""},
		{"whitespace_only", "   ", ""},
		{"no_path", "git@github.com:", ""},
		{"single_segment", "git@github.com:owner", ""},
		{"deeper_path", "https://github.com/owner/repo/branch.git", ""},
		{"missing_owner", "https://github.com//repo.git", ""},
		{"missing_repo", "https://github.com/owner/.git", ""},
		{"random_text", "not a url", ""},
		{"scp_without_user", "github.com:owner/repo.git", ""}, // require @ to distinguish from accidental literals
		// Credential-bearing forms — must normalize to owner/repo so the
		// log-safe helper (OriginRepoForLog) never surfaces the token.
		{"https_x_access_token", "https://x-access-token:TOKEN123@github.com/owner/repo.git", "owner/repo"},
		{"https_userinfo_password", "https://user:p%40ssw0rd@github.com/owner/repo.git", "owner/repo"},
		{"ssh_url_with_credentials", "ssh://git:secret@github.com/owner/repo.git", "owner/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOriginOwnerRepo(tc.in)
			if got != tc.want {
				t.Fatalf("ParseOriginOwnerRepo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOriginRepoForLog(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"normal", "git@github.com:owner/repo.git", "owner/repo"},
		{"credential_stripped", "https://x-access-token:TOKEN123@github.com/owner/repo.git", "owner/repo"},
		{"unparseable_returns_placeholder", "not a url", "<unparsed>"},
		{"empty_returns_placeholder", "", "<unparsed>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OriginRepoForLog(tc.in)
			if got != tc.want {
				t.Fatalf("OriginRepoForLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Defence-in-depth: the helper must never echo the raw
			// input, even when the input is unparseable.
			if got == tc.in && tc.in != "owner/repo" {
				t.Fatalf("OriginRepoForLog leaked raw input %q", tc.in)
			}
		})
	}
}
