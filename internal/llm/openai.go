package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type OpenAIProvider struct {
	client *openai.Client
}

func NewOpenAIProvider(apiKey string, baseURL string) *OpenAIProvider {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
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
		start := time.Now()
		log.Printf("[llm/openai] stream start model=%s", opts.Model)

		// Request a trailing usage-only chunk so token accounting is available.
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}

		stream := p.client.Chat.Completions.NewStreaming(ctx, params)
		textCount := 0
		var usage Usage
		var stopReason string

		for stream.Next() {
			evt := stream.Current()
			// The trailing usage-only chunk (include_usage) carries no choices;
			// capture its accounting before skipping.
			if u := evt.Usage; u.TotalTokens > 0 {
				usage = Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
			}
			if len(evt.Choices) == 0 {
				continue
			}
			delta := evt.Choices[0].Delta

			if delta.Content != "" {
				textCount++
				select {
				case ch <- StreamEvent{Type: EventTextDelta, Text: delta.Content}:
				case <-ctx.Done():
					return
				}
			}

			// Capture reasoning/thinking (BigModel, DeepSeek, QwQ, etc.)
			if raw := evt.RawJSON(); raw != "" {
				var chunk struct {
					Choices []struct {
						Delta struct {
							ReasoningContent string `json:"reasoning_content"`
							Reasoning        string `json:"reasoning"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if json.Unmarshal([]byte(raw), &chunk) == nil && len(chunk.Choices) > 0 {
					rc := chunk.Choices[0].Delta.ReasoningContent
					if rc == "" {
						rc = chunk.Choices[0].Delta.Reasoning
					}
					if rc != "" {
						select {
						case ch <- StreamEvent{Type: EventThinkingDelta, Text: rc}:
						case <-ctx.Done():
							return
						}
					}
				}
			}

			for _, tc := range delta.ToolCalls {
				if tc.ID != "" {
					select {
					case ch <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   tc.ID,
						ToolCallName: tc.Function.Name,
					}:
					case <-ctx.Done():
						return
					}
				}
				if tc.Function.Arguments != "" {
					select {
					case ch <- StreamEvent{
						Type:           EventToolCallDelta,
						ToolCallID:     tc.ID,
						ArgumentsDelta: tc.Function.Arguments,
					}:
					case <-ctx.Done():
						return
					}
				}
			}

			if evt.Choices[0].FinishReason != "" {
				stopReason = string(evt.Choices[0].FinishReason)
			}
		}

		if err := stream.Err(); err != nil {
			log.Printf("[llm/openai] stream error: %v elapsed=%s", err, time.Since(start).Round(time.Millisecond))
			select {
			case ch <- StreamEvent{Type: EventError, Err: fmt.Errorf("openai stream: %w", err)}:
			case <-ctx.Done():
			}
		} else {
			log.Printf("[llm/openai] stream done text_deltas=%d in=%d out=%d elapsed=%s", textCount, usage.InputTokens, usage.OutputTokens, time.Since(start).Round(time.Millisecond))
			select {
			case ch <- StreamEvent{Type: EventDone, StopReason: stopReason, Usage: usage}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

func (p *OpenAIProvider) Complete(ctx context.Context, messages []Message, opts GenerateOptions) (*CompleteResult, error) {
	params, err := p.buildParams(messages, opts)
	if err != nil {
		return nil, fmt.Errorf("openai: build params: %w", err)
	}

	start := time.Now()
	log.Printf("[llm/openai] complete start model=%s", opts.Model)

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai complete: %w", err)
	}

	log.Printf("[llm/openai] complete done elapsed=%s", time.Since(start).Round(time.Millisecond))

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai complete: no choices returned")
	}

	choice := resp.Choices[0]
	result := &CompleteResult{
		Content:    choice.Message.Content,
		StopReason: string(choice.FinishReason),
	}
	result.Usage = Usage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}

	// Extract thinking/reasoning from raw JSON (BigModel, DeepSeek, etc.)
	if raw := resp.RawJSON(); raw != "" {
		var rj struct {
			Choices []struct {
				Message struct {
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(raw), &rj) == nil && len(rj.Choices) > 0 {
			result.Thinking = rj.Choices[0].Message.ReasoningContent
			if result.Thinking == "" {
				result.Thinking = rj.Choices[0].Message.Reasoning
			}
		}
	}

	for _, tc := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return result, nil
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
			if len(msg.ToolCalls) > 0 {
				tcs := make([]openai.ChatCompletionMessageToolCallUnionParam, len(msg.ToolCalls))
				for i, tc := range msg.ToolCalls {
					tcs[i] = openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: tc.ID,
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: tc.Arguments,
							},
						},
					}
				}
				content := openai.ChatCompletionAssistantMessageParamContentUnion{}
				content.OfString = openai.String(msg.Content)
				params.Messages = append(params.Messages, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content:   content,
						ToolCalls: tcs,
					},
				})
			} else {
				params.Messages = append(params.Messages, openai.AssistantMessage(msg.Content))
			}
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
