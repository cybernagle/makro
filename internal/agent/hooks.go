package agent

import (
	"context"
	"fmt"
	"sync"
)

type HookType int

const (
	HookBeforeToolCall HookType = iota
	HookAfterToolCall
	HookAgentStop
	HookPermissionRequest
)

func (t HookType) String() string {
	names := map[HookType]string{
		HookBeforeToolCall:    "before_tool_call",
		HookAfterToolCall:     "after_tool_call",
		HookAgentStop:         "agent_stop",
		HookPermissionRequest: "permission_request",
	}
	if s, ok := names[t]; ok {
		return s
	}
	return "unknown"
}

type BeforeToolCallResult struct {
	Block  bool
	Reason string
}

type AfterToolCallResult struct {
	ModifiedResult string
}

type Hook struct {
	Type    HookType
	Name    string
	Handler func(ctx context.Context, payload any) (any, error)
}

type HookManager struct {
	mu    sync.RWMutex
	hooks map[HookType][]Hook
}

func NewHookManager() *HookManager {
	return &HookManager{
		hooks: make(map[HookType][]Hook),
	}
}

func (hm *HookManager) Register(hook Hook) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.hooks[hook.Type] = append(hm.hooks[hook.Type], hook)
}

func (hm *HookManager) Fire(ctx context.Context, hookType HookType, payload any) (any, error) {
	hm.mu.RLock()
	hooks := make([]Hook, len(hm.hooks[hookType]))
	copy(hooks, hm.hooks[hookType])
	hm.mu.RUnlock()

	for _, hook := range hooks {
		result, err := hook.Handler(ctx, payload)
		if err != nil {
			return nil, fmt.Errorf("hook %q: %w", hook.Name, err)
		}

		// For before-tool-call hooks, check if execution should be blocked.
		if hookType == HookBeforeToolCall {
			if btr, ok := result.(BeforeToolCallResult); ok && btr.Block {
				return btr, nil
			}
		}

		// Pass modified result to next hook.
		if result != nil {
			payload = result
		}
	}

	return payload, nil
}

func (hm *HookManager) HooksByType(hookType HookType) []Hook {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	result := make([]Hook, len(hm.hooks[hookType]))
	copy(result, hm.hooks[hookType])
	return result
}
