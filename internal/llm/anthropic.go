package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AnthropicProvider struct {
	client *anthropic.Client
}

func NewAnthropicProvider(apiKey string, baseURL string) *AnthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := anthropic.NewClient(opts...)
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
		start := time.Now()
		log.Printf("[llm/anthropic] stream start model=%s", params.Model)

		stream := p.client.Messages.NewStreaming(ctx, params)
		var activeToolID string
		textCount := 0
		toolCount := 0

		for stream.Next() {
			event := stream.Current()
			switch e := event.AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				if block, ok := e.ContentBlock.AsAny().(anthropic.ThinkingBlock); ok {
					_ = block
				} else if block, ok := e.ContentBlock.AsAny().(anthropic.ToolUseBlock); ok {
					activeToolID = block.ID
					toolCount++
					log.Printf("[llm/anthropic] tool_call_start id=%s name=%s", block.ID, block.Name)
					select {
					case ch <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   block.ID,
						ToolCallName: block.Name,
					}:
					case <-ctx.Done():
						log.Printf("[llm/anthropic] ctx cancelled during tool_call_start")
						return
					}
				}
			case anthropic.ContentBlockDeltaEvent:
				switch delta := e.Delta.AsAny().(type) {
				case anthropic.ThinkingDelta:
					select {
					case ch <- StreamEvent{Type: EventThinkingDelta, Text: delta.Thinking}:
					case <-ctx.Done():
						return
					}
				case anthropic.TextDelta:
					textCount++
					select {
					case ch <- StreamEvent{Type: EventTextDelta, Text: delta.Text}:
					case <-ctx.Done():
						log.Printf("[llm/anthropic] ctx cancelled during text_delta")
						return
					}
				case anthropic.InputJSONDelta:
					select {
					case ch <- StreamEvent{Type: EventToolCallDelta, ToolCallID: activeToolID, ArgumentsDelta: delta.PartialJSON}:
					case <-ctx.Done():
						return
					}
				}
			case anthropic.MessageDeltaEvent:
				log.Printf("[llm/anthropic] stream done stop_reason=%s text_deltas=%d tool_calls=%d elapsed=%s",
					e.Delta.StopReason, textCount, toolCount, time.Since(start).Round(time.Millisecond))
				select {
				case ch <- StreamEvent{Type: EventDone, StopReason: string(e.Delta.StopReason)}:
				case <-ctx.Done():
					return
				}
			}
		}

		if err := stream.Err(); err != nil {
			log.Printf("[llm/anthropic] stream error: %v elapsed=%s", err, time.Since(start).Round(time.Millisecond))
			select {
			case ch <- StreamEvent{Type: EventError, Err: fmt.Errorf("anthropic stream: %w", err)}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

func (p *AnthropicProvider) Complete(ctx context.Context, messages []Message, opts GenerateOptions) (*CompleteResult, error) {
	params, err := p.buildParams(messages, opts)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build params: %w", err)
	}

	start := time.Now()
	log.Printf("[llm/anthropic] complete start model=%s", params.Model)

	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic complete: %w", err)
	}

	log.Printf("[llm/anthropic] complete done elapsed=%s stop_reason=%s", time.Since(start).Round(time.Millisecond), msg.StopReason)

	result := &CompleteResult{StopReason: string(msg.StopReason)}
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			result.Content += b.Text
		case anthropic.ToolUseBlock:
			args, _ := json.Marshal(b.Input)
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: string(args),
			})
		}
	}

	return result, nil
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
