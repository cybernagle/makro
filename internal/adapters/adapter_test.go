package adapters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTC records commands executed via Exec.
type mockTC struct {
	cmds    []string
	results map[string]string
}

func (m *mockTC) Exec(cmd string) (string, error) {
	m.cmds = append(m.cmds, cmd)
	if r, ok := m.results[cmd]; ok {
		return r, nil
	}
	return "", nil
}

func TestClaudeAdapterName(t *testing.T) {
	a := NewClaudeAdapter(&mockTC{})
	assert.Equal(t, "claude", a.Name())
}

func TestClaudeAdapterLaunch(t *testing.T) {
	mc := &mockTC{}
	a := NewClaudeAdapter(mc)

	err := a.Launch(context.Background(), "test-session", "/tmp")
	require.NoError(t, err)
	assert.Len(t, mc.cmds, 2)
	assert.Contains(t, mc.cmds[0], "claude")
	assert.Contains(t, mc.cmds[0], "test-session")
}

func TestClaudeAdapterSendMessage(t *testing.T) {
	mc := &mockTC{}
	a := NewClaudeAdapter(mc)

	err := a.SendMessage(context.Background(), "test-session", "hello world")
	require.NoError(t, err)
	assert.Len(t, mc.cmds, 2)
	assert.Contains(t, mc.cmds[0], "hello world")
}

func TestClaudeAdapterParseReady(t *testing.T) {
	a := NewClaudeAdapter(&mockTC{})
	out := a.ParseOutput("Some output\n> ")
	assert.True(t, out.Ready)
	assert.False(t, out.Working)
}

func TestClaudeAdapterParseWorking(t *testing.T) {
	a := NewClaudeAdapter(&mockTC{})
	out := a.ParseOutput("Thinking about this...\nReading file.go")
	assert.True(t, out.Working)
}

func TestClaudeAdapterParseCompleted(t *testing.T) {
	a := NewClaudeAdapter(&mockTC{})
	out := a.ParseOutput("Task complete! Modified 3 files.")
	assert.True(t, out.Completed)
}

func TestClaudeAdapterParseError(t *testing.T) {
	a := NewClaudeAdapter(&mockTC{})
	out := a.ParseOutput("Error: file not found")
	assert.Equal(t, "Error", out.Error)
}

func TestClaudeAdapterIsRunning(t *testing.T) {
	a := NewClaudeAdapter(&mockTC{})
	assert.False(t, a.IsRunning(""))
	assert.False(t, a.IsRunning("output\n> "))
	assert.True(t, a.IsRunning("working..."))
}

func TestClaudeAdapterStopConfig(t *testing.T) {
	a := NewClaudeAdapter(&mockTC{})
	cfg := a.StopConfig()
	assert.Equal(t, "Task complete", cfg.CompletionMarker)
	assert.Equal(t, "/stop", cfg.StopCommand)
}

func TestCopilotAdapterName(t *testing.T) {
	a := NewCopilotAdapter(&mockTC{})
	assert.Equal(t, "copilot", a.Name())
}

func TestCopilotAdapterLaunch(t *testing.T) {
	mc := &mockTC{}
	a := NewCopilotAdapter(mc)

	err := a.Launch(context.Background(), "test-session", "/tmp")
	require.NoError(t, err)
	assert.Len(t, mc.cmds, 2)
	assert.Contains(t, mc.cmds[0], "github-copilot-cli")
}

func TestCopilotAdapterSendMessage(t *testing.T) {
	mc := &mockTC{}
	a := NewCopilotAdapter(mc)

	err := a.SendMessage(context.Background(), "test-session", "fix this bug")
	require.NoError(t, err)
	assert.Len(t, mc.cmds, 2)
	assert.Contains(t, mc.cmds[0], "fix this bug")
}

func TestCopilotAdapterParseReady(t *testing.T) {
	a := NewCopilotAdapter(&mockTC{})
	out := a.ParseOutput("Some output\ncopilot> ")
	assert.True(t, out.Ready)
}

func TestCopilotAdapterParseCompleted(t *testing.T) {
	a := NewCopilotAdapter(&mockTC{})
	out := a.ParseOutput("Suggestion: use X instead")
	assert.True(t, out.Completed)
}

func TestCopilotAdapterStopConfig(t *testing.T) {
	a := NewCopilotAdapter(&mockTC{})
	cfg := a.StopConfig()
	assert.Equal(t, "Suggestion:", cfg.CompletionMarker)
	assert.Equal(t, "exit", cfg.StopCommand)
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry(&mockTC{})

	claude, err := r.Get("claude")
	require.NoError(t, err)
	assert.Equal(t, "claude", claude.Name())

	copilot, err := r.Get("copilot")
	require.NoError(t, err)
	assert.Equal(t, "copilot", copilot.Name())
}

func TestRegistryUnknown(t *testing.T) {
	r := NewRegistry(&mockTC{})
	_, err := r.Get("unknown")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown adapter")
}

func TestRegistryList(t *testing.T) {
	r := NewRegistry(&mockTC{})
	names := r.List()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "claude")
	assert.Contains(t, names, "copilot")
}
