-- The developer's approval credential (ADR 0010): a secret only the developer
-- knows, required to approve a gate change. One row at most; only a salted
-- hash is kept.
CREATE TABLE approval_credential (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    salt       BLOB NOT NULL,
    hash       BLOB NOT NULL,
    iterations INTEGER NOT NULL,
    set_at     TEXT NOT NULL
);
