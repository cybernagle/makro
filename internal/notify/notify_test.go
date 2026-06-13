package notify

import (
	"context"
	"errors"
	"testing"
)

func TestTerminalNotifierArgs(t *testing.T) {
	got := terminalNotifierArgs("Makro", "dev", "done", "com.cybernagle.makro")
	want := []string{"-title", "Makro", "-subtitle", "dev", "-message", "done", "-activate", "com.cybernagle.makro", "-sound", "default"}
	if len(got) != len(want) {
		t.Fatalf("args len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTerminalNotifierArgsOmitsEmpty(t *testing.T) {
	got := terminalNotifierArgs("Makro", "", "done", "")
	for _, a := range got {
		if a == "-subtitle" || a == "-activate" {
			t.Errorf("empty subtitle/activate should be omitted; got %v", got)
		}
	}
}

func TestDisplayNotificationScript(t *testing.T) {
	got := displayNotificationScript("Makro", "dev", "all good")
	want := `display notification "all good" with title "Makro" subtitle "dev"`
	if got != want {
		t.Errorf("script = %q, want %q", got, want)
	}
}

func TestAppleQuoteEscapes(t *testing.T) {
	if got := appleQuote(`she said "hi" \n`); got != `"she said \"hi\" \\n"` {
		t.Errorf("appleQuote = %q", got)
	}
}

func TestTruncateBody(t *testing.T) {
	cases := map[string]string{
		"single line":          "single line",
		"first\nsecond\nthird": "first",
		"  trim me  ":          "trim me",
		"\nleading newline":    "leading newline",
	}
	for in, want := range cases {
		if got := truncateBody(in); got != want {
			t.Errorf("truncateBody(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateBodyLongRunes(t *testing.T) {
	long := make([]rune, 500)
	for i := range long {
		long[i] = '字'
	}
	got := truncateBody(string(long))
	if len([]rune(got)) != 201 { // 200 + ellipsis
		t.Errorf("truncated len = %d runes, want 201", len([]rune(got)))
	}
}

func TestIsFrontmost(t *testing.T) {
	orig := frontmostGetter
	defer func() { frontmostGetter = orig }()

	tests := []struct {
		name     string
		getter   func(ctx context.Context) (string, error)
		bundle   string
		expected bool
	}{
		{"match", func(ctx context.Context) (string, error) { return "com.cybernagle.makro", nil }, "com.cybernagle.makro", true},
		{"mismatch", func(ctx context.Context) (string, error) { return "com.google.chrome", nil }, "com.cybernagle.makro", false},
		{"error fails open", func(ctx context.Context) (string, error) { return "", errors.New("boom") }, "com.cybernagle.makro", false},
		{"empty bundle fails open", func(ctx context.Context) (string, error) { return "x", nil }, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frontmostGetter = tt.getter
			if got := IsFrontmost(context.Background(), tt.bundle); got != tt.expected {
				t.Errorf("IsFrontmost = %v, want %v", got, tt.expected)
			}
		})
	}
}
