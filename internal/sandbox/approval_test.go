package sandbox

import (
	"context"
	"testing"
	"time"
)

func TestApprovalEnginePolicies(t *testing.T) {
	strictEngine := NewApprovalEngine(PolicyStrict, 1*time.Second)
	interactiveEngine := NewApprovalEngine(PolicyInteractive, 1*time.Second)
	permissiveEngine := NewApprovalEngine(PolicyPermissive, 1*time.Second)

	// Strict should require approval for all
	if !strictEngine.RequiresApproval("read_file", `{"path":"foo.txt"}`) {
		t.Errorf("Strict policy should require approval for read_file")
	}
	if !strictEngine.RequiresApproval("run_command", `{"command":"ls"}`) {
		t.Errorf("Strict policy should require approval for run_command")
	}

	// Interactive should auto-approve read, but require approval for write and command
	if interactiveEngine.RequiresApproval("read_file", `{"path":"foo.txt"}`) {
		t.Errorf("Interactive policy should NOT require approval for read_file")
	}
	if !interactiveEngine.RequiresApproval("write_file", `{"path":"foo.txt"}`) {
		t.Errorf("Interactive policy should require approval for write_file")
	}
	if !interactiveEngine.RequiresApproval("run_command", `{"command":"ls"}`) {
		t.Errorf("Interactive policy should require approval for run_command")
	}

	// Permissive should not require approval
	if permissiveEngine.RequiresApproval("run_command", `{"command":"ls"}`) {
		t.Errorf("Permissive policy should not require approval")
	}
}

func TestApprovalEngineDecisionFlow(t *testing.T) {
	engine := NewApprovalEngine(PolicyInteractive, 2*time.Second)
	events, unsub := engine.Subscribe()
	defer unsub()

	ctx := context.Background()

	// Launch approval request in goroutine
	done := make(chan bool)
	go func() {
		approved, err := engine.RequestApproval(ctx, "sess-1", "run_command", `{"command":"npm test"}`, "Run test suite")
		if err != nil {
			t.Errorf("RequestApproval error: %v", err)
		}
		done <- approved
	}()

	// Wait for event on subscriber
	var req *ApprovalRequest
	select {
	case req = <-events:
		if req.ToolName != "run_command" {
			t.Fatalf("Unexpected tool name in event: %s", req.ToolName)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for approval request broadcast")
	}

	// Check pending list
	pending := engine.ListPending()
	if len(pending) != 1 {
		t.Fatalf("Expected 1 pending request, got %d", len(pending))
	}

	// Submit approval
	if err := engine.SubmitDecision(req.ID, true); err != nil {
		t.Fatalf("SubmitDecision failed: %v", err)
	}

	// Check outcome
	select {
	case result := <-done:
		if !result {
			t.Errorf("Expected request to be approved, got false")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for approval resolution")
	}

	if len(engine.ListPending()) != 0 {
		t.Errorf("Expected 0 pending requests after decision")
	}
}

func TestApprovalEngineTimeout(t *testing.T) {
	engine := NewApprovalEngine(PolicyInteractive, 50*time.Millisecond)
	ctx := context.Background()

	approved, err := engine.RequestApproval(ctx, "sess-2", "run_command", `{"command":"echo 1"}`, "Quick test")
	if approved {
		t.Errorf("Expected approval to fail due to timeout")
	}
	if err == nil {
		t.Errorf("Expected timeout error, got nil")
	}
}
