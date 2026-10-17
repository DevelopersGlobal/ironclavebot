package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SSETransport implements MCP Transport over Server-Sent Events (SSE) and HTTP POST
type SSETransport struct {
	sseURL      string
	postURL     string
	postURLMu   sync.RWMutex
	headers     map[string]string
	client      *http.Client
	cancelSSE   context.CancelFunc
	pendingMu   sync.Mutex
	pending     map[string]chan *JSONRPCResponse
	closed      atomic.Bool
	endpointRdy chan struct{}
}

// NewSSETransport establishes an SSE connection to a remote MCP server
func NewSSETransport(ctx context.Context, sseURL string, headers map[string]string) (*SSETransport, error) {
	parsedURL, err := url.Parse(sseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SSE URL: %w", err)
	}

	sseCtx, cancel := context.WithCancel(context.Background())

	t := &SSETransport{
		sseURL:      parsedURL.String(),
		headers:     headers,
		client:      &http.Client{Timeout: 0}, // No global timeout for long-lived SSE stream
		cancelSSE:   cancel,
		pending:     make(map[string]chan *JSONRPCResponse),
		endpointRdy: make(chan struct{}),
	}

	req, err := http.NewRequestWithContext(sseCtx, http.MethodGet, t.sseURL, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create SSE request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to connect to SSE stream %s: %w", t.sseURL, err)
	}

	if resp.StatusCode != http.StatusOK {
		cancel()
		_ = resp.Body.Close()
		return nil, fmt.Errorf("SSE connection failed with HTTP status %d", resp.StatusCode)
	}

	go t.sseListenLoop(resp.Body)

	// Wait for the "endpoint" event or timeout after 10s
	select {
	case <-t.endpointRdy:
		// Ready!
	case <-time.After(10 * time.Second):
		// Fallback: If no endpoint event arrived, default post URL to the base SSE URL
		t.setPostURL(t.sseURL)
	case <-ctx.Done():
		t.Close()
		return nil, ctx.Err()
	}

	return t, nil
}

func (t *SSETransport) setPostURL(raw string) {
	t.postURLMu.Lock()
	defer t.postURLMu.Unlock()

	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		t.postURL = raw
	} else {
		// Resolve relative path against base sseURL
		base, err := url.Parse(t.sseURL)
		if err == nil {
			rel, err := url.Parse(raw)
			if err == nil {
				t.postURL = base.ResolveReference(rel).String()
			} else {
				t.postURL = raw
			}
		} else {
			t.postURL = raw
		}
	}

	select {
	case <-t.endpointRdy:
	default:
		close(t.endpointRdy)
	}
}

func (t *SSETransport) getPostURL() string {
	t.postURLMu.RLock()
	defer t.postURLMu.RUnlock()
	return t.postURL
}

func (t *SSETransport) sseListenLoop(body io.ReadCloser) {
	defer body.Close()
	scanner := bufio.NewScanner(body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 16*1024*1024)

	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			currentEvent = ""
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

			if currentEvent == "endpoint" {
				t.setPostURL(data)
				continue
			}

			// Handle JSON-RPC message event
			var rpcResp JSONRPCResponse
			if err := json.Unmarshal([]byte(data), &rpcResp); err == nil && rpcResp.ID != nil {
				idKey := fmt.Sprint(rpcResp.ID)
				t.pendingMu.Lock()
				ch, ok := t.pending[idKey]
				if ok {
					delete(t.pending, idKey)
				}
				t.pendingMu.Unlock()

				if ok && ch != nil {
					ch <- &rpcResp
				}
			}
		}
	}

	t.pendingMu.Lock()
	for k, ch := range t.pending {
		close(ch)
		delete(t.pending, k)
	}
	t.pendingMu.Unlock()
}

func (t *SSETransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("SSE transport is closed")
	}

	postTarget := t.getPostURL()
	if postTarget == "" {
		return nil, fmt.Errorf("MCP post endpoint has not been resolved")
	}

	idKey := fmt.Sprint(req.ID)
	ch := make(chan *JSONRPCResponse, 1)

	t.pendingMu.Lock()
	t.pending[idKey] = ch
	t.pendingMu.Unlock()

	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, idKey)
		t.pendingMu.Unlock()
	}()

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postTarget, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create POST request: %w", err)
	}

	postReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		postReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(postReq)
	if err != nil {
		return nil, fmt.Errorf("MCP POST failed: %w", err)
	}
	defer resp.Body.Close()

	// Some MCP servers immediately return the JSON-RPC response in the HTTP response body
	if resp.StatusCode == http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr == nil && len(respBody) > 0 {
			var directResp JSONRPCResponse
			if err := json.Unmarshal(respBody, &directResp); err == nil && directResp.ID != nil {
				return &directResp, nil
			}
		}
	}

	// Otherwise, wait for the response to arrive via the SSE message event stream
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r, ok := <-ch:
		if !ok || r == nil {
			return nil, fmt.Errorf("SSE connection closed or empty response received")
		}
		return r, nil
	}
}

func (t *SSETransport) Notify(ctx context.Context, req *JSONRPCRequest) error {
	if t.closed.Load() {
		return fmt.Errorf("SSE transport is closed")
	}

	postTarget := t.getPostURL()
	if postTarget == "" {
		return fmt.Errorf("MCP post endpoint has not been resolved")
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postTarget, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	postReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		postReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(postReq)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func (t *SSETransport) Close() error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	t.cancelSSE()
	return nil
}
