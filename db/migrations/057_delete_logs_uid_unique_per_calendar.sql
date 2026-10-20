-- +goose Up
-- Two calendars can hold a series with the same UID (migration 050, issue
-- #842). The delete logs kept one row per (uid, recurrence_id) and per
-- (uid, cutoff_time), so a delete of the same occurrence in two calendars
-- kept the log row of the first calendar only. Scope the keys to the
-- calendar. SQLite cannot drop a table UNIQUE constraint, so rebuild both
-- tables. No table references them, so a plain copy keeps every row.

CREATE TABLE event_exdate_deletes_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    recurrence_id TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (calendar_id, uid, recurrence_id)
);
INSERT INTO event_exdate_deletes_new (id, calendar_id, uid, recurrence_id, deleted_at)
SELECT id, calendar_id, uid, recurrence_id, deleted_at FROM event_exdate_deletes;
DROP INDEX IF EXISTS idx_event_exdate_deletes_calendar;
DROP INDEX IF EXISTS idx_event_exdate_deletes_deleted_at;
DROP TABLE event_exdate_deletes;
ALTER TABLE event_exdate_deletes_new RENAME TO event_exdate_deletes;
CREATE INDEX idx_event_exdate_deletes_calendar ON event_exdate_deletes(calendar_id);
CREATE INDEX idx_event_exdate_deletes_deleted_at ON event_exdate_deletes(deleted_at);

CREATE TABLE event_truncate_deletes_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    cutoff_time TEXT NOT NULL,
    previous_rrule TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    hidden_overrides TEXT,
    removed_rdates TEXT,
    UNIQUE (calendar_id, uid, cutoff_time)
);
INSERT INTO event_truncate_deletes_new (id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates)
SELECT id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates FROM event_truncate_deletes;
DROP INDEX IF EXISTS idx_event_truncate_deletes_calendar;
DROP INDEX IF EXISTS idx_event_truncate_deletes_deleted_at;
DROP TABLE event_truncate_deletes;
ALTER TABLE event_truncate_deletes_new RENAME TO event_truncate_deletes;
CREATE INDEX idx_event_truncate_deletes_calendar ON event_truncate_deletes(calendar_id);
CREATE INDEX idx_event_truncate_deletes_deleted_at ON event_truncate_deletes(deleted_at);

-- +goose Down
-- The old keys hold one row per UID. A second calendar's row of the same
-- occurrence or cutoff does not fit, so the copy keeps the first row.
CREATE TABLE event_exdate_deletes_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    recurrence_id TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (uid, recurrence_id)
);
INSERT OR IGNORE INTO event_exdate_deletes_old (id, calendar_id, uid, recurrence_id, deleted_at)
SELECT id, calendar_id, uid, recurrence_id, deleted_at FROM event_exdate_deletes ORDER BY id;
DROP INDEX IF EXISTS idx_event_exdate_deletes_calendar;
DROP INDEX IF EXISTS idx_event_exdate_deletes_deleted_at;
DROP TABLE event_exdate_deletes;
ALTER TABLE event_exdate_deletes_old RENAME TO event_exdate_deletes;
CREATE INDEX idx_event_exdate_deletes_calendar ON event_exdate_deletes(calendar_id);
CREATE INDEX idx_event_exdate_deletes_deleted_at ON event_exdate_deletes(deleted_at);

CREATE TABLE event_truncate_deletes_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    cutoff_time TEXT NOT NULL,
    previous_rrule TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    hidden_overrides TEXT,
    removed_rdates TEXT,
    UNIQUE (uid, cutoff_time)
);
INSERT OR IGNORE INTO event_truncate_deletes_old (id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates)
SELECT id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates FROM event_truncate_deletes ORDER BY id;
DROP INDEX IF EXISTS idx_event_truncate_deletes_calendar;
DROP INDEX IF EXISTS idx_event_truncate_deletes_deleted_at;
DROP TABLE event_truncate_deletes;
ALTER TABLE event_truncate_deletes_old RENAME TO event_truncate_deletes;
CREATE INDEX idx_event_truncate_deletes_calendar ON event_truncate_deletes(calendar_id);
CREATE INDEX idx_event_truncate_deletes_deleted_at ON event_truncate_deletes(deleted_at);
