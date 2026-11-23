-- What a run was for (dashboard waterfall): a Warden's run records the Plan
-- step it carries out, a Wright's run the Warden run that dispatched it. The
-- dispatch keeps that Warden run so the Wright's run, which the scheduler
-- starts later, can say it. Runs and dispatches from before have none: NULL,
-- read back as the empty string.
ALTER TABLE run_records ADD COLUMN step TEXT;
ALTER TABLE run_records ADD COLUMN parent_run TEXT;
ALTER TABLE dispatches ADD COLUMN warden_run TEXT;
