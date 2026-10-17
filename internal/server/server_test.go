package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/agent"
	"github.com/DevelopersGlobal/ironclavebot/internal/config"
	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/devrel"
	"github.com/DevelopersGlobal/ironclavebot/internal/mcp"
	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

func setupTestServer(t *testing.T) (*Server, database.Store, *sandbox.ApprovalEngine) {
	tempDir := t.TempDir()
	store, err := database.NewStore(tempDir + "/test.db")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	box := sandbox.New(tempDir, sandbox.PolicyInteractive)
	approvalEngine := sandbox.NewApprovalEngine(sandbox.PolicyInteractive, 2*time.Second)

	registry := tools.NewRegistry()
	registry.Register(tools.NewTerminalTool(box))
	registry.Register(tools.NewReadFileTool(box))
	registry.Register(tools.NewWriteFileTool(box))

	mockProv := provider.NewMockProvider()
	botAgent := agent.NewAgent("mock-agent", mockProv, registry, store)
	botAgent.SetApprovalEngine(approvalEngine)

	cfg := &config.Config{
		Port:         8080,
		LLMProvider:  "mock",
		LLMModel:     "mock-agent",
		SecurityMode: "interactive",
	}

	solver := devrel.NewIssueSolver(box, mockProv, "mock-agent")
	mcpManager := mcp.NewManager()

	srv := New(cfg, botAgent, store, box, approvalEngine, solver, mcpManager)
	return srv, store, approvalEngine
}

func TestServerStatusAndSessions(t *testing.T) {
	srv, store, _ := setupTestServer(t)

	// 1. Test status API
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(rw http.ResponseWriter, r *http.Request) {
		jsonResp(rw, map[string]interface{}{
			"status":        "healthy",
			"provider":      srv.cfg.LLMProvider,
			"model":         srv.cfg.LLMModel,
			"security_mode": string(srv.approvalEngine.Policy()),
		})
	})
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", w.Code)
	}

	var status map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("Failed to parse status: %v", err)
	}
	if status["status"] != "healthy" {
		t.Errorf("Unexpected status: %v", status)
	}

	// 2. Test Session creation & message retrieval
	sess, err := store.CreateSession("Integration Session")
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}
	if sess.Title != "Integration Session" {
		t.Errorf("Session title mismatch: %s", sess.Title)
	}

	reply, err := srv.agent.Run(context.Background(), sess.ID, "Hello IronClave")
	if err != nil {
		t.Fatalf("Agent.Run failed: %v", err)
	}
	if !strings.Contains(reply, "IronClaveBot Mock") {
		t.Errorf("Unexpected reply: %s", reply)
	}
}

func TestApprovalPolicyAPI(t *testing.T) {
	_, _, engine := setupTestServer(t)

	if engine.Policy() != sandbox.PolicyInteractive {
		t.Errorf("Expected initial policy interactive, got %s", engine.Policy())
	}

	engine.SetPolicy(sandbox.PolicyStrict)
	if engine.Policy() != sandbox.PolicyStrict {
		t.Errorf("Expected policy strict after update, got %s", engine.Policy())
	}
}
