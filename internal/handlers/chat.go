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
// managing both new chat creation and message handling. It accepts user messages through form data,
// creates appropriate chat contexts, and initiates asynchronous processing for AI responses and chat title generation.
//
// The handler expects a "message" form field and an optional "chat_id" field.
// If no chat_id is provided, it creates a new chat session. The handler streams AI responses through
// Server-Sent Events (SSE) and updates the UI accordingly through template rendering.
//
// The function returns appropriate HTTP error responses for invalid methods, missing required fields,
// or internal processing errors. For successful requests, it renders either a complete chatbox template
// for new chats or individual message templates for existing chats.
func (m Main) HandleChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		m.logger.Error("Method not allowed", slog.String("method", r.Method))
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	msg := r.FormValue("message")
	if msg == "" {
		m.logger.Error("Message is required")
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	var err error

	chatID := r.FormValue("chat_id")
	// We track if this is a new chat to determine the appropriate template rendering strategy
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

	// We create two messages: user's input and a placeholder for AI response
	um := models.Message{
		ID:   uuid.New().String(),
		Role: models.RoleUser,
		Contents: []models.Content{
			{
				Type: models.ContentTypeText,
				Text: msg,
			},
		},
		Timestamp: time.Now(),
	}
	userMsgID, err := m.store.AddMessage(r.Context(), chatID, um)
	if err != nil {
		m.logger.Error("Failed to add user message",
			slog.String("message", fmt.Sprintf("%+v", um)),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
		go m.generateChatTitle(chatID, msg)

		// For new chats, we prepare all messages with appropriate streaming states
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
		err = m.templates.ExecuteTemplate(w, "chatbox", data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	userContent, err := models.RenderContents(um.Contents)
	if err != nil {
		m.logger.Error("Failed to render contents",
			slog.String("message", fmt.Sprintf("%+v", um)),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = m.templates.ExecuteTemplate(w, "user_message", message{
		ID:             userMsgID,
		Role:           string(um.Role),
		Content:        userContent,
		Timestamp:      um.Timestamp,
		StreamingState: "ended",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	aiContent, err := models.RenderContents(am.Contents)
	if err != nil {
		m.logger.Error("Failed to render contents",
			slog.String("message", fmt.Sprintf("%+v", am)),
			slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err = m.templates.ExecuteTemplate(w, "ai_message", message{
		ID:             aiMsgID,
		Role:           string(am.Role),
		Content:        aiContent,
		Timestamp:      am.Timestamp,
		StreamingState: "loading",
	})
	if err != nil {
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
