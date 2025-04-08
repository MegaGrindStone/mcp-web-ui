package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
	"github.com/tmaxmax/go-sse"
)

// Anthropic provides an interface to the Anthropic API for large language model interactions. It implements
// the LLM interface and handles streaming chat completions using Claude models.
type Anthropic struct {
	apiKey       string
	model        string
	maxTokens    int
	systemPrompt string

	params LLMParameters

	client *http.Client
}

type anthropicChatRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system"`
	MaxTokens int                `json:"max_tokens"`
	Tools     []anthropicTool    `json:"tools"`
	Stream    bool               `json:"stream"`

	StopSequences []string `json:"stop_sequences,omitempty"`
	Temperature   *float32 `json:"temperature,omitempty"`
	TopK          *int     `json:"top_k,omitempty"`
	TopP          *float32 `json:"top_p,omitempty"`
}

type anthropicMessage struct {
	Role    string                    `json:"role"`
	Content []anthropicMessageContent `json:"content"`
}

type anthropicMessageContent struct {
	Type string `json:"type"`

	// For text type.
	Text string `json:"text,omitempty"`

	// For tool_use type.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// For tool_result type.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicContentBlockStart struct {
	Type         string
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
}

type anthropicContentBlockDelta struct {
	Type  string `json:"type"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

type anthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

const (
	anthropicAPIEndpoint = "https://api.anthropic.com/v1"
)

// NewAnthropic creates a new Anthropic instance with the specified API key, model name, and maximum
// token limit. It initializes an HTTP client for API communication and returns a configured Anthropic
// instance ready for chat interactions.
func NewAnthropic(apiKey, model, systemPrompt string, maxTokens int, params LLMParameters) Anthropic {
	return Anthropic{
		apiKey:       apiKey,
		model:        model,
		maxTokens:    maxTokens,
		systemPrompt: systemPrompt,
		params:       params,
		client:       &http.Client{},
	}
}

// Chat streams responses from the Anthropic API for a given sequence of messages. It processes system
// messages separately and returns an iterator that yields response chunks and potential errors. The
// context can be used to cancel ongoing requests. Refer to models.Message for message structure details.
func (a Anthropic) Chat(
	ctx context.Context,
	messages []models.Message,
	tools []mcp.Tool,
) iter.Seq2[models.Content, error] {
	return func(yield func(models.Content, error) bool) {
		resp, err := a.doRequest(ctx, messages, tools, true)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			yield(models.Content{}, fmt.Errorf("error sending request: %w", err))
			return
		}
		defer resp.Body.Close()

		isToolUse := false
		inputJSON := ""
		toolContent := models.Content{
			Type: models.ContentTypeCallTool,
		}
		for ev, err := range sse.Read(resp.Body, nil) {
			if err != nil {
				yield(models.Content{}, fmt.Errorf("error reading response: %w", err))
				return
			}
			switch ev.Type {
			case "error":
				var e anthropicError
				if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
					yield(models.Content{}, fmt.Errorf("error unmarshaling error: %w", err))
					return
				}
				yield(models.Content{}, fmt.Errorf("anthropic error %s: %s", e.Error.Type, e.Error.Message))
				return
			case "message_stop":
				return
			case "content_block_start":
				var res anthropicContentBlockStart
				if err := json.Unmarshal([]byte(ev.Data), &res); err != nil {
					yield(models.Content{}, fmt.Errorf("error unmarshaling block start: %w", err))
					return
				}
				if res.ContentBlock.Type != "tool_use" {
					continue
				}
				isToolUse = true
				toolContent.ToolName = res.ContentBlock.Name
				toolContent.CallToolID = res.ContentBlock.ID
			case "content_block_delta":
				var res anthropicContentBlockDelta
				if err := json.Unmarshal([]byte(ev.Data), &res); err != nil {
					yield(models.Content{}, fmt.Errorf("error unmarshaling block delta: %w", err))
					return
				}
				if isToolUse {
					inputJSON += res.Delta.PartialJSON
					continue
				}
				if !yield(models.Content{
					Type: models.ContentTypeText,
					Text: res.Delta.Text,
				}, nil) {
					return
				}
			case "content_block_stop":
				if !isToolUse {
					continue
				}

				if inputJSON == "" {
					inputJSON = "{}"
				}
				toolContent.ToolInput = json.RawMessage(inputJSON)
				if !yield(toolContent, nil) {
					return
				}
				isToolUse = false
				inputJSON = ""
			default:
			}
		}
	}
}

// GenerateTitle generates a title for a given message using the Anthropic API. It sends a single message to the
// Anthropic API and returns the first response content as the title. The context can be used to cancel ongoing
// requests.
func (a Anthropic) GenerateTitle(ctx context.Context, message string) (string, error) {
	messages := []models.Message{
		{
			Role: "user",
			Contents: []models.Content{
				{
					Type: models.ContentTypeText,
					Text: message,
				},
			},
		},
	}
	resp, err := a.doRequest(ctx, messages, nil, false)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var msg anthropicMessage
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return "", fmt.Errorf("error decoding response: %w", err)
	}

	if len(msg.Content) == 0 {
		return "", fmt.Errorf("empty response content")
	}

	return msg.Content[0].Text, nil
}

func (a Anthropic) doRequest(
	ctx context.Context,
	messages []models.Message,
	tools []mcp.Tool,
	stream bool,
) (*http.Response, error) {
	msgs := make([]anthropicMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == models.RoleUser {
			if len(msg.Contents) != 1 {
				return nil, fmt.Errorf("user message should only contain one content, got %d", len(msg.Contents))
			}
			msgs = append(msgs, anthropicMessage{
				Role: string(msg.Role),
				Content: []anthropicMessageContent{
					{
						Type: "text",
						Text: msg.Contents[0].Text,
					},
				},
			})
			continue
		}

		contents := make([]anthropicMessageContent, 0, len(msg.Contents))

		for _, ct := range msg.Contents {
			switch ct.Type {
			case models.ContentTypeText:
				if ct.Text != "" {
					contents = append(contents, anthropicMessageContent{
						Type: "text",
						Text: ct.Text,
					})
				}
			case models.ContentTypeCallTool:
				contents = append(contents, anthropicMessageContent{
					Type:  "tool_use",
					ID:    ct.CallToolID,
					Name:  ct.ToolName,
					Input: ct.ToolInput,
				})
				msgs = append(msgs, anthropicMessage{
					Role:    string(msg.Role),
					Content: contents,
				})
				contents = make([]anthropicMessageContent, 0, len(msg.Contents))
			case models.ContentTypeToolResult:
				msgs = append(msgs, anthropicMessage{
					Role: "user",
					Content: []anthropicMessageContent{
						{
							Type:      "tool_result",
							ToolUseID: ct.CallToolID,
							IsError:   ct.CallToolFailed,
							Content:   ct.ToolResult,
						},
					},
				})
			}
		}
	}

	aTools := make([]anthropicTool, len(tools))
	for i, tool := range tools {
		aTools[i] = anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		}
	}

	// Filter out invalid stop sequences by trimming whitespace
	// because antrhopic doesn't support whitespace in stop sequences
	var validStopSequences []string
	if a.params.Stop != nil {
		for _, seq := range a.params.Stop {
			// Trim all whitespace and check if anything remains
			trimmed := strings.TrimSpace(seq)
			if trimmed != "" {
				validStopSequences = append(validStopSequences, trimmed)
			}
		}
	}

	reqBody := anthropicChatRequest{
		Model:     a.model,
		Messages:  msgs,
		System:    a.systemPrompt,
		MaxTokens: a.maxTokens,
		Tools:     aTools,
		Stream:    stream,

		StopSequences: validStopSequences,
		Temperature:   a.params.Temperature,
		TopK:          a.params.TopK,
		TopP:          a.params.TopP,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		anthropicAPIEndpoint+"/messages", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code: %d, body: %s, request: %s", resp.StatusCode, string(body), jsonBody)
	}

	return resp, nil
}
