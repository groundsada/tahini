package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS orgs (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'user',
	org_id        TEXT REFERENCES orgs(id),
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS templates (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	hcl         TEXT NOT NULL,
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS workspaces (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	template_id TEXT NOT NULL REFERENCES templates(id),
	status      TEXT NOT NULL DEFAULT 'stopped',
	params      TEXT NOT NULL DEFAULT '',
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS builds (
	id           TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	transition   TEXT NOT NULL,
	status       TEXT NOT NULL DEFAULT 'running',
	log_path     TEXT NOT NULL DEFAULT '',
	created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	finished_at  DATETIME
);
`

type DB struct {
	db *sql.DB
}

type Org struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string // owner, user_admin, template_admin, user
	OrgID        string
	CreatedAt    time.Time
}

type Template struct {
	ID          string
	Name        string
	Description string
	HCL         string
	GitURL      string
	CreatedAt   time.Time
}

type Workspace struct {
	ID           string
	Name         string
	TemplateID   string
	TemplateName string
	Status       string
	Params       string
	AgentToken   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Build struct {
	ID          string
	WorkspaceID string
	Transition  string
	Status      string
	LogPath     string
	CreatedAt   time.Time
	FinishedAt  *time.Time
}

func New(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	d := &DB{db: db}
	if err := d.migrateAgentToken(); err != nil {
		return nil, fmt.Errorf("migrate agent_token: %w", err)
	}
	if err := d.migrateTemplateGitURL(); err != nil {
		return nil, fmt.Errorf("migrate git_url: %w", err)
	}
	if err := d.migrateUsers(); err != nil {
		return nil, fmt.Errorf("migrate users: %w", err)
	}
	return d, nil
}

// migrateUsers ensures the orgs and users tables exist (they may not if this is an old DB).
func (d *DB) migrateUsers() error {
	// These are created by schema above; this is a no-op for new DBs.
	// For old DBs the CREATE TABLE IF NOT EXISTS in schema handles it.
	return nil
}

// migrateAgentToken adds the agent_token column if it doesn't exist yet.
func (d *DB) migrateAgentToken() error {
	_, err := d.db.Exec(`ALTER TABLE workspaces ADD COLUMN agent_token TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
}

// migrateTemplateGitURL adds the git_url column to templates if it doesn't exist.
func (d *DB) migrateTemplateGitURL() error {
	_, err := d.db.Exec(`ALTER TABLE templates ADD COLUMN git_url TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	return nil
}

func (d *DB) Close() error { return d.db.Close() }

// --- Orgs ---

func (d *DB) CreateOrg(name string) (Org, error) {
	id := uuid.New().String()
	_, err := d.db.Exec(`INSERT INTO orgs (id, name) VALUES (?, ?)`, id, name)
	if err != nil {
		return Org{}, err
	}
	return d.GetOrg(id)
}

func (d *DB) GetOrg(id string) (Org, error) {
	var o Org
	err := d.db.QueryRow(`SELECT id, name, created_at FROM orgs WHERE id = ?`, id).
		Scan(&o.ID, &o.Name, &o.CreatedAt)
	return o, err
}

func (d *DB) ListOrgs() ([]Org, error) {
	rows, err := d.db.Query(`SELECT id, name, created_at FROM orgs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		var o Org
		if err := rows.Scan(&o.ID, &o.Name, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (d *DB) DeleteOrg(id string) error {
	_, err := d.db.Exec(`DELETE FROM orgs WHERE id = ?`, id)
	return err
}

// --- Users ---

func (d *DB) CreateUser(username, password, role, orgID string) (User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	id := uuid.New().String()
	var orgVal any
	if orgID != "" {
		orgVal = orgID
	}
	_, err = d.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, org_id) VALUES (?, ?, ?, ?, ?)`,
		id, username, string(hash), role, orgVal,
	)
	if err != nil {
		return User{}, err
	}
	return d.GetUserByID(id)
}

func (d *DB) GetUserByID(id string) (User, error) {
	var u User
	var orgID sql.NullString
	err := d.db.QueryRow(
		`SELECT id, username, password_hash, role, COALESCE(org_id,''), created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &orgID, &u.CreatedAt)
	if orgID.Valid {
		u.OrgID = orgID.String
	}
	return u, err
}

func (d *DB) GetUserByUsername(username string) (User, error) {
	var u User
	err := d.db.QueryRow(
		`SELECT id, username, password_hash, role, COALESCE(org_id,''), created_at FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.OrgID, &u.CreatedAt)
	return u, err
}

func (d *DB) AuthenticateUser(username, password string) (User, error) {
	u, err := d.GetUserByUsername(username)
	if err != nil {
		return User{}, fmt.Errorf("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return User{}, fmt.Errorf("invalid credentials")
	}
	return u, nil
}

func (d *DB) ListUsers() ([]User, error) {
	rows, err := d.db.Query(
		`SELECT id, username, password_hash, role, COALESCE(org_id,''), created_at FROM users ORDER BY username`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.OrgID, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) UpdateUserRole(id, role string) error {
	_, err := d.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	return err
}

func (d *DB) UpdateUserOrg(id, orgID string) error {
	var orgVal any
	if orgID != "" {
		orgVal = orgID
	}
	_, err := d.db.Exec(`UPDATE users SET org_id = ? WHERE id = ?`, orgVal, id)
	return err
}

func (d *DB) DeleteUser(id string) error {
	_, err := d.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (d *DB) CountUsers() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) UpdateUserPassword(id, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	return err
}

// --- Templates ---

func (d *DB) CreateTemplate(name, description, hcl, gitURL string) (Template, error) {
	id := uuid.New().String()
	_, err := d.db.Exec(
		`INSERT INTO templates (id, name, description, hcl, git_url) VALUES (?, ?, ?, ?, ?)`,
		id, name, description, hcl, gitURL,
	)
	if err != nil {
		return Template{}, err
	}
	return d.GetTemplate(id)
}

func (d *DB) GetTemplate(id string) (Template, error) {
	var t Template
	err := d.db.QueryRow(
		`SELECT id, name, description, hcl, git_url, created_at FROM templates WHERE id = ?`, id,
	).Scan(&t.ID, &t.Name, &t.Description, &t.HCL, &t.GitURL, &t.CreatedAt)
	return t, err
}

func (d *DB) ListTemplates() ([]Template, error) {
	rows, err := d.db.Query(
		`SELECT id, name, description, hcl, git_url, created_at FROM templates ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.HCL, &t.GitURL, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) UpdateTemplate(id, name, description, hcl, gitURL string) error {
	_, err := d.db.Exec(
		`UPDATE templates SET name = ?, description = ?, hcl = ?, git_url = ? WHERE id = ?`,
		name, description, hcl, gitURL, id,
	)
	return err
}

func (d *DB) UpdateTemplateHCL(id, hcl string) error {
	_, err := d.db.Exec(`UPDATE templates SET hcl = ? WHERE id = ?`, hcl, id)
	return err
}

func (d *DB) DeleteTemplate(id string) error {
	_, err := d.db.Exec(`DELETE FROM templates WHERE id = ?`, id)
	return err
}

func (d *DB) TemplateHasWorkspaces(id string) (bool, error) {
	var count int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM workspaces WHERE template_id = ?`, id,
	).Scan(&count)
	return count > 0, err
}

// --- Workspaces ---

func (d *DB) CreateWorkspace(name, templateID, params string) (Workspace, error) {
	id := uuid.New().String()
	token := uuid.New().String()
	_, err := d.db.Exec(
		`INSERT INTO workspaces (id, name, template_id, params, agent_token) VALUES (?, ?, ?, ?, ?)`,
		id, name, templateID, params, token,
	)
	if err != nil {
		return Workspace{}, err
	}
	return d.GetWorkspace(id)
}

func (d *DB) GetWorkspace(id string) (Workspace, error) {
	var w Workspace
	err := d.db.QueryRow(`
		SELECT w.id, w.name, w.template_id, COALESCE(t.name, ''), w.status, w.params, w.agent_token, w.created_at, w.updated_at
		FROM workspaces w
		LEFT JOIN templates t ON w.template_id = t.id
		WHERE w.id = ?`, id,
	).Scan(&w.ID, &w.Name, &w.TemplateID, &w.TemplateName, &w.Status, &w.Params, &w.AgentToken, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

func (d *DB) GetWorkspaceByAgentToken(token string) (Workspace, error) {
	var w Workspace
	err := d.db.QueryRow(`
		SELECT w.id, w.name, w.template_id, COALESCE(t.name, ''), w.status, w.params, w.agent_token, w.created_at, w.updated_at
		FROM workspaces w
		LEFT JOIN templates t ON w.template_id = t.id
		WHERE w.agent_token = ? AND w.agent_token != ''`, token,
	).Scan(&w.ID, &w.Name, &w.TemplateID, &w.TemplateName, &w.Status, &w.Params, &w.AgentToken, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

func (d *DB) ListWorkspaces() ([]Workspace, error) {
	rows, err := d.db.Query(`
		SELECT w.id, w.name, w.template_id, COALESCE(t.name, ''), w.status, w.params, w.agent_token, w.created_at, w.updated_at
		FROM workspaces w
		LEFT JOIN templates t ON w.template_id = t.id
		ORDER BY w.created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.TemplateID, &w.TemplateName, &w.Status, &w.Params, &w.AgentToken, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (d *DB) UpdateWorkspaceStatus(id, status string) error {
	_, err := d.db.Exec(
		`UPDATE workspaces SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	return err
}

func (d *DB) UpdateWorkspaceParams(id, params string) error {
	_, err := d.db.Exec(
		`UPDATE workspaces SET params = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		params, id,
	)
	return err
}

func (d *DB) DeleteWorkspace(id string) error {
	_, err := d.db.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	return err
}

func (d *DB) WorkspacesForTemplate(templateID string) ([]Workspace, error) {
	rows, err := d.db.Query(`
		SELECT w.id, w.name, w.template_id, COALESCE(t.name, ''), w.status, w.params, w.agent_token, w.created_at, w.updated_at
		FROM workspaces w
		LEFT JOIN templates t ON w.template_id = t.id
		WHERE w.template_id = ?
		ORDER BY w.created_at DESC`, templateID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.TemplateID, &w.TemplateName, &w.Status, &w.Params, &w.AgentToken, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// --- Builds ---

func (d *DB) CreateBuild(id, workspaceID, transition, logPath string) (Build, error) {
	_, err := d.db.Exec(
		`INSERT INTO builds (id, workspace_id, transition, log_path) VALUES (?, ?, ?, ?)`,
		id, workspaceID, transition, logPath,
	)
	if err != nil {
		return Build{}, err
	}
	return d.GetBuild(id)
}

func (d *DB) GetBuild(id string) (Build, error) {
	var b Build
	err := d.db.QueryRow(
		`SELECT id, workspace_id, transition, status, log_path, created_at, finished_at FROM builds WHERE id = ?`, id,
	).Scan(&b.ID, &b.WorkspaceID, &b.Transition, &b.Status, &b.LogPath, &b.CreatedAt, &b.FinishedAt)
	return b, err
}

func (d *DB) GetLatestBuild(workspaceID string) (*Build, error) {
	var b Build
	err := d.db.QueryRow(
		`SELECT id, workspace_id, transition, status, log_path, created_at, finished_at
		 FROM builds WHERE workspace_id = ? ORDER BY created_at DESC LIMIT 1`,
		workspaceID,
	).Scan(&b.ID, &b.WorkspaceID, &b.Transition, &b.Status, &b.LogPath, &b.CreatedAt, &b.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

func (d *DB) ListBuilds(workspaceID string) ([]Build, error) {
	rows, err := d.db.Query(
		`SELECT id, workspace_id, transition, status, log_path, created_at, finished_at
		 FROM builds WHERE workspace_id = ? ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Build
	for rows.Next() {
		var b Build
		if err := rows.Scan(&b.ID, &b.WorkspaceID, &b.Transition, &b.Status, &b.LogPath, &b.CreatedAt, &b.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (d *DB) CompleteBuild(id, status string) error {
	_, err := d.db.Exec(
		`UPDATE builds SET status = ?, finished_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	return err
}
