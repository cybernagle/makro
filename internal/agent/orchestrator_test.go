package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/naglezhang/makro/internal/agent/tools"
	"github.com/naglezhang/makro/internal/llm"
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
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil, "/tmp", nil))
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
	mc.results["has-session -t auth"] = ""
	mp := &mockProvider{}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil, "/tmp", nil))

	events, err := orch.ProcessInput(context.Background(), "@auth echo hello")
	require.NoError(t, err)

	var done bool
	for e := range events {
		if e.Type == EventDone {
			done = true
		}
	}
	assert.True(t, done, "should receive done event")
}

func TestOrchestratorMentionSessionNotFound(t *testing.T) {
	mc := newMockTmuxClient()
	mc.errors["has-session -t missing"] = fmt.Errorf("can't find session")
	mp := &mockProvider{}
	hm := NewHookManager()
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil, "/tmp", nil))

	events, err := orch.ProcessInput(context.Background(), "@missing echo hello")
	require.NoError(t, err)

	var texts []string
	for e := range events {
		if e.Type == EventText {
			texts = append(texts, e.Content)
		}
	}
	assert.Contains(t, texts[0], "not found")
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
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil, "/tmp", nil))

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
	orch := NewOrchestrator(mp, mc, hm, tools.AllTools(mc, nil, "/tmp", nil))

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
	orch := NewOrchestrator(mp, mc, NewHookManager(), tools.AllTools(mc, nil, "/tmp", nil))

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

	orch := NewOrchestrator(bp, mc, NewHookManager(), tools.AllTools(mc, nil, "/tmp", nil))

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

func newTestOrchestrator(maxContextMessages int) *Orchestrator {
	mc := newMockTmuxClient()
	mp := &mockProvider{}
	orch := NewOrchestrator(mp, mc, NewHookManager(), tools.AllTools(mc, nil, "/tmp", nil))
	orch.SetMaxContextMessages(maxContextMessages)
	return orch
}

func TestSnapshotMessagesTruncation(t *testing.T) {
	orch := newTestOrchestrator(4)
	for i := 0; i < 8; i++ {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		orch.appendMessage(llm.Message{Role: role, Content: fmt.Sprintf("msg %d", i)})
	}

	msgs := orch.snapshotMessages()
	assert.Len(t, msgs, 4)
	assert.Equal(t, llm.RoleUser, msgs[0].Role)
	assert.Equal(t, "msg 4", msgs[0].Content)
	assert.Equal(t, "msg 7", msgs[3].Content)

	// Full history preserved in o.messages via Messages()
	all := orch.Messages()
	assert.Len(t, all, 8)
}

func TestSnapshotMessagesNoTruncationWhenUnderLimit(t *testing.T) {
	orch := newTestOrchestrator(10)
	for i := 0; i < 3; i++ {
		orch.appendMessage(llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("msg %d", i)})
	}
	msgs := orch.snapshotMessages()
	assert.Len(t, msgs, 3)
}

func TestSnapshotMessagesTrimmedToUserBoundary(t *testing.T) {
	orch := newTestOrchestrator(3)
	// Build: user, assistant, assistant, user, assistant, user
	// If we keep last 3: assistant, user, assistant — first is not user, skip to user
	orch.appendMessage(llm.Message{Role: llm.RoleUser, Content: "u1"})
	orch.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "a1"})
	orch.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "a2"})
	orch.appendMessage(llm.Message{Role: llm.RoleUser, Content: "u2"})
	orch.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "a3"})
	orch.appendMessage(llm.Message{Role: llm.RoleUser, Content: "u3"})

	msgs := orch.snapshotMessages()
	assert.Equal(t, llm.RoleUser, msgs[0].Role)
	assert.Equal(t, "u2", msgs[0].Content)
}

