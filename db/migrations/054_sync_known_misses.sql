-- +goose Up
--
-- A known miss is an href with a local row whose server body the pull
-- cannot import: the multiget returns 404, or the body does not parse.
-- The engine withholds the sync-token while a known miss is inside its
-- retry budget. After the budget the engine sets gave_up_at, advances the
-- token, and keeps the local row. sync status lists each row with a
-- gave_up_at value. A good fetch of the href deletes the row. See issue #816.
-- etag is the ETag that the sync-collection REPORT listed for the href at
-- the last miss. A given-up row whose href comes back with another ETag
-- names a new version of the object. The pull then deletes the row, so
-- the new version gets a new retry budget. An empty value names no ETag.
-- first_miss_at is the RFC 3339 UTC time of the first miss. The give-up
-- needs the miss count and a minimum time from first_miss_at, so a frequent
-- background sync cannot spend the budget on one transient miss.
-- UNIQUE(calendar_id, href) already indexes calendar_id.

CREATE TABLE sync_known_misses (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    href TEXT NOT NULL,
    uid TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL CHECK(reason IN ('missing', 'unparseable')),
    miss_count INTEGER NOT NULL DEFAULT 1,
    gave_up_at TEXT,
    etag TEXT NOT NULL DEFAULT '',
    first_miss_at TEXT NOT NULL DEFAULT '',
    UNIQUE(calendar_id, href)
);

-- +goose Down
DROP TABLE IF EXISTS sync_known_misses;
