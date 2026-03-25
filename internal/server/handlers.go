package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"tahini.dev/tahini/internal/db"
	hclutil "tahini.dev/tahini/internal/hcl"
	"tahini.dev/tahini/internal/tofu"
)

// --- Auth ---

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.renderLogin(w, map[string]string{"Error": r.URL.Query().Get("error")})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	user := r.FormValue("username")
	pass := r.FormValue("password")

	// Try DB user first.
	if dbUser, err := s.db.AuthenticateUser(user, pass); err == nil {
		s.issueUserSession(w, dbUser.ID, dbUser.Role)
		http.Redirect(w, r, "/environments", http.StatusFound)
		return
	}

	// Fallback to env-var admin.
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.config.AdminUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.config.AdminPass)) == 1
	if !userOK || !passOK {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusFound)
		return
	}

	s.issueSession(w)
	http.Redirect(w, r, "/environments", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// --- Blueprints ---

type blueprintListPage struct {
	Blueprints []db.Blueprint
	Error      string
}

type blueprintDetailPage struct {
	Blueprint    db.Blueprint
	Environments []db.Environment
	Error        string
}

func (s *Server) handleBlueprintsList(w http.ResponseWriter, r *http.Request) {
	blueprints, err := s.db.ListBlueprints()
	if err != nil {
		log.Printf("list blueprints: %v", err)
	}
	s.render(w, "blueprints_list", blueprintListPage{
		Blueprints: blueprints,
		Error:      r.URL.Query().Get("error"),
	})
}

func (s *Server) handleBlueprintNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, "blueprint_new", map[string]string{"Error": r.URL.Query().Get("error")})
}

func (s *Server) handleBlueprintCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	hcl := strings.TrimSpace(r.FormValue("hcl"))
	gitURL := strings.TrimSpace(r.FormValue("git_url"))

	if name == "" || hcl == "" {
		http.Redirect(w, r, "/blueprints/new?error=name+and+hcl+are+required", http.StatusFound)
		return
	}

	bp, err := s.db.CreateBlueprint(name, description, hcl, gitURL)
	if err != nil {
		http.Redirect(w, r, "/blueprints/new?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/blueprints/"+bp.ID, http.StatusFound)
}

func (s *Server) handleBlueprintDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bp, err := s.db.GetBlueprint(id)
	if err != nil {
		http.Redirect(w, r, "/blueprints?error=blueprint+not+found", http.StatusFound)
		return
	}
	envs, _ := s.db.EnvironmentsForBlueprint(id)
	s.render(w, "blueprint_detail", blueprintDetailPage{
		Blueprint:    bp,
		Environments: envs,
		Error:        r.URL.Query().Get("error"),
	})
}

func (s *Server) handleBlueprintEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bp, err := s.db.GetBlueprint(id)
	if err != nil {
		http.Redirect(w, r, "/blueprints?error=blueprint+not+found", http.StatusFound)
		return
	}
	s.render(w, "blueprint_edit", map[string]any{
		"Blueprint": bp,
		"Error":     r.URL.Query().Get("error"),
	})
}

func (s *Server) handleBlueprintUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	hcl := strings.TrimSpace(r.FormValue("hcl"))
	gitURL := strings.TrimSpace(r.FormValue("git_url"))

	if name == "" || hcl == "" {
		http.Redirect(w, r, "/blueprints/"+id+"/edit?error=name+and+hcl+are+required", http.StatusFound)
		return
	}

	if err := s.db.UpdateBlueprint(id, name, description, hcl, gitURL); err != nil {
		http.Redirect(w, r, "/blueprints/"+id+"/edit?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/blueprints/"+id, http.StatusFound)
}

