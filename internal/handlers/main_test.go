package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/handlers"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
)

type mockLLM struct {
	responses []string
	err       error
}

type mockStore struct {
	sync.Mutex
	chats    []models.Chat
	messages map[string][]models.Message
	err      error
}

type mockMCPClient struct {
	serverInfo              mcp.Info
	toolServerSupported     bool
	resourceServerSupported bool
	promptServerSupported   bool

	tools     []mcp.Tool
	resources []mcp.Resource
	prompts   []mcp.Prompt

	getPromptResult  mcp.GetPromptResult
	callToolResult   mcp.CallToolResult
	readResourceFunc func(uri string) (mcp.ReadResourceResult, error)

	err error
}

func TestNewMain(t *testing.T) {
	llm := &mockLLM{}
	store := &mockStore{}
	mcpClient := &mockMCPClient{
		serverInfo: mcp.Info{
			Name: "Test Server",
		},
	}

	main, err := handlers.NewMain(llm, llm, store, []handlers.MCPClient{mcpClient}, slog.Default())
	if err != nil {
		t.Fatalf("NewMain() error = %v", err)
	}

	if main.Shutdown(context.Background()) != nil {
		t.Error("Shutdown() should not return error")
	}
}

func TestHandleHome(t *testing.T) {
	llm := &mockLLM{}
	store := &mockStore{
		chats: []models.Chat{
			{ID: "1", Title: "Test Chat"},
		},
		messages: map[string][]models.Message{
			"1": {{ID: "1", Role: "user", Contents: []models.Content{
				{
					Type: models.ContentTypeText,
					Text: "Hello",
				},
			}}},
		},
	}
	mcpClient := &mockMCPClient{
		serverInfo: mcp.Info{
			Name: "Test Server",
		},
	}

	main, err := handlers.NewMain(llm, llm, store, []handlers.MCPClient{mcpClient}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		url        string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "Home page without chat",
			url:        "/",
			wantStatus: http.StatusOK,
			wantBody:   "Test Chat", // Should contain chat title
		},
		{
			name:       "Home page with chat",
			url:        "/?chat_id=1",
			wantStatus: http.StatusOK,
			wantBody:   "Hello", // Should contain message content
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			w := httptest.NewRecorder()

			main.HandleHome(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("HandleHome() status = %v, want %v", w.Code, tt.wantStatus)
			}

			if !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Errorf("HandleHome() body = %v, want to contain %v", w.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestHandleChats(t *testing.T) {
	llm := &mockLLM{responses: []string{"AI response"}}
	store := &mockStore{
		messages: map[string][]models.Message{},
	}

	// Setup MCP client with prompt and resource support
	mcpClient := &mockMCPClient{
		serverInfo: mcp.Info{
			Name: "Test Server",
		},
		promptServerSupported: true,
		prompts: []mcp.Prompt{
			{Name: "test_prompt"},
		},
		getPromptResult: mcp.GetPromptResult{
			Messages: []mcp.PromptMessage{
				{
					Role: "user",
					Content: mcp.Content{
						Type: mcp.ContentTypeText,
						Text: "Prompt generated text",
					},
				},
			},
		},
		toolServerSupported: true,
		tools: []mcp.Tool{
			{Name: "test_tool"},
		},
		callToolResult: mcp.CallToolResult{
			Content: []mcp.Content{
				{
					Type: mcp.ContentTypeText,
					Text: "Tool execution result",
				},
			},
			IsError: false,
		},
		resourceServerSupported: true,
		resources: []mcp.Resource{
			{URI: "file:///test.txt"},
			{URI: "workspace:///sample.go"},
		},
		readResourceFunc: func(uri string) (mcp.ReadResourceResult, error) {
			switch uri {
			case "file:///test.txt":
				return mcp.ReadResourceResult{
					Contents: []mcp.ResourceContents{
						{
							URI:      uri,
							MimeType: "text/plain",
							Text:     "This is a test file",
						},
					},
				}, nil
			case "workspace:///sample.go":
				return mcp.ReadResourceResult{
					Contents: []mcp.ResourceContents{
						{
							URI:      uri,
							MimeType: "text/x-go",
							Text:     "package main\n\nfunc main() {\n\tfmt.Println(\"Hello\")\n}",
						},
					},
				}, nil
			case "error:///resource":
				return mcp.ReadResourceResult{}, fmt.Errorf("failed to read resource")
			default:
				return mcp.ReadResourceResult{}, fmt.Errorf("resource not found")
			}
		},
	}

	tests := []struct {
		name       string
		method     string
		formData   string
		store      *mockStore
		llm        *mockLLM
		wantStatus int
	}{
		{
			name:       "Invalid method",
			method:     http.MethodGet,
			formData:   "",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "Empty message and no prompt",
			method:     http.MethodPost,
			formData:   "chat_id=",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "New chat with message",
			method:     http.MethodPost,
			formData:   "message=Hello",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Existing chat with message",
			method:     http.MethodPost,
			formData:   "message=Hello&chat_id=1",
			wantStatus: http.StatusOK,
		},
		// Testing prompt functionality
		{
			name:       "Invalid prompt arguments",
			method:     http.MethodPost,
			formData:   `prompt_name=test_prompt&prompt_args=invalid_json`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "Valid prompt with empty args",
			method:     http.MethodPost,
			formData:   `prompt_name=test_prompt&prompt_args={}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "Prompt not found",
			method:     http.MethodPost,
			formData:   `prompt_name=unknown_prompt&prompt_args={"key":"value"}`,
			wantStatus: http.StatusInternalServerError,
		},
		// Resource handling test cases
		{
			name:       "Message with valid attached resources",
			method:     http.MethodPost,
			formData:   `message=Check these files&attached_resources=["file:///test.txt","workspace:///sample.go"]`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "Invalid JSON in attached resources",
			method:     http.MethodPost,
			formData:   `message=Bad JSON&attached_resources=[invalid"json]`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Resource not found",
			method:     http.MethodPost,
			formData:   `message=Missing resource&attached_resources=["unknown:///file.txt"]`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "Error reading resource",
			method:     http.MethodPost,
			formData:   `message=Error case&attached_resources=["error:///resource"]`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "Empty attached resources array",
			method:     http.MethodPost,
			formData:   `message=No attachments&attached_resources=[]`,
			wantStatus: http.StatusOK,
		},
		// Test cases for error paths
		{
			name:       "Store error when adding message",
			method:     http.MethodPost,
			formData:   "message=Hello",
			store:      &mockStore{err: fmt.Errorf("database error")},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:     "Continue chat with pending tool call",
			method:   http.MethodPost,
			formData: "chat_id=existing-chat&message=Hello",
			store: &mockStore{
				messages: map[string][]models.Message{
					"existing-chat": {
						{
							ID:   "last-msg",
							Role: models.RoleAssistant,
							Contents: []models.Content{
								{
									Type:       models.ContentTypeCallTool,
									ToolName:   "test_tool",
									ToolInput:  json.RawMessage(`{"param":"value"}`),
									CallToolID: "tool-call-1",
								},
							},
						},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:     "Tool not found",
			method:   http.MethodPost,
			formData: "chat_id=existing-chat&message=Hello",
			store: &mockStore{
				messages: map[string][]models.Message{
					"existing-chat": {
						{
							ID:   "last-msg",
							Role: models.RoleAssistant,
							Contents: []models.Content{
								{
									Type:       models.ContentTypeCallTool,
									ToolName:   "unknown_tool", // Tool that doesn't exist
									ToolInput:  json.RawMessage(`{"param":"value"}`),
									CallToolID: "tool-call-1",
								},
							},
						},
					},
				},
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use custom store and LLM if provided in the test case
			currentStore := store
			if tt.store != nil {
				currentStore = tt.store
			}

			currentLLM := llm
			if tt.llm != nil {
				currentLLM = tt.llm
			}

			testMain, err := handlers.NewMain(currentLLM, currentLLM, currentStore,
				[]handlers.MCPClient{mcpClient}, slog.Default())
			if err != nil {
				t.Fatal(err)
			}

			form := strings.NewReader(tt.formData)
			req := httptest.NewRequest(tt.method, "/chat", form)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			testMain.HandleChats(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("HandleChats() status = %v, want %v", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestHandleRefreshTitle(t *testing.T) {
	// Test success case first
	t.Run("Success", func(t *testing.T) {
		llm := &mockLLM{}
		store := &mockStore{
			chats: []models.Chat{
				{ID: "1", Title: "Old Title"},
			},
			messages: map[string][]models.Message{
				"1": {
					{
						ID:   "msg1",
						Role: models.RoleUser,
						Contents: []models.Content{
							{
								Type: models.ContentTypeText,
								Text: "First user message",
							},
						},
					},
				},
			},
		}
		mcpClient := &mockMCPClient{
			serverInfo: mcp.Info{
				Name: "Test Server",
			},
		}

		main, err := handlers.NewMain(llm, llm, store, []handlers.MCPClient{mcpClient}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}

		form := strings.NewReader("chat_id=1")
		req := httptest.NewRequest(http.MethodPost, "/refresh-title", form)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()

		main.HandleRefreshTitle(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("HandleRefreshTitle() status = %v, want %v", w.Code, http.StatusOK)
		}

		if !strings.Contains(w.Body.String(), "Test Chat") {
			t.Errorf("HandleRefreshTitle() body = %v, want to contain %v", w.Body.String(), "Test Chat")
		}

		// Verify chat title was updated
		if store.chats[0].Title != "Test Chat" {
			t.Errorf("Chat title not updated, got %s, want %s", store.chats[0].Title, "Test Chat")
		}
	})

	// Test various error cases
	tests := []struct {
		name        string
		method      string
		chatID      string
		messages    map[string][]models.Message
		err         error
		titleGenErr bool
		wantStatus  int
	}{
		{
			name:       "Invalid method",
			method:     http.MethodGet,
			chatID:     "1",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "Missing chat_id",
			method:     http.MethodPost,
			chatID:     "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "No messages",
			method:     http.MethodPost,
			chatID:     "1",
			messages:   map[string][]models.Message{"1": {}},
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "No user messages",
			method: http.MethodPost,
			chatID: "1",
			messages: map[string][]models.Message{
				"1": {
					{
						ID:   "msg3",
						Role: models.RoleAssistant,
						Contents: []models.Content{
							{
								Type: models.ContentTypeText,
								Text: "Assistant message",
							},
						},
					},
				},
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:   "Store error",
			method: http.MethodPost,
			chatID: "1",
			messages: map[string][]models.Message{
				"1": {
					{
						ID:   "msg1",
						Role: models.RoleUser,
						Contents: []models.Content{
							{
								Type: models.ContentTypeText,
								Text: "Hello",
							},
						},
					},
				},
			},
			err:        fmt.Errorf("store error"),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:   "Title generator error",
			method: http.MethodPost,
			chatID: "1",
			messages: map[string][]models.Message{
				"1": {
					{
						ID:   "msg1",
						Role: models.RoleUser,
						Contents: []models.Content{
							{
								Type: models.ContentTypeText,
								Text: "Hello",
							},
						},
					},
				},
			},
			titleGenErr: true,
			wantStatus:  http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llm := &mockLLM{}
			if tt.titleGenErr {
				llm = &mockLLM{
					err: fmt.Errorf("title generation failed"),
				}
			}

			store := &mockStore{
				chats:    []models.Chat{{ID: "1", Title: "Old Title"}},
				messages: tt.messages,
				err:      tt.err,
			}
			mcpClient := &mockMCPClient{
				serverInfo: mcp.Info{
					Name: "Test Server",
				},
			}

			main, err := handlers.NewMain(llm, llm, store, []handlers.MCPClient{mcpClient}, slog.Default())
			if err != nil {
				t.Fatal(err)
			}

			form := strings.NewReader("chat_id=" + tt.chatID)
			req := httptest.NewRequest(tt.method, "/refresh-title", form)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			main.HandleRefreshTitle(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("HandleRefreshTitle() status = %v, want %v", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestMCPToolInteractions(t *testing.T) {
	// Test tool call functionality
	llm := &mockLLM{
		responses: []string{"I'll use a tool"},
	}
	store := &mockStore{
		messages: map[string][]models.Message{},
	}

	// Setup MCP client with tool support
	mcpClient := &mockMCPClient{
		serverInfo: mcp.Info{
			Name: "Test Server",
		},
		toolServerSupported: true,
		tools: []mcp.Tool{
			{Name: "test_tool"},
		},
		callToolResult: mcp.CallToolResult{
			Content: []mcp.Content{
				{
					Type: mcp.ContentTypeText,
					Text: "Tool execution result",
				},
			},
			IsError: false,
		},
	}

	main, err := handlers.NewMain(llm, llm, store, []handlers.MCPClient{mcpClient}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// Create a chat with a message that will trigger tool call
	form := strings.NewReader("message=Use the test_tool")
	req := httptest.NewRequest(http.MethodPost, "/chat", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	main.HandleChats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleChats() status = %v, want %v", w.Code, http.StatusOK)
	}
}

func (m mockLLM) Chat(_ context.Context, _ []models.Message, _ []mcp.Tool) iter.Seq2[models.Content, error] {
	return func(yield func(models.Content, error) bool) {
		if m.err != nil {
			yield(models.Content{}, m.err)
			return
		}
		for _, resp := range m.responses {
			if !yield(models.Content{
				Type: models.ContentTypeText,
				Text: resp,
			}, nil) {
				return
			}
		}
	}
}

func (m mockLLM) GenerateTitle(_ context.Context, _ string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return "Test Chat", nil
}

func (m *mockStore) Chats(_ context.Context) ([]models.Chat, error) {
	m.Lock()
	defer m.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	// Return a copy to avoid race conditions on the slice
	chatsCopy := make([]models.Chat, len(m.chats))
	copy(chatsCopy, m.chats)
	return chatsCopy, nil
}

func (m *mockStore) AddChat(_ context.Context, chat models.Chat) (string, error) {
	m.Lock()
	defer m.Unlock()
	if m.err != nil {
		return "", m.err
	}
	m.chats = append(m.chats, chat)
	return chat.ID, nil
}

func (m *mockStore) UpdateChat(_ context.Context, chat models.Chat) error {
	m.Lock()
	defer m.Unlock()
	idx := slices.IndexFunc(m.chats, func(c models.Chat) bool { return c.ID == chat.ID })
	if idx == -1 {
		return fmt.Errorf("chat not found")
	}
	m.chats[idx] = chat
	return m.err
}

func (m *mockStore) Messages(_ context.Context, chatID string) ([]models.Message, error) {
	m.Lock()
	defer m.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	// Return a copy to avoid race conditions on the slice
	messagesCopy := make([]models.Message, len(m.messages[chatID]))
	copy(messagesCopy, m.messages[chatID])
	return messagesCopy, nil
}

func (m *mockStore) AddMessage(_ context.Context, chatID string, msg models.Message) (string, error) {
	m.Lock()
	defer m.Unlock()
	if m.err != nil {
		return "", m.err
	}
	m.messages[chatID] = append(m.messages[chatID], msg)
	return msg.ID, nil
}

func (m *mockStore) UpdateMessage(_ context.Context, chatID string, msg models.Message) error {
	m.Lock()
	defer m.Unlock()
	if m.err != nil {
		return m.err
	}

	// Find and update the message
	for i, existingMsg := range m.messages[chatID] {
		if existingMsg.ID == msg.ID {
			m.messages[chatID][i] = msg
			return nil
		}
	}

	// If no matching message found, add it
	m.messages[chatID] = append(m.messages[chatID], msg)
	return nil
}

func (m *mockMCPClient) ServerInfo() mcp.Info {
	return m.serverInfo
}

func (m *mockMCPClient) ToolServerSupported() bool {
	return m.toolServerSupported
}

func (m *mockMCPClient) ResourceServerSupported() bool {
	return m.resourceServerSupported
}

func (m *mockMCPClient) PromptServerSupported() bool {
	return m.promptServerSupported
}

func (m *mockMCPClient) ListTools(_ context.Context, _ mcp.ListToolsParams) (mcp.ListToolsResult, error) {
	if m.err != nil {
		return mcp.ListToolsResult{}, m.err
	}
	return mcp.ListToolsResult{Tools: m.tools}, nil
}

func (m *mockMCPClient) ListResources(_ context.Context, _ mcp.ListResourcesParams) (mcp.ListResourcesResult, error) {
	if m.err != nil {
		return mcp.ListResourcesResult{}, m.err
	}
	return mcp.ListResourcesResult{Resources: m.resources}, nil
}

func (m *mockMCPClient) ReadResource(_ context.Context, params mcp.ReadResourceParams) (mcp.ReadResourceResult, error) {
	if m.err != nil {
		return mcp.ReadResourceResult{}, m.err
	}

	if m.readResourceFunc != nil {
		return m.readResourceFunc(params.URI)
	}

	return mcp.ReadResourceResult{
		Contents: []mcp.ResourceContents{
			{
				URI:      params.URI,
				MimeType: "text/plain",
				Text:     "Mock resource content",
			},
		},
	}, nil
}

func (m *mockMCPClient) ListPrompts(_ context.Context, _ mcp.ListPromptsParams) (mcp.ListPromptResult, error) {
	if m.err != nil {
		return mcp.ListPromptResult{}, m.err
	}
	return mcp.ListPromptResult{Prompts: m.prompts}, nil
}

func (m *mockMCPClient) GetPrompt(_ context.Context, _ mcp.GetPromptParams) (mcp.GetPromptResult, error) {
	if m.err != nil {
		return mcp.GetPromptResult{}, m.err
	}
	return m.getPromptResult, nil
}

func (m *mockMCPClient) CallTool(_ context.Context, _ mcp.CallToolParams) (mcp.CallToolResult, error) {
	if m.err != nil {
		return mcp.CallToolResult{}, m.err
	}
	return m.callToolResult, nil
}
