package server

import (
	"html/template"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"tahini.dev/tahini/web"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleAgentConnect is called by the tahini-agent binary inside workspace pods.
// It authenticates via token query param and hands the connection to the hub.
func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	workspace, err := s.db.GetWorkspaceByAgentToken(token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.hub.HandleAgent(workspace.ID, conn)
}

// handleWorkspaceTerminalPage renders the xterm.js terminal UI (standalone, full-screen).
func (s *Server) handleWorkspaceTerminalPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	workspace, err := s.db.GetWorkspace(id)
	if err != nil {
		http.Redirect(w, r, "/workspaces?error=workspace+not+found", http.StatusFound)
		return
	}
	tmpl, err := template.ParseFS(web.TemplateFS, "templates/workspace_terminal.html")
	if err != nil {
		http.Error(w, "template parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "terminal", map[string]any{"Workspace": workspace}); err != nil {
		log.Printf("terminal template render error: %v", err)
	}
}

// handleWorkspaceTerminalWS proxies between the browser and the workspace agent via the hub.
func (s *Server) handleWorkspaceTerminalWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.GetWorkspace(id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	sessionID := uuid.New().String()
	s.hub.HandleBrowser(id, sessionID, conn)
}
