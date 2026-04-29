package llm

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AnthropicProvider struct {
	client *anthropic.Client
}

func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &AnthropicProvider{client: &client}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) Stream(ctx context.Context, messages []Message, opts GenerateOptions) (<-chan StreamEvent, error) {
	params, err := p.buildParams(messages, opts)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build params: %w", err)
	}

	ch := make(chan StreamEvent, 64)

	go func() {
		defer close(ch)

		stream := p.client.Messages.NewStreaming(ctx, params)

		for stream.Next() {
			event := stream.Current()
			switch e := event.AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				if block, ok := e.ContentBlock.AsAny().(anthropic.ToolUseBlock); ok {
					ch <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   block.ID,
						ToolCallName: block.Name,
					}
				}
			case anthropic.ContentBlockDeltaEvent:
				switch delta := e.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					ch <- StreamEvent{Type: EventTextDelta, Text: delta.Text}
				case anthropic.InputJSONDelta:
					ch <- StreamEvent{Type: EventToolCallDelta, ArgumentsDelta: delta.PartialJSON}
				}
			case anthropic.MessageDeltaEvent:
				ch <- StreamEvent{Type: EventDone, StopReason: string(e.Delta.StopReason)}
			}
		}

		if err := stream.Err(); err != nil {
			ch <- StreamEvent{Type: EventError, Err: fmt.Errorf("anthropic stream: %w", err)}
		}
	}()

	return ch, nil
}

func (p *AnthropicProvider) buildParams(messages []Message, opts GenerateOptions) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:     opts.Model,
		MaxTokens: int64(opts.MaxTokens),
	}

	if opts.Temperature > 0 {
		params.Temperature = anthropic.Float(opts.Temperature)
	}

	if opts.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{Text: opts.SystemPrompt}}
	}

	for _, msg := range messages {
		switch msg.Role {
		case RoleUser:
			params.Messages = append(params.Messages, anthropic.NewUserMessage(
				anthropic.NewTextBlock(msg.Content),
			))
		case RoleAssistant:
			blocks := []anthropic.ContentBlockParamUnion{}
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, tc.Arguments, tc.Name))
			}
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(blocks...))
		case RoleTool:
			results := []anthropic.ContentBlockParamUnion{}
			for _, tr := range msg.ToolResults {
				results = append(results, anthropic.NewToolResultBlock(tr.CallID, tr.Content, tr.IsError))
			}
			params.Messages = append(params.Messages, anthropic.NewUserMessage(results...))
		}
	}

	if len(opts.Tools) > 0 {
		tools := make([]anthropic.ToolUnionParam, len(opts.Tools))
		for i, t := range opts.Tools {
			tools[i] = anthropic.ToolUnionParam{
				OfTool: &anthropic.ToolParam{
					Name:        t.Name,
					Description: anthropic.String(t.Description),
					InputSchema: anthropic.ToolInputSchemaParam{
						Properties: t.Parameters,
					},
				},
			}
		}
		params.Tools = tools
	}

	return params, nil
}
