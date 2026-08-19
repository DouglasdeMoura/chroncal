-- +goose Up
--
-- Pending hrefs are unknown paths that sync-collection listed and
-- calendar-multiget then 404'd. Google can list a stale invitation href
-- on every initial snapshot. The engine stores the href, advances the
-- token, and retries the fetch on later pulls. After a miss budget the
-- engine drops the row and treats the absence as stable. See issue #576.
-- UNIQUE(calendar_id, href) already indexes calendar_id.

CREATE TABLE sync_pending_hrefs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    href TEXT NOT NULL,
    miss_count INTEGER NOT NULL DEFAULT 1,
    UNIQUE(calendar_id, href)
);

-- +goose Down
DROP TABLE IF EXISTS sync_pending_hrefs;
