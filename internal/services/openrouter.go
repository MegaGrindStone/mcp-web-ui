package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
	"github.com/tmaxmax/go-sse"
)

// OpenRouter provides an implementation of the LLM interface for interacting with OpenRouter's language models.
type OpenRouter struct {
	apiKey       string
	model        string
	systemPrompt string

	params LLMParameters

	client *http.Client

	logger *slog.Logger
}

type openRouterChatRequest struct {
	Model    string                     `json:"model"`
	Messages []openRouterMessageRequest `json:"messages"`
	Tools    []openRouterTool           `json:"tools,omitempty"`
	Stream   bool                       `json:"stream"`

	Temperature       *float32       `json:"temperature,omitempty"`
	TopP              *float32       `json:"top_p,omitempty"`
	TopK              *int           `json:"top_k,omitempty"`
	FrequencyPenalty  *float32       `json:"frequency_penalty,omitempty"`
	PresencePenalty   *float32       `json:"presence_penalty,omitempty"`
	RepetitionPenalty *float32       `json:"repetition_penalty,omitempty"`
	MinP              *float32       `json:"min_p,omitempty"`
	TopA              *float32       `json:"top_a,omitempty"`
	Seed              *int           `json:"seed,omitempty"`
	MaxTokens         *int           `json:"max_tokens,omitempty"`
	LogitBias         map[string]int `json:"logit_bias,omitempty"`
	Logprobs          *bool          `json:"logprobs,omitempty"`
	TopLogprobs       *int           `json:"top_logprobs,omitempty"`
	Stop              []string       `json:"stop,omitempty"`
	IncludeReasoning  *bool          `json:"include_reasoning,omitempty"`
}

type openRouterMessageRequest struct {
	Role       string                `json:"role"`
	Content    any                   `json:"content,omitempty"`
	ToolCalls  []openRouterToolCalls `json:"tool_calls,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
}

type openRouterUserContent struct {
	Type string `json:"type"`

	// For text type.
	Text string `json:"text,omitempty"`

	// For image_url type.
	ImageURL *openRouterImageContent `json:"image_url,omitempty"`
}

type openRouterImageContent struct {
	URL string `json:"url"`
}

type openRouterMessage struct {
	Role       string                `json:"role"`
	Content    string                `json:"content,omitempty"`
	ToolCalls  []openRouterToolCalls `json:"tool_calls,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
}

type openRouterToolCalls struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function openRouterToolCallFunction `json:"function"`
}

type openRouterToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openRouterTool struct {
	Type     string                 `json:"type"`
	Function openRouterToolFunction `json:"function"`
}

type openRouterToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openRouterStreamingResponse struct {
	Choices []openRouterStreamingChoice `json:"choices"`
}

type openRouterStreamingErrorResponse struct {
	Error openRouterStreamingError `json:"error"`
}

type openRouterStreamingChoice struct {
	Delta              openRouterMessage `json:"delta"`
	FinishReason       string            `json:"finish_reason"`
	NativeFinishReason string            `json:"native_finish_reason"`
}

type openRouterStreamingError struct {
	Code     int            `json:"code"`
	Message  string         `json:"message"`
	Metadata map[string]any `json:"metadata"`
}

type openRouterResponse struct {
	Choices []openRouterChoice `json:"choices"`
}

type openRouterChoice struct {
	Message openRouterMessage `json:"message"`
}

const (
	openRouterAPIEndpoint = "https://openrouter.ai/api/v1"

	openRouterRequestContentTypeText     = "text"
	openRouterRequestContentTypeImageURL = "image_url"
)

// NewOpenRouter creates a new OpenRouter instance with the specified API key, model name, and system prompt.
func NewOpenRouter(apiKey, model, systemPrompt string, params LLMParameters, logger *slog.Logger) OpenRouter {
	return OpenRouter{
		apiKey:       apiKey,
		model:        model,
		systemPrompt: systemPrompt,
		params:       params,
		client:       &http.Client{},
		logger:       logger.With(slog.String("module", "openrouter")),
	}
}

