package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"tahini.dev/tahini/internal/db"
	"tahini.dev/tahini/internal/tofu"
)

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// --- Response shapes ---

type envResponse struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	BlueprintID   string    `json:"blueprint_id"`
	BlueprintName string    `json:"blueprint_name"`
	Status        string    `json:"status"`
	Params        string    `json:"params"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func envToResponse(e db.Environment) envResponse {
	return envResponse{
		ID: e.ID, Name: e.Name, BlueprintID: e.BlueprintID,
		BlueprintName: e.BlueprintName, Status: e.Status,
		Params: e.Params, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

type blueprintResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	HCL         string    `json:"hcl"`
	GitURL      string    `json:"git_url"`
	CreatedAt   time.Time `json:"created_at"`
}

func blueprintToResponse(b db.Blueprint) blueprintResponse {
	return blueprintResponse{
		ID: b.ID, Name: b.Name, Description: b.Description,
		HCL: b.HCL, GitURL: b.GitURL, CreatedAt: b.CreatedAt,
	}
}

type runResponse struct {
	ID            string     `json:"id"`
	EnvironmentID string     `json:"environment_id"`
	Transition    string     `json:"transition"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at"`
}

func runToResponse(r db.Run) runResponse {
	return runResponse{
		ID: r.ID, EnvironmentID: r.EnvironmentID, Transition: r.Transition,
		Status: r.Status, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
}

type tokenResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func tokenToResponse(t db.APIToken) tokenResponse {
	return tokenResponse{
		ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt,
	}
}

// --- Environment handlers ---

func (s *Server) handleAPIV1ListEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := s.db.ListEnvironments()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	resp := make([]envResponse, len(envs))
	for i, e := range envs {
		resp[i] = envToResponse(e)
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleAPIV1CreateEnvironment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		BlueprintID string `json:"blueprint_id"`
		Params      string `json:"params"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if body.Name == "" || body.BlueprintID == "" {
		writeJSON(w, 400, map[string]string{"error": "name and blueprint_id are required"})
		return
	}
	env, err := s.db.CreateEnvironment(body.Name, body.BlueprintID, body.Params)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, envToResponse(env))
}

func (s *Server) handleAPIV1GetEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	env, err := s.db.GetEnvironment(id)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "environment not found"})
		return
	}
	writeJSON(w, 200, envToResponse(env))
}

func (s *Server) handleAPIV1StartEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runID, err := s.scheduleTransition(id, "start")
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]string{"run_id": runID})
}

func (s *Server) handleAPIV1StopEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runID, err := s.scheduleTransition(id, "stop")
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]string{"run_id": runID})
}

func (s *Server) handleAPIV1DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	runID, err := s.scheduleTransition(id, "delete")
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]string{"run_id": runID})
}

// --- Blueprint handlers ---

func (s *Server) handleAPIV1ListBlueprints(w http.ResponseWriter, r *http.Request) {
	blueprints, err := s.db.ListBlueprints()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	resp := make([]blueprintResponse, len(blueprints))
	for i, b := range blueprints {
		resp[i] = blueprintToResponse(b)
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleAPIV1CreateBlueprint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		HCL         string `json:"hcl"`
		GitURL      string `json:"git_url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if body.Name == "" || body.HCL == "" {
		writeJSON(w, 400, map[string]string{"error": "name and hcl are required"})
		return
	}
	bp, err := s.db.CreateBlueprint(body.Name, body.Description, body.HCL, body.GitURL)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, blueprintToResponse(bp))
}

func (s *Server) handleAPIV1GetBlueprint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bp, err := s.db.GetBlueprint(id)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "blueprint not found"})
		return
	}
	writeJSON(w, 200, blueprintToResponse(bp))
}

func (s *Server) handleAPIV1UpdateBlueprint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		HCL         string `json:"hcl"`
		GitURL      string `json:"git_url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if err := s.db.UpdateBlueprint(id, body.Name, body.Description, body.HCL, body.GitURL); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	bp, _ := s.db.GetBlueprint(id)
	writeJSON(w, 200, blueprintToResponse(bp))
}

func (s *Server) handleAPIV1DeleteBlueprint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if has, _ := s.db.BlueprintHasEnvironments(id); has {
		writeJSON(w, 409, map[string]string{"error": "blueprint has active environments"})
		return
	}
	if err := s.db.DeleteBlueprint(id); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(204)
}

// --- Run handlers ---

func (s *Server) handleAPIV1GetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.db.GetRun(id)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "run not found"})
		return
	}
	writeJSON(w, 200, runToResponse(run))
}

func (s *Server) handleAPIV1GetRunLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.db.GetRun(id)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "run not found"})
		return
	}
	logs := tofu.ReadLog(run.LogPath)
	writeJSON(w, 200, map[string]string{"logs": logs})
}

// --- Token handlers ---

func (s *Server) handleAPIV1ListTokens(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r)
	if sess.UserID == "" {
		writeJSON(w, 403, map[string]string{"error": "api tokens require a database user account"})
		return
	}
	tokens, err := s.db.ListAPITokensByUser(sess.UserID)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	resp := make([]tokenResponse, len(tokens))
	for i, t := range tokens {
		resp[i] = tokenToResponse(t)
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleAPIV1CreateToken(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r)
	if sess.UserID == "" {
		writeJSON(w, 403, map[string]string{"error": "api tokens require a database user account"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil || body.Name == "" {
		writeJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}
	rawToken, tokenHash, err := generateAPIToken()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "failed to generate token"})
		return
	}
	t, err := s.db.CreateAPIToken(sess.UserID, body.Name, tokenHash)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 201, map[string]any{
		"token":      rawToken,
		"id":         t.ID,
		"name":       t.Name,
		"created_at": t.CreatedAt,
	})
}

func (s *Server) handleAPIV1DeleteToken(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r)
	if sess.UserID == "" {
		writeJSON(w, 403, map[string]string{"error": "api tokens require a database user account"})
		return
	}
	id := r.PathValue("id")
	if err := s.db.DeleteAPIToken(id, sess.UserID); err != nil {
		writeJSON(w, 404, map[string]string{"error": "token not found"})
		return
	}
	w.WriteHeader(204)
}

// generateAPIToken creates a new thn_* token and returns (rawToken, sha256Hash, error).
func generateAPIToken() (string, string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw := "thn_" + hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(h[:]), nil
}
