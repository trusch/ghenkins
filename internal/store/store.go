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

	"github.com/google/uuid"
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

type RunJobStatus string

const (
	RunJobStatusPending RunJobStatus = "pending"
	RunJobStatusRunning RunJobStatus = "running"
	RunJobStatusSuccess RunJobStatus = "success"
	RunJobStatusFailure RunJobStatus = "failure"
	RunJobStatusSkipped RunJobStatus = "skipped"
)

type RunJob struct {
	ID         string       `json:"id"`
	RunID      string       `json:"run_id"`
	JobName    string       `json:"job_name"`
	Status     RunJobStatus `json:"status"`
	StartedAt  *time.Time   `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at"`
}

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

// DBProject is the new canonical project type, replacing DBWatch.
type DBProject struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Workflows   []*DBWorkflow `json:"workflows"`
	Triggers    []*DBTrigger  `json:"triggers"`
}

// DBTrigger represents a repo/event trigger attached to a project.
type DBTrigger struct {
	ID          string   `json:"id"`
	ProjectName string   `json:"project_name"`
	Type        string   `json:"type"` // "push", "pull_request", "manual"
	Repo        string   `json:"repo"`
	Branch      string   `json:"branch,omitempty"`
	PR          int      `json:"pr,omitempty"`
	OnEvents    []string `json:"on_events"`
}

// DBWatch is the deprecated legacy type. Use DBProject instead.
// Populated from DBProject + first push trigger for backward compatibility.
type DBWatch struct {
	Name      string       `json:"name"`
	Repo      string       `json:"repo"`
	Branch    string       `json:"branch,omitempty"`
	PR        int          `json:"pr,omitempty"`
	OnEvents  []string     `json:"on_events"`
	Workflows []*DBWorkflow `json:"workflows"`
}

type DBWorkflow struct {
	ID             string            `json:"id"`
	ProjectName    string            `json:"project_name"`
	Name           string            `json:"name"`
	Path           string            `json:"path"`
	RunnerImage    string            `json:"runner_image,omitempty"`
	Secrets        map[string]string `json:"secrets"`
	Env            map[string]string `json:"env"`
	TimeoutMinutes int               `json:"timeout_minutes,omitempty"`
}

// WatchName returns ProjectName for backward compatibility.
// Deprecated: use ProjectName directly.
func (wf *DBWorkflow) WatchNameCompat() string { return wf.ProjectName }

type RunArtifact struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

