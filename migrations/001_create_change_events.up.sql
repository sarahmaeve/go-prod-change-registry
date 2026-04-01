CREATE TABLE IF NOT EXISTS change_events (
    id               TEXT PRIMARY KEY,
    user_name        TEXT NOT NULL,
    timestamp_start  TEXT NOT NULL,
    timestamp_end    TEXT,
    event_type       TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    long_description TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_change_events_timestamp_start ON change_events (timestamp_start);
CREATE INDEX IF NOT EXISTS idx_change_events_event_type ON change_events (event_type);
CREATE INDEX IF NOT EXISTS idx_change_events_user_name ON change_events (user_name);

CREATE TABLE IF NOT EXISTS change_event_tags (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT    NOT NULL REFERENCES change_events(id) ON DELETE CASCADE,
    key      TEXT    NOT NULL,
    value    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_change_event_tags_key_value ON change_event_tags (key, value);
CREATE INDEX IF NOT EXISTS idx_change_event_tags_event_id ON change_event_tags (event_id);
