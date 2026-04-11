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
	}
}

// ---------------------------------------------------------------------------
// Tool execution (shared — calls the Claude Bridge HTTP API on localhost)
// ---------------------------------------------------------------------------

type toolExecutor struct {
	baseURL string
	client  *http.Client
}

func newToolExecutor(baseURL string) *toolExecutor {
	return &toolExecutor{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *toolExecutor) execute(name string, args json.RawMessage) callToolResult {
	switch name {
	case "get_whatsapp_status":
		return e.httpGet("/api/status")
	case "send_whatsapp_message":
		return e.sendMessage(args)
	case "read_whatsapp_messages":
		return e.readMessages(args)
	case "list_whatsapp_contacts":
		return e.listContacts(args)
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

	resp := handleRequest(&req, sess.exec)
	if resp != nil {
		data, _ := json.Marshal(resp)
		sess.events <- data
	}

	w.WriteHeader(http.StatusAccepted)
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
