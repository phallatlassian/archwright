CREATE TABLE jobs (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE TABLE plans (
    job_id     TEXT PRIMARY KEY REFERENCES jobs(id),
    steps      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      TEXT NOT NULL,
    job_id  TEXT,
    type    TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}'
);

CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events is append-only'); END;

CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events is append-only'); END;
