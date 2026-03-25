package server

import (
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

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
	InternalURL   string        // base URL agents use to dial back, e.g. http://tahini-svc:8080
	IdleTimeout   time.Duration // stop environment after this long without an agent heartbeat (0 = disabled)
}

type Server struct {
	db       *db.DB
	executor *tofu.Executor
	config   Config
	mux      *http.ServeMux
	hub      *hub.Hub
	building sync.Map // environmentID -> struct{}
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
	if s.config.IdleTimeout > 0 {
		go s.runIdleWatcher()
	}
	return http.ListenAndServe(s.config.Addr, s.mux)
}

func (s *Server) runIdleWatcher() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		envs, err := s.db.ListEnvironments()
		if err != nil {
			continue
		}
		for _, e := range envs {
			if e.Status != "running" {
				continue
			}
			last := s.hub.AgentLastSeen(e.ID)
			if last.IsZero() {
				continue
			}
			if time.Since(last) > s.config.IdleTimeout {
				log.Printf("idle-watcher: stopping environment %s (idle for %s)", e.ID, time.Since(last).Round(time.Second))
				if _, loaded := s.building.LoadOrStore(e.ID, struct{}{}); loaded {
					continue
				}
				bp, err := s.db.GetBlueprint(e.BlueprintID)
				if err != nil {
					s.building.Delete(e.ID)
					continue
				}
				if err := s.executor.WriteHCL(e.ID, bp.HCL); err != nil {
					s.building.Delete(e.ID)
					continue
				}
				runID := generateSecret()[:32]
				logPath := s.executor.LogPath(e.ID, runID)
				if _, err := s.db.CreateRun(e.ID, "stop", logPath); err != nil {
					s.building.Delete(e.ID)
					continue
				}
				s.db.UpdateEnvironmentStatus(e.ID, "stopping")
				params := parseParams(e.Params)
				agentParams := []string{"agent_token=" + e.AgentToken}
				if s.config.InternalURL != "" {
					agentParams = append(agentParams, "tahini_url="+s.config.InternalURL)
				}
				params = append(agentParams, params...)
				go s.executeRun(e.ID, runID, "stop", params)
			}
		}
	}
}

