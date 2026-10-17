package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DevelopersGlobal/ironclavebot/internal/agent"
	"github.com/DevelopersGlobal/ironclavebot/internal/config"
	"github.com/DevelopersGlobal/ironclavebot/internal/database"
	"github.com/DevelopersGlobal/ironclavebot/internal/sandbox"
	"github.com/DevelopersGlobal/ironclavebot/web"
)

const maxRequestBody = 1 << 20 // 1 MiB

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

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Handler builds the HTTP handler with all routes and protections.
func (s *Server) Handler() http.Handler {
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
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
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

		// Unknown sessions never reach the store or the agent.
		if _, err := s.store.GetSession(sessionID); err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

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
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
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

	return s.protect(mux)
}

// protect wraps the API routes with host, origin, content-type and token
// checks. The dashboard page itself is served without a token so a browser
// can load it and then prompt for one.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// DNS-rebinding guard: when bound to loopback, only loopback Host
		// headers are accepted.
		if isLoopbackHost(s.cfg.Host) {
			h := r.Host
			if hh, _, err := net.SplitHostPort(r.Host); err == nil {
				h = hh
			}
			if !isLoopbackHost(h) {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
		}

		// Cross-site browser requests: Origin, when sent, must match Host.
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			// Requiring JSON forces a CORS preflight for cross-origin callers,
			// which this server never approves.
			ct := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
			if ct != "application/json" {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}

		if s.cfg.APIToken != "" {
			got := bearerToken(r)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIToken)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="ironclavebot"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// Start listens on the configured host and port.
func (s *Server) Start() error {
	if !isLoopbackHost(s.cfg.Host) && s.cfg.APIToken == "" {
		return errors.New("refusing to listen on a non-loopback address without API_TOKEN: set API_TOKEN or use HOST=127.0.0.1")
	}

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("🛡️ [IronClaveBot] Running at http://%s", addr)
	log.Printf("🔒 [IronClaveBot] Sandbox workdir %s (policy: %s)", s.sandbox.Workdir(), s.sandbox.Policy())
	if s.cfg.APIToken == "" {
		log.Printf("[IronClaveBot] API token not set; access is limited to loopback")
	}
	return srv.ListenAndServe()
}

func jsonResp(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[IronClaveBot] failed to write response: %v", err)
	}
}
