package llm

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role        Role         `json:"role"`
	Content     string       `json:"content,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type StreamEventType int

const (
	EventTextDelta StreamEventType = iota
	EventThinkingDelta
	EventToolCallStart
	EventToolCallDelta
	EventDone
	EventError
)

func (t StreamEventType) String() string {
	names := map[StreamEventType]string{
		EventTextDelta:     "text_delta",
		EventThinkingDelta: "thinking_delta",
		EventToolCallStart: "tool_call_start",
		EventToolCallDelta: "tool_call_delta",
		EventDone:          "done",
		EventError:         "error",
	}
	if s, ok := names[t]; ok {
		return s
	}
	return "unknown"
}

type StreamEvent struct {
	Type           StreamEventType
	Text           string
	ToolCallID     string
	ToolCallName   string
	ArgumentsDelta string
	StopReason     string
	Err            error
	Usage          Usage // populated on EventDone when the provider reports it
}

// Usage holds token accounting for an LLM call, as reported by the provider.
// TotalTokens is 0 when the provider omits it (derive from Input+Output).
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	TotalTokens         int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// CompleteResult holds the non-streaming LLM response.
type CompleteResult struct {
	Content    string
	Thinking   string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
}

type GenerateOptions struct {
	Model        string
	MaxTokens    int
	Temperature  float64
	Tools        []ToolDefinition
	SystemPrompt string
}
