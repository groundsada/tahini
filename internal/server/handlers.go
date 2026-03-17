package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"tahini.dev/tahini/internal/db"
	"tahini.dev/tahini/internal/tofu"
)

// --- Auth ---

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.renderLogin(w, map[string]string{"Error": r.URL.Query().Get("error")})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	user := r.FormValue("username")
	pass := r.FormValue("password")

	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.config.AdminUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.config.AdminPass)) == 1
	if !userOK || !passOK {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusFound)
		return
	}

	s.issueSession(w)
	http.Redirect(w, r, "/workspaces", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// --- Templates ---

type templateListPage struct {
	Templates []db.Template
	Error     string
}

type templateDetailPage struct {
	Template   db.Template
	Workspaces []db.Workspace
	Error      string
}

func (s *Server) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	templates, err := s.db.ListTemplates()
	if err != nil {
		log.Printf("list templates: %v", err)
	}
	s.render(w, "templates_list", templateListPage{
		Templates: templates,
		Error:     r.URL.Query().Get("error"),
	})
}

func (s *Server) handleTemplateNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, "template_new", map[string]string{"Error": r.URL.Query().Get("error")})
}

func (s *Server) handleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	hcl := strings.TrimSpace(r.FormValue("hcl"))

	if name == "" || hcl == "" {
		http.Redirect(w, r, "/templates/new?error=name+and+hcl+are+required", http.StatusFound)
		return
	}

	tmpl, err := s.db.CreateTemplate(name, description, hcl)
	if err != nil {
		http.Redirect(w, r, "/templates/new?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/templates/"+tmpl.ID, http.StatusFound)
}

func (s *Server) handleTemplateDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tmpl, err := s.db.GetTemplate(id)
	if err != nil {
		http.Redirect(w, r, "/templates?error=template+not+found", http.StatusFound)
		return
	}
	workspaces, _ := s.db.WorkspacesForTemplate(id)
	s.render(w, "template_detail", templateDetailPage{
		Template:   tmpl,
		Workspaces: workspaces,
		Error:      r.URL.Query().Get("error"),
	})
}

func (s *Server) handleTemplateEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tmpl, err := s.db.GetTemplate(id)
	if err != nil {
		http.Redirect(w, r, "/templates?error=template+not+found", http.StatusFound)
		return
	}
	s.render(w, "template_edit", map[string]any{
		"Template": tmpl,
		"Error":    r.URL.Query().Get("error"),
	})
}

func (s *Server) handleTemplateUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	hcl := strings.TrimSpace(r.FormValue("hcl"))

	if name == "" || hcl == "" {
		http.Redirect(w, r, "/templates/"+id+"/edit?error=name+and+hcl+are+required", http.StatusFound)
		return
	}

	if err := s.db.UpdateTemplate(id, name, description, hcl); err != nil {
		http.Redirect(w, r, "/templates/"+id+"/edit?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/templates/"+id, http.StatusFound)
}

func (s *Server) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	has, err := s.db.TemplateHasWorkspaces(id)
	if err != nil || has {
		http.Redirect(w, r, "/templates/"+id+"?error=template+has+workspaces%2C+delete+them+first", http.StatusFound)
		return
	}
	if err := s.db.DeleteTemplate(id); err != nil {
		http.Redirect(w, r, "/templates/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/templates", http.StatusFound)
}

// --- Workspaces ---

type workspaceListPage struct {
	Workspaces []db.Workspace
	Error      string
}

type workspaceNewPage struct {
	Templates []db.Template
	Error     string
}

type workspaceDetailPage struct {
	Workspace      db.Workspace
	LatestBuild    *db.Build
	Builds         []db.Build
	LogContent     string
	AgentConnected bool
	Error          string
}

func (s *Server) handleWorkspacesList(w http.ResponseWriter, r *http.Request) {
	workspaces, err := s.db.ListWorkspaces()
	if err != nil {
		log.Printf("list workspaces: %v", err)
	}
	s.render(w, "workspaces_list", workspaceListPage{
		Workspaces: workspaces,
		Error:      r.URL.Query().Get("error"),
	})
}

func (s *Server) handleWorkspaceNew(w http.ResponseWriter, r *http.Request) {
	templates, _ := s.db.ListTemplates()
	s.render(w, "workspace_new", workspaceNewPage{
		Templates: templates,
		Error:     r.URL.Query().Get("error"),
	})
}

func (s *Server) handleWorkspaceCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	templateID := r.FormValue("template_id")
	params := strings.TrimSpace(r.FormValue("params"))

	if name == "" || templateID == "" {
		http.Redirect(w, r, "/workspaces/new?error=name+and+template+are+required", http.StatusFound)
		return
	}

	workspace, err := s.db.CreateWorkspace(name, templateID, params)
	if err != nil {
		http.Redirect(w, r, "/workspaces/new?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/workspaces/"+workspace.ID, http.StatusFound)
}

func (s *Server) handleWorkspaceDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	workspace, err := s.db.GetWorkspace(id)
	if err != nil {
		http.Redirect(w, r, "/workspaces?error=workspace+not+found", http.StatusFound)
		return
	}

	latestBuild, _ := s.db.GetLatestBuild(id)
	builds, _ := s.db.ListBuilds(id)

	var logContent string
	if latestBuild != nil && latestBuild.LogPath != "" {
		logContent = tofu.ReadLog(latestBuild.LogPath)
	}

	s.render(w, "workspace_detail", workspaceDetailPage{
		Workspace:      workspace,
		LatestBuild:    latestBuild,
		Builds:         builds,
		LogContent:     logContent,
		AgentConnected: s.hub.AgentConnected(id),
		Error:          r.URL.Query().Get("error"),
	})
}