type Store interface {
	CreateRun(ctx context.Context, r *Run) error
	UpdateRunStatus(ctx context.Context, id string, status RunStatus, exitCode *int, finishedAt *time.Time) error
	UpdateRunSHA(ctx context.Context, id, sha string) error
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

	// Projects (replaces Watches)
	ListProjects(ctx context.Context) ([]*DBProject, error)
	GetProject(ctx context.Context, name string) (*DBProject, error)
	CreateProject(ctx context.Context, p *DBProject) error
	UpdateProject(ctx context.Context, p *DBProject) error
	DeleteProject(ctx context.Context, name string) error

	// Triggers
	CreateTrigger(ctx context.Context, t *DBTrigger) error
	UpdateTrigger(ctx context.Context, t *DBTrigger) error
	DeleteTrigger(ctx context.Context, triggerID string) error

	// Workflows
	CreateWorkflow(ctx context.Context, wf *DBWorkflow) error
	UpdateWorkflow(ctx context.Context, wf *DBWorkflow) error
	DeleteWorkflow(ctx context.Context, projectName, workflowName string) error

	// Deprecated: use ListProjects
	ListWatches(ctx context.Context) ([]*DBWatch, error)
	// Deprecated: use GetProject
	GetWatch(ctx context.Context, name string) (*DBWatch, error)
	// Deprecated: use CreateProject
	CreateWatch(ctx context.Context, w *DBWatch) error
	// Deprecated: use UpdateProject
	UpdateWatch(ctx context.Context, w *DBWatch) error
	// Deprecated: use DeleteProject
	DeleteWatch(ctx context.Context, name string) error

	// RunJobs
	UpsertRunJob(ctx context.Context, job *RunJob) error
	ListRunJobs(ctx context.Context, runID string) ([]*RunJob, error)

	// RunArtifacts
	UpsertArtifact(ctx context.Context, a RunArtifact) error
	ListRunArtifacts(ctx context.Context, runID string) ([]*RunArtifact, error)

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
		`INSERT INTO runs (id, project_name, repo, sha, workflow_name, status, started_at, finished_at, exit_code, log_path)
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

func (s *SQLiteStore) UpdateRunSHA(ctx context.Context, id, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET sha = ? WHERE id = ?`, sha, id)
	if err != nil {
		return fmt.Errorf("update run sha: %w", err)
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
		`SELECT id, project_name, repo, sha, workflow_name, status, started_at, finished_at, exit_code, log_path
		 FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *SQLiteStore) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_name, repo, sha, workflow_name, status, started_at, finished_at, exit_code, log_path
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

// ---- Projects ----

func (s *SQLiteStore) ListProjects(ctx context.Context) ([]*DBProject, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, description FROM projects ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []*DBProject
	for rows.Next() {
		p := &DBProject{}
		if err := rows.Scan(&p.Name, &p.Description); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, p := range projects {
		if err := s.loadProjectWorkflows(ctx, p); err != nil {
			return nil, err
		}
		if err := s.loadProjectTriggers(ctx, p); err != nil {
			return nil, err
		}
	}
	return projects, nil
}

func (s *SQLiteStore) GetProject(ctx context.Context, name string) (*DBProject, error) {
	p := &DBProject{}
	err := s.db.QueryRowContext(ctx,
		`SELECT name, description FROM projects WHERE name = ?`, name,
	).Scan(&p.Name, &p.Description)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("project not found")
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	if err := s.loadProjectWorkflows(ctx, p); err != nil {
		return nil, err
	}
	if err := s.loadProjectTriggers(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *SQLiteStore) CreateProject(ctx context.Context, p *DBProject) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (name, description) VALUES (?, ?)`,
		p.Name, p.Description,
	)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}

	for _, wf := range p.Workflows {
		wf.ProjectName = p.Name
		if wf.ID == "" {
			wf.ID = generateStoreID()
		}
		if err := s.insertWorkflow(ctx, wf); err != nil {
			return err
		}
	}

	for _, t := range p.Triggers {
		t.ProjectName = p.Name
		if t.ID == "" {
			t.ID = generateStoreID()
		}
		if err := s.insertTrigger(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) UpdateProject(ctx context.Context, p *DBProject) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET description = ? WHERE name = ?`,
		p.Description, p.Name,
	)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteProject(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}

func (s *SQLiteStore) loadProjectWorkflows(ctx context.Context, p *DBProject) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_name, name, path, COALESCE(runner_image,''), secrets, env, COALESCE(timeout_minutes,0)
		 FROM workflows WHERE project_name = ? ORDER BY name ASC`,
		p.Name)
	if err != nil {
		return fmt.Errorf("load workflows: %w", err)
	}
	defer rows.Close()

	p.Workflows = []*DBWorkflow{}
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return err
		}
		p.Workflows = append(p.Workflows, wf)
	}
	return rows.Err()
}

