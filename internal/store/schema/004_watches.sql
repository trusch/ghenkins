CREATE TABLE IF NOT EXISTS watches (
    name       TEXT PRIMARY KEY,
    repo       TEXT NOT NULL,
    branch     TEXT,
    pr         INTEGER,
    on_events  TEXT NOT NULL DEFAULT '["push"]',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
