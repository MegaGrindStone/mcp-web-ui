package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
	"github.com/ollama/ollama/api"
)

// Ollama provides an implementation of the LLM interface for interacting with Ollama's language models.
// It manages connections to an Ollama server instance and handles streaming chat completions.
type Ollama struct {
	host         string
	model        string
	systemPrompt string

	params LLMParameters

	client *api.Client

	logger *slog.Logger
}

// NewOllama creates a new Ollama instance with the specified host URL and model name. The host
// parameter should be a valid URL pointing to an Ollama server. If the provided host URL is invalid,
// the function will panic.
func NewOllama(host, model, systemPrompt string, params LLMParameters, logger *slog.Logger) Ollama {
	u, err := url.Parse(host)
	if err != nil {
		panic(err)
	}

	return Ollama{
		host:         host,
		model:        model,
		systemPrompt: systemPrompt,
		params:       params,
		client:       api.NewClient(u, &http.Client{}),
		logger:       logger.With(slog.String("module", "ollama")),
	}
}

func ollamaMessages(messages []models.Message) ([]api.Message, error) {
	msgs := make([]api.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == models.RoleUser {
			// Process user messages with potential multiple contents
			content := ""
			var images []api.ImageData

			for _, ct := range msg.Contents {
				switch ct.Type {
				case models.ContentTypeText:
					content += ct.Text
				case models.ContentTypeResource:
					// Process resources (extract images, convert others to text)
					textContent, extractedImages, err := processResourceContentsForOllama(ct.ResourceContents)
					if err != nil {
						return nil, err
					}
					content += textContent
					images = append(images, extractedImages...)
				case models.ContentTypeCallTool, models.ContentTypeToolResult:
					return nil, fmt.Errorf("content type %s is not supported for user messages", ct.Type)
				}
			}

			// Create user message with combined content and images
			msgs = append(msgs, api.Message{
				Role:    string(msg.Role),
				Content: content,
				Images:  images,
			})
			continue
		}

		for _, ct := range msg.Contents {
			switch ct.Type {
			case models.ContentTypeText:
				if ct.Text != "" {
					msgs = append(msgs, api.Message{
						Role:    string(msg.Role),
						Content: ct.Text,
					})
				}
			case models.ContentTypeCallTool:
				args := make(map[string]any)
				if err := json.Unmarshal(ct.ToolInput, &args); err != nil {
					return nil, fmt.Errorf("error unmarshaling tool input: %w", err)
				}
				msgs = append(msgs, api.Message{
					Role: string(msg.Role),
					ToolCalls: []api.ToolCall{
						{
							Function: api.ToolCallFunction{
								Name:      ct.ToolName,
								Arguments: args,
							},
						},
					},
				})
			case models.ContentTypeToolResult:
				msgs = append(msgs, api.Message{
					Role:    "tool",
					Content: string(ct.ToolResult),
				})
			case models.ContentTypeResource:
				return nil, fmt.Errorf("content type %s is not supported for assistant messages", ct.Type)
			}
		}
	}
	return msgs, nil
}

func processResourceContentsForOllama(resources []mcp.ResourceContents) (string, []api.ImageData, error) {
	var textContent string
	var images []api.ImageData

	for _, resource := range resources {
		switch {
		case strings.HasPrefix(resource.MimeType, "image/"):
			// Process images - convert to ImageData for Ollama
			imageData, err := processImageForOllama(resource)
			if err != nil {
				return "", nil, err
			}
			images = append(images, imageData)
		default:
			// Convert other resources to text descriptions
			description := convertResourceToText(resource)
			if textContent != "" && description != "" {
				textContent += "\n\n"
			}
			textContent += description
		}
	}

	return textContent, images, nil
}

func processImageForOllama(resource mcp.ResourceContents) (api.ImageData, error) {
	// If blob is already binary data, use it directly
	if !isBase64(resource.Blob) {
		return api.ImageData(resource.Blob), nil
	}

	// Decode base64 data
	decodedData, err := base64.StdEncoding.DecodeString(resource.Blob)
	if err != nil {
		return nil, fmt.Errorf("error decoding base64 image: %w", err)
	}

	return api.ImageData(decodedData), nil
}

