package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "tahini_session"
const sessionDuration = 24 * time.Hour

type sessionInfo struct {
	UserID string
	Role   string // "owner", "user_admin", "template_admin", "user", or "env_admin"
}

type contextKey int

const ctxSession contextKey = 0

func generateSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) sign(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.config.SessionSecret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// issueSession issues a session for the built-in env-var admin.
func (s *Server) issueSession(w http.ResponseWriter) {
	payload := fmt.Sprintf("admin:%d", time.Now().Unix())
	s.setCookie(w, payload+"."+s.sign(payload))
}

// issueUserSession issues a session for a DB user.
func (s *Server) issueUserSession(w http.ResponseWriter, userID, role string) {
	payload := fmt.Sprintf("user:%s:%s:%d", userID, role, time.Now().Unix())
	s.setCookie(w, payload+"."+s.sign(payload))
}

func (s *Server) setCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration.Seconds()),
		Path:     "/",
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		MaxAge: -1,
		Path:   "/",
	})
}

func (s *Server) parseSession(r *http.Request) (sessionInfo, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return sessionInfo{}, false
	}
	dotIdx := strings.LastIndex(cookie.Value, ".")
	if dotIdx < 0 {
		return sessionInfo{}, false
	}
	payload, sig := cookie.Value[:dotIdx], cookie.Value[dotIdx+1:]

	expected := s.sign(payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return sessionInfo{}, false
	}

	parts := strings.Split(payload, ":")
	switch parts[0] {
	case "admin":
		// legacy env-admin: "admin:<unix>"
		if len(parts) < 2 {
			return sessionInfo{}, false
		}
		ts, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if err != nil || time.Since(time.Unix(ts, 0)) >= sessionDuration {
			return sessionInfo{}, false
		}
		return sessionInfo{UserID: "", Role: "env_admin"}, true
	case "user":
		// DB user: "user:<id>:<role>:<unix>"
		if len(parts) < 4 {
			return sessionInfo{}, false
		}
		ts, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if err != nil || time.Since(time.Unix(ts, 0)) >= sessionDuration {
			return sessionInfo{}, false
		}
		return sessionInfo{UserID: parts[1], Role: parts[2]}, true
	}
	return sessionInfo{}, false
}

func (s *Server) validSession(r *http.Request) bool {
	_, ok := s.parseSession(r)
	return ok
}

func sessionFromContext(r *http.Request) sessionInfo {
	v := r.Context().Value(ctxSession)
	if v == nil {
		return sessionInfo{}
	}
	return v.(sessionInfo)
}

func isOwnerOrAdmin(sess sessionInfo) bool {
	return sess.Role == "env_admin" || sess.Role == "owner" || sess.Role == "user_admin"
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.parseSession(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// parseBearerToken validates a Bearer thn_* token from the Authorization header.
func (s *Server) parseBearerToken(r *http.Request) (sessionInfo, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer thn_") {
		return sessionInfo{}, false
	}
	raw := strings.TrimPrefix(auth, "Bearer ")
	h := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(h[:])
	token, err := s.db.GetAPITokenByHash(hash)
	if err != nil {
		return sessionInfo{}, false
	}
	user, err := s.db.GetUserByID(token.UserID)
	if err != nil {
		return sessionInfo{}, false
	}
	go s.db.TouchAPIToken(token.ID)
	return sessionInfo{UserID: user.ID, Role: user.Role}, true
}

// requireAPIAuth accepts Bearer token or session cookie; returns JSON 401 on failure.
func (s *Server) requireAPIAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.parseBearerToken(r)
		if !ok {
			sess, ok = s.parseSession(r)
		}
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireOwnerOrAdmin(next http.Handler) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFromContext(r)
		if !isOwnerOrAdmin(sess) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
