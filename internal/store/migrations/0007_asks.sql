-- A dispatch can now be 'blocked' on an ask (its run ended, its piece parks) or
-- 'answered' once the ask is answered and a fresh dispatch took over (ADR 0015).
CREATE TABLE dispatches_new (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    piece      TEXT NOT NULL,
    guild      TEXT NOT NULL,
    round      INTEGER NOT NULL,
    run_id     TEXT NOT NULL DEFAULT '',
    brief      TEXT NOT NULL,
    rendered   TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('queued', 'running', 'blocked', 'answered', 'reported', 'rejected')),
    result     TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (job_id, piece) REFERENCES pieces(job_id, piece),
    UNIQUE (job_id, piece, round)
);
INSERT INTO dispatches_new SELECT id, job_id, piece, guild, round, run_id, brief, rendered, status, result, created_at FROM dispatches ORDER BY rowid;
DROP TABLE dispatches;
ALTER TABLE dispatches_new RENAME TO dispatches;

-- Asks: structured questions an agent raises, routed up the layers by the Works.
CREATE TABLE asks (
    id          TEXT PRIMARY KEY,
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    asker       TEXT NOT NULL CHECK (asker IN ('wright', 'warden')),
    guild       TEXT NOT NULL DEFAULT '',
    piece       TEXT NOT NULL DEFAULT '',
    dispatch_id TEXT NOT NULL DEFAULT '',
    parent_id   TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL CHECK (kind IN ('decision', 'manual-step', 'clarification')),
    blocking    INTEGER NOT NULL,
    why         TEXT NOT NULL DEFAULT '',
    questions   TEXT NOT NULL,
    holder      TEXT NOT NULL CHECK (holder IN ('warden', 'archwright')),
    status      TEXT NOT NULL CHECK (status IN ('open', 'escalated', 'answered', 'cancelled')),
    answers     TEXT NOT NULL DEFAULT '[]',
    answered_by TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    answered_at TEXT
);
CREATE INDEX asks_by_job ON asks (job_id, status);
