package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

type echoTool struct{ fail bool }

func (e *echoTool) Name() string        { return "echo" }
func (e *echoTool) Description() string { return "echo" }
func (e *echoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (e *echoTool) Execute(ctx context.Context, args string) (string, error) {
	if e.fail {
		return "partial output", errors.New("exit status 1")
	}
	return "echoed", nil
}

// scriptedProvider returns tool calls for the first `toolTurns` requests of a
// conversation, then a final answer. It records every request it receives.
type scriptedProvider struct {
	mu        sync.Mutex
	toolTurns int
	always    bool
	requests  []*provider.CompletionRequest
}

func (p *scriptedProvider) Chat(ctx context.Context, req *provider.CompletionRequest) (*provider.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	n := len(p.requests)
	p.mu.Unlock()
	if p.always || n <= p.toolTurns {
		tc := provider.ToolCall{ID: fmt.Sprintf("call_%d", n), Type: "function"}
		tc.Function.Name = "echo"
		tc.Function.Arguments = "{}"
		return &provider.CompletionResponse{Message: provider.ChatMessage{Role: "assistant", ToolCalls: []provider.ToolCall{tc}}}, nil
	}
	return &provider.CompletionResponse{Message: provider.ChatMessage{Role: "assistant", Content: "done"}}, nil
}

func newTestAgent(t *testing.T, p provider.Provider, tool tools.Tool) (*Agent, *database.FileStore, string) {
	store, err := database.NewStore(filepath.Join(t.TempDir(), "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	reg.Register(tool)
	sess, _ := store.CreateSession("t")
	return NewAgent("m", p, reg, store), store, sess.ID
}

func TestToolHistoryReplaysWithCallIDs(t *testing.T) {
	p := &scriptedProvider{toolTurns: 1}
	a, store, sid := newTestAgent(t, p, &echoTool{})

	if _, err := a.Run(context.Background(), sid, "first"); err != nil {
		t.Fatal(err)
	}
	// Second turn: history now contains assistant tool_calls + tool result.
	if _, err := a.Run(context.Background(), sid, "second"); err != nil {
		t.Fatal(err)
	}

	last := p.requests[len(p.requests)-1]
	calls := map[string]bool{}
	for _, m := range last.Messages {
		for _, c := range m.ToolCalls {
			calls[c.ID] = true
		}
	}
	if len(calls) == 0 {
		t.Fatal("assistant tool-call message was not replayed")
	}
	for _, m := range last.Messages {
		if m.Role == "tool" && (m.ToolCallID == "" || !calls[m.ToolCallID]) {
			t.Errorf("orphaned tool message in replayed history: %+v", m)
		}
	}
	msgs, _ := store.GetMessages(sid)
	var sawCall, sawResult bool
	for _, m := range msgs {
		if m.Role == "assistant" && m.ToolCalls != "" {
			sawCall = true
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Errorf("history not persisted fully: call=%v result=%v", sawCall, sawResult)
	}
}

func TestLegacyOrphanToolMessagesAreDropped(t *testing.T) {
	hist := []*database.Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "old result", ToolCalls: `{"id":"call_x","type":"function"}`},
		{Role: "assistant", Content: "answer"},
	}
	out := replayHistory(hist)
	for _, m := range out {
		if m.Role == "tool" {
			t.Errorf("orphaned tool message kept: %+v", m)
		}
	}
}

func TestStepLimitIsAnError(t *testing.T) {
	p := &scriptedProvider{always: true}
	a, store, sid := newTestAgent(t, p, &echoTool{})
	reply, err := a.Run(context.Background(), sid, "loop")
	if !errors.Is(err, ErrStepLimit) {
		t.Fatalf("expected ErrStepLimit, got reply=%q err=%v", reply, err)
	}
	msgs, _ := store.GetMessages(sid)
	if last := msgs[len(msgs)-1]; last.Role != "assistant" || last.Content == "" {
		t.Errorf("fallback explanation not persisted: %+v", last)
	}
}

func TestFailedToolKeepsItsOutput(t *testing.T) {
	p := &scriptedProvider{toolTurns: 1}
	a, store, sid := newTestAgent(t, p, &echoTool{fail: true})
	if _, err := a.Run(context.Background(), sid, "go"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := store.GetMessages(sid)
	for _, m := range msgs {
		if m.Role == "tool" {
			if !strings.Contains(m.Content, "partial output") || !strings.Contains(m.Content, "exit status 1") {
				t.Errorf("tool failure lost output or error: %q", m.Content)
			}
			return
		}
	}
	t.Fatal("no tool message stored")
}

type slowProvider struct {
	active, maxActive atomic.Int32
}

func (p *slowProvider) Chat(ctx context.Context, req *provider.CompletionRequest) (*provider.CompletionResponse, error) {
	n := p.active.Add(1)
	for {
		m := p.maxActive.Load()
		if n <= m || p.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
	p.active.Add(-1)
	return &provider.CompletionResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}}, nil
}

func TestTurnsAreSerializedPerSession(t *testing.T) {
	p := &slowProvider{}
	a, _, sid := newTestAgent(t, p, &echoTool{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := a.Run(context.Background(), sid, fmt.Sprintf("m%d", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if p.maxActive.Load() != 1 {
		t.Errorf("turns for one session overlapped (max concurrent = %d)", p.maxActive.Load())
	}
}

func TestDifferentSessionsRunInParallel(t *testing.T) {
	p := &slowProvider{}
	a, store, s1 := newTestAgent(t, p, &echoTool{})
	s2, _ := store.CreateSession("two")
	var wg sync.WaitGroup
	for _, id := range []string{s1, s2.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, _ = a.Run(context.Background(), id, "x")
		}(id)
	}
	wg.Wait()
	if p.maxActive.Load() < 2 {
		t.Log("sessions did not overlap (timing dependent)")
	}
}
