package server

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"tahini.dev/tahini/web"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleAgentConnect is called by the tahini-agent binary inside environment pods.
// It authenticates via token query param and hands the connection to the hub.
func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	env, err := s.db.GetEnvironmentByAgentToken(token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.hub.HandleAgent(env.ID, conn)
}

// handleEnvironmentTerminalPage renders the xterm.js terminal UI (standalone, full-screen).
func (s *Server) handleEnvironmentTerminalPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env, err := s.db.GetEnvironment(id)
	if err != nil {
		http.Redirect(w, r, "/environments?error=environment+not+found", http.StatusFound)
		return
	}
	tmpl, err := template.ParseFS(web.TemplateFS, "templates/environment_terminal.html")
	if err != nil {
		http.Error(w, "template parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "terminal", map[string]any{"Environment": env}); err != nil {
		log.Printf("terminal template render error: %v", err)
	}
}

// handleEnvironmentTerminalWS proxies between the browser and the environment agent via the hub.
func (s *Server) handleEnvironmentTerminalWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.GetEnvironment(id); err != nil {
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

// handleAgentPortForward is called when the agent opens a back-channel for a port-forward.
func (s *Server) handleAgentPortForward(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	channelID := r.URL.Query().Get("channel")
	if token == "" || channelID == "" {
		http.Error(w, "missing params", http.StatusBadRequest)
		return
	}
	if _, err := s.db.GetEnvironmentByAgentToken(token); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if !s.hub.AcceptPortForward(channelID, conn) {
		conn.Close()
	}
}

// handleEnvironmentPortForwardWS opens a port-forward to the environment and proxies WebSocket traffic.
func (s *Server) handleEnvironmentPortForwardWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	if _, err := s.db.GetEnvironment(id); err != nil {
		http.Error(w, "environment not found", http.StatusNotFound)
		return
	}

	channelID := uuid.New().String()
	agentConnCh, err := s.hub.RequestPortForward(id, channelID, port)
	if err != nil {
		http.Error(w, "no agent connected", http.StatusServiceUnavailable)
		return
	}

	// Upgrade browser connection.
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browserConn.Close()

	// Wait for agent back-channel (10s timeout).
	var agentConn *websocket.Conn
	select {
	case agentConn = <-agentConnCh:
	case <-time.After(10 * time.Second):
		browserConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "agent timeout"))
		return
	}
	defer agentConn.Close()

	done := make(chan struct{}, 2)

	// Browser → Agent
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			mt, data, err := browserConn.ReadMessage()
			if err != nil {
				return
			}
			if err := agentConn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}()

	// Agent → Browser
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			mt, data, err := agentConn.ReadMessage()
			if err != nil {
				return
			}
			if err := browserConn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}()

	<-done
}
