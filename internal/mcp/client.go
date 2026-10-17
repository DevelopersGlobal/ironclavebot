package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

const ProtocolVersion = "2024-11-05"

// Client is a high-level client for communicating with an MCP server
type Client struct {
	transport  Transport
	serverInfo ServerInfo
	reqCounter atomic.Int64
}

// NewClient wraps a transport with high-level MCP operations
func NewClient(t Transport) *Client {
	return &Client{
		transport: t,
	}
}

func (c *Client) nextID() int64 {
	return c.reqCounter.Add(1)
}

// Initialize performs the protocol handshake and capability negotiation
func (c *Client) Initialize(ctx context.Context, clientName, clientVersion string) (*InitializeResult, error) {
	if clientName == "" {
		clientName = "ironclavebot"
	}
	if clientVersion == "" {
		clientVersion = "1.0.0"
	}

	initParams := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		// No optional client capabilities are advertised: roots/list and
		// sampling requests are not handled, so claiming them would leave
		// servers waiting for replies that never arrive.
		Capabilities: ClientCapabilities{},
		ClientInfo: ClientInfo{
			Name:    clientName,
			Version: clientVersion,
		},
	}

	paramsBytes, err := json.Marshal(initParams)
	if err != nil {
		return nil, err
	}

	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "initialize",
		Params:  paramsBytes,
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("initialize request failed: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("server returned initialize error: %w", resp.Error)
	}

	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse initialize result: %w", err)
	}

	c.serverInfo = result.ServerInfo

	// Send initialized notification as required by MCP spec
	if err := c.transport.Notify(ctx, &JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}); err != nil {
		return nil, fmt.Errorf("initialized notification failed: %w", err)
	}

	return &result, nil
}

// maxToolPages bounds pagination so a misbehaving server cannot loop forever.
const maxToolPages = 100

// ListTools retrieves all tools available on the MCP server, following
// pagination cursors until the catalog is complete.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	seen := map[string]bool{}

	for page := 0; page < maxToolPages; page++ {
		params := []byte("{}")
		if cursor != "" {
			var err error
			params, err = json.Marshal(map[string]string{"cursor": cursor})
			if err != nil {
				return nil, err
			}
		}

		req := &JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      c.nextID(),
			Method:  "tools/list",
			Params:  params,
		}

		resp, err := c.transport.Send(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("tools/list request failed: %w", err)
		}

		if resp.Error != nil {
			return nil, fmt.Errorf("tools/list error: %w", resp.Error)
		}

		var result ListToolsResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, fmt.Errorf("failed to parse tools/list result: %w", err)
		}
		all = append(all, result.Tools...)

		if result.NextCursor == nil || *result.NextCursor == "" {
			return all, nil
		}
		cursor = *result.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("tools/list returned a repeated cursor %q", cursor)
		}
		seen[cursor] = true
	}
	return nil, fmt.Errorf("tools/list exceeded %d pages", maxToolPages)
}

// CallTool executes an MCP tool on the server
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*CallToolResult, error) {
	callParams := CallToolParams{
		Name:      name,
		Arguments: arguments,
	}

	paramsBytes, err := json.Marshal(callParams)
	if err != nil {
		return nil, err
	}

	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/call",
		Params:  paramsBytes,
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/call %q failed: %w", name, err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tools/call %q returned error: %w", name, resp.Error)
	}

	var result CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tools/call result: %w", err)
	}

	return &result, nil
}

// Ping sends a ping request to verify connection liveness
func (c *Client) Ping(ctx context.Context) error {
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "ping",
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

// ServerInfo returns the information about the connected server
func (c *Client) ServerInfo() ServerInfo {
	return c.serverInfo
}

// Close terminates the client transport
func (c *Client) Close() error {
	return c.transport.Close()
}
