package server

import (
	"html/template"
	"log"
	"net/http"
	"sync"

	"tahini.dev/tahini/internal/db"
	"tahini.dev/tahini/internal/hub"
	"tahini.dev/tahini/internal/tofu"
	"tahini.dev/tahini/web"
)

type Config struct {
	AdminUser     string
	AdminPass     string
	SessionSecret string
	Addr          string
	InternalURL   string // base URL agents use to dial back, e.g. http://tahini-svc:8080
}

type Server struct {
	db       *db.DB
	executor *tofu.Executor
	config   Config
	mux      *http.ServeMux
	hub      *hub.Hub
	building sync.Map // workspaceID -> struct{}
}

func New(database *db.DB, executor *tofu.Executor, config Config) *Server {
	if config.SessionSecret == "" {
		config.SessionSecret = generateSecret()
		log.Println("warning: TAHINI_SESSION_SECRET not set, sessions will not survive restarts")
	}
	s := &Server{
		db:       database,
		executor: executor,
		config:   config,
		mux:      http.NewServeMux(),
		hub:      hub.New(),
	}
	s.routes()
	return s
}

func (s *Server) Start() error {
	return http.ListenAndServe(s.config.Addr, s.mux)
}

func (s *Server) routes() {
	// Public
	s.mux.HandleFunc("GET /login", s.handleLoginPage)
	s.mux.HandleFunc("POST /login", s.handleLogin)
	s.mux.HandleFunc("POST /logout", s.handleLogout)

	// Protected – wrap each route group with requireAuth
	auth := s.requireAuth

	s.mux.Handle("GET /", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/workspaces", http.StatusFound)
	})))

	// Templates
	s.mux.Handle("GET /templates", auth(http.HandlerFunc(s.handleTemplatesList)))
	s.mux.Handle("GET /templates/new", auth(http.HandlerFunc(s.handleTemplateNew)))
	s.mux.Handle("POST /templates", auth(http.HandlerFunc(s.handleTemplateCreate)))
	s.mux.Handle("GET /templates/{id}", auth(http.HandlerFunc(s.handleTemplateDetail)))
	s.mux.Handle("GET /templates/{id}/edit", auth(http.HandlerFunc(s.handleTemplateEdit)))
	s.mux.Handle("POST /templates/{id}/update", auth(http.HandlerFunc(s.handleTemplateUpdate)))
	s.mux.Handle("POST /templates/{id}/delete", auth(http.HandlerFunc(s.handleTemplateDelete)))

	// Workspaces
	s.mux.Handle("GET /workspaces", auth(http.HandlerFunc(s.handleWorkspacesList)))
	s.mux.Handle("GET /workspaces/new", auth(http.HandlerFunc(s.handleWorkspaceNew)))
	s.mux.Handle("POST /workspaces", auth(http.HandlerFunc(s.handleWorkspaceCreate)))
	s.mux.Handle("GET /workspaces/{id}", auth(http.HandlerFunc(s.handleWorkspaceDetail)))
	s.mux.Handle("POST /workspaces/{id}/start", auth(http.HandlerFunc(s.handleWorkspaceStart)))
	s.mux.Handle("POST /workspaces/{id}/stop", auth(http.HandlerFunc(s.handleWorkspaceStop)))
	s.mux.Handle("POST /workspaces/{id}/delete", auth(http.HandlerFunc(s.handleWorkspaceDelete)))

	// JSON API for polling
	s.mux.Handle("GET /api/workspaces/{id}/status", auth(http.HandlerFunc(s.handleAPIWorkspaceStatus)))
	s.mux.Handle("GET /api/builds/{id}", auth(http.HandlerFunc(s.handleAPIBuild)))

	// Agent WebSocket endpoint (no auth middleware – authenticated via token param)
	s.mux.HandleFunc("GET /agent/connect", s.handleAgentConnect)

	// Terminal UI + WebSocket (protected)
	s.mux.Handle("GET /workspaces/{id}/terminal", auth(http.HandlerFunc(s.handleWorkspaceTerminalPage)))
	s.mux.Handle("GET /ws/workspaces/{id}/terminal", auth(http.HandlerFunc(s.handleWorkspaceTerminalWS)))
}

// render parses base.html + the named page template and executes "base".
func (s *Server) render(w http.ResponseWriter, page string, data any) {
	tmpl, err := template.ParseFS(web.TemplateFS, "templates/base.html", "templates/"+page+".html")
	if err != nil {
		http.Error(w, "template parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("template render error (%s): %v", page, err)
	}
}

// renderLogin renders the standalone login page.
func (s *Server) renderLogin(w http.ResponseWriter, data any) {
	tmpl, err := template.ParseFS(web.TemplateFS, "templates/login.html")
	if err != nil {
		http.Error(w, "template parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "login", data); err != nil {
		log.Printf("template render error (login): %v", err)
	}
}
