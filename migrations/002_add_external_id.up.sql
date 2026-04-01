ALTER TABLE change_events ADD COLUMN external_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_change_events_external_id ON change_events (external_id) WHERE external_id IS NOT NULL;
