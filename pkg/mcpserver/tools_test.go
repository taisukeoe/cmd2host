package mcpserver

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

func TestFormatRunOutput_NoTruncation(t *testing.T) {
	resp := &OperationResponse{
		ExitCode: 0,
		Stdout:   "hello world\n",
		Stderr:   "",
	}
	got := formatRunOutput(resp)
	want := "Exit code: 0\n\n**stdout:**\n```\nhello world\n```\n\n"
	if got != want {
		t.Errorf("got =\n%q\nwant =\n%q", got, want)
	}
}

func TestFormatRunOutput_TruncatedStdoutStripsSuffixAndEmitsIndicator(t *testing.T) {
	resp := &OperationResponse{
		ExitCode:            0,
		Stdout:              "hello world" + truncatedSuffix,
		StdoutTruncated:     true,
		StdoutOriginalBytes: 1500000,
	}
	got := formatRunOutput(resp)
	// "hello world" is 11 bytes; the synthetic suffix is stripped before the fence.
	wantIndicator := "*stdout truncated: shown 11 of 1500000 bytes*"
	if !strings.Contains(got, wantIndicator) {
		t.Errorf("expected indicator %q in output:\n%s", wantIndicator, got)
	}
	// The synthetic suffix MUST NOT appear inside the fenced block when the
	// typed flag is set; otherwise the agent sees it twice (once via the
	// fenced body, once via the indicator).
	if strings.Contains(got, "... (truncated)") {
		t.Errorf("expected synthetic suffix stripped from fenced body, got:\n%s", got)
	}
}

func TestFormatRunOutput_TruncatedStderr(t *testing.T) {
	resp := &OperationResponse{
		ExitCode:            1,
		Stderr:              "errlog" + truncatedSuffix,
		StderrTruncated:     true,
		StderrOriginalBytes: 70000,
	}
	got := formatRunOutput(resp)
	wantIndicator := "*stderr truncated: shown 6 of 70000 bytes*"
	if !strings.Contains(got, wantIndicator) {
		t.Errorf("expected indicator %q in output:\n%s", wantIndicator, got)
	}
}

func TestFormatRunOutput_LegacyDaemonPreservesSuffixWithoutIndicator(t *testing.T) {
	// An older daemon that does not set the typed flag still produces the
	// legacy literal suffix inside the stream string. The client MUST preserve
	// it so the agent still sees the visible truncation marker.
	resp := &OperationResponse{
		ExitCode: 0,
		Stdout:   "hello world" + truncatedSuffix,
		// StdoutTruncated / StdoutOriginalBytes left at zero values.
	}
	got := formatRunOutput(resp)
	if !strings.Contains(got, "... (truncated)") {
		t.Errorf("expected legacy suffix preserved when flag unset, got:\n%s", got)
	}
	if strings.Contains(got, "*stdout truncated:") {
		t.Errorf("expected no machine-backed indicator when flag unset, got:\n%s", got)
	}
}
