package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

// MCPToolAdapter adapts an external MCP Tool into the IronClaveBot tools.Tool interface
type MCPToolAdapter struct {
	serverName string
	client     *Client
	tool       Tool
}

// NewMCPToolAdapter creates an adapted tool
func NewMCPToolAdapter(serverName string, client *Client, tool Tool) *MCPToolAdapter {
	return &MCPToolAdapter{
		serverName: serverName,
		client:     client,
		tool:       tool,
	}
}

// maxToolNameLen is the longest function name OpenAI-compatible providers accept.
const maxToolNameLen = 64

// Name returns a provider-compatible tool name (letters, digits, '_' and '-',
// at most 64 characters). Names that needed changes (invalid characters or
// truncation) get a short hash of the original server and tool names, so two
// different tools can never collapse into the same name. '-' is kept as is,
// so "read-file" and "read_file" stay distinct.
func (a *MCPToolAdapter) Name() string {
	raw := fmt.Sprintf("mcp_%s_%s", a.serverName, a.tool.Name)

	var b strings.Builder
	changed := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
			changed = true
		}
	}
	name := b.String()

	if !changed && len(name) <= maxToolNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(a.serverName + "\x00" + a.tool.Name))
	suffix := "_" + hex.EncodeToString(sum[:4])
	if len(name) > maxToolNameLen-len(suffix) {
		name = name[:maxToolNameLen-len(suffix)]
	}
	return name + suffix
}

func (a *MCPToolAdapter) Description() string {
	desc := a.tool.Description
	if desc == "" {
		desc = fmt.Sprintf("External tool %s provided by MCP server %s", a.tool.Name, a.serverName)
	}
	return fmt.Sprintf("[MCP: %s] %s", a.serverName, desc)
}

func (a *MCPToolAdapter) Parameters() map[string]interface{} {
	if a.tool.InputSchema == nil {
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}
	return a.tool.InputSchema
}

func (a *MCPToolAdapter) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args map[string]interface{}
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("invalid MCP arguments JSON: %w", err)
		}
	} else {
		args = make(map[string]interface{})
	}

	result, err := a.client.CallTool(ctx, a.tool.Name, args)
	if err != nil {
		return "", fmt.Errorf("MCP call error: %w", err)
	}

	var sb strings.Builder
	for i, item := range result.Content {
		if i > 0 {
			sb.WriteString("\n")
		}
		if item.Text != "" {
			sb.WriteString(item.Text)
		} else if item.Data != "" {
			sb.WriteString(fmt.Sprintf("[%s data: %d bytes]", item.MimeType, len(item.Data)))
		}
	}

	output := sb.String()
	if result.IsError {
		return output, fmt.Errorf("MCP tool returned error status: %s", output)
	}

	return output, nil
}

// ServerConfig specifies configuration for connecting to an external MCP server
type ServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio" or "sse"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       []string          `json:"env,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// ServerStatus describes a running MCP server
type ServerStatus struct {
	Name       string     `json:"name"`
	Transport  string     `json:"transport"`
	ServerInfo ServerInfo `json:"server_info"`
	ToolCount  int        `json:"tool_count"`
	Tools      []string   `json:"tools"`
}

// Manager orchestrates multiple external MCP client connections
type Manager struct {
	mu         sync.RWMutex
	clients    map[string]*Client
	configs    map[string]ServerConfig
	connecting map[string]bool
}

// NewManager creates an MCP manager instance
func NewManager() *Manager {
	return &Manager{
		clients:    make(map[string]*Client),
		configs:    make(map[string]ServerConfig),
		connecting: make(map[string]bool),
	}
}

// RegisterServer adds a server configuration to the manager
func (m *Manager) RegisterServer(cfg ServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[cfg.Name] = cfg
}

