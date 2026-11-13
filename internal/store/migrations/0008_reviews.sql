-- Review is a separate read-only task after Masonry's handback (ADR 0012).
-- A Job may link a writ, the ticket that authorised its work; the Archwright's
-- PR go-ahead is recorded once a review passes.
ALTER TABLE jobs ADD COLUMN writ TEXT;
ALTER TABLE jobs ADD COLUMN pr_go_ahead_at TEXT;

CREATE TABLE reviews (
    id                 TEXT PRIMARY KEY,
    job_id             TEXT NOT NULL REFERENCES jobs(id),
    seq                INTEGER NOT NULL,
    handback_id        INTEGER NOT NULL REFERENCES handbacks(id),
    branch             TEXT NOT NULL,
    sha                TEXT NOT NULL,
    run_id             TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL CHECK (status IN ('running', 'reported', 'errored')),
    spec_basis         TEXT NOT NULL CHECK (spec_basis IN ('writ', 'plan-and-request', 'none')),
    brief              TEXT NOT NULL,
    overall            TEXT NOT NULL DEFAULT '',
    standards          TEXT NOT NULL DEFAULT '',
    spec               TEXT NOT NULL DEFAULT '',
    reason             TEXT NOT NULL DEFAULT '',
    rework_constraints TEXT NOT NULL DEFAULT '',
    rework_floor       INTEGER NOT NULL DEFAULT 0,
    reworked_at        TEXT,
    created_at         TEXT NOT NULL,
    finished_at        TEXT,
    UNIQUE (job_id, seq)
);
