DROP INDEX IF EXISTS idx_change_events_external_id;
-- SQLite does not support DROP COLUMN before 3.35.0; column left in place on downgrade.
