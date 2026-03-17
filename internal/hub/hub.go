package hub

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// agentMsg is the JSON wire format between hub and agent.
type agentMsg struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Data    string `json:"data,omitempty"` // base64-encoded bytes
	Rows    uint16 `json:"rows,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
}

type agentConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (a *agentConn) send(m agentMsg) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn.WriteMessage(websocket.TextMessage, data)
}

type browserSession struct {
	sessionID string
	send      chan []byte
	closeOnce sync.Once
}

func (b *browserSession) close() {
	b.closeOnce.Do(func() { close(b.send) })
}

// Hub routes terminal I/O between workspace agents and browser sessions.
type Hub struct {
	mu       sync.RWMutex
	agents   map[string]*agentConn              // workspaceID → agent
	browsers map[string]map[string]*browserSession // workspaceID → sessionID → browser
}

func New() *Hub {
	return &Hub{
		agents:   make(map[string]*agentConn),
		browsers: make(map[string]map[string]*browserSession),
	}
}

// AgentConnected reports whether a live agent WebSocket is registered for the workspace.
func (h *Hub) AgentConnected(workspaceID string) bool {
	h.mu.RLock()
	_, ok := h.agents[workspaceID]
	h.mu.RUnlock()
	return ok
}

// HandleAgent registers the agent WebSocket for a workspace and blocks until it disconnects.
func (h *Hub) HandleAgent(workspaceID string, conn *websocket.Conn) {
	agent := &agentConn{conn: conn}

	h.mu.Lock()
	if old, ok := h.agents[workspaceID]; ok {
		old.conn.Close()
	}
	h.agents[workspaceID] = agent
	h.mu.Unlock()

	log.Printf("hub: agent connected for workspace %s", workspaceID)
	defer func() {
		h.mu.Lock()
		if h.agents[workspaceID] == agent {
			delete(h.agents, workspaceID)
		}
		browsers := h.browsers[workspaceID]
		delete(h.browsers, workspaceID)
		h.mu.Unlock()

		for _, b := range browsers {
			b.close()
		}
		log.Printf("hub: agent disconnected for workspace %s", workspaceID)
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var m agentMsg
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		switch m.Type {
		case "output":
			decoded, err := base64.StdEncoding.DecodeString(m.Data)
			if err != nil {
				continue
			}
			h.deliverToBrowser(workspaceID, m.Session, decoded)
		case "closed":
			h.closeBrowserSession(workspaceID, m.Session)
		}
	}
}

// HandleBrowser connects a browser WebSocket to the agent for the given workspace.
// sessionID is a unique ID for this terminal session. Blocks until the browser disconnects.
func (h *Hub) HandleBrowser(workspaceID, sessionID string, conn *websocket.Conn) {
	b := &browserSession{
		sessionID: sessionID,
		send:      make(chan []byte, 256),
	}

	h.mu.Lock()
	if h.browsers[workspaceID] == nil {
		h.browsers[workspaceID] = make(map[string]*browserSession)
	}
	h.browsers[workspaceID][sessionID] = b
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		if sessions := h.browsers[workspaceID]; sessions != nil {
			delete(sessions, sessionID)
			if len(sessions) == 0 {
				delete(h.browsers, workspaceID)
			}
		}
		h.mu.Unlock()
		// Tell agent to close the PTY session.
		h.sendToAgent(workspaceID, agentMsg{Type: "close", Session: sessionID})
	}()

	// Ask the agent to open a PTY.
	if err := h.sendToAgent(workspaceID, agentMsg{Type: "open", Session: sessionID}); err != nil {
		conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"error","message":"no agent connected \u2013 start the workspace first"}`))
		conn.Close()
		return
	}

	// Write pump: forward agent output to browser.
	go func() {
		for data := range b.send {
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				break
			}
		}
		// Channel closed means agent disconnected or session ended.
		conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"closed"}`))
		conn.Close()
	}()

	// Read pump: forward browser input to agent.
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch mt {
		case websocket.BinaryMessage:
			encoded := base64.StdEncoding.EncodeToString(data)
			h.sendToAgent(workspaceID, agentMsg{Type: "input", Session: sessionID, Data: encoded})
		case websocket.TextMessage:
			var m struct {
				Type string `json:"type"`
				Rows uint16 `json:"rows"`
				Cols uint16 `json:"cols"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "resize" && m.Rows > 0 && m.Cols > 0 {
				h.sendToAgent(workspaceID, agentMsg{
					Type: "resize", Session: sessionID, Rows: m.Rows, Cols: m.Cols,
				})
			}
		}
	}
}

func (h *Hub) sendToAgent(workspaceID string, m agentMsg) error {
	h.mu.RLock()
	agent := h.agents[workspaceID]
	h.mu.RUnlock()
	if agent == nil {
		return fmt.Errorf("no agent for workspace %s", workspaceID)
	}
	return agent.send(m)
}

func (h *Hub) deliverToBrowser(workspaceID, sessionID string, data []byte) {
	h.mu.RLock()
	var b *browserSession
	if sessions := h.browsers[workspaceID]; sessions != nil {
		b = sessions[sessionID]
	}
	h.mu.RUnlock()
	if b == nil {
		return
	}
	select {
	case b.send <- data:
	default:
	}
}

func (h *Hub) closeBrowserSession(workspaceID, sessionID string) {
	h.mu.Lock()
	var b *browserSession
	if sessions := h.browsers[workspaceID]; sessions != nil {
		b = sessions[sessionID]
		delete(sessions, sessionID)
		if len(sessions) == 0 {
			delete(h.browsers, workspaceID)
		}
	}
	h.mu.Unlock()
	if b != nil {
		b.close()
	}
}
