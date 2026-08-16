package tools

import (
	"context"
	"testing"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/stretchr/testify/require"
)

// The bare-shell refusal must carry the sentinel so healing paths can match it.
func TestValidateSendTargetBareShellSentinel(t *testing.T) {
	m := newMockTmuxClient()
	m.results[tmux.PaneCurrentCommandCmd("x")] = "zsh"
	m.results[tmux.PanePIDCmd("x")] = "\n"
	m.results[tmux.CapturePaneRangeCmd("x", 5, 0)] = "❯"

	_, err := validateSendTarget(m, "x")
	require.ErrorIs(t, err, ErrBareShell)
}

// A bare shell must be refused even before the capture-based checks: the
// foreground check alone is decisive.
func TestPaneForegroundIsShell(t *testing.T) {
	m := newMockTmuxClient()
	m.results[tmux.PaneCurrentCommandCmd("a")] = "zsh"
	m.results[tmux.PaneCurrentCommandCmd("b")] = "/bin/bash"
	m.results[tmux.PaneCurrentCommandCmd("c")] = "node"
	m.results[tmux.PaneCurrentCommandCmd("d")] = "claude"

	require.True(t, paneForegroundIsShell(m, "a"))
	require.True(t, paneForegroundIsShell(m, "b"))
	require.False(t, paneForegroundIsShell(m, "c"))
	require.False(t, paneForegroundIsShell(m, "d"))
}

// The reboot bug: a recovered tmux session runs bare zsh whose starship prompt
// renders the same "❯" Claude Code's input box uses. The process scan says
// "no agent", but the pane-prompt fallback used to rescue the send anyway —
// task text then landed in the shell and echoed as garbage. A shell foreground
// must now refuse outright.
func TestValidateSendTargetRefusesBareShell(t *testing.T) {
	m := newMockTmuxClient()
	m.sessions["dev"] = true
	m.results[tmux.PaneCurrentCommandCmd("dev")] = "zsh"
	// Empty pane PID makes the process-tree scan fail closed
	// (machine-independent: no real tree walk can find an agent).
	m.results[tmux.PanePIDCmd("dev")] = "\n"
	// Pane ends at a starship-style prompt — identical glyph to the agent's.
	m.results[tmux.CapturePaneRangeCmd("dev", 5, 0)] = "~/project\n❯"

	_, err := validateSendTarget(m, "dev")
	require.Error(t, err)
	require.Contains(t, err.Error(), "bare shell")
}

// The fallback still rescues the case it was built for: an agent whose PID is
// invisible to ps (SIP, nvm/asdf wrappers) sits at its real input prompt. The
// foreground there is a wrapper like node — NOT a shell — so the send goes
// through.
func TestValidateSendTargetPanePromptFallbackStillRescues(t *testing.T) {
	m := newMockTmuxClient()
	m.sessions["dev"] = true
	m.results[tmux.PaneCurrentCommandCmd("dev")] = "node"
	m.results[tmux.PanePIDCmd("dev")] = "\n"
	m.results[tmux.CapturePaneRangeCmd("dev", 5, 0)] = "✳ Working\n❯"
	m.results[tmux.CapturePaneRangeCmd("dev", 30, 0)] = "✳ Working\n❯"

	_, err := validateSendTarget(m, "dev")
	require.NoError(t, err)
}

// A bare shell with a healer wired in self-heals: the healer relaunches the
// agent, WaitForAgent sees it, the gates re-validate, and the send proceeds —
// recovery happens inside the send, invisible to the caller.
func TestSendToSessionSelfHealsBareShell(t *testing.T) {
	mc := newMockTmuxClient()
	mc.sessions["dev"] = true
	mc.results[tmux.PaneCurrentCommandCmd("dev")] = "zsh"
	mc.results[tmux.PanePIDCmd("dev")] = "\n"
	mc.results[tmux.CapturePaneRangeCmd("dev", 5, 0)] = "~/project\n❯"

	healed := false
	healer := stubHealer{fn: func(session string) error {
		healed = true
		// The relaunch took effect: the pane now runs the agent.
		mc.mu.Lock()
		mc.results[tmux.PaneCurrentCommandCmd("dev")] = "claude"
		mc.results[tmux.CapturePaneRangeCmd("dev", 30, 0)] = "✻ Welcome to Claude Code!\n\n❯"
		mc.mu.Unlock()
		return nil
	}}

	tool := NewSendToSessionTool(mc, nil, healer)
	res, err := tool.Execute(context.Background(), map[string]any{
		"name": "dev", "message": "do the thing",
	})
	require.NoError(t, err)
	require.True(t, healed, "healer must have been invoked")
	require.Contains(t, res, "Sent to")
}

// Without a healer the bare-shell refusal still surfaces (nil healer = no
// magic; the caller decides — today only wired paths get auto-recovery).
func TestSendToSessionBareShellWithoutHealerStillRefused(t *testing.T) {
	mc := newMockTmuxClient()
	mc.sessions["dev"] = true
	mc.results[tmux.PaneCurrentCommandCmd("dev")] = "zsh"
	mc.results[tmux.PanePIDCmd("dev")] = "\n"
	mc.results[tmux.CapturePaneRangeCmd("dev", 5, 0)] = "~/project\n❯"

	tool := NewSendToSessionTool(mc, nil, nil)
	_, err := tool.Execute(context.Background(), map[string]any{
		"name": "dev", "message": "do the thing",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBareShell)
}

// stubHealer adapts a function to SessionHealer for tests.
type stubHealer struct {
	fn func(session string) error
}

func (h stubHealer) HealSession(session string) error { return h.fn(session) }
