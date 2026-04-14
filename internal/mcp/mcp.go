// Package mcp implements a Model Context Protocol (MCP) server.
//
// Two transports are supported:
//
//  1. Stdio (primary) — JSON-RPC over stdin/stdout, for Claude Desktop and Claude Code.
//     Launched via: claude-bridge --mcp
//     Configure in claude_desktop_config.json as a local MCP server.
//
//  2. SSE — HTTP-based, available for remote MCP clients or future use.
//     The Claude Bridge HTTP server mounts this at /mcp/sse and /mcp/message.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Shared JSON-RPC + MCP types
// ---------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]interface{} `json:"capabilities"`
	ServerInfo      serverInfo             `json:"serverInfo"`
}

type tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Required   []string               `json:"required,omitempty"`
}

type toolListResult struct {
	Tools []tool `json:"tools"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callToolResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ---------------------------------------------------------------------------
// Tool definitions (shared by both transports)
// ---------------------------------------------------------------------------

func getTools() []tool {
	return []tool{
		// --- WhatsApp tools ---
		{
			Name:        "get_whatsapp_status",
			Description: "Get the current WhatsApp connection status, including whether connected, the phone number, and message/contact counts.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]interface{}{},
			},
		},
		{
			Name:        "send_whatsapp_message",
			Description: "Send a WhatsApp message to a phone number. The phone number should be in international format without the + sign (e.g., '6281234567890' for Indonesia, '14155551234' for US).",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"phone": map[string]interface{}{
						"type":        "string",
						"description": "Phone number in international format without + (e.g., '6281234567890')",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The text message to send",
					},
				},
				Required: []string{"phone", "message"},
			},
		},
		{
			Name:        "read_whatsapp_messages",
			Description: "Read recent WhatsApp messages. Optionally filter by a specific chat JID (e.g., '6281234567890@s.whatsapp.net'). Returns the most recent messages.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"chat": map[string]interface{}{
						"type":        "string",
						"description": "Optional: filter by chat JID (e.g., '6281234567890@s.whatsapp.net'). Leave empty for all chats.",
					},
				},
			},
		},
		{
			Name:        "list_whatsapp_contacts",
			Description: "List WhatsApp contacts. Use the 'q' parameter to search by name (recommended — there may be hundreds of contacts). Returns JID, push name, and last seen time.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"q": map[string]interface{}{
						"type":        "string",
						"description": "Search query to filter contacts by name. Recommended to avoid huge responses.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of contacts to return (default 50).",
					},
				},
			},
		},
		// --- Facebook tools ---
		{
			Name:        "get_facebook_status",
			Description: "Get the current Facebook connection status, including whether logged in, the user name, profile picture URL, connected page name, and Messenger polling status.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]interface{}{},
			},
		},
		{
			Name:        "create_facebook_post",
			Description: "Create a text post on Facebook. Optionally specify a page URL to post to a specific Facebook Page instead of the user's timeline.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"content": map[string]interface{}{
						"type":        "string",
						"description": "The text content of the post",
					},
					"page_url": map[string]interface{}{
						"type":        "string",
						"description": "Optional: Facebook Page URL to post to (e.g., 'https://www.facebook.com/YourPageName'). Leave empty to post to personal timeline.",
					},
				},
				Required: []string{"content"},
			},
		},
		{
			Name:        "read_facebook_messages",
			Description: "Read Facebook Messenger messages for a specific conversation. Uses the official Graph API. Returns cached messages by default with a 'synced_at' timestamp. Set refresh=true to fetch fresh messages from the API. If data is stale, ask the user if they want to refresh.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"conversation_id": map[string]interface{}{
						"type":        "string",
						"description": "The conversation ID to read messages from (required). Get conversation IDs from list_facebook_contacts.",
					},
					"refresh": map[string]interface{}{
						"type":        "boolean",
						"description": "Set to true to fetch fresh messages from the Graph API. Default false returns cached data.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of messages to return (default 50).",
					},
				},
				Required: []string{"conversation_id"},
			},
		},
		{
			Name:        "send_facebook_message",
			Description: "Send a message on Facebook Messenger to a user via the Page. Uses the official Graph API Send API. The recipient_id is the user's Facebook ID (get it from list_facebook_contacts).",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"recipient_id": map[string]interface{}{
						"type":        "string",
						"description": "The recipient's Facebook user ID to send the message to",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The text message to send",
					},
				},
				Required: []string{"recipient_id", "message"},
			},
		},
		{
			Name:        "list_facebook_contacts",
			Description: "List Facebook Messenger conversations (contacts who have messaged the Page). Uses the official Graph API. Returns cached data by default with a 'synced_at' timestamp. Set refresh=true to fetch fresh data. If data is stale, suggest refreshing.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of conversations to return (default 25).",
					},
					"refresh": map[string]interface{}{
						"type":        "boolean",
						"description": "Set to true to fetch fresh conversations from the Graph API. Default false returns cached data.",
					},
				},
			},
		},
		{
			Name:        "get_facebook_posts",
			Description: "Get recent posts from the connected Facebook Page. Uses the Graph API. Returns cached data by default with a 'synced_at' timestamp. Set refresh=true to fetch fresh data.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of posts to return (default 20).",
					},
					"refresh": map[string]interface{}{
						"type":        "boolean",
						"description": "Set to true to fetch fresh posts from the Graph API. Default false returns cached data.",
					},
				},
			},
		},
		{
			Name:        "get_facebook_comments",
			Description: "Get comments on a specific Facebook post. Uses the Graph API. Returns cached data by default. Set refresh=true to fetch fresh data.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"post_id": map[string]interface{}{
						"type":        "string",
						"description": "The post ID to get comments for (required). Get post IDs from get_facebook_posts.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of comments to return (default 50).",
					},
					"refresh": map[string]interface{}{
						"type":        "boolean",
						"description": "Set to true to fetch fresh comments from the Graph API. Default false returns cached data.",
					},
				},
				Required: []string{"post_id"},
			},
		},
		{
			Name:        "reply_facebook_comment",
			Description: "Reply to a comment on a Facebook post. Uses the Graph API.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"comment_id": map[string]interface{}{
						"type":        "string",
						"description": "The comment ID to reply to (required). Get comment IDs from get_facebook_comments.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The reply text",
					},
				},
				Required: []string{"comment_id", "message"},
			},
		},
		{
			Name:        "get_facebook_analytics",
			Description: "Get engagement analytics for the connected Facebook Page — total likes, comments, shares, and average engagement per post.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]interface{}{},
			},
		},
		// --- Batch tools (work across platforms) ---
		{
			Name:        "batch_facebook_posts",
			Description: "Submit multiple Facebook posts as a batch. The app queues them and posts one by one with human-like delays (default 5-10 seconds between each) to avoid rate limits. Returns a batch_id to track progress. Use get_batch_status to check progress.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"posts": map[string]interface{}{
						"type":        "array",
						"description": "Array of post objects, each with 'content' (required) and optional 'page_url'",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"content":  map[string]interface{}{"type": "string"},
								"page_url": map[string]interface{}{"type": "string"},
							},
						},
					},
					"min_delay_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Minimum seconds between posts (default 5)",
					},
					"max_delay_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum seconds between posts (default 10)",
					},
				},
				Required: []string{"posts"},
			},
		},
		{
			Name:        "batch_facebook_messages",
			Description: "Submit multiple Facebook Messenger messages as a batch. The app sends them one by one with human-like delays. Returns a batch_id to track progress.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"messages": map[string]interface{}{
						"type":        "array",
						"description": "Array of message objects, each with 'recipient_id' and 'message'",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"recipient_id": map[string]interface{}{"type": "string"},
								"message":      map[string]interface{}{"type": "string"},
							},
						},
					},
					"min_delay_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Minimum seconds between messages (default 5)",
					},
					"max_delay_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum seconds between messages (default 10)",
					},
				},
				Required: []string{"messages"},
			},
		},
		{
			Name:        "get_batch_status",
			Description: "Check the progress of a batch operation. Returns how many jobs are completed, running, pending, or failed.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"batch_id": map[string]interface{}{
						"type":        "string",
						"description": "The batch ID returned from a batch_facebook_posts or batch_facebook_messages call",
					},
				},
				Required: []string{"batch_id"},
			},
		},
		{
			Name:        "cancel_batch",
			Description: "Cancel a running batch operation. Jobs already completed stay completed, but remaining jobs are cancelled.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"batch_id": map[string]interface{}{
						"type":        "string",
						"description": "The batch ID to cancel",
					},
				},
				Required: []string{"batch_id"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Tool execution (shared — calls the Claude Bridge HTTP API on localhost)
// ---------------------------------------------------------------------------

type toolExecutor struct {
	baseURL       string
	client        *http.Client
	browserClient *http.Client // longer timeout for browser-automation calls
}

func newToolExecutor(baseURL string) *toolExecutor {
	return &toolExecutor{
		baseURL:       strings.TrimRight(baseURL, "/"),
		client:        &http.Client{Timeout: 60 * time.Second},
		browserClient: &http.Client{Timeout: 3 * time.Minute},
	}
}

func (e *toolExecutor) execute(name string, args json.RawMessage) callToolResult {
	switch name {
	// WhatsApp
	case "get_whatsapp_status":
		return e.httpGet("/api/status")
	case "send_whatsapp_message":
		return e.sendMessage(args)
	case "read_whatsapp_messages":
		return e.readMessages(args)
	case "list_whatsapp_contacts":
		return e.listContacts(args)
	// Facebook
	case "get_facebook_status":
		return e.httpGet("/api/facebook/status")
	case "create_facebook_post":
		return e.createFBPost(args)
	case "read_facebook_messages":
		return e.readFBMessages(args)
	case "send_facebook_message":
		return e.sendFBMessage(args)
	case "list_facebook_contacts":
		return e.listFBContacts(args)
	case "get_facebook_posts":
		return e.getFBPosts(args)
	case "get_facebook_comments":
		return e.getFBComments(args)
	case "reply_facebook_comment":
		return e.replyFBComment(args)
	case "get_facebook_analytics":
		return e.httpGet("/api/facebook/messenger/analytics")
	// Batch
	case "batch_facebook_posts":
		return e.batchFBPosts(args)
	case "batch_facebook_messages":
		return e.batchFBMessages(args)
	case "get_batch_status":
		return e.getBatchStatus(args)
	case "cancel_batch":
		return e.cancelBatch(args)
	default:
		return errorResult(fmt.Sprintf("Unknown tool: %s", name))
	}
}

func (e *toolExecutor) httpGet(path string) callToolResult {
	resp, err := e.client.Get(e.baseURL + path)
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge at %s — is the dashboard running? Start it with: ./claude-bridge\nError: %v", e.baseURL, err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return callToolResult{Content: []contentItem{{Type: "text", Text: string(body)}}}
}

func (e *toolExecutor) sendMessage(args json.RawMessage) callToolResult {
	var params struct {
		Phone   string `json:"phone"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("Invalid arguments: need 'phone' and 'message'")
	}
	if params.Phone == "" || params.Message == "" {
		return errorResult("Both 'phone' and 'message' are required")
	}

	payload, _ := json.Marshal(params)
	resp, err := e.client.Post(e.baseURL+"/api/whatsapp/send", "application/json", bytes.NewReader(payload))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if ok, _ := result["ok"].(bool); ok {
		return callToolResult{Content: []contentItem{{Type: "text", Text: fmt.Sprintf("Message sent to %s successfully.", params.Phone)}}}
	}
	errMsg, _ := result["error"].(string)
	return errorResult(fmt.Sprintf("Failed to send: %s", errMsg))
}

