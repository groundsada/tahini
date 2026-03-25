package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS orgs (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'user',
	org_id        TEXT REFERENCES orgs(id),
	created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS blueprints (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	hcl         TEXT NOT NULL,
	git_url     TEXT NOT NULL DEFAULT '',
	created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS environments (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL UNIQUE,
	blueprint_id TEXT NOT NULL REFERENCES blueprints(id),
	status       TEXT NOT NULL DEFAULT 'stopped',
	params       TEXT NOT NULL DEFAULT '',
	agent_token  TEXT NOT NULL DEFAULT '',
	created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS runs (
	id             TEXT PRIMARY KEY,
	environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
	transition     TEXT NOT NULL,
	status         TEXT NOT NULL DEFAULT 'running',
	log_path       TEXT NOT NULL DEFAULT '',
	created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	finished_at    TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_tokens (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	last_used_at TIMESTAMP
);

CREATE TABLE IF NOT EXISTS events (
	id            TEXT PRIMARY KEY,
	actor_id      TEXT NOT NULL DEFAULT '',
	actor_name    TEXT NOT NULL DEFAULT '',
	action        TEXT NOT NULL,
	resource_type TEXT NOT NULL DEFAULT '',
	resource_id   TEXT NOT NULL DEFAULT '',
	resource_name TEXT NOT NULL DEFAULT '',
	ip            TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

type DB struct {
	db     *sql.DB
	driver string // "sqlite" or "postgres"
}

// rebind converts ? placeholders to $1, $2, ... for Postgres.
var reQuestion = regexp.MustCompile(`\?`)

func (d *DB) rebind(q string) string {
	if d.driver != "postgres" {
		return q
	}
	n := 0
	return reQuestion.ReplaceAllStringFunc(q, func(_ string) string {
		n++
		return fmt.Sprintf("$%d", n)
	})
}

// addColumnIfNotExists adds a column to a table if it doesn't already exist.
func (d *DB) addColumnIfNotExists(table, column, definition string) error {
	if d.driver == "postgres" {
		_, err := d.exec(fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s`, table, column, definition,
		))
		return err
	}
	_, err := d.exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

// renameTableIfExists renames a table if the old name exists.
func (d *DB) renameTableIfExists(oldName, newName string) error {
	var exists bool
	if d.driver == "postgres" {
		if err := d.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`, oldName).Scan(&exists); err != nil || !exists {
			return nil
		}
		_, err := d.db.Exec(`ALTER TABLE "` + oldName + `" RENAME TO "` + newName + `"`)
		return err
	}
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, oldName).Scan(&count); err != nil || count == 0 {
		return nil
	}
	_, err := d.db.Exec(`ALTER TABLE "` + oldName + `" RENAME TO "` + newName + `"`)
	return err
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

type Blueprint struct {
	ID          string
	Name        string
	Description string
	HCL         string
	GitURL      string
	CreatedAt   time.Time
}

type Environment struct {
	ID            string
	Name          string
	BlueprintID   string
	BlueprintName string
	Status        string
	Params        string
	AgentToken    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Run struct {
	ID            string
	EnvironmentID string
	Transition    string
	Status        string
	LogPath       string
	CreatedAt     time.Time
	FinishedAt    *time.Time
}

type APIToken struct {
	ID         string
	UserID     string
	Name       string
	TokenHash  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// New opens the database. dsn can be:
//   - a file path (e.g. "/data/tahini.db") → SQLite
//   - a postgres URL (e.g. "postgres://user:pass@host/db") → Postgres
func New(dsn string) (*DB, error) {
	driver := "sqlite"
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		driver = "postgres"
	}

	var connStr string
	if driver == "sqlite" {
		connStr = dsn + "?_foreign_keys=on&_journal_mode=WAL"
	} else {
		connStr = dsn
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}

	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(5 * time.Minute)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}

	d := &DB{db: db, driver: driver}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if _, err := d.exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return d, nil
}

func (d *DB) migrate() error {
	// Rename old tables to new names (runs first since it refs workspaces, then workspaces since it refs templates, then templates).
	if err := d.renameTableIfExists("builds", "runs"); err != nil {
		return fmt.Errorf("rename builds->runs: %w", err)
	}
	if err := d.renameTableIfExists("workspaces", "environments"); err != nil {
		return fmt.Errorf("rename workspaces->environments: %w", err)
	}
	if err := d.renameTableIfExists("templates", "blueprints"); err != nil {
		return fmt.Errorf("rename templates->blueprints: %w", err)
	}
	if err := d.addColumnIfNotExists("environments", "agent_token", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("agent_token: %w", err)
	}
	if err := d.addColumnIfNotExists("blueprints", "git_url", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("git_url: %w", err)
	}
	return nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) exec(q string, args ...any) (sql.Result, error) {
	return d.db.Exec(d.rebind(q), args...)
}
func (d *DB) queryRow(q string, args ...any) *sql.Row {
	return d.db.QueryRow(d.rebind(q), args...)
}
func (d *DB) query(q string, args ...any) (*sql.Rows, error) {
	return d.db.Query(d.rebind(q), args...)
}

// --- Orgs ---

func (d *DB) CreateOrg(name string) (Org, error) {
	id := uuid.New().String()
	_, err := d.exec(`INSERT INTO orgs (id, name) VALUES (?, ?)`, id, name)
	if err != nil {
		return Org{}, err
	}
	return d.GetOrg(id)
}

func (d *DB) GetOrg(id string) (Org, error) {
	var o Org
	err := d.queryRow(`SELECT id, name, created_at FROM orgs WHERE id = ?`, id).
		Scan(&o.ID, &o.Name, &o.CreatedAt)
	return o, err
}

func (d *DB) ListOrgs() ([]Org, error) {
	rows, err := d.query(`SELECT id, name, created_at FROM orgs ORDER BY name`)
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
	_, err := d.exec(`DELETE FROM orgs WHERE id = ?`, id)
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
	_, err = d.exec(
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
	err := d.queryRow(
		`SELECT id, username, password_hash, role, COALESCE(org_id,''), created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &orgID, &u.CreatedAt)
	if orgID.Valid {
		u.OrgID = orgID.String
	}
	return u, err
}

func (d *DB) GetUserByUsername(username string) (User, error) {
	var u User
	err := d.queryRow(
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
	rows, err := d.query(
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
	_, err := d.exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	return err
}

func (d *DB) UpdateUserOrg(id, orgID string) error {
	var orgVal any
	if orgID != "" {
		orgVal = orgID
	}
	_, err := d.exec(`UPDATE users SET org_id = ? WHERE id = ?`, orgVal, id)
	return err
}

func (d *DB) DeleteUser(id string) error {
	_, err := d.exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (d *DB) CountUsers() (int, error) {
	var n int
	err := d.queryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) UpdateUserPassword(id, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = d.exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	return err
}

// --- Blueprints ---

func (d *DB) CreateBlueprint(name, description, hcl, gitURL string) (Blueprint, error) {
	id := uuid.New().String()
	_, err := d.exec(
		`INSERT INTO blueprints (id, name, description, hcl, git_url) VALUES (?, ?, ?, ?, ?)`,
		id, name, description, hcl, gitURL,
	)
	if err != nil {
		return Blueprint{}, err
	}
	return d.GetBlueprint(id)
}

func (d *DB) GetBlueprint(id string) (Blueprint, error) {
	var t Blueprint
	err := d.queryRow(
		`SELECT id, name, description, hcl, git_url, created_at FROM blueprints WHERE id = ?`, id,
	).Scan(&t.ID, &t.Name, &t.Description, &t.HCL, &t.GitURL, &t.CreatedAt)
	return t, err
}

func (d *DB) ListBlueprints() ([]Blueprint, error) {
	rows, err := d.query(
		`SELECT id, name, description, hcl, git_url, created_at FROM blueprints ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Blueprint
	for rows.Next() {
		var t Blueprint
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.HCL, &t.GitURL, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (d *DB) UpdateBlueprint(id, name, description, hcl, gitURL string) error {
	_, err := d.exec(
		`UPDATE blueprints SET name = ?, description = ?, hcl = ?, git_url = ? WHERE id = ?`,
		name, description, hcl, gitURL, id,
	)
	return err
}

func (d *DB) UpdateBlueprintHCL(id, hcl string) error {
	_, err := d.exec(`UPDATE blueprints SET hcl = ? WHERE id = ?`, hcl, id)
	return err
}

func (d *DB) DeleteBlueprint(id string) error {
	_, err := d.exec(`DELETE FROM blueprints WHERE id = ?`, id)
	return err
}

func (d *DB) BlueprintHasEnvironments(id string) (bool, error) {
	var count int
	err := d.queryRow(
		`SELECT COUNT(*) FROM environments WHERE blueprint_id = ?`, id,
	).Scan(&count)
	return count > 0, err
}

// --- Environments ---

func (d *DB) CreateEnvironment(name, blueprintID, params string) (Environment, error) {
	id := uuid.New().String()
	token := uuid.New().String()
	_, err := d.exec(
		`INSERT INTO environments (id, name, blueprint_id, params, agent_token) VALUES (?, ?, ?, ?, ?)`,
		id, name, blueprintID, params, token,
	)
	if err != nil {
		return Environment{}, err
	}
	return d.GetEnvironment(id)
}

func (d *DB) GetEnvironment(id string) (Environment, error) {
	var e Environment
	err := d.queryRow(`
		SELECT e.id, e.name, e.blueprint_id, COALESCE(b.name, ''), e.status, e.params, e.agent_token, e.created_at, e.updated_at
		FROM environments e
		LEFT JOIN blueprints b ON e.blueprint_id = b.id
		WHERE e.id = ?`, id,
	).Scan(&e.ID, &e.Name, &e.BlueprintID, &e.BlueprintName, &e.Status, &e.Params, &e.AgentToken, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

func (d *DB) GetEnvironmentByAgentToken(token string) (Environment, error) {
	var e Environment
	err := d.queryRow(`
		SELECT e.id, e.name, e.blueprint_id, COALESCE(b.name, ''), e.status, e.params, e.agent_token, e.created_at, e.updated_at
		FROM environments e
		LEFT JOIN blueprints b ON e.blueprint_id = b.id
		WHERE e.agent_token = ? AND e.agent_token != ''`, token,
	).Scan(&e.ID, &e.Name, &e.BlueprintID, &e.BlueprintName, &e.Status, &e.Params, &e.AgentToken, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

func (d *DB) ListEnvironments() ([]Environment, error) {
	rows, err := d.query(`
		SELECT e.id, e.name, e.blueprint_id, COALESCE(b.name, ''), e.status, e.params, e.agent_token, e.created_at, e.updated_at
		FROM environments e
		LEFT JOIN blueprints b ON e.blueprint_id = b.id
		ORDER BY e.created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.Name, &e.BlueprintID, &e.BlueprintName, &e.Status, &e.Params, &e.AgentToken, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) UpdateEnvironmentStatus(id, status string) error {
	_, err := d.exec(
		`UPDATE environments SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	return err
}

func (d *DB) UpdateEnvironmentParams(id, params string) error {
	_, err := d.exec(
		`UPDATE environments SET params = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		params, id,
	)
	return err
}

func (d *DB) DeleteEnvironment(id string) error {
	_, err := d.exec(`DELETE FROM environments WHERE id = ?`, id)
	return err
}

func (d *DB) EnvironmentsForBlueprint(blueprintID string) ([]Environment, error) {
	rows, err := d.query(`
		SELECT e.id, e.name, e.blueprint_id, COALESCE(b.name, ''), e.status, e.params, e.agent_token, e.created_at, e.updated_at
		FROM environments e
		LEFT JOIN blueprints b ON e.blueprint_id = b.id
		WHERE e.blueprint_id = ?
		ORDER BY e.created_at DESC`, blueprintID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.Name, &e.BlueprintID, &e.BlueprintName, &e.Status, &e.Params, &e.AgentToken, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Runs ---

func (d *DB) CreateRun(environmentID, transition, logPath string) (Run, error) {
	id := uuid.New().String()
	_, err := d.exec(
		`INSERT INTO runs (id, environment_id, transition, log_path) VALUES (?, ?, ?, ?)`,
		id, environmentID, transition, logPath,
	)
	if err != nil {
		return Run{}, err
	}
	return d.GetRun(id)
}

func (d *DB) GetRun(id string) (Run, error) {
	var r Run
	err := d.queryRow(
		`SELECT id, environment_id, transition, status, log_path, created_at, finished_at FROM runs WHERE id = ?`, id,
	).Scan(&r.ID, &r.EnvironmentID, &r.Transition, &r.Status, &r.LogPath, &r.CreatedAt, &r.FinishedAt)
	return r, err
}

func (d *DB) GetLatestRun(environmentID string) (*Run, error) {
	var r Run
	err := d.queryRow(
		`SELECT id, environment_id, transition, status, log_path, created_at, finished_at
		 FROM runs WHERE environment_id = ? ORDER BY created_at DESC LIMIT 1`,
		environmentID,
	).Scan(&r.ID, &r.EnvironmentID, &r.Transition, &r.Status, &r.LogPath, &r.CreatedAt, &r.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

func (d *DB) ListRuns(environmentID string) ([]Run, error) {
	rows, err := d.query(
		`SELECT id, environment_id, transition, status, log_path, created_at, finished_at
		 FROM runs WHERE environment_id = ? ORDER BY created_at DESC`,
		environmentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.EnvironmentID, &r.Transition, &r.Status, &r.LogPath, &r.CreatedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) CompleteRun(id, status string) error {
	_, err := d.exec(
		`UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, id,
	)
	return err
}

// --- API Tokens ---

func (d *DB) CreateAPIToken(userID, name, tokenHash string) (APIToken, error) {
	id := uuid.New().String()
	_, err := d.exec(`INSERT INTO api_tokens (id,user_id,name,token_hash) VALUES (?,?,?,?)`, id, userID, name, tokenHash)
	if err != nil {
		return APIToken{}, err
	}
	return d.GetAPITokenByID(id)
}

func (d *DB) GetAPITokenByID(id string) (APIToken, error) {
	var t APIToken
	err := d.queryRow(`SELECT id,user_id,name,token_hash,created_at,last_used_at FROM api_tokens WHERE id=?`, id).
		Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.CreatedAt, &t.LastUsedAt)
	return t, err
}

func (d *DB) GetAPITokenByHash(hash string) (APIToken, error) {
	var t APIToken
	err := d.queryRow(`SELECT id,user_id,name,token_hash,created_at,last_used_at FROM api_tokens WHERE token_hash=?`, hash).
		Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.CreatedAt, &t.LastUsedAt)
	return t, err
}

func (d *DB) ListAPITokensByUser(userID string) ([]APIToken, error) {
	rows, err := d.query(`SELECT id,user_id,name,token_hash,created_at,last_used_at FROM api_tokens WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

func (d *DB) DeleteAPIToken(id, userID string) error {
	_, err := d.exec(`DELETE FROM api_tokens WHERE id=? AND user_id=?`, id, userID)
	return err
}

func (d *DB) TouchAPIToken(id string) error {
	_, err := d.exec(`UPDATE api_tokens SET last_used_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	return err
}

// --- Events (audit log) ---

type Event struct {
	ID           string
	ActorID      string
	ActorName    string
	Action       string
	ResourceType string
	ResourceID   string
	ResourceName string
	IP           string
	CreatedAt    time.Time
}

func (d *DB) LogEvent(actorID, actorName, action, resourceType, resourceID, resourceName, ip string) {
	id := uuid.New().String()
	d.exec(
		`INSERT INTO events (id,actor_id,actor_name,action,resource_type,resource_id,resource_name,ip) VALUES (?,?,?,?,?,?,?,?)`,
		id, actorID, actorName, action, resourceType, resourceID, resourceName, ip,
	)
}

func (d *DB) ListEvents(limit int) ([]Event, error) {
	rows, err := d.query(
		`SELECT id,actor_id,actor_name,action,resource_type,resource_id,resource_name,ip,created_at FROM events ORDER BY created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.Action, &e.ResourceType, &e.ResourceID, &e.ResourceName, &e.IP, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
