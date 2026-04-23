-- +goose Up
-- Per-instance deletes for recurring events are recorded via an EXDATE on
-- the master row rather than a separate deleted row. This log lets the
-- trash view surface those deletes so the user can restore a single
-- occurrence (remove the EXDATE) without losing the rest of the series.
CREATE TABLE event_exdate_deletes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    recurrence_id TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (uid, recurrence_id)
);
CREATE INDEX idx_event_exdate_deletes_calendar ON event_exdate_deletes(calendar_id);
CREATE INDEX idx_event_exdate_deletes_deleted_at ON event_exdate_deletes(deleted_at);

-- +goose Down
DROP INDEX IF EXISTS idx_event_exdate_deletes_deleted_at;
DROP INDEX IF EXISTS idx_event_exdate_deletes_calendar;
DROP TABLE IF EXISTS event_exdate_deletes;
