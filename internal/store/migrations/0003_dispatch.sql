ALTER TABLE jobs ADD COLUMN parked_reason TEXT;

CREATE TABLE pieces (
    job_id    TEXT NOT NULL REFERENCES jobs(id),
    piece     TEXT NOT NULL,
    guild     TEXT NOT NULL,
    status    TEXT NOT NULL CHECK (status IN ('open', 'accepted', 'failed')),
    reworks   INTEGER NOT NULL DEFAULT 0,
    reason    TEXT NOT NULL DEFAULT '',
    diagnosis TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (job_id, piece)
);

CREATE TABLE dispatches (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    piece      TEXT NOT NULL,
    guild      TEXT NOT NULL,
    round      INTEGER NOT NULL,
    run_id     TEXT NOT NULL DEFAULT '',
    brief      TEXT NOT NULL,
    rendered   TEXT NOT NULL,
    status     TEXT NOT NULL CHECK (status IN ('running', 'reported', 'rejected')),
    result     TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (job_id, piece) REFERENCES pieces(job_id, piece),
    UNIQUE (job_id, piece, round)
);

CREATE TABLE handbacks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    status     TEXT NOT NULL,
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE flags (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    handback_id INTEGER NOT NULL REFERENCES handbacks(id),
    kind        TEXT NOT NULL CHECK (kind IN ('follow-up', 'decision-needed', 'risk')),
    description TEXT NOT NULL,
    evidence    TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