func convertResourceToText(resource mcp.ResourceContents) string {
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

// Chat implements the LLM interface by streaming responses from the Ollama model. It accepts a context
// for cancellation and a slice of messages representing the conversation history. The function returns
// an iterator that yields response chunks as strings and potential errors. The response is streamed
// incrementally, allowing for real-time processing of model outputs.
func (o Ollama) Chat(
	ctx context.Context,
	messages []models.Message,
	tools []mcp.Tool,
) iter.Seq2[models.Content, error] {
	return func(yield func(models.Content, error) bool) {
		msgs, err := ollamaMessages(messages)
		if err != nil {
			yield(models.Content{}, fmt.Errorf("error creating ollama messages: %w", err))
			return
		}

		msgs = slices.Insert(msgs, 0, api.Message{
			Role:    "system",
			Content: o.systemPrompt,
		})

		oTools := make([]api.Tool, len(tools))
		for i, tool := range tools {
			var params struct {
				Type       string   `json:"type"`
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type        string   `json:"type"`
					Description string   `json:"description"`
					Enum        []string `json:"enum,omitempty"`
				} `json:"properties"`
			}
			if err := json.Unmarshal([]byte(tool.InputSchema), &params); err != nil {
				yield(models.Content{}, fmt.Errorf("error unmarshaling tool input schema: %w", err))
				return
			}
			oTool := api.Tool{
				Type: "function",
				Function: api.ToolFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  params,
				},
			}

			if err := json.Unmarshal([]byte(tool.InputSchema), &oTool.Function.Parameters); err != nil {
				yield(models.Content{}, fmt.Errorf("error unmarshaling tool input schema: %w", err))
				return
			}
			oTools[i] = oTool
		}

		req := o.chatRequest(msgs, oTools, true)

		reqJSON, err := json.Marshal(req)
		if err == nil {
			o.logger.Debug("Request", slog.String("req", string(reqJSON)))
		}

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		if err := o.client.Chat(ctx, &req, func(res api.ChatResponse) error {
			if res.Message.Content != "" {
				if !yield(models.Content{
					Type: models.ContentTypeText,
					Text: res.Message.Content,
				}, nil) {
					cancel()
					return nil
				}
			}
			if len(res.Message.ToolCalls) > 0 {
				args, err := json.Marshal(res.Message.ToolCalls[0].Function.Arguments)
				if err != nil {
					return fmt.Errorf("error marshaling tool arguments: %w", err)
				}
				if len(res.Message.ToolCalls) > 1 {
					o.logger.Warn("Received multiples tool call, but only the first one is supported",
						slog.Int("count", len(res.Message.ToolCalls)),
						slog.String("toolCalls", fmt.Sprintf("%+v", res.Message.ToolCalls)),
					)
				}
				if !yield(models.Content{
					Type:      models.ContentTypeCallTool,
					ToolName:  res.Message.ToolCalls[0].Function.Name,
					ToolInput: args,
				}, nil) {
					cancel()
				}
			}
			return nil
		}); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			yield(models.Content{}, fmt.Errorf("error sending request: %w", err))
			return
		}
	}
}

// GenerateTitle generates a title for a given message using the Ollama API. It sends a single message to the
// Ollama API and returns the first response content as the title. The context can be used to cancel ongoing
// requests.
func (o Ollama) GenerateTitle(ctx context.Context, message string) (string, error) {
	msgs := []api.Message{
		{
			Role:    "system",
			Content: o.systemPrompt,
		},
		{
			Role:    "user",
			Content: message,
		},
	}

	req := o.chatRequest(msgs, nil, false)

	var title string

	if err := o.client.Chat(ctx, &req, func(res api.ChatResponse) error {
		title = res.Message.Content
		return nil
	}); err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}

	return title, nil
}

func (o Ollama) chatRequest(messages []api.Message, tools []api.Tool, stream bool) api.ChatRequest {
	req := api.ChatRequest{
		Model:    o.model,
		Messages: messages,
		Stream:   &stream,
		Tools:    tools,
	}

	opts := make(map[string]any)

	if o.params.Temperature != nil {
		opts["temperature"] = *o.params.Temperature
	}
	if o.params.Seed != nil {
		opts["seed"] = *o.params.Seed
	}
	if o.params.Stop != nil {
		opts["stop"] = o.params.Stop
	}
	if o.params.TopK != nil {
		opts["top_k"] = *o.params.TopK
	}
	if o.params.TopP != nil {
		opts["top_p"] = *o.params.TopP
	}
	if o.params.MinP != nil {
		opts["min_p"] = *o.params.MinP
	}

	req.Options = opts

	return req
}
