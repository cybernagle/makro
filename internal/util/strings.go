package util

import (
	"strings"
)

// Truncate truncates s to at most maxLen runes, appending "..." if truncated.
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// IsNoiseLine returns true for lines that carry no useful content.
func IsNoiseLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	for _, r := range trimmed {
		if 0x2500 <= r && r <= 0x257F {
			continue
		}
		if r == '─' || r == '━' || r == '│' || r == '┃' {
			continue
		}
		if r == '-' || r == '=' || r == '~' || r == '*' {
			continue
		}
		return false
	}
	return true
}

// FilterNoiseLines removes noise lines from the slice.
func FilterNoiseLines(lines []string) []string {
	filtered := make([]string, 0, len(lines))
	for _, l := range lines {
		if !IsNoiseLine(l) {
			filtered = append(filtered, l)
		}
	}
	return filtered
}

// ReadProgressive filters noise from raw terminal output and returns the
// last maxChars runes of cleaned content (most recent output).
func ReadProgressive(raw string, maxChars int) string {
	lines := strings.Split(raw, "\n")
	filtered := FilterNoiseLines(lines)
	cleaned := strings.Join(filtered, "\n")
	runes := []rune(cleaned)
	if len(runes) <= maxChars {
		return cleaned
	}
	return string(runes[len(runes)-maxChars:])
}
