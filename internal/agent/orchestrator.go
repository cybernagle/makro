package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/naglezhang/fingersaver/internal/llm"
)

type OrchestratorEventType int

const (
	EventText OrchestratorEventType = iota
	EventToolCall
	EventToolResult
	EventDone
)

func (t OrchestratorEventType) String() string {
	names := map[OrchestratorEventType]string{
		EventText:       "text",
		EventToolCall:   "tool_call",
		EventToolResult: "tool_result",
		EventDone:       "done",
	}
	if s, ok := names[t]; ok {
		return s
	}
	return "unknown"
}

type OrchestratorEvent struct {
	Type       OrchestratorEventType
	Content    string
	ToolName   string
	ToolArgs   map[string]any
	ToolResult string
}

const maxToolIterations = 10

type Orchestrator struct {
	provider     llm.Provider
	tc           TmuxClient
	tools        []Tool
	toolMap      map[string]Tool
	commands     *CommandRegistry
	hooks        *HookManager
	messages     []llm.Message
	systemPrompt string
}

func NewOrchestrator(provider llm.Provider, tc TmuxClient, hooks *HookManager, tools []Tool) *Orchestrator {
	toolMap := make(map[string]Tool, len(tools))
	for _, t := range tools {
		toolMap[t.Name] = t
	}

	if hooks == nil {
		hooks = NewHookManager()
	}

	return &Orchestrator{
		provider:     provider,
		tc:           tc,
		tools:        tools,
		toolMap:      toolMap,
		hooks:        hooks,
		systemPrompt: defaultSystemPrompt(),
	}
}

func (o *Orchestrator) SetSystemPrompt(prompt string) {
	o.systemPrompt = prompt
}

func (o *Orchestrator) SetCommandRegistry(cr *CommandRegistry) {
	o.commands = cr
}

func (o *Orchestrator) Messages() []llm.Message {
	return o.messages
}

func (o *Orchestrator) Hooks() *HookManager {
	return o.hooks
}

// CrossAgentRelay reads output from sourceSession, asks the LLM to summarize,
// and sends the summary to targetSession.
func (o *Orchestrator) CrossAgentRelay(ctx context.Context, sourceSession, targetSession, prompt string) error {
	// Read source session output.
	readTool, ok := o.toolMap["read_session_output"]
	if !ok {
		return fmt.Errorf("read_session_output tool not found")
	}
	output, err := readTool.Execute(ctx, map[string]any{"name": sourceSession})
	if err != nil {
		return fmt.Errorf("read %s: %w", sourceSession, err)
	}

	// Ask LLM to summarize/extract.
	relayPrompt := fmt.Sprintf("%s\n\nAgent output:\n%s", prompt, output)
	o.messages = append(o.messages, llm.Message{Role: llm.RoleUser, Content: relayPrompt})

	opts := o.buildOptions()
	opts.Tools = nil // No tool calls needed for relay.

	stream, err := o.provider.Stream(ctx, o.messages, opts)
	if err != nil {
		return fmt.Errorf("LLM relay: %w", err)
	}

	var summary strings.Builder
	for event := range stream {
		if event.Type == llm.EventTextDelta {
			summary.WriteString(event.Text)
		}
		if event.Type == llm.EventError {
			return fmt.Errorf("LLM stream error: %v", event.Err)
		}
	}

	// Send summary to target session.
	sendTool, ok := o.toolMap["send_to_session"]
	if !ok {
		return fmt.Errorf("send_to_session tool not found")
	}
	_, err = sendTool.Execute(ctx, map[string]any{
		"name":    targetSession,
		"message": summary.String(),
	})
	return err
}

func (o *Orchestrator) ProcessInput(ctx context.Context, input string) (<-chan OrchestratorEvent, error) {
	ch := make(chan OrchestratorEvent, 64)

	// Route: slash command.
	if strings.HasPrefix(strings.TrimSpace(input), "/") && o.commands != nil {
		go func() {
			defer close(ch)
			result, err := o.commands.Execute(ctx, input)
			if err != nil {
				ch <- OrchestratorEvent{Type: EventText, Content: fmt.Sprintf("Error: %v", err)}
			} else {
				ch <- OrchestratorEvent{Type: EventText, Content: result}
			}
			ch <- OrchestratorEvent{Type: EventDone}
		}()
		return ch, nil
	}

	// Route: @mention.
	sessionName, text := ExtractMention(input)
	if sessionName != "" {
		go func() {
			defer close(ch)
			o.handleMention(ctx, ch, sessionName, text)
		}()
		return ch, nil
	}

	// Route: natural language to LLM.
	go func() {
		defer close(ch)
		o.handleLLM(ctx, ch, input)
	}()

	return ch, nil
}

func (o *Orchestrator) handleMention(ctx context.Context, ch chan<- OrchestratorEvent, sessionName, text string) {
	tool := NewSendToSessionTool(o.tc)
	result, err := tool.Execute(ctx, map[string]any{
		"name":    sessionName,
		"message": text,
	})
	if err != nil {
		ch <- OrchestratorEvent{Type: EventText, Content: fmt.Sprintf("Error sending to @%s: %v", sessionName, err)}
	} else {
		ch <- OrchestratorEvent{Type: EventText, Content: result}
	}
	ch <- OrchestratorEvent{Type: EventDone}
}

