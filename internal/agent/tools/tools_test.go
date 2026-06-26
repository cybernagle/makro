package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/naglezhang/makro/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTmuxClient implements TmuxClient for testing.
type mockTmuxClient struct {
	mu       sync.Mutex
	executed []string
	results  map[string]string
	errors   map[string]error
	state    *tmux.StateMirror
}

func newMockTmuxClient() *mockTmuxClient {
	sm := tmux.NewStateMirror()
	return &mockTmuxClient{
		results: make(map[string]string),
		errors:  make(map[string]error),
		state:   sm,
	}
}

func (m *mockTmuxClient) Exec(cmd string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executed = append(m.executed, cmd)
	if err, ok := m.errors[cmd]; ok {
		return "", err
	}
	if result, ok := m.results[cmd]; ok {
		return result, nil
	}
	return "", nil
}

func (m *mockTmuxClient) State() *tmux.StateMirror {
	return m.state
}

func (m *mockTmuxClient) lastCmd() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.executed) == 0 {
		return ""
	}
	return m.executed[len(m.executed)-1]
}

// executedCmds returns a copy of every tmux command the client ran, for
// asserting that a code path did (or did not) emit a given command.
func (m *mockTmuxClient) executedCmds() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.executed))
	copy(out, m.executed)
	return out
}

func TestListSessionsTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = "session1\nsession2"

	tool := NewListSessionsTool(mc)
	result, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "session1\nsession2", result)
}

func TestListSessionsEmpty(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = ""

	tool := NewListSessionsTool(mc)
	result, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "No sessions found.", result)
}

func TestCreateSessionTool(t *testing.T) {
	mc := newMockTmuxClient()

	tool := NewCreateSessionTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name":        "test-session",
		"working_dir": "/tmp",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "test-session")
	assert.Contains(t, mc.lastCmd(), "new-session")
}

func TestCreateSessionMissingName(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewCreateSessionTool(mc)
	_, err := tool.Execute(context.Background(), map[string]any{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestSwitchSessionTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.state.Apply(tmux.Notification{Type: tmux.NotifSessionChanged, SessionID: "$0", SessionName: "target"})

	tool := NewSwitchSessionTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "target",
	})
	require.NoError(t, err)
	assert.Equal(t, "target", result)
}

