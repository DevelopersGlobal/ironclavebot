package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

// mockTransport simulates an in-memory MCP server
type mockTransport struct {
	responses map[string]*JSONRPCResponse
	closed    bool
}

func (m *mockTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	if m.closed {
		return nil, fmt.Errorf("transport closed")
	}
	resp, ok := m.responses[req.Method]
	if !ok {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: "Method not found"},
		}, nil
	}
	// clone response with matching id
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  resp.Result,
		Error:   resp.Error,
	}, nil
}

func (m *mockTransport) Notify(ctx context.Context, req *JSONRPCRequest) error {
	return nil
}

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

func TestMCPClientHandshakeAndTools(t *testing.T) {
	initResultBytes, _ := json.Marshal(InitializeResult{
		ProtocolVersion: ProtocolVersion,
		ServerInfo: ServerInfo{
			Name:    "test-server",
			Version: "1.0.0",
		},
	})

	listToolsBytes, _ := json.Marshal(ListToolsResult{
		Tools: []Tool{
			{
				Name:        "get_weather",
				Description: "Gets current weather for location",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"city": map[string]interface{}{"type": "string"},
					},
					"required": []string{"city"},
				},
			},
		},
	})

	callToolBytes, _ := json.Marshal(CallToolResult{
		Content: []ContentItem{
			{Type: "text", Text: "Sunny, 24°C"},
		},
	})

	mock := &mockTransport{
		responses: map[string]*JSONRPCResponse{
			"initialize": {Result: initResultBytes},
			"tools/list": {Result: listToolsBytes},
			"tools/call": {Result: callToolBytes},
		},
	}

	client := NewClient(mock)
	ctx := context.Background()

	// 1. Handshake
	initRes, err := client.Initialize(ctx, "ironclave-test", "1.0.0")
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	if initRes.ServerInfo.Name != "test-server" {
		t.Errorf("Expected server name 'test-server', got %s", initRes.ServerInfo.Name)
	}

	// 2. List tools
	toolList, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(toolList) != 1 || toolList[0].Name != "get_weather" {
		t.Fatalf("Unexpected tools list: %+v", toolList)
	}

	// 3. Adapter & Execution
	adapter := NewMCPToolAdapter("weather_service", client, toolList[0])
	if adapter.Name() != "mcp_weather_service_get_weather" {
		t.Errorf("Unexpected adapted name: %s", adapter.Name())
	}

	out, err := adapter.Execute(ctx, `{"city": "Tokyo"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if out != "Sunny, 24°C" {
		t.Errorf("Unexpected output: %s", out)
	}

	// Register in tools.Registry
	reg := tools.NewRegistry()
	reg.Register(adapter)
	retrieved, found := reg.Get("mcp_weather_service_get_weather")
	if !found || retrieved == nil {
		t.Fatal("Failed to retrieve adapter from tools.Registry")
	}
}

func TestMCPSSETransport(t *testing.T) {
	// Try creating a local IPv4 listener
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("Skipping network listener test in sandbox environment without local socket permission: %v", err)
		return
	}

	var postReceived chan string = make(chan string, 1)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Flushing not supported", 500)
				return
			}

			// Send endpoint event
			fmt.Fprintf(w, "event: endpoint\ndata: /messages\n\n")
			flusher.Flush()

			// Wait for POST and send response event
			// Also stop when the client goes away, so a failed test cannot
			// leave this handler blocked and hang server.Close().
			select {
			case msg := <-postReceived:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
				flusher.Flush()
			case <-r.Context().Done():
			}
			return
		}

		if r.Method == http.MethodPost {
			var req JSONRPCRequest
			_ = json.NewDecoder(r.Body).Decode(&req)

			// Generate JSON-RPC response
			resBytes, _ := json.Marshal(map[string]string{"status": "ok"})
			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  resBytes,
			}
			respJSON, _ := json.Marshal(resp)
			postReceived <- string(respJSON)

			w.WriteHeader(http.StatusAccepted)
			return
		}
	}))
	server.Listener = l
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sseTransport, err := NewSSETransport(ctx, server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create SSE transport: %v", err)
	}
	defer sseTransport.Close()

	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      int64(42),
		Method:  "test_ping",
	}

	resp, err := sseTransport.Send(ctx, req)
	if err != nil {
		t.Fatalf("SSE Send failed: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("Expected nil error, got: %v", resp.Error)
	}

	var resMap map[string]string
	_ = json.Unmarshal(resp.Result, &resMap)
	if resMap["status"] != "ok" {
		t.Errorf("Unexpected response: %v", resMap)
	}
}
