package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MegaGrindStone/go-mcp"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// Chat represents a conversation container in the chat system. It provides basic identification and
// labeling capabilities for organizing message threads.
type Chat struct {
	ID    string
	Title string
}

// Message represents an individual communication entry within a chat. It contains the core components
// of a chat message including its unique identifier, the participant's role, the actual content, and
// the precise time when the message was created.
type Message struct {
	ID        string
	Role      Role
	Contents  []Content
	Timestamp time.Time
}

// Content is a message content with its type.
type Content struct {
	Type ContentType

	// Text would be filled if Type is ContentTypeText.
	Text string

	// ResourceContents would be filled if Type is ContentTypeResource.
	ResourceContents []mcp.ResourceContents

	// ToolName would be filled if Type is ContentTypeCallTool.
	ToolName string
	// ToolInput would be filled if Type is ContentTypeCallTool.
	ToolInput json.RawMessage

	// ToolResult would be filled if Type is ContentTypeToolResult. The value would be either tool result or error.
	ToolResult json.RawMessage

	// CallToolID would be filled if Type is ContentTypeCallTool or ContentTypeToolResult.
	CallToolID string
	// CallToolFailed is a flag indicating if the call tool failed.
	// This flag would be set to true if the call tool failed and Type is ContentTypeToolResult.
	CallToolFailed bool
}

// Role represents the role of a message participant.
type Role string

// ContentType represents the type of content in messages.
type ContentType string

const (
	// RoleUser represents a user message. A message with this role would only contain text or resource content.
	RoleUser Role = "user"
	// RoleAssistant represents an assistant message. A message with this role would contain
	// all types of content but resource.
	RoleAssistant Role = "assistant"

	// ContentTypeText represents text content.
	ContentTypeText ContentType = "text"
	// ContentTypeResource represents a resource content.
	ContentTypeResource ContentType = "resource"
	// ContentTypeCallTool represents a call to a tool.
	ContentTypeCallTool ContentType = "call_tool"
	// ContentTypeToolResult represents the result of a tool call.
	ContentTypeToolResult ContentType = "tool_result"
)

var mimeTypeToLanguage = map[string]string{
	"text/x-go":        "go",
	"text/golang":      "go",
	"application/json": "json",
	"text/javascript":  "javascript",
	"text/html":        "html",
	"text/css":         "css",
}

// RenderContents renders contents into a markdown string.
func RenderContents(contents []Content) (string, error) {
	var sb strings.Builder
	for _, content := range contents {
		switch content.Type {
		case ContentTypeText:
			if content.Text == "" {
				continue
			}
			sb.WriteString(content.Text)
		case ContentTypeResource:
			if len(content.ResourceContents) == 0 {
				continue
			}
			for _, resource := range content.ResourceContents {
				sb.WriteString("  \n\n<details>\n")
				sb.WriteString(fmt.Sprintf("<summary>Resource: %s</summary>\n\n", resource.URI))

				if resource.MimeType != "" {
					sb.WriteString(fmt.Sprintf("MIME Type: `%s`\n\n", resource.MimeType))
				}

				if resource.Text != "" {
					// Use map for language determination
					language := "text"
					if lang, exists := mimeTypeToLanguage[resource.MimeType]; exists {
						language = lang
					}

					sb.WriteString(fmt.Sprintf("```%s\n%s\n```\n", language, resource.Text))
				} else if resource.Blob != "" {
					// Handle binary content
					if strings.HasPrefix(resource.MimeType, "image/") {
						// Display images inline
						sb.WriteString(fmt.Sprintf("<img src=\"data:%s;base64,%s\" alt=\"%s\" />\n",
							resource.MimeType, resource.Blob, resource.URI))
					} else {
						// Provide download link for other binary content
						sb.WriteString(fmt.Sprintf("<a href=\"data:%s;base64,%s\" download=\"%s\">Download %s</a>\n",
							resource.MimeType, resource.Blob, resource.URI, resource.URI))
					}
				}

				sb.WriteString("\n</details>  \n\n")
			}
		case ContentTypeCallTool:
			sb.WriteString("  \n\n<details>\n")
			sb.WriteString(fmt.Sprintf("<summary>Calling Tool: %s</summary>\n\n", content.ToolName))
			sb.WriteString("Input:\n")

			var prettyJSON bytes.Buffer
			input := string(content.ToolInput)
			if err := json.Indent(&prettyJSON, content.ToolInput, "", "  "); err == nil {
				input = prettyJSON.String()
			}

			sb.WriteString(fmt.Sprintf("```json\n%s\n```\n", input))
		case ContentTypeToolResult:
			sb.WriteString("\n\n")
			sb.WriteString("Result:\n")

			var prettyJSON bytes.Buffer
			result := string(content.ToolResult)
			if err := json.Indent(&prettyJSON, content.ToolResult, "", "  "); err == nil {
				result = prettyJSON.String()
			}
			sb.WriteString(fmt.Sprintf("```json\n%s\n```\n", result))
			sb.WriteString("\n</details>  \n\n")
		}
	}
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(
				highlighting.WithStyle("rose-pine"),
			),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(), // To render newlines.
			html.WithUnsafe(),    // To render details tag.
		),
	)

	var buf bytes.Buffer
	if err := md.Convert([]byte(sb.String()), &buf); err != nil {
		return "", fmt.Errorf("failed to convert markdown: %w", err)
	}

	return buf.String(), nil
}

// String returns a string representation of the Content.
//
// The reason for this function is to make sure the json.RawMessage fields of c is
// rendered as a string, make it easier to debug.
func (c Content) String() string {
	type content struct {
		Type           ContentType
		Text           string
		ToolName       string
		ToolInput      string
		ToolResult     string
		CallToolID     string
		CallToolFailed bool
	}
	nc := content{
		Type:           c.Type,
		Text:           c.Text,
		ToolName:       c.ToolName,
		ToolInput:      string(c.ToolInput),
		ToolResult:     string(c.ToolResult),
		CallToolID:     c.CallToolID,
		CallToolFailed: c.CallToolFailed,
	}
	return fmt.Sprintf("%+v", nc)
}
