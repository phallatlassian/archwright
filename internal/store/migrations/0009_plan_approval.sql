-- A Plan instantiated from a Jig starts without approval; an ad hoc Plan waits
-- for the developer's go or no-go before any work is dispatched for it.
ALTER TABLE plans ADD COLUMN jig TEXT NOT NULL DEFAULT '';
ALTER TABLE plans ADD COLUMN approval TEXT NOT NULL DEFAULT 'pending'
    CHECK (approval IN ('not_required', 'pending', 'approved', 'rejected'));
-- Plans that existed before the gate keep running.
UPDATE plans SET approval = 'approved';
