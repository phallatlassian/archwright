-- Retention (ADR 0017): when a Job ended, so raw run content can be pruned a
-- set time after it.
ALTER TABLE jobs ADD COLUMN ended_at TEXT;

UPDATE jobs SET ended_at = (
    SELECT at FROM events
    WHERE events.job_id = jobs.id AND events.type IN ('job.accepted', 'job.discarded')
    ORDER BY events.id DESC LIMIT 1
) WHERE outcome IS NOT NULL;
UPDATE jobs SET ended_at = created_at WHERE outcome IS NOT NULL AND ended_at IS NULL;