func (s *Server) handleWorkspaceStart(w http.ResponseWriter, r *http.Request) {
	s.triggerBuild(w, r, r.PathValue("id"), "start")
}

func (s *Server) handleWorkspaceStop(w http.ResponseWriter, r *http.Request) {
	s.triggerBuild(w, r, r.PathValue("id"), "stop")
}

func (s *Server) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	workspace, err := s.db.GetWorkspace(id)
	if err != nil {
		http.Redirect(w, r, "/workspaces", http.StatusFound)
		return
	}
	switch workspace.Status {
	case "starting", "stopping", "deleting":
		http.Redirect(w, r, "/workspaces/"+id+"?error=workspace+is+busy%2C+wait+for+current+operation+to+finish", http.StatusFound)
		return
	}
	s.triggerBuild(w, r, id, "delete")
}

// triggerBuild validates state, writes HCL, creates a build record, and launches the goroutine.
func (s *Server) triggerBuild(w http.ResponseWriter, r *http.Request, workspaceID, transition string) {
	workspace, err := s.db.GetWorkspace(workspaceID)
	if err != nil {
		http.Redirect(w, r, "/workspaces?error=workspace+not+found", http.StatusFound)
		return
	}

	if _, loaded := s.building.LoadOrStore(workspaceID, struct{}{}); loaded {
		http.Redirect(w, r, "/workspaces/"+workspaceID+"?error=a+build+is+already+in+progress", http.StatusFound)
		return
	}

	tmpl, err := s.db.GetTemplate(workspace.TemplateID)
	if err != nil {
		s.building.Delete(workspaceID)
		http.Redirect(w, r, "/workspaces/"+workspaceID+"?error=template+not+found", http.StatusFound)
		return
	}

	if err := s.executor.WriteHCL(workspaceID, tmpl.HCL); err != nil {
		s.building.Delete(workspaceID)
		http.Redirect(w, r, "/workspaces/"+workspaceID+"?error=failed+to+write+template", http.StatusFound)
		return
	}

	buildID := uuid.New().String()
	logPath := s.executor.LogPath(workspaceID, buildID)
	if _, err := s.db.CreateBuild(buildID, workspaceID, transition, logPath); err != nil {
		s.building.Delete(workspaceID)
		http.Redirect(w, r, "/workspaces/"+workspaceID+"?error=failed+to+create+build+record", http.StatusFound)
		return
	}

	statusMap := map[string]string{"start": "starting", "stop": "stopping", "delete": "deleting"}
	s.db.UpdateWorkspaceStatus(workspaceID, statusMap[transition])

	params := parseParams(workspace.Params)
	// Prepend agent vars so templates can wire up terminal access.
	agentParams := []string{"agent_token=" + workspace.AgentToken}
	if s.config.InternalURL != "" {
		agentParams = append(agentParams, "tahini_url="+s.config.InternalURL)
	}
	params = append(agentParams, params...)
	go s.runBuild(workspaceID, buildID, transition, params)

	if transition == "delete" {
		http.Redirect(w, r, "/workspaces", http.StatusFound)
	} else {
		http.Redirect(w, r, "/workspaces/"+workspaceID, http.StatusFound)
	}
}

// runBuild runs the OpenTofu operation and updates workspace status when done.
func (s *Server) runBuild(workspaceID, buildID, transition string, params []string) {
	defer s.building.Delete(workspaceID)

	logPath := s.executor.LogPath(workspaceID, buildID)
	err := s.executor.Run(context.Background(), workspaceID, transition, params, logPath)

	if err != nil {
		log.Printf("build %s (%s) failed: %v", buildID, transition, err)
		s.db.CompleteBuild(buildID, "failed")
		s.db.UpdateWorkspaceStatus(workspaceID, "error")
		return
	}

	s.db.CompleteBuild(buildID, "succeeded")
	switch transition {
	case "start":
		s.db.UpdateWorkspaceStatus(workspaceID, "running")
	case "stop":
		s.db.UpdateWorkspaceStatus(workspaceID, "stopped")
	case "delete":
		os.RemoveAll(s.executor.WorkspaceDir(workspaceID))
		s.db.DeleteWorkspace(workspaceID)
	}
}

// --- JSON API ---

func (s *Server) handleAPIWorkspaceStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	workspace, err := s.db.GetWorkspace(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":          workspace.Status,
		"agent_connected": s.hub.AgentConnected(id),
	})
}

type buildAPIResponse struct {
	ID          string  `json:"id"`
	Transition  string  `json:"transition"`
	Status      string  `json:"status"`
	Logs        string  `json:"logs"`
	FinishedAt  *string `json:"finished_at"`
}

func (s *Server) handleAPIBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	build, err := s.db.GetBuild(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}

	logs := tofu.ReadLog(build.LogPath)
	resp := buildAPIResponse{
		ID:         build.ID,
		Transition: build.Transition,
		Status:     build.Status,
		Logs:       logs,
	}
	if build.FinishedAt != nil {
		s := build.FinishedAt.String()
		resp.FinishedAt = &s
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- Helpers ---

// parseParams splits "KEY=VALUE\nKEY2=VALUE2" into a slice of "KEY=VALUE" strings.
func parseParams(params string) []string {
	var out []string
	for _, line := range strings.Split(params, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, "=") {
			out = append(out, line)
		}
	}
	return out
}
