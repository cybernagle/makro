package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type OpenAIProvider struct {
	client *openai.Client
}

func NewOpenAIProvider(apiKey string) *OpenAIProvider {
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &OpenAIProvider{client: &client}
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) Stream(ctx context.Context, messages []Message, opts GenerateOptions) (<-chan StreamEvent, error) {
	params, err := p.buildParams(messages, opts)
	if err != nil {
		return nil, fmt.Errorf("openai: build params: %w", err)
	}

	ch := make(chan StreamEvent, 64)

	go func() {
		defer close(ch)

		stream := p.client.Chat.Completions.NewStreaming(ctx, params)

		for stream.Next() {
			evt := stream.Current()
			if len(evt.Choices) == 0 {
				continue
			}
			delta := evt.Choices[0].Delta

			if delta.Content != "" {
				ch <- StreamEvent{Type: EventTextDelta, Text: delta.Content}
			}

			for _, tc := range delta.ToolCalls {
				if tc.ID != "" {
					ch <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   tc.ID,
						ToolCallName: tc.Function.Name,
					}
				}
				if tc.Function.Arguments != "" {
					ch <- StreamEvent{
						Type:           EventToolCallDelta,
						ToolCallID:     tc.ID,
						ArgumentsDelta: tc.Function.Arguments,
					}
				}
			}

			if evt.Choices[0].FinishReason != "" {
				ch <- StreamEvent{Type: EventDone, StopReason: string(evt.Choices[0].FinishReason)}
			}
		}

		if err := stream.Err(); err != nil {
			ch <- StreamEvent{Type: EventError, Err: fmt.Errorf("openai stream: %w", err)}
		}
	}()

	return ch, nil
}

func (p *OpenAIProvider) buildParams(messages []Message, opts GenerateOptions) (openai.ChatCompletionNewParams, error) {
	params := openai.ChatCompletionNewParams{
		Model: opts.Model,
	}

	if opts.MaxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
	}

	if opts.Temperature > 0 {
		params.Temperature = openai.Float(opts.Temperature)
	}

	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			params.Messages = append(params.Messages, openai.SystemMessage(msg.Content))
		case RoleUser:
			params.Messages = append(params.Messages, openai.UserMessage(msg.Content))
		case RoleAssistant:
			params.Messages = append(params.Messages, openai.AssistantMessage(msg.Content))
		case RoleTool:
			for _, tr := range msg.ToolResults {
				params.Messages = append(params.Messages, openai.ToolMessage(tr.Content, tr.CallID))
			}
		}
	}

	if opts.SystemPrompt != "" {
		params.Messages = append([]openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(opts.SystemPrompt),
		}, params.Messages...)
	}

	if len(opts.Tools) > 0 {
		tools := make([]openai.ChatCompletionToolUnionParam, len(opts.Tools))
		for i, t := range opts.Tools {
			tools[i] = openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
			})
		}
		params.Tools = tools
	}

	return params, nil
}
