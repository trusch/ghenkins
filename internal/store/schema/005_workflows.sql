CREATE TABLE IF NOT EXISTS workflows (
    id           TEXT PRIMARY KEY,
    watch_name   TEXT NOT NULL REFERENCES watches(name) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    path         TEXT NOT NULL,
    runner_image TEXT,
    secrets      TEXT NOT NULL DEFAULT '{}',
    env          TEXT NOT NULL DEFAULT '{}',
    UNIQUE(watch_name, name)
);
