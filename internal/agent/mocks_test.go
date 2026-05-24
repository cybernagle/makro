package agent

import (
	"sync"

	"github.com/naglezhang/makro/internal/tmux"
)

// mockTmuxClient implements tools.TmuxClient for agent package tests.
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
