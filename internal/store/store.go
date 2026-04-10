package store

import (
	"context"
	"database/sql"
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

type Store interface {
	CreateRun(ctx context.Context, r *Run) error
	UpdateRunStatus(ctx context.Context, id string, status RunStatus, exitCode *int, finishedAt *time.Time) error
	GetRun(ctx context.Context, id string) (*Run, error)
	ListRuns(ctx context.Context, limit int) ([]Run, error)
	DeleteRun(ctx context.Context, id string) error
	RunExists(ctx context.Context, repo, sha, workflowName string) (bool, error)
	MarkSeen(ctx context.Context, sc SeenCommit) error
	IsSeen(ctx context.Context, repo, sha, prOrBranch string) (bool, error)
	Close() error
}

type SQLiteStore struct {
	db  *sql.DB
	mu  sync.Mutex
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

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
