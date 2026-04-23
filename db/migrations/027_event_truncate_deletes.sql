-- +goose Up
-- "This and following" deletes set an UNTIL on the master's RRULE and
-- soft-delete any overrides at/after the cutoff. This log records the
-- pre-truncation RRULE so the trash view can restore it in one step.
CREATE TABLE event_truncate_deletes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    cutoff_time TEXT NOT NULL,
    previous_rrule TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (uid, cutoff_time)
);
CREATE INDEX idx_event_truncate_deletes_calendar ON event_truncate_deletes(calendar_id);
CREATE INDEX idx_event_truncate_deletes_deleted_at ON event_truncate_deletes(deleted_at);

-- +goose Down
DROP INDEX IF EXISTS idx_event_truncate_deletes_deleted_at;
DROP INDEX IF EXISTS idx_event_truncate_deletes_calendar;
DROP TABLE IF EXISTS event_truncate_deletes;
