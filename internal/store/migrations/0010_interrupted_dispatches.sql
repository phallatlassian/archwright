-- A dispatch whose run a stopped Works left in flight is 'interrupted'; a fresh
-- dispatch from its stored brief takes over (ADR 0002).
CREATE TABLE dispatches_new (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    piece      TEXT NOT NULL,
    guild      TEXT NOT NULL,
    round      INTEGER NOT NULL,
    run_id     TEXT NOT NULL DEFAULT '',
    brief      TEXT NOT NULL,
    rendered   TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('queued', 'running', 'blocked', 'answered', 'interrupted', 'reported', 'rejected')),
    result     TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (job_id, piece) REFERENCES pieces(job_id, piece),
    UNIQUE (job_id, piece, round)
);
INSERT INTO dispatches_new SELECT id, job_id, piece, guild, round, run_id, brief, rendered, status, result, created_at FROM dispatches ORDER BY rowid;
DROP TABLE dispatches;
ALTER TABLE dispatches_new RENAME TO dispatches;
