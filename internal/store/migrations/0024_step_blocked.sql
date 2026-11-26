-- A Warden's blocking ask parks its step: the step is 'blocked' until the ask
-- is answered (ADR 0015, ADR 0029). The ask records the step it parked.
CREATE TABLE steps_new (
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    step        TEXT NOT NULL,
    guild       TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('pending', 'running', 'blocked', 'handed_back', 'failed')),
    brief       TEXT NOT NULL,
    handback_id INTEGER,
    reason      TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    PRIMARY KEY (job_id, step)
);
INSERT INTO steps_new SELECT job_id, step, guild, status, brief, handback_id, reason, started_at, finished_at FROM steps;
DROP TABLE steps;
ALTER TABLE steps_new RENAME TO steps;

ALTER TABLE asks ADD COLUMN step TEXT NOT NULL DEFAULT '';
