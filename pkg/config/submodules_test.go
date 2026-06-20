package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeGitmodules writes a fixture .gitmodules at root.
func writeGitmodules(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte(content), 0644); err != nil {
		t.Fatalf("write .gitmodules: %v", err)
	}
}

func TestSuggestSubmodules_NoFile(t *testing.T) {
	tmp := t.TempDir()
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no suggestions when .gitmodules is absent, got %v", got)
	}
}

func TestSuggestSubmodules_SSHForm(t *testing.T) {
	tmp := t.TempDir()
	writeGitmodules(t, tmp, `[submodule "lib-a"]
	path = vendor/lib-a
	url = git@github.com:owner/lib-a.git
`)
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubmoduleSuggestion{{Repo: "owner/lib-a", RepoPath: "vendor/lib-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSuggestSubmodules_HTTPSForm(t *testing.T) {
	tmp := t.TempDir()
	writeGitmodules(t, tmp, `[submodule "lib-b"]
	path = libs/lib-b
	url = https://github.com/org/lib-b.git
`)
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubmoduleSuggestion{{Repo: "org/lib-b", RepoPath: "libs/lib-b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSuggestSubmodules_TrailingDotGitOptional(t *testing.T) {
	tmp := t.TempDir()
	writeGitmodules(t, tmp, `[submodule "lib-c"]
	path = vendor/lib-c
	url = git@github.com:owner/lib-c
[submodule "lib-d"]
	path = vendor/lib-d
	url = https://github.com/owner/lib-d
`)
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubmoduleSuggestion{
		{Repo: "owner/lib-c", RepoPath: "vendor/lib-c"},
		{Repo: "owner/lib-d", RepoPath: "vendor/lib-d"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSuggestSubmodules_MissingFields(t *testing.T) {
	tmp := t.TempDir()
	writeGitmodules(t, tmp, `[submodule "no-url"]
	path = vendor/no-url
[submodule "no-path"]
	url = git@github.com:owner/no-path.git
[submodule "complete"]
	path = vendor/complete
	url = git@github.com:owner/complete.git
`)
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubmoduleSuggestion{{Repo: "owner/complete", RepoPath: "vendor/complete"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries missing path or url must be skipped; got %v, want %v", got, want)
	}
}

func TestSuggestSubmodules_CommentsAndBlankLines(t *testing.T) {
	tmp := t.TempDir()
	writeGitmodules(t, tmp, `# top-level comment

[submodule "lib-e"]
	# inline comment
	path = vendor/lib-e

	url = git@github.com:owner/lib-e.git

`)
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubmoduleSuggestion{{Repo: "owner/lib-e", RepoPath: "vendor/lib-e"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSuggestSubmodules_MultipleSubmodules(t *testing.T) {
	tmp := t.TempDir()
	writeGitmodules(t, tmp, `[submodule "sub-a"]
	path = a
	url = git@github.com:owner/sub-a.git
[submodule "sub-b"]
	path = nested/sub-b
	url = https://github.com/owner/sub-b
[submodule "sub-c"]
	path = vendor/third-party/sub-c
	url = git@github.com:third/sub-c.git
`)
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubmoduleSuggestion{
		{Repo: "owner/sub-a", RepoPath: "a"},
		{Repo: "owner/sub-b", RepoPath: "nested/sub-b"},
		{Repo: "third/sub-c", RepoPath: "vendor/third-party/sub-c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSuggestSubmodules_UnparseableURLSkipped(t *testing.T) {
	tmp := t.TempDir()
	writeGitmodules(t, tmp, `[submodule "weird"]
	path = vendor/weird
	url = file:///local/path/to/repo
[submodule "good"]
	path = vendor/good
	url = git@github.com:owner/good.git
`)
	got, err := SuggestSubmodules(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []SubmoduleSuggestion{{Repo: "owner/good", RepoPath: "vendor/good"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unparseable URLs must be skipped; got %v, want %v", got, want)
	}
}
