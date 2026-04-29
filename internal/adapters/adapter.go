package adapters

import (
	"context"
	"fmt"
	"sync"
)

// AgentAdapter defines how to interact with a coding agent running in a tmux session.
type AgentAdapter interface {
	Name() string
	Launch(ctx context.Context, sessionName, workingDir string) error
	SendMessage(ctx context.Context, sessionName, text string) error
	IsRunning(output string) bool
	ParseOutput(output string) AgentOutput
	StopConfig() StopConfig
}

// AgentOutput holds parsed information from agent output.
type AgentOutput struct {
	Ready     bool
	Working   bool
	Completed bool
	Error     string
	LastLine  string
}

// StopConfig describes how to detect agent task completion.
type StopConfig struct {
	CompletionMarker string
	ErrorMarkers     []string
	StopCommand      string
}

// tmuxClient is the subset of tmux.TmuxClient that adapters need.
type tmuxClient interface {
	Exec(cmd string) (string, error)
}

// Registry manages available agent adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]AgentAdapter
}

// NewRegistry creates a registry and registers all built-in adapters.
func NewRegistry(tc tmuxClient) *Registry {
	r := &Registry{
		adapters: make(map[string]AgentAdapter),
	}
	r.Register(NewClaudeAdapter(tc))
	r.Register(NewCopilotAdapter(tc))
	return r
}

func (r *Registry) Register(a AgentAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Name()] = a
}

func (r *Registry) Get(name string) (AgentAdapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown adapter: %s", name)
	}
	return a, nil
}

func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}
