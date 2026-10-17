package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

func TestAgentRunAndStream(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.NewStore(tempDir + "/agent_test.db")
	if err != nil {
		t.Fatalf("Failed to initialize store: %v", err)
	}

	box := sandbox.New(tempDir, sandbox.PolicyPermissive)
	registry := tools.NewRegistry()
	registry.Register(tools.NewTerminalTool(box))
	registry.Register(tools.NewReadFileTool(box))
	registry.Register(tools.NewWriteFileTool(box))

	mockProv := provider.NewMockProvider()
	botAgent := NewAgent("mock-agent", mockProv, registry, store)

	sess, err := store.CreateSession("Streaming Test")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	var events []AgentEvent
	reply, err := botAgent.RunStream(context.Background(), sess.ID, "Analyze repo status", func(evt AgentEvent) error {
		events = append(events, evt)
		return nil
	})

	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}

	if !strings.Contains(reply, "IronClaveBot Mock") {
		t.Errorf("Unexpected reply: %s", reply)
	}

	var hasThought, hasToken, hasDone bool
	for _, evt := range events {
		if evt.Type == EventThought {
			hasThought = true
		}
		if evt.Type == EventToken {
			hasToken = true
		}
		if evt.Type == EventDone {
			hasDone = true
		}
	}

	if !hasThought {
		t.Errorf("Expected streaming thoughts in events list")
	}
	if !hasToken {
		t.Errorf("Expected streaming tokens in events list")
	}
	if !hasDone {
		t.Errorf("Expected done event")
	}
}

func TestAgentApprovalGate(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := database.NewStore(tempDir + "/approval_agent.db")
	box := sandbox.New(tempDir, sandbox.PolicyStrict)
	approvalEngine := sandbox.NewApprovalEngine(sandbox.PolicyStrict, 100*time.Millisecond)

	registry := tools.NewRegistry()
	registry.Register(tools.NewTerminalTool(box))

	mockProv := provider.NewMockProvider()
	botAgent := NewAgent("mock-agent", mockProv, registry, store)
	botAgent.SetApprovalEngine(approvalEngine)

	if !approvalEngine.RequiresApproval("run_command", `{"command":"ls"}`) {
		t.Errorf("Strict engine should require approval for run_command")
	}
}
