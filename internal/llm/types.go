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
	EventToolCallStart
	EventToolCallDelta
	EventDone
	EventError
)

func (t StreamEventType) String() string {
	names := map[StreamEventType]string{
		EventTextDelta:     "text_delta",
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
}

type GenerateOptions struct {
	Model        string
	MaxTokens    int
	Temperature  float64
	Tools        []ToolDefinition
	SystemPrompt string
}
