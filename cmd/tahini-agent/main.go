package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

type msg struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Data    string `json:"data,omitempty"` // base64
	Rows    uint16 `json:"rows,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
}

type session struct {
	ptmx *os.File
}

func main() {
	tahiniURL := os.Getenv("TAHINI_URL")
	token := os.Getenv("TAHINI_AGENT_TOKEN")

	if tahiniURL == "" || token == "" {
		log.Fatal("TAHINI_URL and TAHINI_AGENT_TOKEN must be set")
	}

	backoff := 2 * time.Second
	for {
		if err := run(tahiniURL, token); err != nil {
			log.Printf("agent: disconnected (%v), reconnecting in %s", err, backoff)
		}
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func run(tahiniURL, token string) error {
	u, err := url.Parse(tahiniURL)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		// already ws/wss, leave as-is
	}
	u.Path = "/agent/connect"
	q := url.Values{"token": {token}}
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("agent: connected to %s", tahiniURL)

	var mu sync.Mutex
	sessions := make(map[string]*session)

	// send is goroutine-safe write helper.
	var writeMu sync.Mutex
	send := func(m msg) {
		data, _ := json.Marshal(m)
		writeMu.Lock()
		conn.WriteMessage(websocket.TextMessage, data)
		writeMu.Unlock()
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			mu.Lock()
			for _, s := range sessions {
				s.ptmx.Close()
			}
			mu.Unlock()
			return err
		}

		var m msg
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		switch m.Type {
		case "open":
			sessionID := m.Session
			go func() {
				shell := findShell()
				c := exec.Command(shell)
				ptmx, err := pty.Start(c)
				if err != nil {
					log.Printf("agent: pty start: %v", err)
					send(msg{Type: "closed", Session: sessionID})
					return
				}
				mu.Lock()
				sessions[sessionID] = &session{ptmx: ptmx}
				mu.Unlock()

				buf := make([]byte, 4096)
				for {
					n, err := ptmx.Read(buf)
					if n > 0 {
						encoded := base64.StdEncoding.EncodeToString(buf[:n])
						send(msg{Type: "output", Session: sessionID, Data: encoded})
					}
					if err != nil {
						break
					}
				}

				mu.Lock()
				delete(sessions, sessionID)
				mu.Unlock()

				send(msg{Type: "closed", Session: sessionID})
			}()

		case "input":
			mu.Lock()
			s := sessions[m.Session]
			mu.Unlock()
			if s != nil {
				raw, err := base64.StdEncoding.DecodeString(m.Data)
				if err == nil {
					s.ptmx.Write(raw)
				}
			}

		case "resize":
			mu.Lock()
			s := sessions[m.Session]
			mu.Unlock()
			if s != nil && m.Rows > 0 && m.Cols > 0 {
				pty.Setsize(s.ptmx, &pty.Winsize{Rows: m.Rows, Cols: m.Cols})
			}

		case "close":
			mu.Lock()
			s := sessions[m.Session]
			delete(sessions, m.Session)
			mu.Unlock()
			if s != nil {
				s.ptmx.Close()
			}

		case "ping":
			send(msg{Type: "pong"})
		}
	}
}

func findShell() string {
	for _, sh := range []string{"/bin/bash", "/bin/sh", "/bin/ash"} {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	return "sh"
}
