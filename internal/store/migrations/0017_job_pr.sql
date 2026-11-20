-- A Job may record the URL of the pull request opened for it (ADR 0022): the
-- Forge push tool is allowed while a Job's PR is open. Existing Jobs have
-- none: NULL, read back as the empty string.
ALTER TABLE jobs ADD COLUMN pr_url TEXT;
