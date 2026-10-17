package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Tool represents an executable capability for the AI agent
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]interface{}
	Execute(ctx context.Context, argsJSON string) (string, error)
}

// Registry manages the set of available agent tools
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates a new tool catalog
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Get retrieves a tool by name
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, t)
	}
	return list
}

// DescribeTools returns JSON descriptions suitable for LLM tool calling (OpenAI / Anthropic format)
func (r *Registry) DescribeTools() []map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := make([]map[string]interface{}, 0, len(r.tools))
	for _, t := range r.tools {
		res = append(res, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Name(),
				"description": t.Description(),
				"parameters":  t.Parameters(),
			},
		})
	}
	return res
}

// ParseArgs is a generic helper to decode tool arguments
func ParseArgs[T any](argsJSON string, target *T) error {
	if err := json.Unmarshal([]byte(argsJSON), target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}
