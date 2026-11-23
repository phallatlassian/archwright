-- The exact prompt a run was started with. A runner gets it as a command-line
-- argument and nothing else keeps it: the dispatch's rendered brief and the
-- step's brief leave out what is added at run start (a reworked round's
-- starting point, a restarted Warden's "Resumed" note). It is a summary, so it
-- is kept when raw run content is pruned (ADR 0017). Runs from before have
-- none: NULL.
ALTER TABLE run_records ADD COLUMN brief TEXT;
