CREATE TABLE sites (
    name            TEXT PRIMARY KEY,
    path            TEXT NOT NULL UNIQUE,
    base_branch     TEXT NOT NULL,
    remote          TEXT NOT NULL DEFAULT '',
    concurrency_cap INTEGER NOT NULL,
    gates           TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

ALTER TABLE jobs ADD COLUMN site TEXT REFERENCES sites(name);
ALTER TABLE jobs ADD COLUMN outcome TEXT;

CREATE TABLE worktrees (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    site       TEXT NOT NULL REFERENCES sites(name),
    kind       TEXT NOT NULL CHECK (kind IN ('integration', 'wright', 'readonly')),
    path       TEXT NOT NULL,
    branch     TEXT NOT NULL DEFAULT '',
    base_ref   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    removed_at TEXT
);

CREATE UNIQUE INDEX worktrees_one_integration ON worktrees(job_id) WHERE kind = 'integration';