func (e *toolExecutor) readMessages(args json.RawMessage) callToolResult {
	var params struct {
		Chat string `json:"chat"`
	}
	json.Unmarshal(args, &params)

	url := e.baseURL + "/api/whatsapp/messages"
	if params.Chat != "" {
		url += "?chat=" + params.Chat
	}

	resp, err := e.client.Get(url)
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return callToolResult{Content: []contentItem{{Type: "text", Text: string(body)}}}
}

func (e *toolExecutor) listContacts(args json.RawMessage) callToolResult {
	var params struct {
		Q     string `json:"q"`
		Limit int    `json:"limit"`
	}
	json.Unmarshal(args, &params)

	url := e.baseURL + "/api/whatsapp/contacts?"
	if params.Q != "" {
		url += "q=" + params.Q + "&"
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	url += "limit=" + fmt.Sprintf("%d", limit)

	resp, err := e.client.Get(url)
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return callToolResult{Content: []contentItem{{Type: "text", Text: string(body)}}}
}

// --- Facebook tool implementations ---

func (e *toolExecutor) createFBPost(args json.RawMessage) callToolResult {
	var params struct {
		Content string `json:"content"`
		PageURL string `json:"page_url"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("Invalid arguments: need 'content'")
	}
	if params.Content == "" {
		return errorResult("'content' is required")
	}

	payload, _ := json.Marshal(params)
	resp, err := e.browserClient.Post(e.baseURL+"/api/facebook/post", "application/json", bytes.NewReader(payload))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if ok, _ := result["ok"].(bool); ok {
		return callToolResult{Content: []contentItem{{Type: "text", Text: "Facebook post created successfully."}}}
	}
	errMsg, _ := result["error"].(string)
	return errorResult(fmt.Sprintf("Failed to create post: %s", errMsg))
}

func (e *toolExecutor) readFBMessages(args json.RawMessage) callToolResult {
	var params struct {
		ConversationID string `json:"conversation_id"`
		Refresh        bool   `json:"refresh"`
		Limit          int    `json:"limit"`
	}
	json.Unmarshal(args, &params)

	if params.ConversationID == "" {
		return errorResult("'conversation_id' is required — use list_facebook_contacts to get conversation IDs")
	}

	url := e.baseURL + "/api/facebook/messenger/messages?"
	url += "conversation=" + params.ConversationID + "&"
	if params.Refresh {
		url += "refresh=true&"
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	url += fmt.Sprintf("limit=%d", limit)

	hc := e.client
	if params.Refresh {
		hc = e.browserClient
	}
	resp, err := hc.Get(url)
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Parse to add cache info to the response
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	cached, _ := result["cached"].(bool)
	prefix := ""
	if cached {
		if syncedAt, ok := result["synced_at"].(string); ok {
			prefix = fmt.Sprintf("⚡ This is cached data (last synced: %s). To get fresh data, set refresh=true.\n\n", syncedAt)
		}
	}

	return callToolResult{Content: []contentItem{{Type: "text", Text: prefix + string(body)}}}
}

func (e *toolExecutor) sendFBMessage(args json.RawMessage) callToolResult {
	var params struct {
		RecipientID string `json:"recipient_id"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("Invalid arguments: need 'recipient_id' and 'message'")
	}
	if params.RecipientID == "" || params.Message == "" {
		return errorResult("Both 'recipient_id' and 'message' are required")
	}

	payload, _ := json.Marshal(map[string]string{
		"recipient_id": params.RecipientID,
		"message":      params.Message,
	})
	resp, err := e.client.Post(e.baseURL+"/api/facebook/messenger/send", "application/json", bytes.NewReader(payload))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if ok, _ := result["ok"].(bool); ok {
		return callToolResult{Content: []contentItem{{Type: "text", Text: fmt.Sprintf("Message sent to %s successfully via Messenger API.", params.RecipientID)}}}
	}
	errMsg, _ := result["error"].(string)
	return errorResult(fmt.Sprintf("Failed to send: %s", errMsg))
}

func (e *toolExecutor) listFBContacts(args json.RawMessage) callToolResult {
	var params struct {
		Limit   int  `json:"limit"`
		Refresh bool `json:"refresh"`
	}
	json.Unmarshal(args, &params)

	limit := params.Limit
	if limit <= 0 {
		limit = 25
	}

	url := fmt.Sprintf("%s/api/facebook/messenger/conversations?limit=%d", e.baseURL, limit)
	if params.Refresh {
		url += "&refresh=true"
	}

	hc := e.client
	if params.Refresh {
		hc = e.browserClient
	}
	resp, err := hc.Get(url)
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	cached, _ := result["cached"].(bool)
	prefix := ""
	if cached {
		if syncedAt, ok := result["synced_at"].(string); ok {
			prefix = fmt.Sprintf("⚡ This is cached data (last synced: %s). To get fresh data, set refresh=true.\n\n", syncedAt)
		}
	}

	return callToolResult{Content: []contentItem{{Type: "text", Text: prefix + string(body)}}}
}

func (e *toolExecutor) getFBPosts(args json.RawMessage) callToolResult {
	var params struct {
		Limit   int  `json:"limit"`
		Refresh bool `json:"refresh"`
	}
	json.Unmarshal(args, &params)

	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}

	url := fmt.Sprintf("%s/api/facebook/messenger/posts?limit=%d", e.baseURL, limit)
	if params.Refresh {
		url += "&refresh=true"
	}

	resp, err := e.client.Get(url)
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	cached, _ := result["cached"].(bool)
	prefix := ""
	if cached {
		if syncedAt, ok := result["synced_at"].(string); ok {
			prefix = fmt.Sprintf("⚡ This is cached data (last synced: %s). To get fresh data, set refresh=true.\n\n", syncedAt)
		}
	}

	return callToolResult{Content: []contentItem{{Type: "text", Text: prefix + string(body)}}}
}

