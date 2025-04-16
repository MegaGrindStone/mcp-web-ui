package handlers

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"text/template"
	"time"

	"github.com/MegaGrindStone/go-mcp"
	mcpwebui "github.com/MegaGrindStone/mcp-web-ui"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
	"github.com/tmaxmax/go-sse"
)

// LLM represents a large language model interface that provides chat functionality. It accepts a context
// and a sequence of messages, returning an iterator that yields response chunks and potential errors.
type LLM interface {
	Chat(ctx context.Context, messages []models.Message, tools []mcp.Tool) iter.Seq2[models.Content, error]
}

// TitleGenerator represents a title generator interface that generates a title for a given message.
type TitleGenerator interface {
	GenerateTitle(ctx context.Context, message string) (string, error)
}

// Store defines the interface for managing chat and message persistence. It provides methods for
// creating, reading, and updating chats and their associated messages. The interface supports both
// atomic operations and bulk retrieval of chats and messages.
type Store interface {
	Chats(ctx context.Context) ([]models.Chat, error)
	AddChat(ctx context.Context, chat models.Chat) (string, error)
	UpdateChat(ctx context.Context, chat models.Chat) error

	Messages(ctx context.Context, chatID string) ([]models.Message, error)
	AddMessage(ctx context.Context, chatID string, message models.Message) (string, error)
	UpdateMessage(ctx context.Context, chatID string, message models.Message) error
}

// MCPClient defines the interface for interacting with an MCP server.
// This allows for mocking in tests.
type MCPClient interface {
	ServerInfo() mcp.Info
	ToolServerSupported() bool
	ResourceServerSupported() bool
	PromptServerSupported() bool
	ListTools(ctx context.Context, params mcp.ListToolsParams) (mcp.ListToolsResult, error)
	ListResources(ctx context.Context, params mcp.ListResourcesParams) (mcp.ListResourcesResult, error)
	ListPrompts(ctx context.Context, params mcp.ListPromptsParams) (mcp.ListPromptResult, error)
	GetPrompt(ctx context.Context, params mcp.GetPromptParams) (mcp.GetPromptResult, error)
	CallTool(ctx context.Context, params mcp.CallToolParams) (mcp.CallToolResult, error)
}

// Main handles the core functionality of the chat application, managing server-sent events,
// HTML templates, and interactions between the LLM and Store components.
type Main struct {
	sseSrv    *sse.Server
	templates *template.Template

	llm            LLM
	titleGenerator TitleGenerator
	store          Store

	mcpClients []MCPClient

	servers   []mcp.Info
	tools     []mcp.Tool
	resources []mcp.Resource
	prompts   []mcp.Prompt

	promptsMap   map[string]int // Map of prompt names to mcpClients index.
	resourcesMap map[string]int // Map of resource uri to mcpClients index.
	toolsMap     map[string]int // Map of tool names to mcpClients index.
	logger       *slog.Logger
}

const (
	chatsSSETopic = "chats"
	errLoggerKey  = "err"
)

// NewMain creates a new Main instance with the provided LLM and Store implementations. It initializes
// the SSE server with default configurations and parses the required HTML templates from the embedded
// filesystem. The SSE server is configured to handle both default events and chat-specific topics.
func NewMain(
	llm LLM,
	titleGen TitleGenerator,
	store Store,
	mcpClients []MCPClient,
	logger *slog.Logger,
) (Main, error) {
	// We parse templates from three distinct directories to separate layout, pages, and partial views
	tmpl, err := template.ParseFS(
		mcpwebui.TemplateFS,
		"templates/layout/*.html",
		"templates/pages/*.html",
		"templates/partials/*.html",
	)
	if err != nil {
		return Main{}, err
	}

	servers := make([]mcp.Info, len(mcpClients))
	tools := make([]mcp.Tool, 0, len(mcpClients))
	resources := make([]mcp.Resource, 0, len(mcpClients))
	prompts := make([]mcp.Prompt, 0, len(mcpClients))
	pm := make(map[string]int)
	rm := make(map[string]int)
	tm := make(map[string]int)
	for i := range mcpClients {
		servers[i] = mcpClients[i].ServerInfo()
		serverName := servers[i].Name

		var ts []mcp.Tool
		if mcpClients[i].ToolServerSupported() {
			listTools, err := mcpClients[i].ListTools(context.Background(), mcp.ListToolsParams{})
			if err != nil {
				return Main{}, fmt.Errorf("failed to list tools from server %s: %w", serverName, err)
			}
			ts = listTools.Tools
			for _, tool := range ts {
				tm[tool.Name] = i
			}
		}

		var rs []mcp.Resource
		if mcpClients[i].ResourceServerSupported() {
			listResources, err := mcpClients[i].ListResources(context.Background(), mcp.ListResourcesParams{})
			if err != nil {
				return Main{}, fmt.Errorf("failed to list resources from server %s: %w", serverName, err)
			}
			rs = listResources.Resources
			for _, resource := range rs {
				rm[resource.URI] = i
			}
		}

		var ps []mcp.Prompt
		if mcpClients[i].PromptServerSupported() {
			listPrompts, err := mcpClients[i].ListPrompts(context.Background(), mcp.ListPromptsParams{})
			if err != nil {
				return Main{}, fmt.Errorf("failed to list prompts from server %s: %w", serverName, err)
			}
			ps = listPrompts.Prompts
			for _, prompt := range ps {
				pm[prompt.Name] = i
			}
		}

		tools = append(tools, ts...)
		resources = append(resources, rs...)
		prompts = append(prompts, ps...)
	}

	return Main{
		sseSrv: &sse.Server{
			OnSession: func(s *sse.Session) (sse.Subscription, bool) {
				// We start with default topics that all clients should subscribe to
				topics := []string{sse.DefaultTopic, chatsSSETopic}

				// We create a message-specific topic if the client requests updates for a particular message
				messageID := s.Req.URL.Query().Get("message_id")
				if messageID != "" {
					topics = append(topics, messageIDTopic(messageID))
				}

				return sse.Subscription{
					Client:      s,
					LastEventID: s.LastEventID,
					Topics:      topics,
				}, true
			},
		},
		templates:      tmpl,
		llm:            llm,
		titleGenerator: titleGen,
		store:          store,
		mcpClients:     mcpClients,
		promptsMap:     pm,
		resourcesMap:   rm,
		toolsMap:       tm,
		logger:         logger.With(slog.String("module", "main")),
		servers:        servers,
		tools:          tools,
		resources:      resources,
		prompts:        prompts,
	}, nil
}

func messageIDTopic(messageID string) string {
	return fmt.Sprintf("message-%s", messageID)
}

// Shutdown gracefully terminates the Main instance's SSE server. It broadcasts a close message to all
// connected clients and waits up to 5 seconds for connections to terminate. After the timeout, any
// remaining connections are forcefully closed.
func (m Main) Shutdown(ctx context.Context) error {
	e := &sse.Message{Type: sse.Type("closeChat")}
	// We create a close event that complies with SSE spec requiring data
	e.AppendData("bye")

	// We ignore the error here since we're shutting down anyway
	_ = m.sseSrv.Publish(e)

	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	return m.sseSrv.Shutdown(ctx)
}