func (s *Server) routes() {
	// Static assets
	staticFS, _ := fs.Sub(web.StaticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

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
		http.Redirect(w, r, "/environments", http.StatusFound)
	})))

	// Blueprints
	s.mux.Handle("GET /blueprints", auth(http.HandlerFunc(s.handleBlueprintsList)))
	s.mux.Handle("GET /blueprints/new", auth(http.HandlerFunc(s.handleBlueprintNew)))
	s.mux.Handle("POST /blueprints", auth(http.HandlerFunc(s.handleBlueprintCreate)))
	s.mux.Handle("GET /blueprints/{id}", auth(http.HandlerFunc(s.handleBlueprintDetail)))
	s.mux.Handle("GET /blueprints/{id}/edit", auth(http.HandlerFunc(s.handleBlueprintEdit)))
	s.mux.Handle("POST /blueprints/{id}/update", auth(http.HandlerFunc(s.handleBlueprintUpdate)))
	s.mux.Handle("POST /blueprints/{id}/delete", auth(http.HandlerFunc(s.handleBlueprintDelete)))
	s.mux.Handle("POST /blueprints/{id}/sync", auth(http.HandlerFunc(s.handleBlueprintSync)))

	// Environments
	s.mux.Handle("GET /environments", auth(http.HandlerFunc(s.handleEnvironmentsList)))
	s.mux.Handle("GET /environments/new", auth(http.HandlerFunc(s.handleEnvironmentNew)))
	s.mux.Handle("POST /environments", auth(http.HandlerFunc(s.handleEnvironmentCreate)))
	s.mux.Handle("GET /environments/{id}", auth(http.HandlerFunc(s.handleEnvironmentDetail)))
	s.mux.Handle("POST /environments/{id}/start", auth(http.HandlerFunc(s.handleEnvironmentStart)))
	s.mux.Handle("POST /environments/{id}/stop", auth(http.HandlerFunc(s.handleEnvironmentStop)))
	s.mux.Handle("POST /environments/{id}/delete", auth(http.HandlerFunc(s.handleEnvironmentDelete)))
	s.mux.Handle("POST /environments/{id}/params", auth(http.HandlerFunc(s.handleEnvironmentUpdateParams)))

	// Internal JSON API for UI polling
	s.mux.Handle("GET /api/environments/{id}/status", auth(http.HandlerFunc(s.handleAPIEnvironmentStatus)))
	s.mux.Handle("GET /api/runs/{id}", auth(http.HandlerFunc(s.handleAPIRun)))
	s.mux.Handle("GET /api/runs/{id}/stream", auth(http.HandlerFunc(s.handleAPIRunStream)))

	// Agent WebSocket endpoints (no auth middleware – authenticated via token param)
	s.mux.HandleFunc("GET /agent/connect", s.handleAgentConnect)
	s.mux.HandleFunc("GET /agent/portforward", s.handleAgentPortForward)

	// Terminal UI + WebSocket (protected)
	s.mux.Handle("GET /environments/{id}/terminal", auth(http.HandlerFunc(s.handleEnvironmentTerminalPage)))
	s.mux.Handle("GET /ws/environments/{id}/terminal", auth(http.HandlerFunc(s.handleEnvironmentTerminalWS)))

	// Port forwarding via agent (protected)
	s.mux.Handle("GET /ws/environments/{id}/ports/{port}", auth(http.HandlerFunc(s.handleEnvironmentPortForwardWS)))

	// Admin (owner + user_admin)
	ownerAuth := s.requireOwnerOrAdmin
	s.mux.Handle("GET /admin/users", ownerAuth(http.HandlerFunc(s.handleAdminUsers)))
	s.mux.Handle("POST /admin/users", ownerAuth(http.HandlerFunc(s.handleAdminUserCreate)))
	s.mux.Handle("POST /admin/users/{id}/role", ownerAuth(http.HandlerFunc(s.handleAdminUserUpdateRole)))
	s.mux.Handle("POST /admin/users/{id}/delete", ownerAuth(http.HandlerFunc(s.handleAdminUserDelete)))
	s.mux.Handle("GET /admin/orgs", ownerAuth(http.HandlerFunc(s.handleAdminOrgs)))
	s.mux.Handle("POST /admin/orgs", ownerAuth(http.HandlerFunc(s.handleAdminOrgCreate)))
	s.mux.Handle("POST /admin/orgs/{id}/delete", ownerAuth(http.HandlerFunc(s.handleAdminOrgDelete)))
	s.mux.Handle("GET /admin/audit", ownerAuth(http.HandlerFunc(s.handleAdminAudit)))

	// User profile + API token management
	s.mux.Handle("GET /profile", auth(http.HandlerFunc(s.handleProfilePage)))
	s.mux.Handle("POST /profile/password", auth(http.HandlerFunc(s.handleProfilePassword)))
	s.mux.Handle("POST /profile/tokens", auth(http.HandlerFunc(s.handleProfileCreateToken)))
	s.mux.Handle("POST /profile/tokens/{id}/delete", auth(http.HandlerFunc(s.handleProfileDeleteToken)))

	// REST API v1 (Bearer token or session, returns JSON)
	apiAuth := s.requireAPIAuth
	s.mux.Handle("GET /api/v1/environments", apiAuth(http.HandlerFunc(s.handleAPIV1ListEnvironments)))
	s.mux.Handle("POST /api/v1/environments", apiAuth(http.HandlerFunc(s.handleAPIV1CreateEnvironment)))
	s.mux.Handle("GET /api/v1/environments/{id}", apiAuth(http.HandlerFunc(s.handleAPIV1GetEnvironment)))
	s.mux.Handle("POST /api/v1/environments/{id}/start", apiAuth(http.HandlerFunc(s.handleAPIV1StartEnvironment)))
	s.mux.Handle("POST /api/v1/environments/{id}/stop", apiAuth(http.HandlerFunc(s.handleAPIV1StopEnvironment)))
	s.mux.Handle("POST /api/v1/environments/{id}/delete", apiAuth(http.HandlerFunc(s.handleAPIV1DeleteEnvironment)))
	s.mux.Handle("GET /api/v1/blueprints", apiAuth(http.HandlerFunc(s.handleAPIV1ListBlueprints)))
	s.mux.Handle("POST /api/v1/blueprints", apiAuth(http.HandlerFunc(s.handleAPIV1CreateBlueprint)))
	s.mux.Handle("GET /api/v1/blueprints/{id}", apiAuth(http.HandlerFunc(s.handleAPIV1GetBlueprint)))
	s.mux.Handle("PUT /api/v1/blueprints/{id}", apiAuth(http.HandlerFunc(s.handleAPIV1UpdateBlueprint)))
	s.mux.Handle("DELETE /api/v1/blueprints/{id}", apiAuth(http.HandlerFunc(s.handleAPIV1DeleteBlueprint)))
	s.mux.Handle("GET /api/v1/runs/{id}", apiAuth(http.HandlerFunc(s.handleAPIV1GetRun)))
	s.mux.Handle("GET /api/v1/runs/{id}/logs", apiAuth(http.HandlerFunc(s.handleAPIV1GetRunLogs)))
	s.mux.Handle("GET /api/v1/tokens", apiAuth(http.HandlerFunc(s.handleAPIV1ListTokens)))
	s.mux.Handle("POST /api/v1/tokens", apiAuth(http.HandlerFunc(s.handleAPIV1CreateToken)))
	s.mux.Handle("DELETE /api/v1/tokens/{id}", apiAuth(http.HandlerFunc(s.handleAPIV1DeleteToken)))
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
