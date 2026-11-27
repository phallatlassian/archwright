-- A Warden may work in an empty worktree of the Site's main checkout: a
-- --no-checkout worktree with an empty index and tree (ADR 0030).
CREATE TABLE worktrees_new (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    site       TEXT NOT NULL REFERENCES sites(name),
    kind       TEXT NOT NULL CHECK (kind IN ('integration', 'wright', 'readonly', 'empty')),
    path       TEXT NOT NULL,
    branch     TEXT NOT NULL DEFAULT '',
    base_ref   TEXT NOT NULL,
    base_sha   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    removed_at TEXT
);
INSERT INTO worktrees_new SELECT id, job_id, site, kind, path, branch, base_ref, base_sha, created_at, removed_at FROM worktrees;
DROP TABLE worktrees;
ALTER TABLE worktrees_new RENAME TO worktrees;
CREATE UNIQUE INDEX worktrees_one_integration ON worktrees(job_id) WHERE kind = 'integration';
