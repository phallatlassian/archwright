CREATE TABLE gate_runs (
    handback_id INTEGER PRIMARY KEY REFERENCES handbacks(id),
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    status      TEXT NOT NULL CHECK (status IN ('running', 'passed', 'failed', 'not_run')),
    branch      TEXT NOT NULL DEFAULT '',
    sha         TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    results     TEXT NOT NULL DEFAULT '[]',
    started_at  TEXT NOT NULL,
    finished_at TEXT
);

CREATE TABLE gate_changes (
    id         TEXT PRIMARY KEY,
    site       TEXT NOT NULL REFERENCES sites(name),
    gates      TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    created_at TEXT NOT NULL,
    decided_at TEXT
);
