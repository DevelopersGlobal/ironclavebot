package provider

import (
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
}

// CompletionResponse is received from the LLM
type CompletionResponse struct {
	Message ChatMessage `json:"message"`
}

// Provider is the common interface across OpenAI, Ollama, Anthropic, etc.
type Provider interface {
	Chat(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
}

// OpenAICompatibleProvider connects to OpenAI, Ollama, DeepSeek, vLLM, Groq
type OpenAICompatibleProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewOpenAIProvider creates an API client
func NewOpenAIProvider(baseURL, apiKey string) *OpenAICompatibleProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAICompatibleProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 60 * time.Second},
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
