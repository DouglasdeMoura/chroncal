-- +goose Up

-- Resource-level sync tracking. One row per iCal resource (UID) per calendar.
-- A single resource may map to multiple local rows (master + overrides).
CREATE TABLE sync_resources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,              -- iCal UID
    owner_type TEXT NOT NULL        -- 'event', 'todo', 'journal'
        CHECK(owner_type IN ('event', 'todo', 'journal')),
    remote_url TEXT NOT NULL DEFAULT '',  -- CalDAV href for this resource
    etag TEXT NOT NULL DEFAULT '',        -- server ETag for change detection
    dirty INTEGER NOT NULL DEFAULT 0
        CHECK(dirty IN (0, 1)),    -- 1 = locally modified, needs push
    sync_strategy TEXT NOT NULL DEFAULT 'sync-token'
        CHECK(sync_strategy IN ('sync-token', 'ctag-etag')),  -- 'sync-token' or 'ctag-etag'
    UNIQUE(calendar_id, uid)
);

CREATE INDEX idx_sync_resources_dirty ON sync_resources(calendar_id, dirty) WHERE dirty = 1;

-- Tombstones track deletions of synced items so the sync engine can
-- send DELETEs to the server.
CREATE TABLE tombstones (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    remote_url TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX idx_tombstones_calendar ON tombstones(calendar_id);

-- +goose Down
DROP TABLE IF EXISTS tombstones;
DROP TABLE IF EXISTS sync_resources;
