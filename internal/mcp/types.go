package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// JSONRPCRequest represents a standard JSON-RPC 2.0 request
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"` // string or int64
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents a standard JSON-RPC 2.0 response
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a standard JSON-RPC 2.0 error
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	return e.Message
}

// ClientInfo describes the MCP client
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerInfo describes the connected MCP server
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities declares supported MCP client features
type ClientCapabilities struct {
	Roots    *RootsCapability    `json:"roots,omitempty"`
	Sampling *SamplingCapability `json:"sampling,omitempty"`
}

type RootsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type SamplingCapability struct{}

// ServerCapabilities declares supported MCP server features
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeParams sent during handshake
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// InitializeResult returned by server handshake
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// Tool represents a capability advertised by an MCP server
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// ListToolsResult returned by tools/list
type ListToolsResult struct {
	Tools      []Tool  `json:"tools"`
	NextCursor *string `json:"nextCursor,omitempty"`
}

// CallToolParams sent during tools/call
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// ContentItem in a tool call result
type ContentItem struct {
	Type     string `json:"type"` // "text", "image", "resource"
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallToolResult returned by tools/call
type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// incomingMessage is used to classify anything a server sends: a response
// (has result/error, no method), a request (method and id) or a notification
// (method, no id).
type incomingMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

func (m *incomingMessage) isServerRequest() bool      { return m.Method != "" && m.ID != nil }
func (m *incomingMessage) isServerNotification() bool { return m.Method != "" && m.ID == nil }
func (m *incomingMessage) response() *JSONRPCResponse {
	return &JSONRPCResponse{JSONRPC: m.JSONRPC, ID: m.ID, Result: m.Result, Error: m.Error}
}

// idKey normalizes a JSON-RPC id so ids decoded from JSON (float64) match the
// ids this client generated (int64), including values of one million and up.
func idKey(id interface{}) string {
	switch v := id.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// replyToServerRequest builds the answer to a request initiated by the server.
// The client advertises no optional capabilities, so only ping is supported.
func replyToServerRequest(m *incomingMessage) *JSONRPCResponse {
	if m.Method == "ping" {
		return &JSONRPCResponse{JSONRPC: "2.0", ID: m.ID, Result: json.RawMessage(`{}`)}
	}
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      m.ID,
		Error:   &JSONRPCError{Code: -32601, Message: "Method not found: " + m.Method},
	}
}