func (e *toolExecutor) getFBComments(args json.RawMessage) callToolResult {
	var params struct {
		PostID  string `json:"post_id"`
		Limit   int    `json:"limit"`
		Refresh bool   `json:"refresh"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("Invalid arguments: need 'post_id'")
	}
	if params.PostID == "" {
		return errorResult("'post_id' is required — use get_facebook_posts to get post IDs")
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}

	url := fmt.Sprintf("%s/api/facebook/messenger/comments?post_id=%s&limit=%d", e.baseURL, params.PostID, limit)
	if params.Refresh {
		url += "&refresh=true"
	}

	resp, err := e.client.Get(url)
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	cached, _ := result["cached"].(bool)
	prefix := ""
	if cached {
		if syncedAt, ok := result["synced_at"].(string); ok {
			prefix = fmt.Sprintf("⚡ This is cached data (last synced: %s). To get fresh data, set refresh=true.\n\n", syncedAt)
		}
	}

	return callToolResult{Content: []contentItem{{Type: "text", Text: prefix + string(body)}}}
}

func (e *toolExecutor) batchFBPosts(args json.RawMessage) callToolResult {
	var params struct {
		Posts    []map[string]string `json:"posts"`
		MinDelay int                `json:"min_delay_seconds"`
		MaxDelay int                `json:"max_delay_seconds"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("Invalid arguments: need 'posts' array")
	}
	if len(params.Posts) == 0 {
		return errorResult("'posts' array is required and must not be empty")
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"platform":          "facebook",
		"type":              "create_post",
		"items":             params.Posts,
		"min_delay_seconds": params.MinDelay,
		"max_delay_seconds": params.MaxDelay,
	})
	resp, err := e.client.Post(e.baseURL+"/api/batch/submit", "application/json", bytes.NewReader(payload))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return callToolResult{Content: []contentItem{{Type: "text", Text: string(body)}}}
}

