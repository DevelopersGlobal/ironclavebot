package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/DevelopersGlobal/ironclavebot/internal/agent"
	"github.com/DevelopersGlobal/ironclavebot/internal/config"
	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
	"github.com/DevelopersGlobal/ironclavebot/web"
)

// Server provides the HTTP dashboard and agent REST API
type Server struct {
	cfg     *config.Config
	agent   *agent.Agent
	store   database.Store
	sandbox *sandbox.Sandbox
}

// New creates an HTTP API server instance
func New(cfg *config.Config, a *agent.Agent, s database.Store, sb *sandbox.Sandbox) *Server {
	return &Server{
		cfg:     cfg,
		agent:   a,
		store:   s,
		sandbox: sb,
	}
}

// Start listens on the configured port
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// 1. Embedded static frontend dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := web.Assets.ReadFile("index.html")
		if err != nil {
			http.Error(w, "Asset not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	// 2. Status API
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, map[string]interface{}{
			"status":        "healthy",
			"provider":      s.cfg.LLMProvider,
			"model":         s.cfg.LLMModel,
			"security_mode": s.cfg.SecurityMode,
			"sandbox":       s.sandbox.Workdir(),
		})
	})

	// 3. Sessions API
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			sessions, err := s.store.ListSessions()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			jsonResp(w, sessions)
			return
		}

		if r.Method == http.MethodPost {
			var body struct {
				Title string `json:"title"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Title == "" {
				body.Title = "Persistent Teammate"
			}
			sess, err := s.store.CreateSession(body.Title)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			jsonResp(w, sess)
			return
		}

		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	// 4. Session detail routes (/api/sessions/{id}/...)
	mux.HandleFunc("/api/sessions/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}

		sessionID := parts[2]

		// GET /api/sessions/{id}/messages
		if len(parts) == 4 && parts[3] == "messages" && r.Method == http.MethodGet {
			msgs, err := s.store.GetMessages(sessionID)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			jsonResp(w, msgs)
			return
		}

		// POST /api/sessions/{id}/chat
		if len(parts) == 4 && parts[3] == "chat" && r.Method == http.MethodPost {
			var body struct {
				Prompt string `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
				http.Error(w, "invalid prompt payload", 400)
				return
			}

			reply, err := s.agent.Run(r.Context(), sessionID, body.Prompt)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}

			jsonResp(w, map[string]string{
				"response": reply,
			})
			return
		}

		http.NotFound(w, r)
	})

	addr := fmt.Sprintf(":%d", s.cfg.Port)
	log.Printf("🛡️ [IronClaveBot] Running at http://127.0.0.1%s", addr)
	log.Printf("🔒 [IronClaveBot] Sandbox isolated at %s", s.sandbox.Workdir())
	return http.ListenAndServe(addr, mux)
}

func jsonResp(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}
