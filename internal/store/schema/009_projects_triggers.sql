-- Rename watches → projects
ALTER TABLE watches RENAME TO projects;

-- Add description column to projects
ALTER TABLE projects ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- Rename watch_name → project_name in workflows table
-- (SQLite requires recreating the table)
CREATE TABLE workflows_new (
    id              TEXT NOT NULL,
    project_name    TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    path            TEXT NOT NULL,
    runner_image    TEXT,
    secrets         TEXT NOT NULL DEFAULT '{}',
    env             TEXT NOT NULL DEFAULT '{}',
    timeout_minutes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    UNIQUE(project_name, name)
);
INSERT INTO workflows_new SELECT id, watch_name, name, path, runner_image, secrets, env, timeout_minutes FROM workflows;
DROP TABLE workflows;
ALTER TABLE workflows_new RENAME TO workflows;

-- Rename watch_name → project_name in runs table
CREATE TABLE runs_new (
    id            TEXT PRIMARY KEY,
    project_name  TEXT NOT NULL,
    repo          TEXT NOT NULL,
    sha           TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued',
    started_at    DATETIME NOT NULL,
    finished_at   DATETIME,
    exit_code     INTEGER,
    log_path      TEXT NOT NULL DEFAULT ''
);
INSERT INTO runs_new SELECT id, watch_name, repo, sha, workflow_name, status, started_at, finished_at, exit_code, log_path FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;
CREATE INDEX IF NOT EXISTS idx_runs_repo_sha_workflow ON runs(repo, sha, workflow_name);

-- New triggers table
CREATE TABLE IF NOT EXISTS project_triggers (
    id           TEXT PRIMARY KEY,
    project_name TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
    type         TEXT NOT NULL DEFAULT 'push',  -- 'push', 'pull_request', 'manual'
    repo         TEXT,
    branch       TEXT,
    pr           INTEGER,
    on_events    TEXT NOT NULL DEFAULT '["push"]'
);
CREATE INDEX IF NOT EXISTS idx_triggers_project ON project_triggers(project_name);

-- Migrate existing watch trigger data into triggers table
INSERT INTO project_triggers (id, project_name, type, repo, branch, pr, on_events)
SELECT lower(hex(randomblob(16))), name,
    CASE WHEN pr IS NOT NULL AND pr > 0 THEN 'pull_request' ELSE 'push' END,
    repo, branch, pr, on_events
FROM projects
WHERE repo IS NOT NULL AND repo != '';

-- Remove trigger columns from projects (keep only identity + description)
-- SQLite: recreate projects table without repo/branch/pr/on_events
CREATE TABLE projects_new (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO projects_new SELECT name, description, created_at FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;
