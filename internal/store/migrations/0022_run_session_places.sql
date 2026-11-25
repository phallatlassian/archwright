-- Where a run's interactive Session works, so a restarted Works can resume it
-- in the same directory (ADR 0029). The vendor is the run's runner column.
ALTER TABLE run_records ADD COLUMN session_dir TEXT;
