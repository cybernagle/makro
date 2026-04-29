package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderValid(t *testing.T) {
	p, err := NewProvider("anthropic", "test-key")
	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())

	p, err = NewProvider("openai", "test-key")
	require.NoError(t, err)
	assert.Equal(t, "openai", p.Name())
}

func TestNewProviderInvalid(t *testing.T) {
	_, err := NewProvider("invalid", "test-key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported provider")
}

func TestStreamEventTypeString(t *testing.T) {
	assert.Equal(t, "text_delta", EventTextDelta.String())
	assert.Equal(t, "tool_call_start", EventToolCallStart.String())
	assert.Equal(t, "done", EventDone.String())
	assert.Equal(t, "unknown", StreamEventType(99).String())
}

func TestMessageTypes(t *testing.T) {
	msg := Message{
		Role:    RoleUser,
		Content: "hello",
	}
	assert.Equal(t, "user", string(msg.Role))

	msg = Message{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{
			{ID: "tc_1", Name: "list_sessions", Arguments: "{}"},
		},
	}
	assert.Len(t, msg.ToolCalls, 1)

	msg = Message{
		Role: RoleTool,
		ToolResults: []ToolResult{
			{CallID: "tc_1", Content: "session1", IsError: false},
		},
	}
	assert.False(t, msg.ToolResults[0].IsError)
}

func TestAnthropicBuildParams(t *testing.T) {
	p := NewAnthropicProvider("test-key")
	params, err := p.buildParams([]Message{
		{Role: RoleUser, Content: "hello"},
	}, GenerateOptions{
		Model:        "claude-sonnet-4-6",
		MaxTokens:    1024,
		Temperature:  0.7,
		SystemPrompt: "You are helpful.",
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", string(params.Model))
	assert.Len(t, params.Messages, 1)
}

func TestAnthropicBuildParamsWithTools(t *testing.T) {
	p := NewAnthropicProvider("test-key")
	params, err := p.buildParams([]Message{
		{Role: RoleUser, Content: "list sessions"},
	}, GenerateOptions{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
		Tools: []ToolDefinition{
			{
				Name:        "list_sessions",
				Description: "List all tmux sessions",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"filter": map[string]any{"type": "string"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Len(t, params.Tools, 1)
}

func TestAnthropicBuildParamsWithToolResult(t *testing.T) {
	p := NewAnthropicProvider("test-key")
	params, err := p.buildParams([]Message{
		{Role: RoleUser, Content: "list sessions"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "tc_1", Name: "list_sessions", Arguments: "{}"},
		}},
		{Role: RoleTool, ToolResults: []ToolResult{
			{CallID: "tc_1", Content: "session1\nsession2"},
		}},
	}, GenerateOptions{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
	})
	require.NoError(t, err)
	assert.Len(t, params.Messages, 3)
}

func TestOpenAIBuildParams(t *testing.T) {
	p := NewOpenAIProvider("test-key")
	params, err := p.buildParams([]Message{
		{Role: RoleUser, Content: "hello"},
	}, GenerateOptions{
		Model:        "gpt-4o",
		MaxTokens:    1024,
		Temperature:  0.7,
		SystemPrompt: "You are helpful.",
	})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", string(params.Model))
}

func TestOpenAIBuildParamsWithTools(t *testing.T) {
	p := NewOpenAIProvider("test-key")
	params, err := p.buildParams([]Message{
		{Role: RoleUser, Content: "list sessions"},
	}, GenerateOptions{
		Model:     "gpt-4o",
		MaxTokens: 1024,
		Tools: []ToolDefinition{
			{
				Name:        "list_sessions",
				Description: "List all tmux sessions",
				Parameters: map[string]any{
					"type": "object",
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Len(t, params.Tools, 1)
}