func (o *Orchestrator) handleLLM(ctx context.Context, ch chan<- OrchestratorEvent, input string) {
	o.messages = append(o.messages, llm.Message{Role: llm.RoleUser, Content: input})

	opts := o.buildOptions()

	for i := 0; i < maxToolIterations; i++ {
		stream, err := o.provider.Stream(ctx, o.messages, opts)
		if err != nil {
			ch <- OrchestratorEvent{Type: EventText, Content: fmt.Sprintf("LLM error: %v", err)}
			ch <- OrchestratorEvent{Type: EventDone}
			return
		}

		var textParts strings.Builder
		var toolCalls []llm.ToolCall
		activeToolCalls := make(map[string]*llm.ToolCall)

		for event := range stream {
			switch event.Type {
			case llm.EventTextDelta:
				textParts.WriteString(event.Text)
				ch <- OrchestratorEvent{Type: EventText, Content: event.Text}

			case llm.EventToolCallStart:
				tc := &llm.ToolCall{ID: event.ToolCallID, Name: event.ToolCallName}
				activeToolCalls[event.ToolCallID] = tc

			case llm.EventToolCallDelta:
				if tc, ok := activeToolCalls[event.ToolCallID]; ok {
					tc.Arguments += event.ArgumentsDelta
				}

			case llm.EventDone:
				// Finalize active tool calls.
				for _, tc := range activeToolCalls {
					toolCalls = append(toolCalls, *tc)
				}

			case llm.EventError:
				ch <- OrchestratorEvent{Type: EventText, Content: fmt.Sprintf("Stream error: %v", event.Err)}
				ch <- OrchestratorEvent{Type: EventDone}
				return
			}
		}

		// Add assistant message to history.
		assistantMsg := llm.Message{Role: llm.RoleAssistant}
		if textParts.Len() > 0 {
			assistantMsg.Content = textParts.String()
		}
		assistantMsg.ToolCalls = toolCalls
		o.messages = append(o.messages, assistantMsg)

		// If no tool calls, we're done.
		if len(toolCalls) == 0 {
			ch <- OrchestratorEvent{Type: EventDone}
			return
		}

		// Execute tool calls and collect results.
		var toolResults []llm.ToolResult
		for _, tc := range toolCalls {
			ch <- OrchestratorEvent{Type: EventToolCall, ToolName: tc.Name, ToolArgs: parseJSONArgs(tc.Arguments)}

			result := o.executeTool(ctx, tc)
			toolResults = append(toolResults, result)

			ch <- OrchestratorEvent{Type: EventToolResult, ToolName: tc.Name, ToolResult: result.Content}
		}

		// Add tool results to history.
		o.messages = append(o.messages, llm.Message{Role: llm.RoleTool, ToolResults: toolResults})

		// Reset opts — tools and system prompt only sent on first call.
		opts = o.buildOptions()
		opts.Tools = nil // Subsequent calls don't need tool definitions re-sent.
	}

	ch <- OrchestratorEvent{Type: EventText, Content: "Max tool iterations reached."}
	ch <- OrchestratorEvent{Type: EventDone}
}

func (o *Orchestrator) executeTool(ctx context.Context, tc llm.ToolCall) llm.ToolResult {
	tool, exists := o.toolMap[tc.Name]
	if !exists {
		return llm.ToolResult{CallID: tc.ID, Content: fmt.Sprintf("Unknown tool: %s", tc.Name), IsError: true}
	}

	args := parseJSONArgs(tc.Arguments)

	// Fire before-tool-call hooks.
	result, err := o.hooks.Fire(ctx, HookBeforeToolCall, args)
	if err != nil {
		return llm.ToolResult{CallID: tc.ID, Content: fmt.Sprintf("Hook error: %v", err), IsError: true}
	}
	if btr, ok := result.(BeforeToolCallResult); ok && btr.Block {
		return llm.ToolResult{CallID: tc.ID, Content: fmt.Sprintf("Blocked: %s", btr.Reason), IsError: true}
	}

	output, err := tool.Execute(ctx, args)
	if err != nil {
		return llm.ToolResult{CallID: tc.ID, Content: err.Error(), IsError: true}
	}

	// Fire after-tool-call hooks — can modify result.
	afterResult, err := o.hooks.Fire(ctx, HookAfterToolCall, output)
	if err != nil {
		return llm.ToolResult{CallID: tc.ID, Content: fmt.Sprintf("After-hook error: %v", err), IsError: true}
	}
	if modified, ok := afterResult.(AfterToolCallResult); ok && modified.ModifiedResult != "" {
		output = modified.ModifiedResult
	}

	return llm.ToolResult{CallID: tc.ID, Content: output}
}

func (o *Orchestrator) buildOptions() llm.GenerateOptions {
	tools := make([]llm.ToolDefinition, len(o.tools))
	for i, t := range o.tools {
		params := map[string]any{"type": "object", "properties": map[string]any{}}
		required := []string{}
		for _, p := range t.Parameters {
			params["properties"].(map[string]any)[p.Name] = map[string]any{
				"type":        p.Type,
				"description": p.Description,
			}
			if p.Required {
				required = append(required, p.Name)
			}
		}
		if len(required) > 0 {
			params["required"] = required
		}

		tools[i] = llm.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		}
	}

	return llm.GenerateOptions{
		MaxTokens:    4096,
		Tools:        tools,
		SystemPrompt: o.systemPrompt,
	}
}

func parseJSONArgs(raw string) map[string]any {
	args := make(map[string]any)
	json.Unmarshal([]byte(raw), &args)
	return args
}

func defaultSystemPrompt() string {
	return `You are FingerSaver, a coding agent orchestrator. You manage multiple tmux sessions running coding agents like Claude Code and GitHub Copilot.

Available tools:
- list_sessions: List all tmux sessions
- create_session: Create a new session (args: name, working_dir)
- switch_session: Switch viewer to a session (args: name)
- kill_session: Kill a session (args: name)
- send_to_session: Send text to a session (args: name, message)
- read_session_output: Read a session's current output (args: name)

When the user refers to a session by name (e.g., "check the auth service"), use switch_session and read_session_output to understand the state. Be concise in your responses.`
}
