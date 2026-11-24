-- Where a run's interactive Session lives and what it is doing (ADR 0026).
-- The pane id is canonical: the Works finds a Session again by it, and the
-- state comes from the vendor's hooks. Runs from before, and runs with no
-- Session, have none: NULL.
ALTER TABLE run_records ADD COLUMN pane_id TEXT;
ALTER TABLE run_records ADD COLUMN session_id TEXT;
ALTER TABLE run_records ADD COLUMN transcript_path TEXT;
ALTER TABLE run_records ADD COLUMN session_state TEXT;
