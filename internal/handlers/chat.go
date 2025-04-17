package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
	"github.com/google/uuid"
	"github.com/tmaxmax/go-sse"
)

type chat struct {
	ID    string
	Title string

	Active bool
}

type message struct {
	ID        string
	Role      string
	Content   string
	Timestamp time.Time

	StreamingState string
}

// SSE event types for real-time updates.
var (
	chatsSSEType    = sse.Type("chats")
	messagesSSEType = sse.Type("messages")
)

func callToolError(err error) json.RawMessage {
	contents := []mcp.Content{
		{
			Type: mcp.ContentTypeText,
			Text: err.Error(),
		},
	}

	res, _ := json.Marshal(contents)
	return res
}

// HandleChats processes chat interactions through HTTP POST requests,
// managing both new chat creation and message handling. It supports three input methods:
// 1. Regular messages via the "message" form field
// 2. Predefined prompts via "prompt_name" and "prompt_args" form fields
// 3. Attached resources via the "attached_resources" JSON array of resource URIs
//
// When resources are attached, they're processed and appended to the latest user message.
// Resources are retrieved from registered MCP clients based on their URIs.
//
// The handler expects an optional "chat_id" field. If no chat_id is provided,
// it creates a new chat session. For new chats, it asynchronously generates a title
// based on the first message or prompt.
//
// The function handles different rendering strategies based on whether it's a new chat
// (complete chatbox template) or an existing chat (individual message templates). For
// all chats, it adds messages to the database and initiates asynchronous AI response
// generation that will be streamed via Server-Sent Events (SSE).
//
// The function returns appropriate HTTP error responses for invalid methods, missing required fields,
// resource processing failures, or internal processing errors. For successful requests, it renders
// the appropriate templates with messages marked with correct streaming states.
func (m Main) HandleChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		m.logger.Error("Method not allowed", slog.String("method", r.Method))
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var err error
	chatID := r.FormValue("chat_id")
	isNewChat := false

	if chatID == "" {
		chatID, err = m.newChat()
		if err != nil {
			m.logger.Error("Failed to create new chat", slog.String(errLoggerKey, err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		isNewChat = true
	} else {
		if err := m.continueChat(r.Context(), chatID); err != nil {
			m.logger.Error("Failed to continue chat", slog.String(errLoggerKey, err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var userMessages []models.Message
	var addedMessageIDs []string
	var firstMessageForTitle string

	// Process input based on type (prompt or regular message)
	promptName := r.FormValue("prompt_name")
	if promptName != "" {
		// Handle prompt-based input
		promptArgs := r.FormValue("prompt_args")
		userMessages, firstMessageForTitle, err = m.processPromptInput(r.Context(), promptName, promptArgs)
		if err != nil {
			m.logger.Error("Failed to process prompt",
				slog.String("promptName", promptName),
				slog.String(errLoggerKey, err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// Handle regular message input
		msg := r.FormValue("message")
		if msg == "" {
			m.logger.Error("Message is required")
			http.Error(w, "Message is required", http.StatusBadRequest)
			return
		}

		firstMessageForTitle = msg
		userMessages = []models.Message{m.processUserMessage(msg)}
	}

	// Handle attached resources
	attachedResourcesJSON := r.FormValue("attached_resources")
	if attachedResourcesJSON != "" && attachedResourcesJSON != "[]" {
		var resourceURIs []string
		if err := json.Unmarshal([]byte(attachedResourcesJSON), &resourceURIs); err != nil {
			m.logger.Error("Failed to unmarshal attached resources",
				slog.String("attachedResources", attachedResourcesJSON),
				slog.String(errLoggerKey, err.Error()))
			http.Error(w, "Invalid attached resources format", http.StatusBadRequest)
			return
		}

		// Process resources and add resource contents to user message
		if len(resourceURIs) > 0 {
			resourceContents, err := m.processAttachedResources(r.Context(), resourceURIs)
			if err != nil {
				m.logger.Error("Failed to process attached resources",
					slog.String("resourceURIs", fmt.Sprintf("%v", resourceURIs)),
					slog.String(errLoggerKey, err.Error()))
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			// Add resource contents to the last user message
			if len(userMessages) > 0 {
				lastMsgIdx := len(userMessages) - 1
				userMessages[lastMsgIdx].Contents = append(userMessages[lastMsgIdx].Contents, resourceContents...)
			}
		}
	}

	// Add all user messages to the chat
	for _, msg := range userMessages {
		msgID, err := m.store.AddMessage(r.Context(), chatID, msg)
		if err != nil {
			m.logger.Error("Failed to add message",
				slog.String("message", fmt.Sprintf("%+v", msg)),
				slog.String(errLoggerKey, err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		addedMessageIDs = append(addedMessageIDs, msgID)
	}

	// Initialize empty AI message to be streamed later
	am := models.Message{
		ID:        uuid.New().String(),
		Role:      models.RoleAssistant,
		Timestamp: time.Now(),
	}
	aiMsgID, err := m.store.AddMessage(r.Context(), chatID, am)
	if err != nil {
		m.logger.Error("Failed to add AI message",
			slog.String("message", fmt.Sprintf("%+v", am)),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	messages, err := m.store.Messages(r.Context(), chatID)
	if err != nil {
		m.logger.Error("Failed to get messages",
			slog.String("chatID", chatID),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Start async processes for chat response and title generation
	go m.chat(chatID, messages)

	if isNewChat {
		go m.generateChatTitle(chatID, firstMessageForTitle)
		m.renderNewChatResponse(w, chatID, messages, aiMsgID)
		return
	}

	// For existing chats, render each message separately
	m.renderExistingChatResponse(w, messages, addedMessageIDs, am, aiMsgID)
}

// HandleRefreshTitle handles requests to regenerate a chat title. It accepts POST requests with a chat_id
// parameter, retrieves the first user message from the chat history, and uses the title generator to create
// a new title. The handler updates the chat title in the database and returns the new title to be displayed.
//
// The function expects a "chat_id" form field identifying which chat's title should be refreshed.
// After updating the database, it asynchronously notifies all connected clients through Server-Sent Events (SSE)
// to maintain UI consistency across sessions while immediately returning the new title text to the requesting client.
//
// The function returns appropriate HTTP error responses for invalid methods, missing required fields,
// or when no messages are found for title generation. On success, it returns just the title text to be
// inserted into the targeted span element via HTMX.
func (m Main) HandleRefreshTitle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		m.logger.Error("Method not allowed", slog.String("method", r.Method))
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	chatID := r.FormValue("chat_id")
	if chatID == "" {
		m.logger.Error("Chat ID is required")
		http.Error(w, "Chat ID is required", http.StatusBadRequest)
		return
	}

	// Get messages to find first user message
	messages, err := m.store.Messages(r.Context(), chatID)
	if err != nil {
		m.logger.Error("Failed to get messages",
			slog.String("chatID", chatID),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(messages) == 0 {
		m.logger.Error("No messages found for chat", slog.String("chatID", chatID))
		http.Error(w, "No messages found for chat", http.StatusNotFound)
		return
	}

	// Find first user message for title generation
	var firstUserMessage string
	for _, msg := range messages {
		if msg.Role == models.RoleUser && len(msg.Contents) > 0 && msg.Contents[0].Type == models.ContentTypeText {
			firstUserMessage = msg.Contents[0].Text
			break
		}
	}

	if firstUserMessage == "" {
		m.logger.Error("No user message found for title generation", slog.String("chatID", chatID))
		http.Error(w, "No user message found for title generation", http.StatusInternalServerError)
		return
	}

	// Generate and update title
	title, err := m.titleGenerator.GenerateTitle(r.Context(), firstUserMessage)
	if err != nil {
		m.logger.Error("Error generating chat title",
			slog.String("message", firstUserMessage),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, "Failed to generate title", http.StatusInternalServerError)
		return
	}

	updatedChat := models.Chat{
		ID:    chatID,
		Title: title,
	}
	if err := m.store.UpdateChat(r.Context(), updatedChat); err != nil {
		m.logger.Error("Failed to update chat title",
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, "Failed to update chat title", http.StatusInternalServerError)
		return
	}

	// Update all clients via SSE asynchronously
	go func() {
		divs, err := m.chatDivs(chatID)
		if err != nil {
			m.logger.Error("Failed to generate chat divs",
				slog.String(errLoggerKey, err.Error()))
			return
		}

		msg := sse.Message{
			Type: chatsSSEType,
		}
		msg.AppendData(divs)
		if err := m.sseSrv.Publish(&msg, chatsSSETopic); err != nil {
			m.logger.Error("Failed to publish chats",
				slog.String(errLoggerKey, err.Error()))
		}
	}()

	// Return just the title text for HTMX to insert into the span
	fmt.Fprintf(w, "%s", title)
}

// processPromptInput handles prompt-based inputs, extracting arguments and retrieving
// prompt messages from the MCP client.
func (m Main) processPromptInput(ctx context.Context, promptName, promptArgs string) ([]models.Message, string, error) {
	var args map[string]string
	if err := json.Unmarshal([]byte(promptArgs), &args); err != nil {
		return nil, "", fmt.Errorf("invalid prompt arguments: %w", err)
	}

	// Get the prompt data directly from the server
	clientIdx, ok := m.promptsMap[promptName]
	if !ok {
		return nil, "", fmt.Errorf("prompt not found: %s", promptName)
	}

	promptResult, err := m.mcpClients[clientIdx].GetPrompt(ctx, mcp.GetPromptParams{
		Name:      promptName,
		Arguments: args,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to get prompt: %w", err)
	}

	// Convert prompt messages to our internal model format
	messages := make([]models.Message, 0, len(promptResult.Messages))
	firstMessageText := ""

	for i, promptMsg := range promptResult.Messages {
		// For now, ignore non-text content
		if promptMsg.Content.Type != mcp.ContentTypeText {
			continue
		}
		content := promptMsg.Content.Text

		// Save the first message for title generation
		if i == 0 {
			firstMessageText = content
		}

		messages = append(messages, models.Message{
			ID:   uuid.New().String(),
			Role: models.Role(promptMsg.Role),
			Contents: []models.Content{
				{
					Type: models.ContentTypeText,
					Text: content,
				},
			},
			Timestamp: time.Now(),
		})
	}

	return messages, firstMessageText, nil
}

// processUserMessage handles standard user message inputs.
func (m Main) processUserMessage(message string) models.Message {
	return models.Message{
		ID:   uuid.New().String(),
		Role: models.RoleUser,
		Contents: []models.Content{
			{
				Type: models.ContentTypeText,
				Text: message,
			},
		},
		Timestamp: time.Now(),
	}
}

// processAttachedResources processes attached resource URIs from the form data
// and returns content objects for each resource.
func (m Main) processAttachedResources(ctx context.Context, resourceURIs []string) ([]models.Content, error) {
	var contents []models.Content

	for _, uri := range resourceURIs {
		clientIdx, ok := m.resourcesMap[uri]
		if !ok {
			return nil, fmt.Errorf("resource not found: %s", uri)
		}

		result, err := m.mcpClients[clientIdx].ReadResource(ctx, mcp.ReadResourceParams{
			URI: uri,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to read resource %s: %w", uri, err)
		}

		contents = append(contents, models.Content{
			Type:             models.ContentTypeResource,
			ResourceContents: result.Contents,
		})
	}

	return contents, nil
}

// renderNewChatResponse renders the complete chatbox for new chats.
func (m Main) renderNewChatResponse(w http.ResponseWriter, chatID string, messages []models.Message, aiMsgID string) {
	msgs := make([]message, len(messages))
	for i := range messages {
		// Mark only the AI message as "loading", others as "ended"
		streamingState := "ended"
		if messages[i].ID == aiMsgID {
			streamingState = "loading"
		}
		content, err := models.RenderContents(messages[i].Contents)
		if err != nil {
			m.logger.Error("Failed to render contents",
				slog.String("message", fmt.Sprintf("%+v", messages[i])),
				slog.String(errLoggerKey, err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		msgs[i] = message{
			ID:             messages[i].ID,
			Role:           string(messages[i].Role),
			Content:        content,
			Timestamp:      messages[i].Timestamp,
			StreamingState: streamingState,
		}
	}

	data := homePageData{
		CurrentChatID: chatID,
		Messages:      msgs,
	}
	if err := m.templates.ExecuteTemplate(w, "chatbox", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderExistingChatResponse renders each message individually for existing chats.
func (m Main) renderExistingChatResponse(w http.ResponseWriter, messages []models.Message, addedMessageIDs []string,
	aiMessage models.Message, aiMsgID string,
) {
	for _, msgID := range addedMessageIDs {
		for i := range messages {
			if messages[i].ID == msgID {
				content, err := models.RenderContents(messages[i].Contents)
				if err != nil {
					m.logger.Error("Failed to render contents",
						slog.String("message", fmt.Sprintf("%+v", messages[i])),
						slog.String(errLoggerKey, err.Error()))
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}

				templateName := "user_message"
				if messages[i].Role == models.RoleAssistant {
					templateName = "ai_message"
				}

				if err := m.templates.ExecuteTemplate(w, templateName, message{
					ID:             msgID,
					Role:           string(messages[i].Role),
					Content:        content,
					Timestamp:      messages[i].Timestamp,
					StreamingState: "ended",
				}); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				break
			}
		}
	}

	// Render AI response message (always the last one added)
	aiContent, err := models.RenderContents(aiMessage.Contents)
	if err != nil {
		m.logger.Error("Failed to render contents",
			slog.String("message", fmt.Sprintf("%+v", aiMessage)),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := m.templates.ExecuteTemplate(w, "ai_message", message{
		ID:             aiMsgID,
		Role:           string(aiMessage.Role),
		Content:        aiContent,
		Timestamp:      aiMessage.Timestamp,
		StreamingState: "loading",
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (m Main) newChat() (string, error) {
	newChat := models.Chat{
		ID: uuid.New().String(),
	}
	newChatID, err := m.store.AddChat(context.Background(), newChat)
	if err != nil {
		return "", fmt.Errorf("failed to add chat: %w", err)
	}
	newChat.ID = newChatID

	divs, err := m.chatDivs(newChat.ID)
	if err != nil {
		return "", fmt.Errorf("failed to create chat divs: %w", err)
	}

	msg := sse.Message{
		Type: chatsSSEType,
	}
	msg.AppendData(divs)

	if err := m.sseSrv.Publish(&msg, chatsSSETopic); err != nil {
		return "", fmt.Errorf("failed to publish chats: %w", err)
	}

	return newChat.ID, nil
}

// continueChat continues chat with given chatID.
//
// If the last content of the last message is not a CallTool type, it will do nothing.
// But if it is, as it may happen due to the corrupted data, this function will call the tool,
// then append the result to the chat.
func (m Main) continueChat(ctx context.Context, chatID string) error {
	messages, err := m.store.Messages(ctx, chatID)
	if err != nil {
		return fmt.Errorf("failed to get messages: %w", err)
	}

	if len(messages) == 0 {
		return nil
	}

	lastMessage := messages[len(messages)-1]

	if lastMessage.Role != models.RoleAssistant {
		return nil
	}

	if len(lastMessage.Contents) == 0 {
		return nil
	}

	if lastMessage.Contents[len(lastMessage.Contents)-1].Type != models.ContentTypeCallTool {
		return nil
	}

	toolRes, success := m.callTool(mcp.CallToolParams{
		Name:      lastMessage.Contents[len(lastMessage.Contents)-1].ToolName,
		Arguments: lastMessage.Contents[len(lastMessage.Contents)-1].ToolInput,
	})

	lastMessage.Contents = append(lastMessage.Contents, models.Content{
		Type:       models.ContentTypeToolResult,
		CallToolID: lastMessage.Contents[len(lastMessage.Contents)-1].CallToolID,
	})

	lastMessage.Contents[len(lastMessage.Contents)-1].ToolResult = toolRes
	lastMessage.Contents[len(lastMessage.Contents)-1].CallToolFailed = !success

	err = m.store.UpdateMessage(ctx, chatID, lastMessage)
	if err != nil {
		return fmt.Errorf("failed to update message: %w", err)
	}

	return nil
}

func (m Main) callTool(params mcp.CallToolParams) (json.RawMessage, bool) {
	clientIdx, ok := m.toolsMap[params.Name]
	if !ok {
		m.logger.Error("Tool not found", slog.String("toolName", params.Name))
		return callToolError(fmt.Errorf("tool %s is not found", params.Name)), false
	}

	toolRes, err := m.mcpClients[clientIdx].CallTool(context.Background(), params)
	if err != nil {
		m.logger.Error("Tool call failed",
			slog.String("toolName", params.Name),
			slog.String(errLoggerKey, err.Error()))
		return callToolError(fmt.Errorf("tool call failed: %w", err)), false
	}

	resContent, err := json.Marshal(toolRes.Content)
	if err != nil {
		m.logger.Error("Failed to marshal tool result content",
			slog.String("toolName", params.Name),
			slog.String(errLoggerKey, err.Error()))
		return callToolError(fmt.Errorf("failed to marshal content: %w", err)), false
	}

	m.logger.Debug("Tool result content",
		slog.String("toolName", params.Name),
		slog.String("toolResult", string(resContent)))

	return resContent, !toolRes.IsError
}

func (m Main) chat(chatID string, messages []models.Message) {
	// Ensure SSE connection cleanup on function exit
	defer func() {
		e := &sse.Message{Type: sse.Type("closeMessage")}
		e.AppendData("bye")
		_ = m.sseSrv.Publish(e)
	}()

	aiMsg := messages[len(messages)-1]
	contentIdx := -1

	for {
		it := m.llm.Chat(context.Background(), messages, m.tools)
		aiMsg.Contents = append(aiMsg.Contents, models.Content{
			Type: models.ContentTypeText,
			Text: "",
		})
		contentIdx++
		callTool := false
		badToolInputFlag := false
		badToolInput := json.RawMessage("{}")

		for content, err := range it {
			msg := sse.Message{
				Type: messagesSSEType,
			}
			if err != nil {
				m.logger.Error("Error from llm provider", slog.String(errLoggerKey, err.Error()))
				msg.AppendData(err.Error())
				_ = m.sseSrv.Publish(&msg, messageIDTopic(aiMsg.ID))
				return
			}

			m.logger.Debug("LLM response", slog.String("content", fmt.Sprintf("%+v", content)))

			switch content.Type {
			case models.ContentTypeText:
				aiMsg.Contents[contentIdx].Text += content.Text
			case models.ContentTypeCallTool:
				// Non-anthropic models sometimes give a bad tool input which can't be json-marshalled, and it would lead to failure
				// when the store try to save the message. So we check if the tool input is valid json, and if not, we set a flag
				// to inform the models that the tool input is invalid. And to avoid save failure, we change the tool input to
				// empty json string.
				_, err := json.Marshal(content.ToolInput)
				if err != nil {
					badToolInputFlag = true
					badToolInput = content.ToolInput
					content.ToolInput = []byte("{}")
				}
				callTool = true
				aiMsg.Contents = append(aiMsg.Contents, content)
				contentIdx++
			case models.ContentTypeResource:
				m.logger.Error("Content type resource is not allowed")
				return
			case models.ContentTypeToolResult:
				m.logger.Error("Content type tool results is not allowed")
				return
			}

			if err := m.store.UpdateMessage(context.Background(), chatID, aiMsg); err != nil {
				m.logger.Error("Failed to update message",
					slog.String("message", fmt.Sprintf("%+v", aiMsg)),
					slog.String(errLoggerKey, err.Error()))
				return
			}

			rc, err := models.RenderContents(aiMsg.Contents)
			if err != nil {
				m.logger.Error("Failed to render contents",
					slog.String("message", fmt.Sprintf("%+v", aiMsg)),
					slog.String(errLoggerKey, err.Error()))
				return
			}
			m.logger.Debug("Render contents",
				slog.String("origMsg", fmt.Sprintf("%+v", aiMsg.Contents)),
				slog.String("renderedMsg", rc))
			msg.AppendData(rc)
			if err := m.sseSrv.Publish(&msg, messageIDTopic(aiMsg.ID)); err != nil {
				m.logger.Error("Failed to publish message",
					slog.String("message", fmt.Sprintf("%+v", aiMsg)),
					slog.String(errLoggerKey, err.Error()))
				return
			}

			if callTool {
				break
			}
		}

		if !callTool {
			break
		}

		callToolContent := aiMsg.Contents[len(aiMsg.Contents)-1]

		toolResContent := models.Content{
			Type:       models.ContentTypeToolResult,
			CallToolID: callToolContent.CallToolID,
		}

		if badToolInputFlag {
			toolResContent.ToolResult = callToolError(fmt.Errorf("tool input %s is not valid json", string(badToolInput)))
			toolResContent.CallToolFailed = true
			aiMsg.Contents = append(aiMsg.Contents, toolResContent)
			contentIdx++
			messages[len(messages)-1] = aiMsg
			continue
		}

		toolResult, success := m.callTool(mcp.CallToolParams{
			Name:      callToolContent.ToolName,
			Arguments: callToolContent.ToolInput,
		})

		toolResContent.ToolResult = toolResult
		toolResContent.CallToolFailed = !success
		aiMsg.Contents = append(aiMsg.Contents, toolResContent)
		contentIdx++
		messages[len(messages)-1] = aiMsg
	}
}

func (m Main) generateChatTitle(chatID string, message string) {
	title, err := m.titleGenerator.GenerateTitle(context.Background(), message)
	if err != nil {
		m.logger.Error("Error generating chat title",
			slog.String("message", message),
			slog.String(errLoggerKey, err.Error()))
		return
	}

	updatedChat := models.Chat{
		ID:    chatID,
		Title: title,
	}
	if err := m.store.UpdateChat(context.Background(), updatedChat); err != nil {
		m.logger.Error("Failed to update chat title",
			slog.String(errLoggerKey, err.Error()))
		return
	}

	divs, err := m.chatDivs(chatID)
	if err != nil {
		m.logger.Error("Failed to generate chat divs",
			slog.String(errLoggerKey, err.Error()))
		return
	}

	msg := sse.Message{
		Type: chatsSSEType,
	}
	msg.AppendData(divs)
	if err := m.sseSrv.Publish(&msg, chatsSSETopic); err != nil {
		m.logger.Error("Failed to publish chats",
			slog.String(errLoggerKey, err.Error()))
	}
}

func (m Main) chatDivs(activeID string) (string, error) {
	chats, err := m.store.Chats(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get chats: %w", err)
	}

	var sb strings.Builder
	for _, ch := range chats {
		err := m.templates.ExecuteTemplate(&sb, "chat_title", chat{
			ID:     ch.ID,
			Title:  ch.Title,
			Active: ch.ID == activeID,
		})
		if err != nil {
			return "", fmt.Errorf("failed to execute chat_title template: %w", err)
		}
	}
	return sb.String(), nil
}
