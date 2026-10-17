package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatMessage represents an LLM conversation turn
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall represents a model-requested function invocation
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// CompletionRequest is sent to the LLM
type CompletionRequest struct {
	Model       string                   `json:"model"`
	Messages    []ChatMessage            `json:"messages"`
	Tools       []map[string]interface{} `json:"tools,omitempty"`
	Temperature float64                  `json:"temperature"`
	Stream      bool                     `json:"stream,omitempty"`
}

// CompletionResponse is received from the LLM
type CompletionResponse struct {
	Message ChatMessage `json:"message"`
}

// StreamChunk conveys real-time tokens or thoughts
type StreamChunk struct {
	Token     string `json:"token"`
	IsThought bool   `json:"is_thought"`
}

// StreamCallback receives incremental chunks
type StreamCallback func(chunk StreamChunk) error

// Provider is the common interface across OpenAI, Ollama, Anthropic, DeepSeek, vLLM, etc.
type Provider interface {
	Chat(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
	ChatStream(ctx context.Context, req *CompletionRequest, cb StreamCallback) (*CompletionResponse, error)
}

// OpenAICompatibleProvider connects to OpenAI, Ollama, DeepSeek, vLLM, Groq
type OpenAICompatibleProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewOpenAIProvider creates an OpenAI-compatible API client (OpenAI, DeepSeek, Ollama, vLLM)
func NewOpenAIProvider(baseURL, apiKey string) *OpenAICompatibleProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAICompatibleProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	url := fmt.Sprintf("%s/chat/completions", p.baseURL)
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider connection error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider error (status %d): %s", resp.StatusCode, string(body))
	}

	var openAIResp struct {
		Choices []struct {
			Message ChatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to parse completion response: %w", err)
	}
	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no completion choices returned by model")
	}

	return &CompletionResponse{Message: openAIResp.Choices[0].Message}, nil
}

func (p *OpenAICompatibleProvider) ChatStream(ctx context.Context, req *CompletionRequest, cb StreamCallback) (*CompletionResponse, error) {
	streamReq := *req
	streamReq.Stream = true

	url := fmt.Sprintf("%s/chat/completions", p.baseURL)
	payload, err := json.Marshal(streamReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider stream error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider error (status %d): %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)

	var finalContent strings.Builder
	var toolCallsMap = make(map[int]*ToolCall)
	inThinkTag := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		// 1. Check reasoning / thought tokens (e.g. DeepSeek-R1 reasoning_content)
		if delta.ReasoningContent != "" {
			if cb != nil {
				_ = cb(StreamChunk{Token: delta.ReasoningContent, IsThought: true})
			}
		}

		// 2. Check standard content with optional <think> tags
		if delta.Content != "" {
			token := delta.Content
			if strings.Contains(token, "<think>") {
				inThinkTag = true
				token = strings.ReplaceAll(token, "<think>", "")
			}
			if strings.Contains(token, "</think>") {
				parts := strings.Split(token, "</think>")
				if cb != nil && parts[0] != "" {
					_ = cb(StreamChunk{Token: parts[0], IsThought: true})
				}
				inThinkTag = false
				if len(parts) > 1 && parts[1] != "" {
					finalContent.WriteString(parts[1])
					if cb != nil {
						_ = cb(StreamChunk{Token: parts[1], IsThought: false})
					}
				}
				continue
			}

			if inThinkTag {
				if cb != nil {
					_ = cb(StreamChunk{Token: token, IsThought: true})
				}
			} else {
				finalContent.WriteString(token)
				if cb != nil {
					_ = cb(StreamChunk{Token: token, IsThought: false})
				}
			}
		}

		// 3. Accumulate stream tool calls
		for _, tc := range delta.ToolCalls {
			existing, exists := toolCallsMap[tc.Index]
			if !exists {
				existing = &ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
				}
				toolCallsMap[tc.Index] = existing
			}
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Function.Name != "" {
				existing.Function.Name += tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				existing.Function.Arguments += tc.Function.Arguments
			}
		}
	}

	var accumulatedToolCalls []ToolCall
	for i := 0; i < len(toolCallsMap); i++ {
		if tc, ok := toolCallsMap[i]; ok {
			accumulatedToolCalls = append(accumulatedToolCalls, *tc)
		}
	}

	return &CompletionResponse{
		Message: ChatMessage{
			Role:      "assistant",
			Content:   finalContent.String(),
			ToolCalls: accumulatedToolCalls,
		},
	}, nil
}

