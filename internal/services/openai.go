package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"slices"
	"strings"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
	goopenai "github.com/sashabaranov/go-openai"
)

// OpenAI provides an implementation of the LLM interface for interacting with OpenAI's language models.
type OpenAI struct {
	model        string
	systemPrompt string

	params LLMParameters

	client *goopenai.Client

	logger *slog.Logger
}

// NewOpenAI creates a new OpenAI instance with the specified API key, base URL, model name, and system prompt.
func NewOpenAI(
	apiKey, model, systemPrompt, endpoint string,
	params LLMParameters,
	logger *slog.Logger,
) OpenAI {
	var client *goopenai.Client
	if endpoint == "" {
		client = goopenai.NewClient(apiKey)
	} else {
		cfg := goopenai.DefaultConfig(apiKey)
		cfg.BaseURL = endpoint
		client = goopenai.NewClientWithConfig(cfg)
	}

	return OpenAI{
		model:        model,
		systemPrompt: systemPrompt,
		params:       params,
		client:       client,
		logger:       logger.With(slog.String("module", "openai")),
	}
}

func openAIMessages(messages []models.Message) ([]goopenai.ChatCompletionMessage, error) {
	msgs := make([]goopenai.ChatCompletionMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == models.RoleUser {
			// Process user message with potential resources
			userMsg, err := processUserMessageForOpenAI(msg)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, userMsg)
			continue
		}

		// Handle assistant and other roles
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
			case models.ContentTypeResource:
				return nil, fmt.Errorf("content type %s is not supported for assistant messages", ct.Type)
			}
		}
	}
	return msgs, nil
}

func processUserMessageForOpenAI(msg models.Message) (goopenai.ChatCompletionMessage, error) {
	// Check if we have any resource contents
	hasResources := false
	for _, ct := range msg.Contents {
		if ct.Type == models.ContentTypeResource {
			hasResources = true
			break
		}
	}

	// If no resources, combine all text contents
	if !hasResources {
		var textParts []string
		for _, ct := range msg.Contents {
			if ct.Type == models.ContentTypeText && ct.Text != "" {
				textParts = append(textParts, ct.Text)
			}
		}
		return goopenai.ChatCompletionMessage{
			Role:    string(msg.Role),
			Content: strings.Join(textParts, "\n\n"),
		}, nil
	}

	// If we have resources, we need to use MultiContent
	var contentParts []goopenai.ChatMessagePart
	textContent := ""

	for _, ct := range msg.Contents {
		switch ct.Type {
		case models.ContentTypeText:
			if ct.Text != "" {
				if textContent != "" {
					textContent += "\n\n"
				}
				textContent += ct.Text
			}
		case models.ContentTypeResource:
			for _, resource := range ct.ResourceContents {
				if strings.HasPrefix(resource.MimeType, "image/") {
					// Process image for OpenAI
					imageURL := processImageForOpenAI(resource)

					contentParts = append(contentParts, goopenai.ChatMessagePart{
						Type: goopenai.ChatMessagePartTypeImageURL,
						ImageURL: &goopenai.ChatMessageImageURL{
							URL:    imageURL,
							Detail: goopenai.ImageURLDetailAuto,
						},
					})
				} else {
					// For non-image resources, convert to text
					resourceText := convertResourceToTextForOpenAI(resource)
					if resourceText != "" {
						if textContent != "" {
							textContent += "\n\n"
						}
						textContent += resourceText
					}
				}
			}
		case models.ContentTypeCallTool, models.ContentTypeToolResult:
			return goopenai.ChatCompletionMessage{}, fmt.Errorf("content type %s is not supported for user messages", ct.Type)
		}
	}

	// Add text content as a part if we have any
	if textContent != "" {
		contentParts = append(contentParts, goopenai.ChatMessagePart{
			Type: goopenai.ChatMessagePartTypeText,
			Text: textContent,
		})
	}

	return goopenai.ChatCompletionMessage{
		Role:         string(msg.Role),
		MultiContent: contentParts,
	}, nil
}

func processImageForOpenAI(resource mcp.ResourceContents) string {
	// Format should be: data:image/jpeg;base64,<base64-data>
	mimeType := resource.MimeType
	if mimeType == "" {
		mimeType = "image/png" // Default
	}

	var imageData string
	if isBase64(resource.Blob) {
		imageData = resource.Blob
	} else {
		imageData = base64.StdEncoding.EncodeToString([]byte(resource.Blob))
	}

	return fmt.Sprintf("data:%s;base64,%s", mimeType, imageData)
}

func convertResourceToTextForOpenAI(resource mcp.ResourceContents) string {
	if resource.Text != "" {
		return fmt.Sprintf("[Document of type %s]\n%s", resource.MimeType, resource.Text)
	}

	// For binary data that isn't an image (e.g., PDF), provide base64 data
	if resource.Blob != "" {
		data := resource.Blob
		if !isBase64(resource.Blob) {
			data = base64.StdEncoding.EncodeToString([]byte(resource.Blob))
		}
		return fmt.Sprintf("[Document of type %s]\n%s", resource.MimeType, data)
	}

	return ""
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

		req := o.chatRequest(msgs, oTools, true)

		reqJSON, err := json.Marshal(req)
		if err == nil {
			o.logger.Debug("Request", slog.String("req", string(reqJSON)))
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
					o.logger.Warn("Received multiples tool call, but only the first one is supported",
						slog.Int("count", len(res.ToolCalls)),
						slog.String("toolCalls", fmt.Sprintf("%+v", res.ToolCalls)),
					)
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
			o.logger.Debug("Call Tool",
				slog.String("name", callToolContent.ToolName),
				slog.String("args", toolArgs),
			)
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

	req := o.chatRequest(msgs, nil, false)

	resp, err := o.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("no choices found")
	}

	return resp.Choices[0].Message.Content, nil
}

func (o OpenAI) chatRequest(
	messages []goopenai.ChatCompletionMessage,
	tools []goopenai.Tool,
	stream bool,
) goopenai.ChatCompletionRequest {
	req := goopenai.ChatCompletionRequest{
		Model:    o.model,
		Messages: messages,
		Stream:   stream,
		Tools:    tools,
	}

	if o.params.Temperature != nil {
		req.Temperature = *o.params.Temperature
	}
	if o.params.TopP != nil {
		req.TopP = *o.params.TopP
	}
	if o.params.Stop != nil {
		req.Stop = o.params.Stop
	}
	if o.params.PresencePenalty != nil {
		req.PresencePenalty = *o.params.PresencePenalty
	}
	if o.params.Seed != nil {
		req.Seed = o.params.Seed
	}
	if o.params.FrequencyPenalty != nil {
		req.FrequencyPenalty = *o.params.FrequencyPenalty
	}
	if o.params.LogitBias != nil {
		req.LogitBias = o.params.LogitBias
	}
	if o.params.Logprobs != nil {
		req.LogProbs = *o.params.Logprobs
	}
	if o.params.TopLogprobs != nil {
		req.TopLogProbs = *o.params.TopLogprobs
	}

	return req
}
