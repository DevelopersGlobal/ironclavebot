package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

const SystemPrompt = `You are IronClaveBot, a security-first, persistent autonomous developer teammate built by DevelopersGlobal.
You run in a strictly isolated, sandboxed environment.
You help developers build software, debug code, review repositories, and automate tasks safely.
Always verify file paths and command risks. If an operation could be destructive, explain the rationale clearly.
Be concise, accurate, and write robust, production-grade code.`

// Agent coordinates conversational turns, tool execution, and memory persistence
type Agent struct {
	model    string
	provider provider.Provider
	tools    *tools.Registry
	store    database.Store
	maxSteps int
}

// NewAgent initializes an autonomous agent instance
func NewAgent(model string, p provider.Provider, r *tools.Registry, s database.Store) *Agent {
	return &Agent{
		model:    model,
		provider: p,
		tools:    r,
		store:    s,
		maxSteps: 8,
	}
}

// Run executes a user interaction turn within a session
func (a *Agent) Run(ctx context.Context, sessionID, userInput string) (string, error) {
	// 1. Record incoming user message
	userMsg := &database.Message{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		SessionID: sessionID,
		Role:      "user",
		Content:   userInput,
		Timestamp: time.Now(),
	}
	if err := a.store.AddMessage(userMsg); err != nil {
		return "", err
	}

	// 2. Fetch entire session history
	history, err := a.store.GetMessages(sessionID)
	if err != nil {
		return "", err
	}

	// 3. Format history for completion provider
	messages := make([]provider.ChatMessage, 0, len(history)+1)
	messages = append(messages, provider.ChatMessage{
		Role:    "system",
		Content: SystemPrompt,
	})

	for _, m := range history {
		messages = append(messages, provider.ChatMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	toolDefs := a.tools.DescribeTools()

	// 4. Autonomous Agent Loop (ReAct)
	for step := 0; step < a.maxSteps; step++ {
		req := &provider.CompletionRequest{
			Model:       a.model,
			Messages:    messages,
			Tools:       toolDefs,
			Temperature: 0.2,
		}

		resp, err := a.provider.Chat(ctx, req)
		if err != nil {
			return "", fmt.Errorf("agent reasoning error: %w", err)
		}

		// Check if LLM requested tool execution
		if len(resp.Message.ToolCalls) == 0 {
			// Final response reached
			finalMsg := &database.Message{
				ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
				SessionID: sessionID,
				Role:      "assistant",
				Content:   resp.Message.Content,
				Timestamp: time.Now(),
			}
			_ = a.store.AddMessage(finalMsg)
			return resp.Message.Content, nil
		}

		// Process each tool call
		messages = append(messages, resp.Message)
		for _, tc := range resp.Message.ToolCalls {
			toolName := tc.Function.Name
			toolArgs := tc.Function.Arguments
			log.Printf("[IronClave Agent] Tool Invocation: %s args=%s", toolName, toolArgs)

			tool, exists := a.tools.Get(toolName)
			var toolResult string
			if !exists {
				toolResult = fmt.Sprintf("Error: tool %s does not exist", toolName)
			} else {
				out, execErr := tool.Execute(ctx, toolArgs)
				if execErr != nil {
					toolResult = fmt.Sprintf("Error executing tool: %v", execErr)
				} else {
					toolResult = out
				}
			}

			// Record tool output
			toolCallJSON, _ := json.Marshal(tc)
			toolHistoryMsg := &database.Message{
				ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
				SessionID: sessionID,
				Role:      "tool",
				Content:   toolResult,
				ToolCalls: string(toolCallJSON),
				Timestamp: time.Now(),
			}
			_ = a.store.AddMessage(toolHistoryMsg)

			messages = append(messages, provider.ChatMessage{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
			})
		}
	}

	return "Task completed maximum step limit.", nil
}
