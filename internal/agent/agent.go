package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
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

// ErrStepLimit is returned when the model keeps requesting tools and never
// produces a final answer within the step budget.
var ErrStepLimit = errors.New("agent stopped at the reasoning step limit without a final answer")

// Agent coordinates conversational turns, tool execution, and memory persistence
type Agent struct {
	model    string
	provider provider.Provider
	tools    *tools.Registry
	store    database.Store
	maxSteps int

	// sessionLocks serializes whole turns per session.
	sessionLocks sync.Map // sessionID -> *sync.Mutex
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
	// Serialize the full turn for this session; other sessions run in parallel.
	lockAny, _ := a.sessionLocks.LoadOrStore(sessionID, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

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

	messages = append(messages, replayHistory(history)...)

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
			if err := a.store.AddMessage(finalMsg); err != nil {
				return "", fmt.Errorf("failed to save assistant reply: %w", err)
			}
			return resp.Message.Content, nil
		}

		// Persist the assistant message that requested the tools so the
		// history can be replayed with matching tool_call IDs later.
		callsJSON, err := json.Marshal(resp.Message.ToolCalls)
		if err != nil {
			return "", fmt.Errorf("failed to encode tool calls: %w", err)
		}
		callMsg := &database.Message{
			ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
			SessionID: sessionID,
			Role:      "assistant",
			Content:   resp.Message.Content,
			ToolCalls: string(callsJSON),
			Timestamp: time.Now(),
		}
		if err := a.store.AddMessage(callMsg); err != nil {
			return "", fmt.Errorf("failed to save tool-call message: %w", err)
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
					if out != "" {
						toolResult += "\nOutput:\n" + out
					}
				} else {
					toolResult = out
				}
			}

			// Record tool output
			toolHistoryMsg := &database.Message{
				ID:         fmt.Sprintf("msg-%d", time.Now().UnixNano()),
				SessionID:  sessionID,
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
				Timestamp:  time.Now(),
			}
			if err := a.store.AddMessage(toolHistoryMsg); err != nil {
				return "", fmt.Errorf("failed to save tool result: %w", err)
			}

			messages = append(messages, provider.ChatMessage{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
			})
		}
	}

	// No final answer was produced. Record why, and report it as an error
	// rather than pretending the task completed.
	note := fmt.Sprintf("Stopped after %d reasoning steps without a final answer.", a.maxSteps)
	_ = a.store.AddMessage(&database.Message{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		SessionID: sessionID,
		Role:      "assistant",
		Content:   note,
		Timestamp: time.Now(),
	})
	return "", ErrStepLimit
}

// replayHistory rebuilds provider messages from stored history, restoring
// assistant tool calls and tool_call_id links. Tool results that have no
// matching assistant tool call (for example from older stored data) are
// dropped, because providers reject orphaned tool messages.
func replayHistory(history []*database.Message) []provider.ChatMessage {
	out := make([]provider.ChatMessage, 0, len(history))
	seenCalls := make(map[string]bool)
	for _, m := range history {
		switch m.Role {
		case "assistant":
			cm := provider.ChatMessage{Role: "assistant", Content: m.Content}
			if m.ToolCalls != "" {
				var calls []provider.ToolCall
				if err := json.Unmarshal([]byte(m.ToolCalls), &calls); err == nil {
					cm.ToolCalls = calls
					for _, c := range calls {
						seenCalls[c.ID] = true
					}
				}
			}
			out = append(out, cm)
		case "tool":
			id := m.ToolCallID
			if id == "" && m.ToolCalls != "" {
				// Older records stored the single call as a JSON object.
				var legacy provider.ToolCall
				if json.Unmarshal([]byte(m.ToolCalls), &legacy) == nil {
					id = legacy.ID
				}
			}
			if id == "" || !seenCalls[id] {
				continue
			}
			out = append(out, provider.ChatMessage{Role: "tool", Content: m.Content, ToolCallID: id})
		default:
			out = append(out, provider.ChatMessage{Role: m.Role, Content: m.Content})
		}
	}
	return out
}
