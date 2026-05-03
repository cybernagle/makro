package tools

import (
	"context"

	"github.com/naglezhang/fingersaver/internal/tmux"
)

// Tool represents a callable tool that the orchestrator can invoke.
type Tool struct {
	Name        string
	Description string
	Parameters  []Param
	Execute     func(ctx context.Context, args map[string]any) (string, error)
}

// Param describes a single tool parameter.
type Param struct {
	Name        string
	Type        string // "string", "number", "boolean"
	Description string
	Required    bool
}

// TmuxClient is the subset of tmux functionality that tools need.
type TmuxClient interface {
	Exec(cmd string) (string, error)
	State() *tmux.StateMirror
}

// Guardian is the interface for session guardian functionality.
// Implemented by agent.GuardianManager; defined here to avoid circular imports.
type Guardian interface {
	Watch(ctx context.Context, name string) error
	AutoWatch(ctx context.Context, name string)
	Stop(name string) error
	StopAll()
	ActiveGuardians() []string
}
