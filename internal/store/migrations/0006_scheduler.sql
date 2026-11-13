-- A dispatch now waits as 'queued' until the scheduler has room for it (ADR 0008).
CREATE TABLE dispatches_new (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    piece      TEXT NOT NULL,
    guild      TEXT NOT NULL,
    round      INTEGER NOT NULL,
    run_id     TEXT NOT NULL DEFAULT '',
    brief      TEXT NOT NULL,
    rendered   TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('queued', 'running', 'reported', 'rejected')),
    result     TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (job_id, piece) REFERENCES pieces(job_id, piece),
    UNIQUE (job_id, piece, round)
);
INSERT INTO dispatches_new SELECT id, job_id, piece, guild, round, run_id, brief, rendered, status, result, created_at FROM dispatches ORDER BY rowid;
DROP TABLE dispatches;
ALTER TABLE dispatches_new RENAME TO dispatches;

-- The Needs-you inbox: items that want the developer's eye without blocking work.
CREATE TABLE needs_you (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    site       TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '{}',
    dedupe_key TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);