// Chat streams responses from the OpenRouter API for a given sequence of messages. It processes system
// messages separately and returns an iterator that yields response chunks and potential errors. The
// context can be used to cancel ongoing requests. Refer to models.Message for message structure details.
func (o OpenRouter) Chat(
	ctx context.Context,
	messages []models.Message,
	tools []mcp.Tool,
) iter.Seq2[models.Content, error] {
	return func(yield func(models.Content, error) bool) {
		resp, err := o.doRequest(ctx, messages, tools, true)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			yield(models.Content{}, fmt.Errorf("error sending request: %w", err))
			return
		}
		defer resp.Body.Close()

		toolUse := false
		toolArgs := ""
		callToolContent := models.Content{
			Type: models.ContentTypeCallTool,
		}
		for ev, err := range sse.Read(resp.Body, nil) {
			if err != nil {
				yield(models.Content{}, fmt.Errorf("error reading response: %w", err))
				return
			}

			o.logger.Debug("Received event",
				slog.String("event", ev.Data),
			)

			if ev.Data == "[DONE]" {
				break
			}

			// Before we try to unmarshall response to the expected format, we try to unmarshall it to
			// the streaming error format.
			var resErr openRouterStreamingErrorResponse
			if err := json.Unmarshal([]byte(ev.Data), &resErr); err == nil {
				if resErr.Error.Code != 0 {
					o.logger.Error("Received streaming error response",
						slog.String("error", fmt.Sprintf("%+v", resErr)),
					)
					yield(models.Content{}, fmt.Errorf("openrouter error: %+v", resErr.Error))
					return
				}
			}

			var res openRouterStreamingResponse
			if err := json.Unmarshal([]byte(ev.Data), &res); err != nil {
				yield(models.Content{}, fmt.Errorf("error unmarshaling response: %w", err))
				return
			}

			if len(res.Choices) == 0 {
				continue
			}

			choice := res.Choices[0]

			if len(choice.Delta.ToolCalls) > 0 {
				if len(choice.Delta.ToolCalls) > 1 {
					o.logger.Warn("Received multiples tool call, but only the first one is supported",
						slog.Int("count", len(choice.Delta.ToolCalls)),
						slog.String("toolCalls", fmt.Sprintf("%+v", choice.Delta.ToolCalls)),
					)
				}
				toolArgs += choice.Delta.ToolCalls[0].Function.Arguments
				if !toolUse {
					toolUse = true
					callToolContent.ToolName = choice.Delta.ToolCalls[0].Function.Name
					callToolContent.CallToolID = choice.Delta.ToolCalls[0].ID
				}
			}

			if choice.Delta.Content != "" {
				if !yield(models.Content{
					Type: models.ContentTypeText,
					Text: choice.Delta.Content,
				}, nil) {
					break
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

// GenerateTitle generates a title for a given message using the OpenRouter API. It sends a single message to the
// OpenRouter API and returns the first response content as the title. The context can be used to cancel ongoing
// requests.
func (o OpenRouter) GenerateTitle(ctx context.Context, message string) (string, error) {
	msgs := []models.Message{
		{
			Role: models.RoleUser,
			Contents: []models.Content{
				{
					Type: models.ContentTypeText,
					Text: message,
				},
			},
		},
	}

	resp, err := o.doRequest(ctx, msgs, nil, false)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var res openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("error decoding response: %w", err)
	}

	if len(res.Choices) == 0 {
		return "", errors.New("no choices found")
	}

	return res.Choices[0].Message.Content, nil
}

func (o OpenRouter) doRequest(
	ctx context.Context,
	messages []models.Message,
	tools []mcp.Tool,
	stream bool,
) (*http.Response, error) {
	msgs := make([]openRouterMessageRequest, 0, len(messages))
	// Process messages
	for _, msg := range messages {
		if msg.Role == models.RoleUser {
			// Process user message with potential resources
			userMsgs, err := o.processUserMessageForOpenRouter(msg)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, userMsgs)
			continue
		}

		// Handle assistant and tool messages
		for _, ct := range msg.Contents {
			switch ct.Type {
			case models.ContentTypeText:
				if ct.Text != "" {
					msgs = append(msgs, openRouterMessageRequest{
						Role:    string(msg.Role),
						Content: ct.Text,
					})
				}
			case models.ContentTypeCallTool:
				msgs = append(msgs, openRouterMessageRequest{
					Role: "assistant",
					ToolCalls: []openRouterToolCalls{
						{
							ID:   ct.CallToolID,
							Type: "function",
							Function: openRouterToolCallFunction{
								Name:      ct.ToolName,
								Arguments: string(ct.ToolInput),
							},
						},
					},
				})
			case models.ContentTypeToolResult:
				msgs = append(msgs, openRouterMessageRequest{
					Role:       "tool",
					ToolCallID: ct.CallToolID,
					Content:    string(ct.ToolResult),
				})
			case models.ContentTypeResource:
				return nil, fmt.Errorf("content type %s is not supported for assistant messages", ct.Type)
			}
		}
	}

	msgs = slices.Insert(msgs, 0, openRouterMessageRequest{
		Role:    "system",
		Content: o.systemPrompt,
	})

	oTools := make([]openRouterTool, len(tools))
	for i, tool := range tools {
		parameters := tool.InputSchema
		// Check if parameters represent an empty object
		// This is required for some Google models, as if we don't do this, the model would return
		// bad request error (http 400), with message something like:
		// GenerateContentRequest.parameters.properties: should be non-empty for OBJECT type
		if len(parameters) > 0 {
			var obj map[string]any
			if err := json.Unmarshal(parameters, &obj); err == nil {
				if props, ok := obj["properties"].(map[string]any); ok && len(props) == 0 {
					parameters = nil
				}
			}
		}

		oTools[i] = openRouterTool{
			Type: "function",
			Function: openRouterToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		}
	}

	reqBody := openRouterChatRequest{
		Model:    o.model,
		Messages: msgs,
		Stream:   stream,
		Tools:    oTools,

		Temperature:       o.params.Temperature,
		TopP:              o.params.TopP,
		TopK:              o.params.TopK,
		FrequencyPenalty:  o.params.FrequencyPenalty,
		PresencePenalty:   o.params.PresencePenalty,
		RepetitionPenalty: o.params.RepetitionPenalty,
		MinP:              o.params.MinP,
		TopA:              o.params.TopA,
		Seed:              o.params.Seed,
		MaxTokens:         o.params.MaxTokens,
		LogitBias:         o.params.LogitBias,
		Logprobs:          o.params.Logprobs,
		TopLogprobs:       o.params.TopLogprobs,
		Stop:              o.params.Stop,
		IncludeReasoning:  o.params.IncludeReasoning,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}

	o.logger.Debug("Request Body", slog.String("body", string(jsonBody)))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		openRouterAPIEndpoint+"/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("HTTP-Referer", "https://github.com/MegaGrindStone/mcp-web-ui/")
	req.Header.Set("X-Title", "MCP Web UI")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code: %d, body: %s, request: %s", resp.StatusCode, string(body), jsonBody)
	}

	return resp, nil
}

func (o OpenRouter) processUserMessageForOpenRouter(msg models.Message) (openRouterMessageRequest, error) {
	var contents []openRouterUserContent

	for _, ct := range msg.Contents {
		switch ct.Type {
		case models.ContentTypeText:
			if ct.Text != "" {
				contents = append(contents, openRouterUserContent{
					Type: openRouterRequestContentTypeText,
					Text: ct.Text,
				})
			}
		case models.ContentTypeResource:
			for _, resource := range ct.ResourceContents {
				if strings.HasPrefix(resource.MimeType, "image/") {
					// Process image for OpenRouter
					imageURL := processImageForOpenRouter(resource)

					contents = append(contents, openRouterUserContent{
						Type: openRouterRequestContentTypeImageURL,
						ImageURL: &openRouterImageContent{
							URL: imageURL,
						},
					})
					continue
				}

				// For non-image resources, convert to text
				resourceText := convertResourceToTextForOpenRouter(resource)
				contents = append(contents, openRouterUserContent{
					Type: openRouterRequestContentTypeText,
					Text: resourceText,
				})
			}
		case models.ContentTypeCallTool, models.ContentTypeToolResult:
			return openRouterMessageRequest{}, fmt.Errorf("content type %s is not supported for user messages", ct.Type)
		}
	}

	return openRouterMessageRequest{
		Role:    string(msg.Role),
		Content: contents,
	}, nil
}

func processImageForOpenRouter(resource mcp.ResourceContents) string {
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

func convertResourceToTextForOpenRouter(resource mcp.ResourceContents) string {
	if resource.Text != "" {
		return fmt.Sprintf("[Document of type %s]\n%s", resource.MimeType, resource.Text)
	}

	if resource.Blob != "" {
		data := resource.Blob
		if !isBase64(resource.Blob) {
			data = base64.StdEncoding.EncodeToString([]byte(resource.Blob))
		}
		return fmt.Sprintf("[Document of type %s]\n%s", resource.MimeType, data)
	}

	return ""
}
