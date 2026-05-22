package main

import (
	"strings"
	"testing"
)

func TestFenceLengthFor(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"no backticks", "hello world", 3},
		{"one backtick", "use `x` here", 3},
		{"two backticks", "see ``x`` literal", 3},
		{"three backticks", "fence ``` open", 4},
		{"four backticks", "fence ```` open", 5},
		{"non-adjacent runs", "`a`` b ``c", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fenceLengthFor(tc.content); got != tc.want {
				t.Fatalf("fenceLengthFor(%q) = %d, want %d", tc.content, got, tc.want)
			}
		})
	}
}

func TestWrapAsFencedBlock(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty content",
			content: "",
			want:    "```\n\n```\n",
		},
		{
			name:    "no trailing newline",
			content: "hello",
			want:    "```\nhello\n```\n",
		},
		{
			name:    "already trailing newline",
			content: "hello\n",
			want:    "```\nhello\n```\n",
		},
		{
			name:    "triple backticks in content escalates to 4",
			content: "before\n```\nafter\n",
			want:    "````\nbefore\n```\nafter\n````\n",
		},
		{
			name:    "four backticks in content escalates to 5",
			content: "x ```` y",
			want:    "`````\nx ```` y\n`````\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapAsFencedBlock(tc.content)
			if got != tc.want {
				t.Fatalf("wrapAsFencedBlock(%q) =\n%q\nwant\n%q", tc.content, got, tc.want)
			}
		})
	}
}

func TestWrapAsFencedBlockEnclosesBackticks(t *testing.T) {
	content := "leading ``` mid ```` end"
	wrapped := wrapAsFencedBlock(content)
	fenceLen := fenceLengthFor(content)
	wantFence := strings.Repeat("`", fenceLen)
	if !strings.HasPrefix(wrapped, wantFence+"\n") {
		t.Fatalf("wrapped output does not start with expected fence:\n%q", wrapped)
	}
	if !strings.HasSuffix(wrapped, "\n"+wantFence+"\n") {
		t.Fatalf("wrapped output does not end with expected fence:\n%q", wrapped)
	}
	if strings.Contains(content, wantFence) {
		t.Fatalf("content contains a backtick run as long as the chosen fence (%d); fence would not enclose safely", fenceLen)
	}
}
