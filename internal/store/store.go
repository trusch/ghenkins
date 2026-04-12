package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type RunStatus string

const (
	RunStatusQueued   RunStatus = "queued"
	RunStatusRunning  RunStatus = "running"
	RunStatusSuccess  RunStatus = "success"
	RunStatusFailure  RunStatus = "failure"
	RunStatusError    RunStatus = "error"
	RunStatusCanceled RunStatus = "canceled"
)

type Run struct {
	ID           string     `json:"id"`
	WatchName    string     `json:"watch_name"`
	Repo         string     `json:"repo"`
	SHA          string     `json:"sha"`
	WorkflowName string     `json:"workflow_name"`
	Status       RunStatus  `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	ExitCode     *int       `json:"exit_code"`
	LogPath      string     `json:"-"`
}

type SeenCommit struct {
	Repo        string
	SHA         string
	PrOrBranch  string
	FirstSeenAt time.Time
}

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	Token     string
	UserID    string
	ExpiresAt time.Time
}

type DBWatch struct {
	Name      string       `json:"name"`
	Repo      string       `json:"repo"`
	Branch    string       `json:"branch,omitempty"`
	PR        int          `json:"pr,omitempty"`
	OnEvents  []string     `json:"on_events"`
	Workflows []*DBWorkflow `json:"workflows"`
}

type DBWorkflow struct {
	ID          string            `json:"id"`
	WatchName   string            `json:"watch_name"`
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	RunnerImage string            `json:"runner_image,omitempty"`
	Secrets     map[string]string `json:"secrets"`
	Env         map[string]string `json:"env"`
}

type Store interface {
	CreateRun(ctx context.Context, r *Run) error
	UpdateRunStatus(ctx context.Context, id string, status RunStatus, exitCode *int, finishedAt *time.Time) error
	GetRun(ctx context.Context, id string) (*Run, error)
	ListRuns(ctx context.Context, limit int) ([]Run, error)
	DeleteRun(ctx context.Context, id string) error
	RunExists(ctx context.Context, repo, sha, workflowName string) (bool, error)
	MarkSeen(ctx context.Context, sc SeenCommit) error
	IsSeen(ctx context.Context, repo, sha, prOrBranch string) (bool, error)

	// Users
	CreateUser(ctx context.Context, id, username, passwordHash, role string) error
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)
	DeleteUser(ctx context.Context, id string) error
	UpdateUserPassword(ctx context.Context, id, passwordHash string) error
	UserCount(ctx context.Context) (int, error)

	// Sessions
	CreateSession(ctx context.Context, token, userID string, expiresAt time.Time) error
	GetSession(ctx context.Context, token string) (*Session, error)
	DeleteSession(ctx context.Context, token string) error

	// Watches
	ListWatches(ctx context.Context) ([]*DBWatch, error)
	GetWatch(ctx context.Context, name string) (*DBWatch, error)
	CreateWatch(ctx context.Context, w *DBWatch) error
	UpdateWatch(ctx context.Context, w *DBWatch) error
	DeleteWatch(ctx context.Context, name string) error
	CreateWorkflow(ctx context.Context, wf *DBWorkflow) error
	UpdateWorkflow(ctx context.Context, wf *DBWorkflow) error
	DeleteWorkflow(ctx context.Context, watchName, workflowName string) error

	Close() error
}

type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

func Open(path string) (Store, error) {
	sep := "?"
	if len(path) > 0 && path[len(path)-1] != '?' {
		for _, c := range path {
			if c == '?' {
				sep = "&"
				break
			}
		}
	}
	dsn := path + sep + "_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := applyMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ---- Runs ----

func (s *SQLiteStore) CreateRun(ctx context.Context, r *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (id, watch_name, repo, sha, workflow_name, status, started_at, finished_at, exit_code, log_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.WatchName, r.Repo, r.SHA, r.WorkflowName, string(r.Status),
		r.StartedAt.UTC().Format(time.RFC3339Nano),
		nullTime(r.FinishedAt), r.ExitCode, r.LogPath,
	)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateRunStatus(ctx context.Context, id string, status RunStatus, exitCode *int, finishedAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, exit_code = ?, finished_at = ? WHERE id = ?`,
		string(status), exitCode, nullTime(finishedAt), id,
	)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetRun(ctx context.Context, id string) (*Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, watch_name, repo, sha, workflow_name, status, started_at, finished_at, exit_code, log_path
		 FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *SQLiteStore) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, watch_name, repo, sha, workflow_name, status, started_at, finished_at, exit_code, log_path
		 FROM runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}

func (s *SQLiteStore) DeleteRun(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete run: %w", err)
	}
	return nil
}

