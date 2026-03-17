package server

import (
	"net/http"
	"net/url"
	"strings"

	"tahini.dev/tahini/internal/db"
)

type adminUsersPage struct {
	Users []db.User
	Orgs  []db.Org
	Error string
}

type adminOrgsPage struct {
	Orgs  []db.Org
	Error string
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, _ := s.db.ListUsers()
	orgs, _ := s.db.ListOrgs()
	s.render(w, "admin_users", adminUsersPage{
		Users: users,
		Orgs:  orgs,
		Error: r.URL.Query().Get("error"),
	})
}

func (s *Server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	role := r.FormValue("role")
	orgID := r.FormValue("org_id")

	if username == "" || password == "" {
		http.Redirect(w, r, "/admin/users?error=username+and+password+required", http.StatusFound)
		return
	}
	validRoles := map[string]bool{"owner": true, "user_admin": true, "template_admin": true, "user": true}
	if !validRoles[role] {
		role = "user"
	}

	if _, err := s.db.CreateUser(username, password, role, orgID); err != nil {
		http.Redirect(w, r, "/admin/users?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func (s *Server) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteUser(id); err != nil {
		http.Redirect(w, r, "/admin/users?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func (s *Server) handleAdminUserUpdateRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	role := r.FormValue("role")
	orgID := r.FormValue("org_id")

	validRoles := map[string]bool{"owner": true, "user_admin": true, "template_admin": true, "user": true}
	if validRoles[role] {
		s.db.UpdateUserRole(id, role)
	}
	s.db.UpdateUserOrg(id, orgID)
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func (s *Server) handleAdminOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, _ := s.db.ListOrgs()
	s.render(w, "admin_orgs", adminOrgsPage{
		Orgs:  orgs,
		Error: r.URL.Query().Get("error"),
	})
}

func (s *Server) handleAdminOrgCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/orgs?error=name+required", http.StatusFound)
		return
	}
	if _, err := s.db.CreateOrg(name); err != nil {
		http.Redirect(w, r, "/admin/orgs?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/orgs", http.StatusFound)
}

func (s *Server) handleAdminOrgDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteOrg(id); err != nil {
		http.Redirect(w, r, "/admin/orgs?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/orgs", http.StatusFound)
}

func (s *Server) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r)
	var user *db.User
	if sess.UserID != "" {
		u, err := s.db.GetUserByID(sess.UserID)
		if err == nil {
			user = &u
		}
	}
	s.render(w, "profile", map[string]any{
		"User":  user,
		"Error": r.URL.Query().Get("error"),
	})
}

func (s *Server) handleProfilePassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r)
	if sess.UserID == "" {
		http.Redirect(w, r, "/profile?error=env+admin+cannot+change+password+here", http.StatusFound)
		return
	}
	newPass := r.FormValue("password")
	if len(newPass) < 8 {
		http.Redirect(w, r, "/profile?error=password+must+be+at+least+8+characters", http.StatusFound)
		return
	}
	if err := s.db.UpdateUserPassword(sess.UserID, newPass); err != nil {
		http.Redirect(w, r, "/profile?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/profile", http.StatusFound)
}