func (s *Server) handleBlueprintSync(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bp, err := s.db.GetBlueprint(id)
	if err != nil {
		http.Redirect(w, r, "/blueprints?error=blueprint+not+found", http.StatusFound)
		return
	}
	if bp.GitURL == "" {
		http.Redirect(w, r, "/blueprints/"+id+"?error=no+git+url+configured", http.StatusFound)
		return
	}

	hcl, err := fetchURL(bp.GitURL)
	if err != nil {
		http.Redirect(w, r, "/blueprints/"+id+"?error="+url.QueryEscape("fetch failed: "+err.Error()), http.StatusFound)
		return
	}
	if err := s.db.UpdateBlueprintHCL(id, hcl); err != nil {
		http.Redirect(w, r, "/blueprints/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/blueprints/"+id, http.StatusFound)
}

func (s *Server) handleBlueprintDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	has, err := s.db.BlueprintHasEnvironments(id)
	if err != nil || has {
		http.Redirect(w, r, "/blueprints/"+id+"?error=blueprint+has+environments%2C+delete+them+first", http.StatusFound)
		return
	}
	if err := s.db.DeleteBlueprint(id); err != nil {
		http.Redirect(w, r, "/blueprints/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/blueprints", http.StatusFound)
}

// --- Environments ---

type environmentListPage struct {
	Environments []db.Environment
	Error        string
}

type environmentNewPage struct {
	Blueprints    []db.Blueprint
	BlueprintVars map[string]string // blueprint ID → comma-separated var names
	Error         string
}

type environmentDetailPage struct {
	Environment    db.Environment
	LatestRun      *db.Run
	Runs           []db.Run
	LogContent     string
	AgentConnected bool
	BlueprintVars  []string // variable names from the blueprint HCL
	Error          string
}

func (s *Server) handleEnvironmentsList(w http.ResponseWriter, r *http.Request) {
	envs, err := s.db.ListEnvironments()
	if err != nil {
		log.Printf("list environments: %v", err)
	}
	s.render(w, "environments_list", environmentListPage{
		Environments: envs,
		Error:        r.URL.Query().Get("error"),
	})
}

func (s *Server) handleEnvironmentNew(w http.ResponseWriter, r *http.Request) {
	blueprints, _ := s.db.ListBlueprints()
	bvars := make(map[string]string, len(blueprints))
	for _, b := range blueprints {
		vars := hclutil.ParseVariables(b.HCL)
		bvars[b.ID] = strings.Join(vars, ",")
	}
	s.render(w, "environment_new", environmentNewPage{
		Blueprints:    blueprints,
		BlueprintVars: bvars,
		Error:         r.URL.Query().Get("error"),
	})
}

func (s *Server) handleEnvironmentCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	blueprintID := r.FormValue("blueprint_id")
	params := strings.TrimSpace(r.FormValue("params"))

	if name == "" || blueprintID == "" {
		http.Redirect(w, r, "/environments/new?error=name+and+blueprint+are+required", http.StatusFound)
		return
	}

	env, err := s.db.CreateEnvironment(name, blueprintID, params)
	if err != nil {
		http.Redirect(w, r, "/environments/new?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/environments/"+env.ID, http.StatusFound)
}

func (s *Server) handleEnvironmentDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env, err := s.db.GetEnvironment(id)
	if err != nil {
		http.Redirect(w, r, "/environments?error=environment+not+found", http.StatusFound)
		return
	}

	latestRun, _ := s.db.GetLatestRun(id)
	runs, _ := s.db.ListRuns(id)

	var logContent string
	if latestRun != nil && latestRun.LogPath != "" {
		logContent = tofu.ReadLog(latestRun.LogPath)
	}

	var bpVars []string
	if bp, err := s.db.GetBlueprint(env.BlueprintID); err == nil {
		bpVars = hclutil.ParseVariables(bp.HCL)
	}

	s.render(w, "environment_detail", environmentDetailPage{
		Environment:    env,
		LatestRun:      latestRun,
		Runs:           runs,
		LogContent:     logContent,
		AgentConnected: s.hub.AgentConnected(id),
		BlueprintVars:  bpVars,
		Error:          r.URL.Query().Get("error"),
	})
}