func TestContextOverflowRecovery(t *testing.T) {
	mc := newMockTmuxClient()
	cp := &contextOverflowProvider{}
	orch := NewOrchestrator(cp, mc, NewHookManager(), tools.AllTools(mc, nil, "/tmp", nil))

	for i := 0; i < 6; i++ {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		orch.appendMessage(llm.Message{Role: role, Content: fmt.Sprintf("msg %d", i)})
	}
	initialCount := orch.messageCount()

	events, err := orch.ProcessInput(context.Background(), "test")
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
	assert.Contains(t, texts[len(texts)-1], "recovered")
	// After recovery: user msg added (+1), oldest turn trimmed (-2: user+assistant),
	// then assistant response added (+1). Net: 6 - 2 + 2 = 6 == initialCount+0, which is < initialCount+2.
	assert.Less(t, orch.messageCount(), initialCount+2)
}

func TestTrimOldestTurn(t *testing.T) {
	orch := newTestOrchestrator(0)

	orch.appendMessage(llm.Message{Role: llm.RoleUser, Content: "u1"})
	orch.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "a1", ToolCalls: []llm.ToolCall{{ID: "tc_1", Name: "test"}}})
	orch.appendMessage(llm.Message{Role: llm.RoleTool, ToolResults: []llm.ToolResult{{CallID: "tc_1", Content: "result"}}})
	orch.appendMessage(llm.Message{Role: llm.RoleUser, Content: "u2"})
	orch.appendMessage(llm.Message{Role: llm.RoleAssistant, Content: "a2"})

	orch.trimOldestTurn()

	msgs := orch.Messages()
	assert.Len(t, msgs, 2)
	assert.Equal(t, llm.RoleUser, msgs[0].Role)
	assert.Equal(t, "u2", msgs[0].Content)
	assert.Equal(t, "a2", msgs[1].Content)
}

// contextOverflowProvider returns context_length_exceeded on first call, then succeeds.
type contextOverflowProvider struct {
	called int
}

func (p *contextOverflowProvider) Name() string { return "overflow" }

func (p *contextOverflowProvider) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (*llm.CompleteResult, error) {
	p.called++
	if p.called == 1 {
		return nil, errors.New("request too large: context_length_exceeded: token count 210000 > max 200000")
	}
	return &llm.CompleteResult{Content: "recovered", StopReason: "end_turn"}, nil
}

func (p *contextOverflowProvider) Stream(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 1)
	ch <- llm.StreamEvent{Type: llm.EventDone}
	close(ch)
	return ch, nil
}

func TestContextOverflowUnrecoverable(t *testing.T) {
	mc := newMockTmuxClient()
	up := &unrecoverableOverflowProvider{}
	orch := NewOrchestrator(up, mc, NewHookManager(), tools.AllTools(mc, nil, "/tmp", nil))

	done := make(chan struct{})
	go func() {
		defer close(done)
		events, err := orch.ProcessInput(context.Background(), "huge message")
		assert.NoError(t, err)

		var texts []string
		var gotDone bool
		for e := range events {
			if e.Type == EventText {
				texts = append(texts, e.Content)
			}
			if e.Type == EventDone {
				gotDone = true
			}
		}
		assert.True(t, gotDone)
		if assert.NotEmpty(t, texts, "expected at least one text event") {
			assert.Contains(t, texts[len(texts)-1], "too long")
		}
	}()

	select {
	case <-done:
		// Test completed — no infinite loop.
	case <-time.After(5 * time.Second):
		t.Fatal("TestContextOverflowUnrecoverable timed out — possible infinite retry loop")
	}
}

type unrecoverableOverflowProvider struct{}

func (p *unrecoverableOverflowProvider) Name() string { return "unrecoverable" }

func (p *unrecoverableOverflowProvider) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (*llm.CompleteResult, error) {
	return nil, errors.New("context_length_exceeded: token count 500000 > max 200000")
}

func (p *unrecoverableOverflowProvider) Stream(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 1)
	ch <- llm.StreamEvent{Type: llm.EventDone}
	close(ch)
	return ch, nil
}