func (e *toolExecutor) batchFBMessages(args json.RawMessage) callToolResult {
	var params struct {
		Messages []map[string]string `json:"messages"`
		MinDelay int                 `json:"min_delay_seconds"`
		MaxDelay int                 `json:"max_delay_seconds"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("Invalid arguments: need 'messages' array")
	}
	if len(params.Messages) == 0 {
		return errorResult("'messages' array is required and must not be empty")
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"platform":          "facebook",
		"type":              "send_message",
		"items":             params.Messages,
		"min_delay_seconds": params.MinDelay,
		"max_delay_seconds": params.MaxDelay,
	})
	resp, err := e.client.Post(e.baseURL+"/api/batch/submit", "application/json", bytes.NewReader(payload))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return callToolResult{Content: []contentItem{{Type: "text", Text: string(body)}}}
}

func (e *toolExecutor) getBatchStatus(args json.RawMessage) callToolResult {
	var params struct {
		BatchID string `json:"batch_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.BatchID == "" {
		return errorResult("'batch_id' is required")
	}
	return e.httpGet("/api/batch/status?batch_id=" + params.BatchID)
}

func (e *toolExecutor) cancelBatch(args json.RawMessage) callToolResult {
	var params struct {
		BatchID string `json:"batch_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.BatchID == "" {
		return errorResult("'batch_id' is required")
	}
	payload, _ := json.Marshal(params)
	resp, err := e.client.Post(e.baseURL+"/api/batch/cancel", "application/json", bytes.NewReader(payload))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge: %v", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return callToolResult{Content: []contentItem{{Type: "text", Text: string(body)}}}
}

func (e *toolExecutor) replyFBComment(args json.RawMessage) callToolResult {
	var params struct {
		CommentID string `json:"comment_id"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("Invalid arguments: need 'comment_id' and 'message'")
	}
	if params.CommentID == "" || params.Message == "" {
		return errorResult("Both 'comment_id' and 'message' are required")
	}

	payload, _ := json.Marshal(params)
	resp, err := e.client.Post(e.baseURL+"/api/facebook/messenger/comments/reply", "application/json", bytes.NewReader(payload))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge — is the dashboard running? Error: %v", err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if ok, _ := result["ok"].(bool); ok {
		return callToolResult{Content: []contentItem{{Type: "text", Text: fmt.Sprintf("Reply posted to comment %s successfully.", params.CommentID)}}}
	}
	errMsg, _ := result["error"].(string)
	return errorResult(fmt.Sprintf("Failed to reply: %s", errMsg))
}

