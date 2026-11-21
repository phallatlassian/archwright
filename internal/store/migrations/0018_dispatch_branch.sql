-- The branch a Wright's work was committed to (ADR 0025), so that accepting
-- the piece can merge it into the Job's integration branch. Empty for a
-- dispatch that has no branch of its own: a merge Wright, which commits on
-- the integration branch, and every dispatch before this column.
ALTER TABLE dispatches ADD COLUMN branch TEXT NOT NULL DEFAULT '';