func (s *SQLiteStore) RunExists(ctx context.Context, repo, sha, workflowName string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE repo = ? AND sha = ? AND workflow_name = ? AND status IN ('queued', 'running')`,
		repo, sha, workflowName,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("run exists: %w", err)
	}
	return count > 0, nil
}

func (s *SQLiteStore) MarkSeen(ctx context.Context, sc SeenCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO seen_commits (repo, sha, pr_or_branch, first_seen_at) VALUES (?, ?, ?, ?)`,
		sc.Repo, sc.SHA, sc.PrOrBranch, sc.FirstSeenAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("mark seen: %w", err)
	}
	return nil
}

func (s *SQLiteStore) IsSeen(ctx context.Context, repo, sha, prOrBranch string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM seen_commits WHERE repo = ? AND sha = ? AND pr_or_branch = ?`,
		repo, sha, prOrBranch,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("is seen: %w", err)
	}
	return count > 0, nil
}

// ---- Users ----

func (s *SQLiteStore) CreateUser(ctx context.Context, id, username, passwordHash, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role) VALUES (?, ?, ?, ?)`,
		id, username, passwordHash, role,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *SQLiteStore) GetUserByID(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *SQLiteStore) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, role, created_at FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *SQLiteStore) DeleteUser(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateUserPassword(ctx context.Context, id, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return fmt.Errorf("update user password: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UserCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("user count: %w", err)
	}
	return count, nil
}

// ---- Sessions ----

func (s *SQLiteStore) CreateSession(ctx context.Context, token, userID string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, expiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetSession(ctx context.Context, token string) (*Session, error) {
	var sess Session
	var expiresAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT token, user_id, expires_at FROM sessions WHERE token = ?`, token,
	).Scan(&sess.Token, &sess.UserID, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}

	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05Z", expiresAt)
		if err != nil {
			return nil, fmt.Errorf("parse session expires_at: %w", err)
		}
	}
	sess.ExpiresAt = t

	if time.Now().After(sess.ExpiresAt) {
		// Expired — delete and return nil
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
		return nil, nil
	}

	return &sess, nil
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ---- Watches ----

func (s *SQLiteStore) ListWatches(ctx context.Context) ([]*DBWatch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, repo, COALESCE(branch,''), COALESCE(pr,0), on_events FROM watches ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list watches: %w", err)
	}
	defer rows.Close()

	var watches []*DBWatch
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		watches = append(watches, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, w := range watches {
		if err := s.loadWorkflows(ctx, w); err != nil {
			return nil, err
		}
	}
	return watches, nil
}

func (s *SQLiteStore) GetWatch(ctx context.Context, name string) (*DBWatch, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT name, repo, COALESCE(branch,''), COALESCE(pr,0), on_events FROM watches WHERE name = ?`, name)
	w, err := scanWatch(row)
	if err != nil {
		return nil, err
	}
	if err := s.loadWorkflows(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *SQLiteStore) loadWorkflows(ctx context.Context, w *DBWatch) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, watch_name, name, path, COALESCE(runner_image,''), secrets, env FROM workflows WHERE watch_name = ? ORDER BY name ASC`,
		w.Name)
	if err != nil {
		return fmt.Errorf("load workflows: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return err
		}
		w.Workflows = append(w.Workflows, wf)
	}
	return rows.Err()
}

func (s *SQLiteStore) CreateWatch(ctx context.Context, w *DBWatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	onEventsJSON, err := json.Marshal(w.OnEvents)
	if err != nil {
		return fmt.Errorf("marshal on_events: %w", err)
	}

	var prVal any
	if w.PR != 0 {
		prVal = w.PR
	}
	var branchVal any
	if w.Branch != "" {
		branchVal = w.Branch
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO watches (name, repo, branch, pr, on_events) VALUES (?, ?, ?, ?, ?)`,
		w.Name, w.Repo, branchVal, prVal, string(onEventsJSON),
	)
	if err != nil {
		return fmt.Errorf("create watch: %w", err)
	}

	for _, wf := range w.Workflows {
		wf.WatchName = w.Name
		if wf.ID == "" {
			wf.ID = generateStoreID()
		}
		if err := s.insertWorkflow(ctx, wf); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) UpdateWatch(ctx context.Context, w *DBWatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	onEventsJSON, err := json.Marshal(w.OnEvents)
	if err != nil {
		return fmt.Errorf("marshal on_events: %w", err)
	}

	var prVal any
	if w.PR != 0 {
		prVal = w.PR
	}
	var branchVal any
	if w.Branch != "" {
		branchVal = w.Branch
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE watches SET repo = ?, branch = ?, pr = ?, on_events = ? WHERE name = ?`,
		w.Repo, branchVal, prVal, string(onEventsJSON), w.Name,
	)
	if err != nil {
		return fmt.Errorf("update watch: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteWatch(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM watches WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete watch: %w", err)
	}
	return nil
}

func (s *SQLiteStore) CreateWorkflow(ctx context.Context, wf *DBWorkflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if wf.ID == "" {
		wf.ID = generateStoreID()
	}
	return s.insertWorkflow(ctx, wf)
}

// insertWorkflow is the internal insert, called with or without the mutex held.
func (s *SQLiteStore) insertWorkflow(ctx context.Context, wf *DBWorkflow) error {
	secretsJSON, err := json.Marshal(wf.Secrets)
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}
	envJSON, err := json.Marshal(wf.Env)
	if err != nil {
		return fmt.Errorf("marshal env: %w", err)
	}
	var runnerImage any
	if wf.RunnerImage != "" {
		runnerImage = wf.RunnerImage
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workflows (id, watch_name, name, path, runner_image, secrets, env) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		wf.ID, wf.WatchName, wf.Name, wf.Path, runnerImage, string(secretsJSON), string(envJSON),
	)
	if err != nil {
		return fmt.Errorf("insert workflow: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateWorkflow(ctx context.Context, wf *DBWorkflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	secretsJSON, err := json.Marshal(wf.Secrets)
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}
	envJSON, err := json.Marshal(wf.Env)
	if err != nil {
		return fmt.Errorf("marshal env: %w", err)
	}
	var runnerImage any
	if wf.RunnerImage != "" {
		runnerImage = wf.RunnerImage
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE workflows SET path = ?, runner_image = ?, secrets = ?, env = ? WHERE watch_name = ? AND name = ?`,
		wf.Path, runnerImage, string(secretsJSON), string(envJSON), wf.WatchName, wf.Name,
	)
	if err != nil {
		return fmt.Errorf("update workflow: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteWorkflow(ctx context.Context, watchName, workflowName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM workflows WHERE watch_name = ? AND name = ?`, watchName, workflowName)
	if err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}
	return nil
}

// ---- Scanners ----

// scanner is implemented by both *sql.Row and *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanRun(s scanner) (*Run, error) {
	var r Run
	var status string
	var startedAt string
	var finishedAt sql.NullString
	var exitCode sql.NullInt64

	err := s.Scan(&r.ID, &r.WatchName, &r.Repo, &r.SHA, &r.WorkflowName,
		&status, &startedAt, &finishedAt, &exitCode, &r.LogPath)
	if err != nil {
		return nil, fmt.Errorf("scan run: %w", err)
	}

	r.Status = RunStatus(status)

	r.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		// fallback for other datetime formats SQLite may return
		r.StartedAt, err = time.Parse("2006-01-02T15:04:05Z", startedAt)
		if err != nil {
			return nil, fmt.Errorf("parse started_at: %w", err)
		}
	}

	if finishedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05Z", finishedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse finished_at: %w", err)
			}
		}
		r.FinishedAt = &t
	}

	if exitCode.Valid {
		v := int(exitCode.Int64)
		r.ExitCode = &v
	}

	return &r, nil
}