func (s *Server) handleEnvironmentUpdateParams(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env, err := s.db.GetEnvironment(id)
	if err != nil {
		http.Redirect(w, r, "/environments", http.StatusFound)
		return
	}
	if env.Status != "stopped" && env.Status != "error" {
		http.Redirect(w, r, "/environments/"+id+"?error=environment+must+be+stopped+to+edit+params", http.StatusFound)
		return
	}
	params := strings.TrimSpace(r.FormValue("params"))
	if err := s.db.UpdateEnvironmentParams(id, params); err != nil {
		http.Redirect(w, r, "/environments/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/environments/"+id, http.StatusFound)
}

func (s *Server) handleEnvironmentStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.scheduleTransition(id, "start"); err != nil {
		http.Redirect(w, r, "/environments/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/environments/"+id, http.StatusFound)
}

func (s *Server) handleEnvironmentStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.scheduleTransition(id, "stop"); err != nil {
		http.Redirect(w, r, "/environments/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/environments/"+id, http.StatusFound)
}

func (s *Server) handleEnvironmentDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env, err := s.db.GetEnvironment(id)
	if err != nil {
		http.Redirect(w, r, "/environments", http.StatusFound)
		return
	}
	switch env.Status {
	case "starting", "stopping", "deleting":
		http.Redirect(w, r, "/environments/"+id+"?error=environment+is+busy%2C+wait+for+current+operation+to+finish", http.StatusFound)
		return
	}
	if _, err := s.scheduleTransition(id, "delete"); err != nil {
		http.Redirect(w, r, "/environments/"+id+"?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/environments", http.StatusFound)
}

// scheduleTransition validates state, writes HCL, creates a run record, and launches the goroutine.
// Returns the run ID or an error.
func (s *Server) scheduleTransition(environmentID, transition string) (string, error) {
	env, err := s.db.GetEnvironment(environmentID)
	if err != nil {
		return "", fmt.Errorf("environment not found")
	}

	if _, loaded := s.building.LoadOrStore(environmentID, struct{}{}); loaded {
		return "", fmt.Errorf("a run is already in progress")
	}

	bp, err := s.db.GetBlueprint(env.BlueprintID)
	if err != nil {
		s.building.Delete(environmentID)
		return "", fmt.Errorf("blueprint not found")
	}

	if err := s.executor.WriteHCL(environmentID, bp.HCL); err != nil {
		s.building.Delete(environmentID)
		return "", fmt.Errorf("failed to write blueprint")
	}

	runID := uuid.New().String()
	logPath := s.executor.LogPath(environmentID, runID)
	if _, err := s.db.CreateRun(environmentID, transition, logPath); err != nil {
		s.building.Delete(environmentID)
		return "", fmt.Errorf("failed to create run record")
	}

	statusMap := map[string]string{"start": "starting", "stop": "stopping", "delete": "deleting"}
	s.db.UpdateEnvironmentStatus(environmentID, statusMap[transition])

	params := parseParams(env.Params)
	agentParams := []string{"agent_token=" + env.AgentToken}
	if s.config.InternalURL != "" {
		agentParams = append(agentParams, "tahini_url="+s.config.InternalURL)
	}
	params = append(agentParams, params...)
	go s.executeRun(environmentID, runID, transition, params)

	return runID, nil
}

// executeRun runs the OpenTofu operation and updates environment status when done.
func (s *Server) executeRun(environmentID, runID, transition string, params []string) {
	defer s.building.Delete(environmentID)

	logPath := s.executor.LogPath(environmentID, runID)
	err := s.executor.Run(context.Background(), environmentID, transition, params, logPath)

	if err != nil {
		log.Printf("run %s (%s) failed: %v", runID, transition, err)
		s.db.CompleteRun(runID, "failed")
		s.db.UpdateEnvironmentStatus(environmentID, "error")
		return
	}

	s.db.CompleteRun(runID, "succeeded")
	switch transition {
	case "start":
		s.db.UpdateEnvironmentStatus(environmentID, "running")
	case "stop":
		s.db.UpdateEnvironmentStatus(environmentID, "stopped")
	case "delete":
		os.RemoveAll(s.executor.WorkspaceDir(environmentID))
		s.db.DeleteEnvironment(environmentID)
	}
}

// --- JSON API (internal polling endpoints) ---

func (s *Server) handleAPIEnvironmentStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env, err := s.db.GetEnvironment(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":          env.Status,
		"agent_connected": s.hub.AgentConnected(id),
	})
}

type runAPIResponse struct {
	ID         string  `json:"id"`
	Transition string  `json:"transition"`
	Status     string  `json:"status"`
	Logs       string  `json:"logs"`
	FinishedAt *string `json:"finished_at"`
}

func (s *Server) handleAPIRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.db.GetRun(id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}

	logs := tofu.ReadLog(run.LogPath)
	resp := runAPIResponse{
		ID:         run.ID,
		Transition: run.Transition,
		Status:     run.Status,
		Logs:       logs,
	}
	if run.FinishedAt != nil {
		s := run.FinishedAt.String()
		resp.FinishedAt = &s
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAPIRunStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.db.GetRun(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			content := tofu.ReadLog(run.LogPath)
			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			fmt.Fprintf(w, "data: %s\n\n", encoded)
			flusher.Flush()

			b, err := s.db.GetRun(id)
			if err != nil || b.Status != "running" {
				fmt.Fprintf(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
		}
	}
}

// --- Helpers ---

func fetchURL(rawURL string) (string, error) {
	resp, err := http.Get(rawURL) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

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
