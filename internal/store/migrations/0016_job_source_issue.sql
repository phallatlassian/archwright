-- A Job may record the Forge issue it is for (ADR 0022). Existing Jobs have
-- none: NULL, read back as the empty string.
ALTER TABLE jobs ADD COLUMN source_issue TEXT;
