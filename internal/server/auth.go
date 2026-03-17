package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "tahini_session"
const sessionDuration = 24 * time.Hour

func generateSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) issueSession(w http.ResponseWriter) {
	payload := fmt.Sprintf("admin:%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(s.config.SessionSecret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    payload + "." + sig,
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

func (s *Server) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payload, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, []byte(s.config.SessionSecret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}

	// Validate expiry from payload "admin:<unix>"
	colonIdx := strings.LastIndex(payload, ":")
	if colonIdx < 0 {
		return false
	}
	ts, err := strconv.ParseInt(payload[colonIdx+1:], 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < sessionDuration
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}