// AnthropicProvider connects to Anthropic Claude Messages API (/v1/messages)
type AnthropicProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewAnthropicProvider creates a native Anthropic Claude client
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		baseURL: "https://api.anthropic.com/v1",
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AnthropicProvider) Chat(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	var systemPrompt string
	var anthroMsgs []map[string]interface{}

	for _, m := range req.Messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}
		if m.Role == "tool" {
			anthroMsgs = append(anthroMsgs, map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": m.ToolCallID,
						"content":     m.Content,
					},
				},
			})
			continue
		}
		anthroMsgs = append(anthroMsgs, map[string]interface{}{
			"role":    m.Role,
			"content": m.Content,
		})
	}

	var anthroTools []map[string]interface{}
	for _, t := range req.Tools {
		if fn, ok := t["function"].(map[string]interface{}); ok {
			anthroTools = append(anthroTools, map[string]interface{}{
				"name":         fn["name"],
				"description":  fn["description"],
				"input_schema": fn["parameters"],
			})
		}
	}

	payloadMap := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": 4096,
		"messages":   anthroMsgs,
	}
	if systemPrompt != "" {
		payloadMap["system"] = systemPrompt
	}
	if len(anthroTools) > 0 {
		payloadMap["tools"] = anthroTools
	}

	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic connection error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic error (status %d): %s", resp.StatusCode, string(body))
	}

	var anthroResp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &anthroResp); err != nil {
		return nil, fmt.Errorf("failed to parse anthropic response: %w", err)
	}

	var textBuilder strings.Builder
	var toolCalls []ToolCall
	for _, item := range anthroResp.Content {
		if item.Type == "text" {
			textBuilder.WriteString(item.Text)
		} else if item.Type == "tool_use" {
			tc := ToolCall{
				ID:   item.ID,
				Type: "function",
			}
			tc.Function.Name = item.Name
			tc.Function.Arguments = string(item.Input)
			toolCalls = append(toolCalls, tc)
		}
	}

	return &CompletionResponse{
		Message: ChatMessage{
			Role:      "assistant",
			Content:   textBuilder.String(),
			ToolCalls: toolCalls,
		},
	}, nil
}

func (a *AnthropicProvider) ChatStream(ctx context.Context, req *CompletionRequest, cb StreamCallback) (*CompletionResponse, error) {
	resp, err := a.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if cb != nil && resp.Message.Content != "" {
		words := strings.Fields(resp.Message.Content)
		for _, w := range words {
			_ = cb(StreamChunk{Token: w + " ", IsThought: false})
		}
	}
	return resp, nil
}

// MockProvider enables testing and local development without requiring external credentials
type MockProvider struct{}

func NewMockProvider() *MockProvider {
	return &MockProvider{}
}

func (m *MockProvider) Chat(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	lastMsg := ""
	if len(req.Messages) > 0 {
		lastMsg = req.Messages[len(req.Messages)-1].Content
	}

	reply := fmt.Sprintf("🛡️ [IronClaveBot Mock] Received: %q. Configure OPENAI_API_KEY or OLLAMA_BASE_URL to enable full autonomous model reasoning.", lastMsg)
	return &CompletionResponse{
		Message: ChatMessage{
			Role:    "assistant",
			Content: reply,
		},
	}, nil
}

func (m *MockProvider) ChatStream(ctx context.Context, req *CompletionRequest, cb StreamCallback) (*CompletionResponse, error) {
	// 1. Emit simulated reasoning thoughts
	if cb != nil {
		_ = cb(StreamChunk{Token: "Analyzing user instruction and isolating environment...\n", IsThought: true})
		_ = cb(StreamChunk{Token: "Verifying sandbox boundaries and command policy filters...\n", IsThought: true})
	}

	// 2. Stream assistant reply tokens
	resp, err := m.Chat(ctx, req)
	if err != nil {
		return nil, err
	}

	if cb != nil {
		words := strings.Fields(resp.Message.Content)
		for _, w := range words {
			_ = cb(StreamChunk{Token: w + " ", IsThought: false})
			time.Sleep(10 * time.Millisecond)
		}
	}

	return resp, nil
}
