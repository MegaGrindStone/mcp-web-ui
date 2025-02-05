package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"slices"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
	goopenai "github.com/sashabaranov/go-openai"
)

// OpenAI provides an implementation of the LLM interface for interacting with OpenAI's language models.
type OpenAI struct {
	model        string
	systemPrompt string

	client *goopenai.Client
}

// NewOpenAI creates a new OpenAI instance with the specified API key, base URL, model name, and system prompt.
func NewOpenAI(apiKey, baseURL, model, systemPrompt string) OpenAI {
	cfg := goopenai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	return OpenAI{
		model:        model,
		systemPrompt: systemPrompt,
		client:       goopenai.NewClientWithConfig(cfg),
	}
}

func openAIMessages(messages []models.Message) ([]goopenai.ChatCompletionMessage, error) {
	msgs := make([]goopenai.ChatCompletionMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == models.RoleUser {
			if len(msg.Contents) != 1 {
				return nil, fmt.Errorf("user message should only contain one content, got %d", len(msg.Contents))
			}
			msgs = append(msgs, goopenai.ChatCompletionMessage{
				Role:    string(msg.Role),
				Content: msg.Contents[0].Text,
			})
			continue
		}

		for _, ct := range msg.Contents {
			switch ct.Type {
			case models.ContentTypeText:
				if ct.Text != "" {
					msgs = append(msgs, goopenai.ChatCompletionMessage{
						Role:    string(msg.Role),
						Content: ct.Text,
					})
				}
			case models.ContentTypeCallTool:
				msgs = append(msgs, goopenai.ChatCompletionMessage{
					Role: string(msg.Role),
					ToolCalls: []goopenai.ToolCall{
						{
							Type: "function",
							ID:   ct.CallToolID,
							Function: goopenai.FunctionCall{
								Name:      ct.ToolName,
								Arguments: string(ct.ToolInput),
							},
						},
					},
				})
			case models.ContentTypeToolResult:
				msgs = append(msgs, goopenai.ChatCompletionMessage{
					Role:       "tool",
					Content:    string(ct.ToolResult),
					ToolCallID: ct.CallToolID,
				})
			}
		}
	}
	return msgs, nil
}

// Chat is a wrapper around the OpenAI chat completion API.
func (o OpenAI) Chat(
	ctx context.Context,
	messages []models.Message,
	tools []mcp.Tool,
) iter.Seq2[models.Content, error] {
	return func(yield func(models.Content, error) bool) {
		msgs, err := openAIMessages(messages)
		if err != nil {
			yield(models.Content{}, fmt.Errorf("error creating ollama messages: %w", err))
			return
		}

		msgs = slices.Insert(msgs, 0, goopenai.ChatCompletionMessage{
			Role:    "system",
			Content: o.systemPrompt,
		})

		oTools := make([]goopenai.Tool, len(tools))
		for i, tool := range tools {
			oTools[i] = goopenai.Tool{
				Type: "function",
				Function: &goopenai.FunctionDefinition{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			}
		}

		req := goopenai.ChatCompletionRequest{
			Model:    o.model,
			Messages: msgs,
			Stream:   true,
			Tools:    oTools,
		}

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		stream, err := o.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			yield(models.Content{}, fmt.Errorf("error sending request: %w", err))
			return
		}

		toolUse := false
		toolArgs := ""
		callToolContent := models.Content{
			Type: models.ContentTypeCallTool,
		}
		for {
			response, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				if errors.Is(err, context.Canceled) {
					return
				}
				yield(models.Content{}, fmt.Errorf("error receiving response: %w", err))
				return
			}

			if len(response.Choices) == 0 {
				continue
			}

			res := response.Choices[0].Delta
			if res.Content != "" {
				if !yield(models.Content{
					Type: models.ContentTypeText,
					Text: res.Content,
				}, nil) {
					return
				}
			}
			if len(res.ToolCalls) > 0 {
				if len(res.ToolCalls) > 1 {
					log.Printf("Received %d tool calls, but only the first one is supported", len(res.ToolCalls))
					log.Printf("%+v", res.ToolCalls)
				}
				toolArgs += res.ToolCalls[0].Function.Arguments
				if !toolUse {
					toolUse = true
					callToolContent.ToolName = res.ToolCalls[0].Function.Name
					callToolContent.CallToolID = res.ToolCalls[0].ID
				}
			}
		}
		if toolUse {
			if toolArgs == "" {
				toolArgs = "{}"
			}
			log.Printf("Tool Name: %s", callToolContent.ToolName)
			log.Printf("toolArgs: %s", toolArgs)
			callToolContent.ToolInput = json.RawMessage(toolArgs)
			yield(callToolContent, nil)
		}
	}
}

// GenerateTitle is a wrapper around the OpenAI chat completion API.
func (o OpenAI) GenerateTitle(ctx context.Context, message string) (string, error) {
	msgs := []goopenai.ChatCompletionMessage{
		{
			Role:    goopenai.ChatMessageRoleSystem,
			Content: o.systemPrompt,
		},
		{
			Role:    goopenai.ChatMessageRoleUser,
			Content: message,
		},
	}
	resp, err := o.client.CreateChatCompletion(
		ctx,
		goopenai.ChatCompletionRequest{
			Model:    o.model,
			Messages: msgs,
		},
	)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("no choices found")
	}

	return resp.Choices[0].Message.Content, nil
}