func errorResult(msg string) callToolResult {
	return callToolResult{Content: []contentItem{{Type: "text", Text: msg}}, IsError: true}
}

// ---------------------------------------------------------------------------
// Request handler (shared logic for both transports)
// ---------------------------------------------------------------------------

func handleRequest(req *jsonRPCRequest, exec *toolExecutor) *jsonRPCResponse {
	switch req.Method {

	case "initialize":
		return makeResult(req.ID, initResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    map[string]interface{}{"tools": map[string]interface{}{}},
			ServerInfo:      serverInfo{Name: "claude-bridge", Version: "1.0.0"},
		})

	case "notifications/initialized":
		return nil // no response for notifications

	case "tools/list":
		return makeResult(req.ID, toolListResult{Tools: getTools()})

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return makeError(req.ID, -32602, "invalid tool call params")
		}
		result := exec.execute(params.Name, params.Arguments)
		return makeResult(req.ID, result)

	default:
		if req.ID != nil {
			return makeError(req.ID, -32601, fmt.Sprintf("unknown method: %s", req.Method))
		}
		return nil
	}
}

func makeResult(id json.RawMessage, result interface{}) *jsonRPCResponse {
	return &jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func makeError(id json.RawMessage, code int, message string) *jsonRPCResponse {
	return &jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: message}}
}

