package mcp

import "context"

// Transport defines the communication layer for exchanging JSON-RPC messages with an MCP server
type Transport interface {
	// Send issues a request and waits for the matching response
	Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error)

	// Notify sends a notification without expecting a response
	Notify(ctx context.Context, req *JSONRPCRequest) error

	// Close terminates the transport connection and frees resources
	Close() error
}
