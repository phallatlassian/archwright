-- What the trust probe a Site's registration runs found, per runner: whether a
-- Session of that runner started in a worktree of the Site, which needs the
-- developer's folder trust (and, for Codex, its hook trust) and shows the
-- runner's hooks reach the Works (docs/spec/tmux-runtime.md).
CREATE TABLE site_trust (
    site       TEXT NOT NULL REFERENCES sites(name),
    runner     TEXT NOT NULL,
    state      TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    checked_at TEXT NOT NULL,
    PRIMARY KEY (site, runner)
);

-- A probe's Needs-you item belongs to a Site, not a Job, so job_id may be
-- empty and no longer references jobs: the table is rebuilt without the key.
CREATE TABLE needs_you_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT NOT NULL,
    job_id     TEXT NOT NULL DEFAULT '',
    site       TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '{}',
    dedupe_key TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);
INSERT INTO needs_you_new (id, kind, job_id, site, summary, detail, dedupe_key, created_at)
    SELECT id, kind, job_id, site, summary, detail, dedupe_key, created_at FROM needs_you;
DROP TABLE needs_you;
ALTER TABLE needs_you_new RENAME TO needs_you;
