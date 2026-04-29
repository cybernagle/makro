package agent

import (
	"context"
	"testing"

	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProvider implements llm.Provider for testing.
type mockProvider struct {
	responses [][]llm.StreamEvent // responses to return per call
	callIndex int
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Stream(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 64)
	responseIdx := m.callIndex
	m.callIndex++

	go func() {
		defer close(ch)
		if responseIdx < len(m.responses) {
			for _, event := range m.responses[responseIdx] {
				ch <- event
			}
		}
		// Always send done.
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()

	return ch, nil
}

func TestOrchestratorSlashCommand(t *testing.T) {
	mc := newMockTmuxClient()
	mp := &mockProvider{}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, AllTools(mc))
	orch.SetCommandRegistry(NewCommandRegistry(mc))

	events, err := orch.ProcessInput(context.Background(), "/help")
	require.NoError(t, err)

	var texts []string
	for e := range events {
		if e.Type == EventText {
			texts = append(texts, e.Content)
		}
	}
	assert.True(t, len(texts) > 0)
	assert.Contains(t, texts[0], "create")
}

func TestOrchestratorMention(t *testing.T) {
	mc := newMockTmuxClient()
	mp := &mockProvider{}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, AllTools(mc))

	events, err := orch.ProcessInput(context.Background(), "@auth echo hello")
	require.NoError(t, err)

	var texts []string
	for e := range events {
		if e.Type == EventText {
			texts = append(texts, e.Content)
		}
	}
	assert.Contains(t, texts[0], "Sent to")
}

func TestOrchestratorLLMTextResponse(t *testing.T) {
	mc := newMockTmuxClient()
	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			{
				{Type: llm.EventTextDelta, Text: "Hello! "},
				{Type: llm.EventTextDelta, Text: "How can I help?"},
			},
		},
	}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, AllTools(mc))

	events, err := orch.ProcessInput(context.Background(), "hi there")
	require.NoError(t, err)

	var texts []string
	var done bool
	for e := range events {
		if e.Type == EventText {
			texts = append(texts, e.Content)
		}
		if e.Type == EventDone {
			done = true
		}
	}
	assert.True(t, done)
	assert.Contains(t, texts, "Hello! ")
	assert.Contains(t, texts, "How can I help?")
}

func TestOrchestratorLLMToolCall(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = "session1\nsession2"
	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			// First call: LLM calls list_sessions tool.
			{
				{Type: llm.EventToolCallStart, ToolCallID: "tc_1", ToolCallName: "list_sessions"},
				{Type: llm.EventToolCallDelta, ToolCallID: "tc_1", ArgumentsDelta: "{}"},
			},
			// Second call: LLM responds with text after seeing tool result.
			{
				{Type: llm.EventTextDelta, Text: "You have 2 sessions."},
			},
		},
	}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, AllTools(mc))

	events, err := orch.ProcessInput(context.Background(), "what sessions do I have?")
	require.NoError(t, err)

	var toolCalls []OrchestratorEvent
	var toolResults []OrchestratorEvent
	var texts []string
	var done bool

	for e := range events {
		switch e.Type {
		case EventToolCall:
			toolCalls = append(toolCalls, e)
		case EventToolResult:
			toolResults = append(toolResults, e)
		case EventText:
			texts = append(texts, e.Content)
		case EventDone:
			done = true
		}
	}

	assert.True(t, done)
	assert.Len(t, toolCalls, 1)
	assert.Equal(t, "list_sessions", toolCalls[0].ToolName)
	assert.Len(t, toolResults, 1)
	assert.Contains(t, toolResults[0].ToolResult, "session1")
	assert.Contains(t, texts[len(texts)-1], "2 sessions")
}

func TestOrchestratorMessagesAccumulate(t *testing.T) {
	mc := newMockTmuxClient()
	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			{{Type: llm.EventTextDelta, Text: "first response"}},
			{{Type: llm.EventTextDelta, Text: "second response"}},
		},
	}
	orch := NewOrchestrator(mp, mc, NewHookManager(), AllTools(mc))

	events1, _ := orch.ProcessInput(context.Background(), "message 1")
	for range events1 {
	}

	events2, _ := orch.ProcessInput(context.Background(), "message 2")
	for range events2 {
	}

	msgs := orch.Messages()
	assert.Len(t, msgs, 4) // 2 user + 2 assistant
	assert.Equal(t, llm.RoleUser, msgs[0].Role)
	assert.Equal(t, "message 1", msgs[0].Content)
	assert.Equal(t, llm.RoleAssistant, msgs[1].Role)
}
