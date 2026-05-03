package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/naglezhang/fingersaver/internal/agent/skills"
	"github.com/naglezhang/fingersaver/internal/agent/tools"
	"github.com/naglezhang/fingersaver/internal/llm"
	"github.com/naglezhang/fingersaver/internal/util"
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

type Orchestrator struct {
	provider     llm.Provider
	tc           tools.TmuxClient
	toolList     []tools.Tool
	toolMap      map[string]tools.Tool
	commands     *CommandRegistry
	hooks        *HookManager
	msgMu        sync.Mutex
	messages     []llm.Message
	systemPrompt string
	model        string
	callTimeout  time.Duration
	cancelMu     sync.Mutex
	cancelFn     context.CancelFunc
	skillMu      sync.Mutex
	activeSkill  *skills.Skill
}

func NewOrchestrator(provider llm.Provider, tc tools.TmuxClient, hooks *HookManager, toolList []tools.Tool) *Orchestrator {
	toolMap := make(map[string]tools.Tool, len(toolList))
	for _, t := range toolList {
		toolMap[t.Name] = t
	}

	if hooks == nil {
		hooks = NewHookManager()
	}

	return &Orchestrator{
		provider:    provider,
		tc:          tc,
		toolList:    toolList,
		toolMap:     toolMap,
		hooks:       hooks,
		callTimeout: 60 * time.Second,
	}
}

func (o *Orchestrator) SetSystemPrompt(prompt string) {
	o.systemPrompt = prompt
}

func (o *Orchestrator) SetModel(model string) {
	o.model = model
}

func (o *Orchestrator) SetCallTimeout(d time.Duration) {
	o.callTimeout = d
}

func (o *Orchestrator) LoadSkills(dirs []string) error {
	all, err := skills.LoadAll(dirs)
	if err != nil {
		return err
	}
	for name, skill := range all {
		s := skill
		o.commands.Register(&SlashCommand{
			Name:        name,
			Usage:       "/" + name,
			Description: s.Description,
			Skill:       s,
		})
	}
	return nil
}

func (o *Orchestrator) SetCommandRegistry(cr *CommandRegistry) {
	o.commands = cr
}

func (o *Orchestrator) Commands() []*SlashCommand {
	if o.commands == nil {
		return nil
	}
	return o.commands.List()
}

func (o *Orchestrator) Messages() []llm.Message {
	return o.snapshotMessages()
}

func (o *Orchestrator) Hooks() *HookManager {
	return o.hooks
}

// Cancel aborts the current in-progress LLM/tool call chain.
func (o *Orchestrator) Cancel() {
	o.cancelMu.Lock()
	defer o.cancelMu.Unlock()
	if o.cancelFn != nil {
		o.cancelFn()
	}
}

func (o *Orchestrator) appendMessage(msg llm.Message) {
	o.msgMu.Lock()
	o.messages = append(o.messages, msg)
	o.msgMu.Unlock()
}

func (o *Orchestrator) snapshotMessages() []llm.Message {
	o.msgMu.Lock()
	defer o.msgMu.Unlock()
	cp := make([]llm.Message, len(o.messages))
	copy(cp, o.messages)
	return cp
}

func (o *Orchestrator) setActiveSkill(s *skills.Skill) {
	o.skillMu.Lock()
	o.activeSkill = s
	o.skillMu.Unlock()
}

func (o *Orchestrator) getActiveSkill() *skills.Skill {
	o.skillMu.Lock()
	defer o.skillMu.Unlock()
	return o.activeSkill
}

