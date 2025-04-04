package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/MegaGrindStone/mcp-web-ui/internal/models"
)

type homePageData struct {
	Chats         []chat
	Messages      []message
	CurrentChatID string

	Servers   []mcp.Info
	Tools     []mcp.Tool
	Resources []mcp.Resource
	Prompts   []mcp.Prompt
}

// HandleHome renders the home page template with chat and message data. It displays a list of available
// chats and, if a chat_id query parameter is provided, shows the messages for the selected chat.
// The handler retrieves chat and message data from the store and prepares it for template rendering.
func (m Main) HandleHome(w http.ResponseWriter, r *http.Request) {
	cs, err := m.store.Chats(r.Context())
	if err != nil {
		m.logger.Error("Failed to get chats", slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// We transform the store's chat data into our view-specific chat structs
	// to avoid exposing internal implementation details to the template
	chats := make([]chat, len(cs))
	for i := range cs {
		chats[i] = chat{
			ID:     cs[i].ID,
			Title:  cs[i].Title,
			Active: false,
		}
	}

	currentChatID := ""
	var messages []message
	if chatID := r.URL.Query().Get("chat_id"); chatID != "" {
		// We find and mark the currently selected chat as active for UI highlighting
		idx := slices.IndexFunc(chats, func(c chat) bool {
			return c.ID == chatID
		})

		// Only proceed if the chat was found
		if idx >= 0 {
			currentChatID = chatID
			chats[idx].Active = true

			// We fetch and transform messages for the selected chat,
			// setting initial streaming state to "ended" for all messages
			ms, err := m.store.Messages(r.Context(), currentChatID)
			if err != nil {
				m.logger.Error("Failed to get messages", slog.String(errLoggerKey, err.Error()))
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			messages = make([]message, len(ms))
			for i := range ms {
				rc, err := models.RenderContents(ms[i].Contents)
				if err != nil {
					m.logger.Error("Failed to render contents",
						slog.String("message", fmt.Sprintf("%+v", ms[i])),
						slog.String(errLoggerKey, err.Error()))
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				m.logger.Debug("Render contents",
					slog.String("origMsg", fmt.Sprintf("%+v", ms[i].Contents)),
					slog.String("renderedMsg", rc))
				messages[i] = message{
					ID:             ms[i].ID,
					Role:           string(ms[i].Role),
					Content:        rc,
					Timestamp:      ms[i].Timestamp,
					StreamingState: "ended",
				}
			}
		}
	}
	data := homePageData{
		Chats:         chats,
		Messages:      messages,
		CurrentChatID: currentChatID,
		Servers:       m.servers,
		Tools:         m.tools,
		Resources:     m.resources,
		Prompts:       m.prompts,
	}

	if err := m.templates.ExecuteTemplate(w, "home.html", data); err != nil {
		m.logger.Error("Failed to execute home template", slog.String(errLoggerKey, err.Error()))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// HandleSSE serves Server-Sent Events (SSE) requests by delegating to the underlying SSE server.
// This endpoint enables real-time updates for the client.
func (m Main) HandleSSE(w http.ResponseWriter, r *http.Request) {
	m.sseSrv.ServeHTTP(w, r)
}
