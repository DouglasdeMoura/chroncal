-- +goose Up
CREATE TABLE sync_conflicts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    owner_type TEXT NOT NULL,      -- 'event', 'todo', 'journal'
    owner_id INTEGER NOT NULL,     -- local item ID
    uid TEXT NOT NULL,
    local_ical TEXT NOT NULL,      -- full iCal data of local version
    server_ical TEXT NOT NULL,     -- full iCal data of server version
    server_etag TEXT NOT NULL,     -- ETag of server version (needed for resolution PUT)
    detected_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- +goose Down
DROP TABLE IF EXISTS sync_conflicts;
