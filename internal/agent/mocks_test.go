package agent

import (
	"sync"
)

// mockTmuxClient implements tools.TmuxClient for agent package tests.
type mockTmuxClient struct {
	mu       sync.Mutex
	executed []string
	results  map[string]string
	errors   map[string]error
	sessions map[string]bool // names reported as existing by HasSession
}

func newMockTmuxClient() *mockTmuxClient {
	return &mockTmuxClient{
		results:  make(map[string]string),
		errors:   make(map[string]error),
		sessions: make(map[string]bool),
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

func (m *mockTmuxClient) HasSession(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[name]
}

func (m *mockTmuxClient) lastCmd() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.executed) == 0 {
		return ""
	}
	return m.executed[len(m.executed)-1]
}
