CREATE TABLE IF NOT EXISTS run_jobs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    job_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    started_at DATETIME,
    finished_at DATETIME,
    UNIQUE(run_id, job_name)
);
CREATE INDEX IF NOT EXISTS idx_run_jobs_run_id ON run_jobs(run_id);
