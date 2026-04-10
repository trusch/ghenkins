CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY,
    watch_name    TEXT NOT NULL,
    repo          TEXT NOT NULL,
    sha           TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued',
    started_at    DATETIME NOT NULL,
    finished_at   DATETIME,
    exit_code     INTEGER,
    log_path      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS seen_commits (
    repo          TEXT NOT NULL,
    sha           TEXT NOT NULL,
    pr_or_branch  TEXT NOT NULL,
    first_seen_at DATETIME NOT NULL,
    PRIMARY KEY (repo, sha, pr_or_branch)
);

CREATE INDEX IF NOT EXISTS idx_runs_repo_sha_workflow ON runs(repo, sha, workflow_name);
CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);
