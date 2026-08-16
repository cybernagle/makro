package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotHadAgent(t *testing.T) {
	require.True(t, snapshotHadAgent(SessionSnapshot{Command: "claude"}))
	require.True(t, snapshotHadAgent(SessionSnapshot{Command: "claude.exe"}))
	require.True(t, snapshotHadAgent(SessionSnapshot{ClaudeSession: "abc"}))
	require.False(t, snapshotHadAgent(SessionSnapshot{Command: "zsh"}))
	require.False(t, snapshotHadAgent(SessionSnapshot{Command: "node"}))
}

func TestAgentLaunchCommand(t *testing.T) {
	cases := []struct {
		name string
		ss   SessionSnapshot
		want string
	}{
		{"resume", SessionSnapshot{Command: "claude.exe", ClaudeSession: "sess-1", ClaudeCwd: "/Users/x/proj"},
			`cd "/Users/x/proj" && claude --resume sess-1`},
		{"fresh-in-claude-cwd", SessionSnapshot{Command: "claude.exe", ClaudeCwd: "/Users/x/proj"},
			`cd "/Users/x/proj" && claude`},
		{"fresh-in-workdir", SessionSnapshot{Command: "claude", WorkDir: "/Users/x/other"},
			`cd "/Users/x/other" && claude`},
		{"fresh-no-dirs", SessionSnapshot{Command: "claude"}, "claude"},
		{"no-agent", SessionSnapshot{Command: "zsh"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, agentLaunchCommand(c.ss))
		})
	}
}

func TestIsShellProcessName(t *testing.T) {
	require.True(t, isShellProcessName("zsh"))
	require.True(t, isShellProcessName("/bin/bash"))
	require.False(t, isShellProcessName("claude"))
	require.False(t, isShellProcessName("node"))
	require.False(t, isShellProcessName(""))
}

func TestIsClaudeCommand(t *testing.T) {
	require.True(t, isClaudeCommand("claude"))
	require.True(t, isClaudeCommand("claude.exe")) // this machine reports the agent this way
	require.True(t, isClaudeCommand("node /Users/x/.nvm/versions/node/v22/bin/claude"))
	require.False(t, isClaudeCommand("zsh"))
	require.False(t, isClaudeCommand("/bin/bash"))
	require.False(t, isClaudeCommand("claude-code-wrapper"))
}