// ===========================================================================
// SSE Transport (for remote MCP clients)
// ===========================================================================

// SSEHandler manages SSE-based MCP sessions. Mount it on your HTTP server.
type SSEHandler struct {
	baseURL  string // e.g., "http://127.0.0.1:10002"
	sessions sync.Map
}

type sseSession struct {
	id     string
	events chan []byte
	exec   *toolExecutor
}

// NewSSEHandler creates a handler that proxies MCP tool calls to the given base URL.
func NewSSEHandler(baseURL string) *SSEHandler {
	return &SSEHandler{baseURL: baseURL}
}

// HandleSSE is the GET /mcp/sse endpoint. Claude Desktop connects here.
func (h *SSEHandler) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	sessionID := uuid.New().String()
	sess := &sseSession{
		id:     sessionID,
		events: make(chan []byte, 64),
		exec:   newToolExecutor(h.baseURL),
	}
	h.sessions.Store(sessionID, sess)

	log.Printf("[mcp-sse] New session: %s", sessionID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send the endpoint URL as the first event.
	msgEndpoint := fmt.Sprintf("/mcp/message?sessionId=%s", sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", msgEndpoint)
	flusher.Flush()

	// Stream events until client disconnects.
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[mcp-sse] Session %s disconnected", sessionID)
			h.sessions.Delete(sessionID)
			return
		case data := <-sess.events:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// HandleMessage is the POST /mcp/message endpoint. Claude Desktop sends JSON-RPC here.
func (h *SSEHandler) HandleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	val, ok := h.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	sess := val.(*sseSession)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		resp := makeError(nil, -32700, "parse error")
		data, _ := json.Marshal(resp)
		sess.events <- data
		w.WriteHeader(http.StatusAccepted)
		return
	}

	log.Printf("[mcp-sse] Session %s method: %s", sessionID, req.Method)

	// Return 202 immediately — MCP over SSE requires the POST to return right away.
	// The actual JSON-RPC response is delivered asynchronously on the SSE stream.
	// Browser-automation tools can take 60-90s; blocking here caused Claude Desktop
	// to time out on the POST and never receive the result.
	w.WriteHeader(http.StatusAccepted)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		resp := handleRequest(&req, sess.exec)
		if resp != nil {
			data, _ := json.Marshal(resp)
			sess.events <- data
		}
	}()
}