// CrossAgentRelay reads output from sourceSession, asks the LLM to summarize,
// and sends the summary to targetSession.
func (o *Orchestrator) CrossAgentRelay(ctx context.Context, sourceSession, targetSession, prompt string) error {
	readTool, ok := o.toolMap["read_session_output"]
	if !ok {
		return fmt.Errorf("read_session_output tool not found")
	}
	output, err := readTool.Execute(ctx, map[string]any{"name": sourceSession})
	if err != nil {
		return fmt.Errorf("read %s: %w", sourceSession, err)
	}

	relayPrompt := fmt.Sprintf("%s\n\nAgent output:\n%s", prompt, output)
	o.appendMessage(llm.Message{Role: llm.RoleUser, Content: relayPrompt})

	opts := o.buildOptions()
	opts.Tools = nil

	stream, err := o.provider.Stream(ctx, o.snapshotMessages(), opts)
	if err != nil {
		return fmt.Errorf("LLM relay: %w", err)
	}
	defer func() {
		for range stream {
		}
	}()

	var summary strings.Builder
	for event := range stream {
		if event.Type == llm.EventTextDelta {
			summary.WriteString(event.Text)
		}
		if event.Type == llm.EventError {
			return fmt.Errorf("LLM stream error: %v", event.Err)
		}
	}

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
	// Create a cancellable context for this request so Cancel() can stop it.
	ctx, cancel := context.WithCancel(ctx)
	o.cancelMu.Lock()
	o.cancelFn = cancel
	o.cancelMu.Unlock()

	ch := make(chan OrchestratorEvent, 64)

	if strings.HasPrefix(strings.TrimSpace(input), "/") && o.commands != nil {
		cmdName, cmdArgs, ok := ParseSlashCommand(input)
		if !ok {
			go func() {
				defer close(ch)
				defer cancel()
				ch <- OrchestratorEvent{Type: EventText, Content: "Invalid command"}
				ch <- OrchestratorEvent{Type: EventDone}
			}()
			return ch, nil
		}

		cmd, found := o.commands.Lookup(cmdName)
		if found && cmd.Skill != nil {
			expanded := cmd.Skill.ExpandPrompt(cmdArgs)
			o.setActiveSkill(cmd.Skill)
			go func() {
				defer close(ch)
				defer cancel()
				defer func() { o.setActiveSkill(nil) }()
				o.handleLLM(ctx, ch, expanded)
			}()
			return ch, nil
		}

		go func() {
			defer close(ch)
			defer cancel()
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

	sessionName, text := ExtractMention(input)
	if sessionName != "" {
		go func() {
			defer close(ch)
			defer cancel()
			o.handleMention(ctx, ch, sessionName, text)
		}()
		return ch, nil
	}

	go func() {
		defer close(ch)
		defer cancel()
		defer func() {
			o.cancelMu.Lock()
			o.cancelFn = nil
			o.cancelMu.Unlock()
		}()
		o.handleLLM(ctx, ch, input)
	}()

	return ch, nil
}

func (o *Orchestrator) handleMention(ctx context.Context, ch chan<- OrchestratorEvent, sessionName, text string) {
	tool := tools.NewSendToSessionTool(o.tc)
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
	o.appendMessage(llm.Message{Role: llm.RoleUser, Content: input})

	opts := o.buildOptions()
	log.Printf("[orchestrator] handleLLM start inputLen=%d model=%s", len(input), opts.Model)

	for {
		if ctx.Err() != nil {
			log.Printf("[orchestrator] handleLLM cancelled")
			ch <- OrchestratorEvent{Type: EventText, Content: "Cancelled."}
			ch <- OrchestratorEvent{Type: EventDone}
			return
		}

		msgs := o.snapshotMessages()
		log.Printf("[orchestrator] LLM call messages=%d", len(msgs))

		callCtx, callCancel := context.WithTimeout(ctx, o.callTimeout)
		log.Printf("[orchestrator] starting LLM complete timeout=%s ctx_err=%v", o.callTimeout, ctx.Err())
		result, err := o.provider.Complete(callCtx, msgs, opts)
		callCancel()
		if err != nil {
			log.Printf("[orchestrator] LLM complete error: %v", err)
			ch <- OrchestratorEvent{Type: EventText, Content: fmt.Sprintf("LLM error: %v", err)}
			ch <- OrchestratorEvent{Type: EventDone}
			return
		}

		log.Printf("[orchestrator] complete done text_len=%d tools=%d stop=%s", len(result.Content), len(result.ToolCalls), result.StopReason)

		if result.Content != "" {
			ch <- OrchestratorEvent{Type: EventText, Content: result.Content}
		}

		o.appendMessage(llm.Message{
			Role:      llm.RoleAssistant,
			Content:   result.Content,
			ToolCalls: result.ToolCalls,
		})

		if len(result.ToolCalls) == 0 {
			log.Printf("[orchestrator] handleLLM done text_len=%d", len(result.Content))
			ch <- OrchestratorEvent{Type: EventDone}
			return
		}

		var toolResults []llm.ToolResult
		for _, tc := range result.ToolCalls {
			if ctx.Err() != nil {
				log.Printf("[orchestrator] handleLLM cancelled before tool %s", tc.Name)
				ch <- OrchestratorEvent{Type: EventText, Content: "Cancelled."}
				ch <- OrchestratorEvent{Type: EventDone}
				return
			}
			log.Printf("[orchestrator] executing tool name=%s", tc.Name)
			ch <- OrchestratorEvent{Type: EventToolCall, ToolName: tc.Name, ToolArgs: parseJSONArgs(tc.Arguments)}

			toolResult := o.executeTool(ctx, tc)
			toolResults = append(toolResults, toolResult)

			log.Printf("[orchestrator] tool result name=%s len=%d isError=%v", tc.Name, len(toolResult.Content), toolResult.IsError)
			ch <- OrchestratorEvent{Type: EventToolResult, ToolName: tc.Name, Content: toolResult.Content, ToolResult: toolResult.Content}
		}

		o.appendMessage(llm.Message{Role: llm.RoleTool, ToolResults: toolResults})

		opts = o.buildOptions()
	}
}

func (o *Orchestrator) executeTool(ctx context.Context, tc llm.ToolCall) llm.ToolResult {
	tool, exists := o.toolMap[tc.Name]
	if !exists {
		return llm.ToolResult{CallID: tc.ID, Content: fmt.Sprintf("Unknown tool: %s", tc.Name), IsError: true}
	}

	args := parseJSONArgs(tc.Arguments)

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

	afterResult, err := o.hooks.Fire(ctx, HookAfterToolCall, output)
	if err != nil {
		return llm.ToolResult{CallID: tc.ID, Content: fmt.Sprintf("After-hook error: %v", err), IsError: true}
	}
	if modified, ok := afterResult.(AfterToolCallResult); ok && modified.ModifiedResult != "" {
		output = modified.ModifiedResult
	}

	return llm.ToolResult{CallID: tc.ID, Content: util.Truncate(output, 3000)}
}

func (o *Orchestrator) buildOptions() llm.GenerateOptions {
	toolList := o.toolList
	if skill := o.getActiveSkill(); skill != nil && len(skill.AllowedTools) > 0 {
		allowed := make(map[string]bool, len(skill.AllowedTools))
		for _, t := range skill.AllowedTools {
			allowed[t] = true
		}
		var filtered []tools.Tool
		for _, t := range o.toolList {
			if allowed[t.Name] {
				filtered = append(filtered, t)
			}
		}
		toolList = filtered
	}

	defs := make([]llm.ToolDefinition, len(toolList))
	for i, t := range toolList {
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

		defs[i] = llm.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		}
	}

	return llm.GenerateOptions{
		MaxTokens:    4096,
		Tools:        defs,
		SystemPrompt: o.systemPrompt,
		Model:        o.model,
	}
}

func parseJSONArgs(raw string) map[string]any {
	args := make(map[string]any)
	if err := json.Unmarshal([]byte(raw), &args); err != nil && raw != "" && raw != "{}" {
		args["_parse_error"] = err.Error()
	}
	return args
}

func DefaultSystemPrompt() string {
	return `You are FingerSaver, a coding agent orchestrator. You manage multiple tmux sessions running coding agents like Claude Code and GitHub Copilot.

CRITICAL RULES:
- ALWAYS use tools to perform actions. NEVER just describe what you will do.
- To relay information between sessions: first read_session_output, then send_to_session with the actual content.
- When the user asks you to send content to a session, call send_to_session with the full content as the message argument. Do NOT just say you will send it — actually call the tool.
- Be concise in responses.

Available tools:
- list_sessions: List all tmux sessions
- create_session: Create a new session (args: name, working_dir)
- switch_session: Switch viewer to a session (args: name)
- kill_session: Kill a session (args: name)
- send_to_session: Send text to a session (args: name, message)
- read_session_output: Read a session's current output (args: name)
- read_structured_output: Parse session output into structured JSON with status, messages, errors, files (args: name)
- relay_message: Relay structured message between sessions with source summary (args: from_session, to_session, message_type, content)
- save_context: Save session snapshot to disk (args: name, label)
- restore_context: Restore saved context to a session (args: name, source_session, label)
- wait_until_idle: Poll a session until agent is idle. Returns "blocked" if a confirmation prompt is pending (args: session_name, timeout_seconds)
- assess_confirmation: Assess a pending confirmation prompt — decide approve or reject (args: session_name)
- respond_confirmation: Send Yes/No to a session's pending confirmation (args: session_name, approve)
- set_state: Persist key-value state (args: key, value)
- get_state: Read key-value state (args: key)

When the user refers to a session by name (e.g., "check the auth service"), use switch_session and read_session_output to understand the state.

Confirmation handling workflow:
When wait_until_idle returns "blocked", use assess_confirmation to evaluate the prompt, then respond_confirmation to approve or reject. Then call wait_until_idle again to continue waiting.
Example: send_to_session → wait_until_idle → (blocked) → assess_confirmation → respond_confirmation → wait_until_idle → (idle)`
}
