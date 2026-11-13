-- Dashboard commands (ADR 0007): a Job can be paused and its raw content
-- purged; the Archwright's inbox holds the judgment commands it must act on and
-- reports of the mechanical ones the Works already executed.
ALTER TABLE jobs ADD COLUMN paused_at TEXT;
ALTER TABLE jobs ADD COLUMN purged_at TEXT;

CREATE TABLE inbox (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL CHECK (kind IN ('command', 'report')),
    name       TEXT NOT NULL,
    job_id     TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL,
    payload    TEXT NOT NULL DEFAULT '{}',
    event_id   INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    read_at    TEXT
);

-- Deleting a Job removes its events too; the append-only guard lets that one
-- delete through, and only for the Job named here for the length of the delete.
CREATE TABLE event_deletions (job_id TEXT PRIMARY KEY);

DROP TRIGGER events_no_delete;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events
WHEN NOT EXISTS (SELECT 1 FROM event_deletions WHERE job_id = OLD.job_id)
BEGIN SELECT RAISE(ABORT, 'events is append-only'); END;
