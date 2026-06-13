// Package notify posts macOS desktop notifications and detects the frontmost
// application. It is best-effort: errors are logged and never surfaced, so a
// notification failure can never break the host application.
package notify

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// execCommand is the command runner; overridable in tests.
var execCommand = exec.CommandContext

// frontmostGetter returns the bundle id of the frontmost application; overridable in tests.
var frontmostGetter = defaultFrontmostGetter

var (
	tnOnce      sync.Once
	tnAvailable bool
)

// Notify posts a desktop notification.
//   - title:   banner title (e.g. "Makro")
//   - subtitle: banner subtitle (e.g. session name)
//   - body:    main text; truncated to the first line / 200 runes
//   - activate: if non-empty and terminal-notifier is installed, clicking the
//     banner brings that bundle id to the foreground.
//
// Prefers terminal-notifier (clickable); falls back to osascript when absent.
func Notify(ctx context.Context, title, subtitle, body, activate string) {
	if err := send(ctx, title, subtitle, body, activate); err != nil {
		log.Printf("[notify] %v", err)
	}
}

func send(ctx context.Context, title, subtitle, body, activate string) error {
	tnOnce.Do(func() {
		_, err := exec.LookPath("terminal-notifier")
		tnAvailable = err == nil
		if !tnAvailable {
			log.Printf("[notify] terminal-notifier not found; falling back to osascript. " +
				"Install for clickable notifications: brew install terminal-notifier")
		}
	})

	body = truncateBody(body)

	if tnAvailable {
		args := terminalNotifierArgs(title, subtitle, body, activate)
		out, err := execCommand(ctx, "terminal-notifier", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("terminal-notifier: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	script := displayNotificationScript(title, subtitle, body)
	out, err := execCommand(ctx, "osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// terminalNotifierArgs builds the argv for `terminal-notifier`.
func terminalNotifierArgs(title, subtitle, body, activate string) []string {
	args := []string{"-title", title}
	if subtitle != "" {
		args = append(args, "-subtitle", subtitle)
	}
	args = append(args, "-message", body)
	if activate != "" {
		args = append(args, "-activate", activate)
	}
	return append(args, "-sound", "default")
}

// displayNotificationScript builds an AppleScript `display notification` line
// with proper string escaping.
func displayNotificationScript(title, subtitle, body string) string {
	return fmt.Sprintf("display notification %s with title %s subtitle %s",
		appleQuote(body), appleQuote(title), appleQuote(subtitle))
}

// appleQuote escapes a string for an AppleScript double-quoted literal.
func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// truncateBody keeps the first line, capped at maxBodyRunes runes.
func truncateBody(s string) string {
	const maxBodyRunes = 200
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > maxBodyRunes {
		return string(r[:maxBodyRunes]) + "…"
	}
	return s
}

// FrontmostBundleID returns the bundle id of the currently frontmost app.
func FrontmostBundleID(ctx context.Context) (string, error) {
	return frontmostGetter(ctx)
}

// IsFrontmost reports whether bundleID is the frontmost app.
// Returns false on any error or empty input (fail-open: when unsure, notify).
func IsFrontmost(ctx context.Context, bundleID string) bool {
	if bundleID == "" {
		return false
	}
	id, err := frontmostGetter(ctx)
	if err != nil {
		return false
	}
	return id == bundleID
}

func defaultFrontmostGetter(ctx context.Context) (string, error) {
	out, err := execCommand(ctx, "osascript", "-e",
		`tell application "System Events" to get bundle identifier of first application process whose frontmost is true`).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
