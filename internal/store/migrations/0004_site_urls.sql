-- Sites are registered by remote URL and the Works keeps its own clone
-- (ADR 0019). Existing path-registered Sites are not migrated: their rows
-- stay so Job and worktree history keeps its Site name, but they are marked
-- unregistered (unregistered_at set, no URL) and resolve to nothing until the
-- developer re-registers them by URL. `path` was UNIQUE NOT NULL and cannot
-- be dropped, so it becomes `clone_path`; an unregistered row's value is a
-- placeholder that is never a filesystem path.
ALTER TABLE sites RENAME COLUMN path TO clone_path;
ALTER TABLE sites DROP COLUMN remote;
ALTER TABLE sites ADD COLUMN url TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN url_key TEXT;
ALTER TABLE sites ADD COLUMN unregistered_at TEXT;

UPDATE sites SET clone_path = 'unregistered:' || name,
                 unregistered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

CREATE UNIQUE INDEX sites_url_key ON sites(url_key);
