package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DevelopersGlobal/ironclavebot/internal/agent"
	"github.com/DevelopersGlobal/ironclavebot/internal/config"
	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/provider"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
	"github.com/DevelopersGlobal/ironclavebot/internal/tools"
)

type countingProvider struct{ calls int }

func (c *countingProvider) Chat(ctx context.Context, req *provider.CompletionRequest) (*provider.CompletionResponse, error) {
	c.calls++
	return &provider.CompletionResponse{Message: provider.ChatMessage{Role: "assistant", Content: "hi"}}, nil
}

func newServer(t *testing.T, host, token string) (*Server, *countingProvider, string) {
	store, err := database.NewStore(filepath.Join(t.TempDir(), "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	prov := &countingProvider{}
	a := agent.NewAgent("m", prov, tools.NewRegistry(), store)
	box := sandbox.New(t.TempDir(), sandbox.PolicyStrict)
	cfg := &config.Config{Host: host, APIToken: token, Port: 0, LLMProvider: "mock", LLMModel: "m", SecurityMode: "strict"}
	sess, _ := store.CreateSession("s")
	return New(cfg, a, store, box), prov, sess.ID
}

func do(h http.Handler, method, target, ctype, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8080"
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUnknownSessionNeverReachesAgent(t *testing.T) {
	s, prov, _ := newServer(t, "127.0.0.1", "")
	rec := do(s.Handler(), "POST", "/api/sessions/does-not-exist/chat", "application/json", `{"prompt":"x"}`, nil)
	if rec.Code != 404 || prov.calls != 0 {
		t.Fatalf("code=%d provider calls=%d", rec.Code, prov.calls)
	}
}

func TestChatWorksForKnownSession(t *testing.T) {
	s, prov, id := newServer(t, "127.0.0.1", "")
	rec := do(s.Handler(), "POST", "/api/sessions/"+id+"/chat", "application/json", `{"prompt":"x"}`, nil)
	if rec.Code != 200 || prov.calls != 1 {
		t.Fatalf("code=%d calls=%d body=%s", rec.Code, prov.calls, rec.Body.String())
	}
}

func TestCrossSitePlainTextPostIsRejected(t *testing.T) {
	s, prov, id := newServer(t, "127.0.0.1", "")
	// A hostile page can send a "simple" text/plain POST without a preflight.
	rec := do(s.Handler(), "POST", "/api/sessions/"+id+"/chat", "text/plain", `{"prompt":"rm"}`, map[string]string{"Origin": "http://evil.example"})
	if rec.Code == 200 || prov.calls != 0 {
		t.Fatalf("cross-site request executed: code=%d", rec.Code)
	}
	// Even a JSON request from another origin is refused.
	rec = do(s.Handler(), "POST", "/api/sessions/"+id+"/chat", "application/json", `{"prompt":"x"}`, map[string]string{"Origin": "http://evil.example"})
	if rec.Code != 403 || prov.calls != 0 {
		t.Fatalf("foreign origin accepted: code=%d", rec.Code)
	}
	// Text/plain without an Origin header is rejected by the content-type rule.
	rec = do(s.Handler(), "POST", "/api/sessions/"+id+"/chat", "text/plain", `{"prompt":"x"}`, nil)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain accepted: code=%d", rec.Code)
	}
}

func TestRebindingHostRejectedOnLoopback(t *testing.T) {
	s, _, _ := newServer(t, "127.0.0.1", "")
	rec := do(s.Handler(), "GET", "/api/sessions", "", "", map[string]string{"Host": "attacker.example:8080"})
	if rec.Code != 403 {
		t.Fatalf("rebinding host accepted: %d", rec.Code)
	}
}

func TestTokenRequiredWhenSet(t *testing.T) {
	s, _, _ := newServer(t, "0.0.0.0", "s3cret")
	h := s.Handler()
	if rec := do(h, "GET", "/api/sessions", "", "", nil); rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/sessions", "", "", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != 401 {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/sessions", "", "", map[string]string{"Authorization": "Bearer s3cret"}); rec.Code != 200 {
		t.Errorf("right token: %d", rec.Code)
	}
	// The dashboard page itself loads so it can prompt for the token.
	if rec := do(h, "GET", "/", "", "", nil); rec.Code != 200 {
		t.Errorf("dashboard: %d", rec.Code)
	}
}

func TestStartRefusesPublicBindWithoutToken(t *testing.T) {
	s, _, _ := newServer(t, "0.0.0.0", "")
	if err := s.Start(); err == nil || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	s, prov, id := newServer(t, "127.0.0.1", "")
	big := `{"prompt":"` + strings.Repeat("a", 2<<20) + `"}`
	rec := do(s.Handler(), "POST", "/api/sessions/"+id+"/chat", "application/json", big, nil)
	if rec.Code != 400 || prov.calls != 0 {
		t.Fatalf("code=%d calls=%d", rec.Code, prov.calls)
	}
}
