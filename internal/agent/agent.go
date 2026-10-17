package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

const SystemPrompt = `You are IronClaveBot, a security-first, persistent autonomous developer teammate built by DevelopersGlobal.
You run in a strictly isolated, sandboxed environment.
You help developers build software, debug code, review repositories, and automate tasks safely.
Always verify file paths and command risks. If an operation could be destructive, explain the rationale clearly.
Be concise, accurate, and write robust, production-grade code.`

// EventType categorizes real-time agent output events
type EventType string

const (
	EventThought          EventType = "thought"
	EventToken            EventType = "token"
	EventToolCall         EventType = "tool_call"
	EventApprovalRequired EventType = "approval_required"
	EventToolResult       EventType = "tool_result"
	EventDone             EventType = "done"
	EventError            EventType = "error"
)

// AgentEvent represents a streaming event emitted during agent reasoning
type AgentEvent struct {
	Type      EventType `json:"type"`
	Content   string    `json:"content,omitempty"`
	ToolName  string    `json:"tool_name,omitempty"`
	ToolArgs  string    `json:"tool_args,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
}

// EventHandler receives real-time agent events
type EventHandler func(event AgentEvent) error

// Agent coordinates conversational turns, tool execution, and memory persistence
type Agent struct {
	model          string
	provider       provider.Provider
	tools          *tools.Registry
	store          database.Store
	approvalEngine *sandbox.ApprovalEngine
	maxSteps       int
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

// SetApprovalEngine binds the permission approval engine to the agent
func (a *Agent) SetApprovalEngine(engine *sandbox.ApprovalEngine) {
	a.approvalEngine = engine
}

// Run executes a user interaction turn synchronously
func (a *Agent) Run(ctx context.Context, sessionID, userInput string) (string, error) {
	return a.RunStream(ctx, sessionID, userInput, nil)
}

// RunStream executes a user turn while streaming thoughts, tokens, and tool events
func (a *Agent) RunStream(ctx context.Context, sessionID, userInput string, handler EventHandler) (string, error) {
	emit := func(evt AgentEvent) {
		if handler != nil {
			evt.SessionID = sessionID
			_ = handler(evt)
		}
	}

	// 1. Record incoming user message
	userMsg := &database.Message{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		SessionID: sessionID,
		Role:      "user",
		Content:   userInput,
		Timestamp: time.Now(),
	}
	if err := a.store.AddMessage(userMsg); err != nil {
		emit(AgentEvent{Type: EventError, Content: err.Error()})
		return "", err
	}

	// 2. Fetch entire session history
	history, err := a.store.GetMessages(sessionID)
	if err != nil {
		emit(AgentEvent{Type: EventError, Content: err.Error()})
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

		// Stream reasoning and tokens
		resp, err := a.provider.ChatStream(ctx, req, func(chunk provider.StreamChunk) error {
			if chunk.IsThought {
				emit(AgentEvent{Type: EventThought, Content: chunk.Token})
			} else {
				emit(AgentEvent{Type: EventToken, Content: chunk.Token})
			}
			return nil
		})

		if err != nil {
			emit(AgentEvent{Type: EventError, Content: err.Error()})
			return "", fmt.Errorf("agent reasoning error: %w", err)
		}

		// Check if LLM reached final answer (no tool calls)
		if len(resp.Message.ToolCalls) == 0 {
			finalMsg := &database.Message{
				ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
				SessionID: sessionID,
				Role:      "assistant",
				Content:   resp.Message.Content,
				Timestamp: time.Now(),
			}
			_ = a.store.AddMessage(finalMsg)
			emit(AgentEvent{Type: EventDone, Content: resp.Message.Content})
			return resp.Message.Content, nil
		}

		// Process each tool call
		messages = append(messages, resp.Message)
		for _, tc := range resp.Message.ToolCalls {
			toolName := tc.Function.Name
			toolArgs := tc.Function.Arguments
			log.Printf("[IronClave Agent] Tool Invocation: %s args=%s", toolName, toolArgs)

			emit(AgentEvent{
				Type:     EventToolCall,
				ToolName: toolName,
				ToolArgs: toolArgs,
			})

			// Granular Permission Approval Check
			if a.approvalEngine != nil && a.approvalEngine.RequiresApproval(toolName, toolArgs) {
				emit(AgentEvent{
					Type:     EventApprovalRequired,
					ToolName: toolName,
					ToolArgs: toolArgs,
				})

				approved, appErr := a.approvalEngine.RequestApproval(
					ctx,
					sessionID,
					toolName,
					toolArgs,
					fmt.Sprintf("Agent requesting invocation of tool: %s", toolName),
				)

				if appErr != nil || !approved {
					toolResult := fmt.Sprintf("⚠️ Security Gate: Tool execution blocked or rejected by user approval policy.")
					emit(AgentEvent{
						Type:     EventToolResult,
						ToolName: toolName,
						Content:  toolResult,
					})

					messages = append(messages, provider.ChatMessage{
						Role:       "tool",
						Content:    toolResult,
						ToolCallID: tc.ID,
					})
					continue
				}
			}

			// Tool Execution
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

			emit(AgentEvent{
				Type:     EventToolResult,
				ToolName: toolName,
				Content:  toolResult,
			})

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

	limitMsg := "Task reached maximum reasoning step limit."
	emit(AgentEvent{Type: EventDone, Content: limitMsg})
	return limitMsg, nil
}
