package agent

import (
	"context"
	"testing"
	"time"

	"github.com/naglezhang/fingersaver/internal/agent/tools"
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

func (m *mockProvider) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (*llm.CompleteResult, error) {
	responseIdx := m.callIndex
	m.callIndex++

	if responseIdx >= len(m.responses) {
		return &llm.CompleteResult{StopReason: "end_turn"}, nil
	}

	var result llm.CompleteResult
	var currentTC *llm.ToolCall
	for _, ev := range m.responses[responseIdx] {
		switch ev.Type {
		case llm.EventTextDelta:
			result.Content += ev.Text
		case llm.EventToolCallStart:
			if currentTC != nil {
				result.ToolCalls = append(result.ToolCalls, *currentTC)
			}
			currentTC = &llm.ToolCall{ID: ev.ToolCallID, Name: ev.ToolCallName}
		case llm.EventToolCallDelta:
			if currentTC != nil {
				currentTC.Arguments += ev.ArgumentsDelta
			}
		}
	}
	if currentTC != nil {
		result.ToolCalls = append(result.ToolCalls, *currentTC)
	}
	result.StopReason = "end_turn"
	return &result, nil
}

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
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()

	return ch, nil
}

func TestOrchestratorSlashCommand(t *testing.T) {
	mc := newMockTmuxClient()
	mp := &mockProvider{}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil))
	orch.SetCommandRegistry(NewCommandRegistry(mc, nil))

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
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil))

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
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil))

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
	assert.Equal(t, []string{"Hello! How can I help?"}, texts)
}

func TestOrchestratorLLMToolCall(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = "session1\nsession2"
	mp := &mockProvider{
		responses: [][]llm.StreamEvent{
			{
				{Type: llm.EventToolCallStart, ToolCallID: "tc_1", ToolCallName: "list_sessions"},
				{Type: llm.EventToolCallDelta, ToolCallID: "tc_1", ArgumentsDelta: "{}"},
			},
			{
				{Type: llm.EventTextDelta, Text: "You have 2 sessions."},
			},
		},
	}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil))

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
	orch := NewOrchestrator(mp, mc, NewHookManager(), tools.AllTools(mc, nil))

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

func TestOrchestratorCancel(t *testing.T) {
	mc := newMockTmuxClient()
	mc.results["list-sessions"] = "session1"

	// blockingProvider blocks on the second Complete call until cancelled.
	bp := &blockingProvider{
		firstResult: &llm.CompleteResult{
			ToolCalls:  []llm.ToolCall{{ID: "tc_1", Name: "list_sessions", Arguments: "{}"}},
			StopReason: "tool_use",
		},
	}

	orch := NewOrchestrator(bp, mc, NewHookManager(), tools.AllTools(mc, nil))

	events, err := orch.ProcessInput(context.Background(), "list sessions")
	require.NoError(t, err)

	// Cancel after a short delay (the second LLM call blocks, so cancel will interrupt it).
	go func() {
		time.Sleep(50 * time.Millisecond)
		orch.Cancel()
	}()

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
	assert.Contains(t, texts[len(texts)-1], "LLM error")
}

// blockingProvider blocks on the second Complete call to test cancellation.
type blockingProvider struct {
	firstResult *llm.CompleteResult
	called      int32
}

func (m *blockingProvider) Name() string { return "blocking" }

func (m *blockingProvider) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (*llm.CompleteResult, error) {
	if m.firstResult != nil {
		r := m.firstResult
		m.firstResult = nil
		return r, nil
	}
	// Block until context is cancelled.
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingProvider) Stream(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent)
	close(ch)
	return ch, nil
}