func (s *SQLiteStore) loadProjectTriggers(ctx context.Context, p *DBProject) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_name, type, COALESCE(repo,''), COALESCE(branch,''), COALESCE(pr,0), on_events
		 FROM project_triggers WHERE project_name = ? ORDER BY id ASC`,
		p.Name)
	if err != nil {
		return fmt.Errorf("load triggers: %w", err)
	}
	defer rows.Close()

	p.Triggers = []*DBTrigger{}
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return err
		}
		p.Triggers = append(p.Triggers, t)
	}
	return rows.Err()
}

// ---- Triggers ----

func (s *SQLiteStore) CreateTrigger(ctx context.Context, t *DBTrigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t.ID == "" {
		t.ID = generateStoreID()
	}
	return s.insertTrigger(ctx, t)
}

func (s *SQLiteStore) insertTrigger(ctx context.Context, t *DBTrigger) error {
	onEventsJSON, err := json.Marshal(t.OnEvents)
	if err != nil {
		return fmt.Errorf("marshal on_events: %w", err)
	}
	var branchVal, repoVal any
	if t.Branch != "" {
		branchVal = t.Branch
	}
	if t.Repo != "" {
		repoVal = t.Repo
	}
	var prVal any
	if t.PR != 0 {
		prVal = t.PR
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO project_triggers (id, project_name, type, repo, branch, pr, on_events)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectName, t.Type, repoVal, branchVal, prVal, string(onEventsJSON),
	)
	if err != nil {
		return fmt.Errorf("insert trigger: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateTrigger(ctx context.Context, t *DBTrigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	onEventsJSON, err := json.Marshal(t.OnEvents)
	if err != nil {
		return fmt.Errorf("marshal on_events: %w", err)
	}
	var branchVal, repoVal any
	if t.Branch != "" {
		branchVal = t.Branch
	}
	if t.Repo != "" {
		repoVal = t.Repo
	}
	var prVal any
	if t.PR != 0 {
		prVal = t.PR
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE project_triggers SET type = ?, repo = ?, branch = ?, pr = ?, on_events = ? WHERE id = ?`,
		t.Type, repoVal, branchVal, prVal, string(onEventsJSON), t.ID,
	)
	if err != nil {
		return fmt.Errorf("update trigger: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteTrigger(ctx context.Context, triggerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM project_triggers WHERE id = ?`, triggerID)
	if err != nil {
		return fmt.Errorf("delete trigger: %w", err)
	}
	return nil
}

// ---- Watches (deprecated — thin wrappers over Project methods) ----

func (s *SQLiteStore) ListWatches(ctx context.Context) ([]*DBWatch, error) {
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	watches := make([]*DBWatch, 0, len(projects))
	for _, p := range projects {
		watches = append(watches, projectToWatch(p))
	}
	return watches, nil
}

func (s *SQLiteStore) GetWatch(ctx context.Context, name string) (*DBWatch, error) {
	p, err := s.GetProject(ctx, name)
	if err != nil {
		return nil, err
	}
	return projectToWatch(p), nil
}

func (s *SQLiteStore) CreateWatch(ctx context.Context, w *DBWatch) error {
	p := watchToProject(w)
	return s.CreateProject(ctx, p)
}

func (s *SQLiteStore) UpdateWatch(ctx context.Context, w *DBWatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Update the first push trigger to reflect new repo/branch/pr/on_events.
	onEventsJSON, err := json.Marshal(w.OnEvents)
	if err != nil {
		return fmt.Errorf("marshal on_events: %w", err)
	}
	var prVal, branchVal any
	if w.PR != 0 {
		prVal = w.PR
	}
	if w.Branch != "" {
		branchVal = w.Branch
	}
	triggerType := "push"
	if w.PR != 0 {
		triggerType = "pull_request"
	}

	// Upsert: update existing trigger if any, else insert.
	var triggerID string
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM project_triggers WHERE project_name = ? LIMIT 1`, w.Name,
	).Scan(&triggerID)
	if err == sql.ErrNoRows {
		triggerID = generateStoreID()
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO project_triggers (id, project_name, type, repo, branch, pr, on_events)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			triggerID, w.Name, triggerType, w.Repo, branchVal, prVal, string(onEventsJSON),
		)
	} else if err == nil {
		_, err = s.db.ExecContext(ctx,
			`UPDATE project_triggers SET type = ?, repo = ?, branch = ?, pr = ?, on_events = ? WHERE id = ?`,
			triggerType, w.Repo, branchVal, prVal, string(onEventsJSON), triggerID,
		)
	}
	if err != nil {
		return fmt.Errorf("update watch trigger: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteWatch(ctx context.Context, name string) error {
	return s.DeleteProject(ctx, name)
}

// projectToWatch builds a DBWatch from a DBProject using the first trigger.
func projectToWatch(p *DBProject) *DBWatch {
	w := &DBWatch{
		Name:      p.Name,
		OnEvents:  []string{"push"},
		Workflows: p.Workflows,
	}
	if w.Workflows == nil {
		w.Workflows = []*DBWorkflow{}
	}
	for _, t := range p.Triggers {
		w.Repo = t.Repo
		w.Branch = t.Branch
		w.PR = t.PR
		if len(t.OnEvents) > 0 {
			w.OnEvents = t.OnEvents
		}
		break // use first trigger only
	}
	return w
}

// watchToProject converts a DBWatch into a DBProject with one trigger.
func watchToProject(w *DBWatch) *DBProject {
	p := &DBProject{
		Name:      w.Name,
		Workflows: w.Workflows,
	}
	if p.Workflows == nil {
		p.Workflows = []*DBWorkflow{}
	}
	onEvents := w.OnEvents
	if len(onEvents) == 0 {
		onEvents = []string{"push"}
	}
	triggerType := "push"
	if w.PR != 0 {
		triggerType = "pull_request"
	}
	if w.Repo != "" {
		p.Triggers = []*DBTrigger{{
			ProjectName: w.Name,
			Type:        triggerType,
			Repo:        w.Repo,
			Branch:      w.Branch,
			PR:          w.PR,
			OnEvents:    onEvents,
		}}
	}
	return p
}

// ---- Workflows ----

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
		`INSERT INTO workflows (id, project_name, name, path, runner_image, secrets, env, timeout_minutes) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		wf.ID, wf.ProjectName, wf.Name, wf.Path, runnerImage, string(secretsJSON), string(envJSON), wf.TimeoutMinutes,
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
		`UPDATE workflows SET path = ?, runner_image = ?, secrets = ?, env = ?, timeout_minutes = ? WHERE project_name = ? AND name = ?`,
		wf.Path, runnerImage, string(secretsJSON), string(envJSON), wf.TimeoutMinutes, wf.ProjectName, wf.Name,
	)
	if err != nil {
		return fmt.Errorf("update workflow: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteWorkflow(ctx context.Context, projectName, workflowName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM workflows WHERE project_name = ? AND name = ?`, projectName, workflowName)
	if err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}
	return nil
}

// ---- RunJobs ----

func (s *SQLiteStore) UpsertRunJob(ctx context.Context, job *RunJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run_jobs (id, run_id, job_name, status, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, job_name) DO UPDATE SET
		   id = excluded.id,
		   status = excluded.status,
		   started_at = excluded.started_at,
		   finished_at = excluded.finished_at`,
		job.ID, job.RunID, job.JobName, string(job.Status),
		nullTime(job.StartedAt), nullTime(job.FinishedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert run job: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListRunJobs(ctx context.Context, runID string) ([]*RunJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, job_name, status, started_at, finished_at
		 FROM run_jobs WHERE run_id = ? ORDER BY rowid ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*RunJob
	for rows.Next() {
		rj, err := scanRunJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, rj)
	}
	return jobs, rows.Err()
}

// ---- RunArtifacts ----

func (s *SQLiteStore) UpsertArtifact(ctx context.Context, a RunArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run_artifacts (id, run_id, filename, size, sha256, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, filename) DO UPDATE SET
		   id = excluded.id,
		   size = excluded.size,
		   sha256 = excluded.sha256,
		   created_at = excluded.created_at`,
		a.ID, a.RunID, a.Filename, a.Size, a.SHA256,
		a.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert artifact: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListRunArtifacts(ctx context.Context, runID string) ([]*RunArtifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, filename, size, sha256, created_at
		 FROM run_artifacts WHERE run_id = ? ORDER BY filename ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []*RunArtifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
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

func scanWorkflow(s scanner) (*DBWorkflow, error) {
	var wf DBWorkflow
	var secretsJSON, envJSON string
	err := s.Scan(&wf.ID, &wf.ProjectName, &wf.Name, &wf.Path, &wf.RunnerImage, &secretsJSON, &envJSON, &wf.TimeoutMinutes)
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

func scanTrigger(s scanner) (*DBTrigger, error) {
	var t DBTrigger
	var onEventsJSON string
	err := s.Scan(&t.ID, &t.ProjectName, &t.Type, &t.Repo, &t.Branch, &t.PR, &onEventsJSON)
	if err != nil {
		return nil, fmt.Errorf("scan trigger: %w", err)
	}
	if err := json.Unmarshal([]byte(onEventsJSON), &t.OnEvents); err != nil {
		t.OnEvents = []string{"push"}
	}
	return &t, nil
}

func scanRunJob(s scanner) (*RunJob, error) {
	var rj RunJob
	var status string
	var startedAt, finishedAt sql.NullString

	err := s.Scan(&rj.ID, &rj.RunID, &rj.JobName, &status, &startedAt, &finishedAt)
	if err != nil {
		return nil, fmt.Errorf("scan run job: %w", err)
	}
	rj.Status = RunJobStatus(status)

	if startedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, startedAt.String)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05Z", startedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse run job started_at: %w", err)
			}
		}
		rj.StartedAt = &t
	}
	if finishedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05Z", finishedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse run job finished_at: %w", err)
			}
		}
		rj.FinishedAt = &t
	}
	return &rj, nil
}

func scanArtifact(s scanner) (*RunArtifact, error) {
	var a RunArtifact
	var createdAt string
	err := s.Scan(&a.ID, &a.RunID, &a.Filename, &a.Size, &a.SHA256, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("scan artifact: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05Z", createdAt)
		if err != nil {
			t = time.Time{}
		}
	}
	a.CreatedAt = t
	return &a, nil
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
