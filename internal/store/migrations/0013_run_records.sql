-- What the dashboard shows of a run (ADR 0007): usage and cost, and its
-- timeline. The raw vendor event of a timeline entry is raw run content and
-- is pruned with it (ADR 0017); the kind and summary stay.
CREATE TABLE run_records (
    run_id        TEXT PRIMARY KEY,
    job_id        TEXT NOT NULL REFERENCES jobs(id),
    dispatch_id   TEXT NOT NULL DEFAULT '',
    guild         TEXT NOT NULL DEFAULT '',
    runner        TEXT NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL NOT NULL DEFAULT 0,
    status        TEXT NOT NULL DEFAULT 'running',
    started_at    TEXT NOT NULL,
    ended_at      TEXT
);

CREATE TABLE run_events (
    run_id  TEXT NOT NULL REFERENCES run_records(run_id),
    seq     INTEGER NOT NULL,
    kind    TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    raw     TEXT NOT NULL DEFAULT '',
    at      TEXT NOT NULL,
    PRIMARY KEY (run_id, seq)
);
