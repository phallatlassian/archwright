-- A Plan step the Archwright started is carried out by a Warden (ADR 0020).
-- A step with no row is pending; the row is made when start_step runs.
CREATE TABLE steps (
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    step        TEXT NOT NULL,
    guild       TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'handed_back', 'failed')),
    brief       TEXT NOT NULL,
    handback_id INTEGER,
    reason      TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    PRIMARY KEY (job_id, step)
);