func scanUser(s scanner) (*User, error) {
	var u User
	var createdAt string
	err := s.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05", createdAt)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05Z", createdAt)
			if err != nil {
				// best effort
				t = time.Time{}
			}
		}
	}
	u.CreatedAt = t
	return &u, nil
}

func scanWatch(s scanner) (*DBWatch, error) {
	var w DBWatch
	var onEventsJSON string
	err := s.Scan(&w.Name, &w.Repo, &w.Branch, &w.PR, &onEventsJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("watch not found")
	}
	if err != nil {
		return nil, fmt.Errorf("scan watch: %w", err)
	}
	if err := json.Unmarshal([]byte(onEventsJSON), &w.OnEvents); err != nil {
		w.OnEvents = []string{"push"}
	}
	w.Workflows = []*DBWorkflow{}
	return &w, nil
}

func scanWorkflow(s scanner) (*DBWorkflow, error) {
	var wf DBWorkflow
	var secretsJSON, envJSON string
	err := s.Scan(&wf.ID, &wf.WatchName, &wf.Name, &wf.Path, &wf.RunnerImage, &secretsJSON, &envJSON)
	if err != nil {
		return nil, fmt.Errorf("scan workflow: %w", err)
	}
	if err := json.Unmarshal([]byte(secretsJSON), &wf.Secrets); err != nil {
		wf.Secrets = map[string]string{}
	}
	if err := json.Unmarshal([]byte(envJSON), &wf.Env); err != nil {
		wf.Env = map[string]string{}
	}
	return &wf, nil
}

// ---- Helpers ----

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func generateStoreID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
