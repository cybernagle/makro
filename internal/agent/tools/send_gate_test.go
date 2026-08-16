package tools

import (
	"testing"

	"github.com/naglezhang/makro/internal/tmux"
	"github.com/stretchr/testify/require"
)

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