// ===========================================================================
// Stdio Transport (primary — for Claude Desktop / Claude Code)
// ===========================================================================

// StdioServer reads JSON-RPC from stdin and writes to stdout.
// All logging goes to stderr to keep stdout clean for JSON-RPC.
type StdioServer struct {
	exec   *toolExecutor
	reader *bufio.Reader
	writer *bufio.Writer
	logger *log.Logger
}

// NewStdioServer creates a stdio MCP server proxying to the given Claude Bridge URL.
func NewStdioServer(baseURL string) *StdioServer {
	return &StdioServer{
		exec:   newToolExecutor(baseURL),
		reader: bufio.NewReader(os.Stdin),
		writer: bufio.NewWriter(os.Stdout),
		logger: log.New(os.Stderr, "[mcp-stdio] ", log.LstdFlags),
	}
}

// Run starts the stdio loop. Blocks until EOF.
func (s *StdioServer) Run() error {
	s.logger.Printf("MCP stdio server started, proxying to %s", s.exec.baseURL)

	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				s.logger.Println("EOF on stdin, shutting down")
				return nil
			}
			return fmt.Errorf("read stdin: %w", err)
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.logger.Printf("Parse error: %v", err)
			resp := makeError(nil, -32700, "parse error")
			s.writeResponse(resp)
			continue
		}

		s.logger.Printf("Method: %s", req.Method)

		resp := handleRequest(&req, s.exec)
		if resp != nil {
			s.writeResponse(resp)
			s.logger.Printf("Responded to %s", req.Method)
		}
	}
}

func (s *StdioServer) writeResponse(resp *jsonRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Printf("Marshal error: %v", err)
		return
	}
	s.writer.Write(data)
	s.writer.Write([]byte("\n"))
	s.writer.Flush()
}
