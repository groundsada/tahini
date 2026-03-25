package server

import (
	"net"
	"net/http"
)

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}


// logEvent records an audit event. actorName is resolved from the session.
// Call fire-and-forget; errors are silently dropped (audit must not block requests).
func (s *Server) logEvent(r *http.Request, action, resourceType, resourceID, resourceName string) {
	sess := sessionFromContext(r)
	actorID := sess.UserID
	actorName := sess.Role // fallback label for env_admin
	if actorID != "" {
		if u, err := s.db.GetUserByID(actorID); err == nil {
			actorName = u.Username
		}
	}
	go s.db.LogEvent(actorID, actorName, action, resourceType, resourceID, resourceName, clientIP(r))
}