// Connect starts a client for a registered server configuration
func (m *Manager) Connect(ctx context.Context, name string) (*Client, error) {
	m.mu.Lock()
	cfg, ok := m.configs[name]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("server configuration %q not found", name)
	}
	// Reuse a live connection instead of leaking it by overwriting it.
	if existing, ok := m.clients[name]; ok {
		m.mu.Unlock()
		return existing, nil
	}
	if m.connecting[name] {
		m.mu.Unlock()
		return nil, fmt.Errorf("server %q is already connecting", name)
	}
	m.connecting[name] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.connecting, name)
		m.mu.Unlock()
	}()

	var transport Transport
	var err error

	switch strings.ToLower(cfg.Transport) {
	case "stdio":
		if cfg.Command == "" {
			return nil, fmt.Errorf("stdio transport requires 'command'")
		}
		transport, err = NewStdioTransport(cfg.Command, cfg.Args, cfg.Env, cfg.Dir)
		if err != nil {
			return nil, fmt.Errorf("failed to start stdio MCP %q: %w", name, err)
		}
	case "sse":
		if cfg.URL == "" {
			return nil, fmt.Errorf("sse transport requires 'url'")
		}
		transport, err = NewSSETransport(ctx, cfg.URL, cfg.Headers)
		if err != nil {
			return nil, fmt.Errorf("failed to connect sse MCP %q: %w", name, err)
		}
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q (use 'stdio' or 'sse')", cfg.Transport)
	}

	client := NewClient(transport)
	if _, err := client.Initialize(ctx, "IronClaveBot", "1.0.0"); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("MCP handshake failed for %q: %w", name, err)
	}

	m.mu.Lock()
	m.clients[name] = client
	m.mu.Unlock()

	return client, nil
}

// ConnectAllAndRegisterTools initializes all configured MCP servers and registers tools with the registry
func (m *Manager) ConnectAllAndRegisterTools(ctx context.Context, reg *tools.Registry) error {
	m.mu.RLock()
	names := make([]string, 0, len(m.configs))
	for name := range m.configs {
		names = append(names, name)
	}
	m.mu.RUnlock()

	for _, name := range names {
		client, err := m.Connect(ctx, name)
		if err != nil {
			log.Printf("⚠️ [MCP] Failed to connect server %q: %v", name, err)
			continue
		}

		mcpTools, err := client.ListTools(ctx)
		if err != nil {
			log.Printf("⚠️ [MCP] Failed to list tools from %q: %v", name, err)
			continue
		}

		for _, t := range mcpTools {
			adapter := NewMCPToolAdapter(name, client, t)
			if err := reg.RegisterUnique(adapter); err != nil {
				log.Printf("⚠️ [MCP] Skipping tool %q from server %q: %v", t.Name, name, err)
				continue
			}
			log.Printf("🔌 [MCP] Registered tool %s from server %s", adapter.Name(), name)
		}
	}
	return nil
}

// ListServers returns status for all managed servers. Remote calls happen
// after the lock is released so Close can never be blocked by a slow server.
func (m *Manager) ListServers(ctx context.Context) []ServerStatus {
	type entry struct {
		name   string
		client *Client
		cfg    ServerConfig
	}

	m.mu.RLock()
	entries := make([]entry, 0, len(m.clients))
	for name, client := range m.clients {
		entries = append(entries, entry{name: name, client: client, cfg: m.configs[name]})
	}
	m.mu.RUnlock()

	res := make([]ServerStatus, 0, len(entries))
	for _, e := range entries {
		toolsList, _ := e.client.ListTools(ctx)
		toolNames := make([]string, 0, len(toolsList))
		for _, t := range toolsList {
			toolNames = append(toolNames, t.Name)
		}

		res = append(res, ServerStatus{
			Name:       e.name,
			Transport:  e.cfg.Transport,
			ServerInfo: e.client.ServerInfo(),
			ToolCount:  len(toolsList),
			Tools:      toolNames,
		})
	}
	return res
}

// Close disconnects all active MCP clients
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			log.Printf("⚠️ [MCP] Error closing client %q: %v", name, err)
		}
	}
	m.clients = make(map[string]*Client)
	return nil
}