func TestSwitchSessionNotFound(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewSwitchSessionTool(mc)
	_, err := tool.Execute(context.Background(), map[string]any{
		"name": "nonexistent",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSendToSessionTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", "target")] = "claude"

	tool := NewSendToSessionTool(mc, nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name":    "target",
		"message": "echo hello",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "Sent to")
}

// TestSendToSessionRefusesBareShell reproduces the bug where send_to_session
// dumped natural-language text into a freshly-created session whose pane was
// still a bare shell (no coding agent running). The tool must refuse AND must
// not emit any send-keys — otherwise the raw text lands in the shell.
func TestSendToSessionRefusesBareShell(t *testing.T) {
	mc := newMockTmuxClient()
	// Foreground process is a shell: agent never started, or has exited.
	mc.results[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", "fresh")] = "zsh"

	tool := NewSendToSessionTool(mc, nil)
	_, err := tool.Execute(context.Background(), map[string]any{
		"name":    "fresh",
		"message": "please refactor the auth module and add tests",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no coding agent")

	// The decisive assertion: nothing was sent to the pane, so the shell never
	// receives the natural-language text.
	for _, c := range mc.executedCmds() {
		assert.NotContains(t, c, "send-keys", "send-keys must not fire when no agent is alive")
	}
}

// TestSendToSessionNotFound verifies the session-missing case is still refused
// (checkAgentAlive surfaces it via the pane-lookup error).
func TestSendToSessionNotFound(t *testing.T) {
	mc := newMockTmuxClient()
	mc.errors[fmt.Sprintf("list-panes -t %s -F #{pane_current_command}", "ghost")] = fmt.Errorf("can't find session: ghost")

	tool := NewSendToSessionTool(mc, nil)
	_, err := tool.Execute(context.Background(), map[string]any{
		"name":    "ghost",
		"message": "hi",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestSendToSessionReadyGate covers the readiness check that prevents sending a
// task while claude is showing a Yes/No selection (startup trust dialog or
// in-turn permission) — the screen where task text would otherwise be eaten as
// an answer instead of becoming a prompt.
func TestSendToSessionReadyGate(t *testing.T) {
	tests := []struct {
		name    string
		paneCmd string // #{pane_current_command}: the agent process
		pane    string // captured pane tail (empty = capture returns nothing)
		wantErr bool
		wantSub string
	}{
		{
			name:    "ready_bare_input_prompt",
			paneCmd: "claude",
			pane:    "✻ Welcome to Claude Code!\n\n❯ ",
			wantErr: false,
			wantSub: "Sent to",
		},
		{
			name:    "asking_numbered_permission_prompt",
			paneCmd: "claude",
			pane:    "Do you want to proceed?\n❯ 1. Yes\n❯ 2. No",
			wantErr: true,
			wantSub: "Yes/No",
		},
		{
			name:    "asking_startup_trust_dialog",
			paneCmd: "claude",
			pane:    "  Yes, I trust the files in this folder and want to proceed\n❯ No, I will leave and come back later",
			wantErr: true,
			wantSub: "Yes/No",
		},
		{
			name:    "pane_unreadable_falls_back_to_send",
			paneCmd: "claude",
			pane:    "",
			wantErr: false,
			wantSub: "Sent to",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockTmuxClient()
			mc.results[tmux.PaneCurrentCommandCmd("s")] = tt.paneCmd
			if tt.pane != "" {
				mc.results[tmux.CapturePaneRangeCmd("s", 40, 0)] = tt.pane
			}
			tool := NewSendToSessionTool(mc, nil)
			res, err := tool.Execute(context.Background(), map[string]any{
				"name": "s", "message": "do the thing",
			})
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantSub)
				// And nothing was sent into the dialog.
				for _, c := range mc.executedCmds() {
					assert.NotContains(t, c, "send-keys", "send-keys must not fire while a Yes/No selection is shown")
				}
				return
			}
			require.NoError(t, err)
			assert.Contains(t, res, tt.wantSub)
		})
	}
}

// TestSendTextShortIsAtomic locks in the race fix: a short message must type its
// text AND press Enter in a single tmux invocation, not two. Two calls can drop
// the Enter and leave the text sitting in the agent's input box unsubmitted.
func TestSendTextShortIsAtomic(t *testing.T) {
	mc := newMockTmuxClient()
	require.NoError(t, sendText(mc, "s", "hi"))
	cmds := mc.executedCmds()
	require.Len(t, cmds, 1, "short text + Enter must be one atomic tmux call, not two")
	assert.Contains(t, cmds[0], "hi")
	assert.Contains(t, cmds[0], "Enter")
	assert.Contains(t, cmds[0], ";")
}

func TestConfirmAgentStarted(t *testing.T) {
	n := newMockNotifier()
	before := n.Snapshot("s")
	go func() {
		time.Sleep(20 * time.Millisecond)
		n.Notify("agent_start")
	}()
	assert.True(t, confirmAgentStarted(context.Background(), n, "s", before, time.Second))
}

func TestConfirmAgentStartedTimeout(t *testing.T) {
	n := newMockNotifier()
	before := n.Snapshot("s")
	assert.False(t, confirmAgentStarted(context.Background(), n, "s", before, 50*time.Millisecond))
}

// TestSendToSessionConfirmsSubmitted: a normal send where the agent starts the
// turn (UserPromptSubmit hook fires) returns success.
func TestSendToSessionConfirmsSubmitted(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[tmux.PaneCurrentCommandCmd("s")] = "claude"
	mc.results[tmux.CapturePaneRangeCmd("s", 40, 0)] = "✻ Welcome to Claude Code!\n\n❯ "
	n := newMockNotifier()
	go func() {
		time.Sleep(20 * time.Millisecond)
		n.Notify("agent_start")
	}()
	tool := NewSendToSessionTool(mc, n)
	res, err := tool.Execute(context.Background(), map[string]any{
		"name": "s", "message": "do the thing",
	})
	require.NoError(t, err)
	assert.Contains(t, res, "Sent to")
}

// TestSendToSessionDetectsUnsubmittedHang: text is delivered but the agent never
// starts a turn (Enter lost / prompt not submitted). The tool must fail fast
// instead of letting the caller hang on wait_until_idle for the full timeout.
func TestSendToSessionDetectsUnsubmittedHang(t *testing.T) {
	orig := submitConfirmGrace
	submitConfirmGrace = 60 * time.Millisecond
	defer func() { submitConfirmGrace = orig }()

	mc := newMockTmuxClient()
	mc.results[tmux.PaneCurrentCommandCmd("s")] = "claude"
	mc.results[tmux.CapturePaneRangeCmd("s", 40, 0)] = "✻ Welcome to Claude Code!\n\n❯ "
	n := newMockNotifier() // never fires agent_start

	tool := NewSendToSessionTool(mc, n)
	_, err := tool.Execute(context.Background(), map[string]any{
		"name": "s", "message": "do the thing",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not start")
}

func TestSendToSessionMissingArgs(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewSendToSessionTool(mc, nil)
	_, err := tool.Execute(context.Background(), map[string]any{"name": "x"})
	assert.Error(t, err)
}

func TestReadSessionOutputTool(t *testing.T) {
	mc := newMockTmuxClient()
	allCmd := fmt.Sprintf("capture-pane -t %s -p -S -", "reader")
	rangeCmd := fmt.Sprintf("capture-pane -t %s -p -S -%d -E -%d", "reader", 501, 0)
	mc.results[allCmd] = "line1\nline2\nline3"
	mc.results[rangeCmd] = "line1\nline2\nline3"

	tool := NewReadSessionOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "reader",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "line1")
	assert.Contains(t, result, `"total_lines":3`)
	assert.Contains(t, result, `"has_more":false`)
}

func TestReadSessionOutputPaging(t *testing.T) {
	mc := newMockTmuxClient()
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	allOutput := strings.Join(lines, "\n")
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "pager")] = allOutput
	// Range command for page 1 (lines=3, offset=0): capture 4 lines from end.
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -%d -E -%d", "pager", 4, 0)] = allOutput

	tool := NewReadSessionOutputTool(mc)

	// Page 1: last 3 lines (offset=0, lines=3)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name":   "pager",
		"lines":  float64(3),
		"offset": float64(0),
	})
	require.NoError(t, err)
	assert.Contains(t, result, "line 10")
	assert.Contains(t, result, `"total_lines":10`)
	assert.Contains(t, result, `"has_more":true`)

	// Page 2: lines 4-6 from end (offset=3, lines=3)
	result, err = tool.Execute(context.Background(), map[string]any{
		"name":   "pager",
		"lines":  float64(3),
		"offset": float64(3),
	})
	require.NoError(t, err)
	assert.Contains(t, result, "line 7")
	assert.Contains(t, result, `"has_more":true`)
}

func TestReadSessionOutputEmpty(t *testing.T) {
	mc := newMockTmuxClient()
	tool := NewReadSessionOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{
		"name": "empty",
	})
	require.NoError(t, err)
	assert.Contains(t, result, `"total_lines":0`)
	assert.Contains(t, result, `"has_more":false`)
}

func TestAllToolsCount(t *testing.T) {
	mc := newMockTmuxClient()
	ts := AllTools(mc, nil, "/tmp", nil)
	assert.Len(t, ts, 17)
}

func TestReadStructuredOutputTool(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results[fmt.Sprintf("capture-pane -t %s -p -S -", "dev")] = "⏺ I'll fix the auth bug.\n⏺ Read file: internal/auth/handler.go\n> check status\n⏺ All tests pass."

	tool := NewReadStructuredOutputTool(mc)
	result, err := tool.Execute(context.Background(), map[string]any{"name": "dev"})
	require.NoError(t, err)
	assert.Contains(t, result, `"status"`)
	assert.Contains(t, result, `"thinking"`)
	assert.Contains(t, result, `internal/auth/handler.go`)
	assert.Contains(t, result, `check status`)
}

func TestParseStructuredOutputWaitingInput(t *testing.T) {
	raw := "⏺ Do you want to proceed with this change?\n❯ 1. Yes\n  2. No"
	out := parseStructuredOutput(raw)
	assert.Equal(t, "waiting_input", out.Status)
}

func TestParseStructuredOutputError(t *testing.T) {
	raw := "⏺ Running… go test ./...\nFAIL: TestAuth\nError: test failed"
	out := parseStructuredOutput(raw)
	assert.True(t, len(out.Errors) > 0)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", util.Truncate("short", 10))
	assert.Equal(t, "a very long string i...", util.Truncate("a very long string indeed", 20))
}
