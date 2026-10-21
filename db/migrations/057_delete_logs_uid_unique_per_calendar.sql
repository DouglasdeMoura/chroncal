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
    hidden_ids TEXT,
    UNIQUE (calendar_id, uid, cutoff_time)
);
-- hidden_ids lists the override rows that the truncation hid, by row ID
-- (comma-separated). A restore un-hides exactly those rows, so a change
-- of the series owner after the truncation does not strand a row. A row
-- from before this migration has NULL: its restore reads hidden_overrides
-- (recurrence IDs) through the series rule.
INSERT INTO event_truncate_deletes_new (id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates)
SELECT id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates FROM event_truncate_deletes;
DROP INDEX IF EXISTS idx_event_truncate_deletes_calendar;
DROP INDEX IF EXISTS idx_event_truncate_deletes_deleted_at;
DROP TABLE event_truncate_deletes;
ALTER TABLE event_truncate_deletes_new RENAME TO event_truncate_deletes;
CREATE INDEX idx_event_truncate_deletes_calendar ON event_truncate_deletes(calendar_id);
CREATE INDEX idx_event_truncate_deletes_deleted_at ON event_truncate_deletes(deleted_at);

-- The log of a series delete. The row lists the override rows that the
-- series delete hid (hidden_ids, comma-separated event row IDs). A
-- restore of the series un-hides exactly those rows, by ID, so a change
-- of the series owner after the delete does not strand a row. An
-- override that a sync prune or an occurrence delete hid before stays
-- hidden.
CREATE TABLE event_series_deletes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    hidden_ids TEXT NOT NULL DEFAULT '',
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (calendar_id, uid)
);
CREATE INDEX idx_event_series_deletes_calendar ON event_series_deletes(calendar_id);
CREATE INDEX idx_event_series_deletes_deleted_at ON event_series_deletes(deleted_at);

-- The rows of each event series. One row of the view pairs an event row
-- (id, live or in the trash) with the calendar whose series owns it
-- (owner_calendar_id). The rule has four parts:
--
-- 1. A row in a calendar with a master row of its UID belongs to the
--    series of that calendar.
-- 2. A legacy override is a row in a calendar with no master row of its
--    UID. It belongs to the series of the master row c when no other
--    calendar holds a live master. When c is in the trash, no other
--    calendar can hold a master row at all.
-- 3. A legacy override with no such master has no owner. It is not in
--    the view.
-- 4. A row of a UID with no master row in any calendar belongs to the
--    series of its own calendar. Google serves such an orphan instance
--    under its own UID, and the export of the calendar still carries it.
--
-- uidmove.LegacyOwner is the Go form of part 2 and part 3. Drop the view
-- before a rebuild of the events table. Create it again after.
CREATE VIEW event_series_rows AS
SELECT
    e.id AS id,
    e.uid AS uid,
    e.calendar_id AS calendar_id,
    e.recurrence_id AS recurrence_id,
    e.deleted_at AS deleted_at,
    c.calendar_id AS owner_calendar_id
FROM events AS e
JOIN events AS c ON c.uid = e.uid AND c.recurrence_id = ''
WHERE c.calendar_id = e.calendar_id
   OR (NOT EXISTS (
        SELECT 1 FROM events AS m
        WHERE m.uid = e.uid AND m.recurrence_id = '' AND m.calendar_id = e.calendar_id)
       AND NOT EXISTS (
        SELECT 1 FROM events AS o
        WHERE o.uid = e.uid AND o.recurrence_id = '' AND o.calendar_id != c.calendar_id
          AND (o.deleted_at IS NULL OR c.deleted_at IS NOT NULL)))
UNION ALL
SELECT
    e.id AS id,
    e.uid AS uid,
    e.calendar_id AS calendar_id,
    e.recurrence_id AS recurrence_id,
    e.deleted_at AS deleted_at,
    e.calendar_id AS owner_calendar_id
FROM events AS e
WHERE NOT EXISTS (
    SELECT 1 FROM events AS m WHERE m.uid = e.uid AND m.recurrence_id = '');

-- +goose Down
DROP VIEW IF EXISTS event_series_rows;
DROP INDEX IF EXISTS idx_event_series_deletes_deleted_at;
DROP INDEX IF EXISTS idx_event_series_deletes_calendar;
DROP TABLE IF EXISTS event_series_deletes;
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
