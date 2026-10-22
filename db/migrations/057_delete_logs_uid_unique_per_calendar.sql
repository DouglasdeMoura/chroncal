-- +goose Up
-- Two calendars can hold a series with the same UID (migration 050, issue
-- #842). The delete logs kept one row per (uid, recurrence_id) and per (uid,
-- cutoff_time). A delete of the same occurrence in two calendars then kept
-- the log row of the first calendar only. Scope the keys to the calendar.
-- SQLite cannot drop a table UNIQUE constraint, so rebuild both tables. No
-- table references them, so a plain copy keeps every row.

-- pruned is 1 for a marker of a pull prune: the server dropped the
-- override, and the pull hid its row. A marker is no delete of the user.
-- The trash and the EXDATE contract ignore it. The restore of a master
-- with no series-delete log reads it, so the pruned row stays hidden.
-- hidden_ids holds one entry for each delete of the occurrence, in the form
-- seq:row. seq names the delete in its log, and row is an override row that
-- the delete hid. A delete with no stored override writes seq: with no row.
-- delete_seq is the seq of the last delete. owns_exdate is 1 while the log
-- owns a copy of the EXDATE of the occurrence. The invariant: that copy
-- exists only while no live override of the series sits at the occurrence
-- (see softdelete.SettleOccurrence).
CREATE TABLE event_exdate_deletes_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    recurrence_id TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    pruned INTEGER NOT NULL DEFAULT 0,
    hidden_ids TEXT NOT NULL DEFAULT '',
    owns_exdate INTEGER NOT NULL DEFAULT 1,
    delete_seq INTEGER NOT NULL DEFAULT 0,
    UNIQUE (calendar_id, uid, recurrence_id)
);
INSERT INTO event_exdate_deletes_new (id, calendar_id, uid, recurrence_id, deleted_at)
SELECT id, calendar_id, uid, recurrence_id, deleted_at FROM event_exdate_deletes;
-- A log from before this migration records one delete, and that delete
-- recorded the EXDATE. The backfill writes it in the entry form: delete 1
-- names each override in the trash at its occurrence, or no row. The code
-- then has one log format. The old schema has no pruned marker.
UPDATE event_exdate_deletes_new SET delete_seq = 1, owns_exdate = 1,
    hidden_ids = COALESCE((
        SELECT GROUP_CONCAT('1:' || e.id, ',') FROM events AS e
        WHERE e.calendar_id = event_exdate_deletes_new.calendar_id
          AND e.uid = event_exdate_deletes_new.uid
          AND e.recurrence_id = event_exdate_deletes_new.recurrence_id
          AND e.deleted_at IS NOT NULL), '1:');
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

-- The log of a series delete. The row lists the override rows that the series
-- delete hid (hidden_ids, comma-separated event row IDs). A restore of the
-- series un-hides exactly those rows, by ID. A change of the series owner
-- after the delete then strands no row. An override that a sync prune or an
-- occurrence delete hid before stays hidden. had_master is 1 for the log of a
-- series delete with a master in the calendar. It is 0 for the log of a
-- delete of orphan rows. A restore of a log whose master row is gone refuses
-- the rows only when had_master is 1.
CREATE TABLE event_series_deletes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    hidden_ids TEXT NOT NULL DEFAULT '',
    deleted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    had_master INTEGER NOT NULL DEFAULT 0
);
-- One row per series delete: the id names the delete. Two deletes of orphan
-- rows in one calendar keep two rows. A restore by UID reverses the newest
-- one, and the other one stays restorable.
CREATE INDEX idx_event_series_deletes_calendar ON event_series_deletes(calendar_id, uid);
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
-- beside_master is 1 for a row in the calendar of its owner master, and 0
-- for a legacy override or an orphan row. The series override queries order
-- by it: within one occurrence the row beside the master wins, then the
-- lowest ID. uidmove.TieBefore is the Go form of that tie rule.
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
    c.calendar_id AS owner_calendar_id,
    CAST(c.calendar_id = e.calendar_id AS INTEGER) AS beside_master
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
    e.calendar_id AS owner_calendar_id,
    0 AS beside_master
FROM events AS e
WHERE NOT EXISTS (
    SELECT 1 FROM events AS m WHERE m.uid = e.uid AND m.recurrence_id = '');

-- A master that comes back from the trash by any path consumes the
-- series-delete log of its calendar. The restore of the series reads and
-- drops the log before it un-hides the master. A pull, an import, or a
-- re-import un-hides the master with no log read. The log is then stale.
-- A later restore must not replay it (issue #287).
-- +goose StatementBegin
CREATE TRIGGER events_master_revive_drops_series_log
AFTER UPDATE OF deleted_at ON events
WHEN OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL AND NEW.recurrence_id = ''
BEGIN
    DELETE FROM event_series_deletes WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid;
END;
-- +goose StatementEnd

-- A new live master consumes the delete logs of its UID in its calendar. A
-- log there describes a delete with no master in the calendar. It records the
-- delete of orphan rows, or a delete by ID of an orphan row (an occurrence
-- log). A pull or an import that inserts a master makes such a log stale. An
-- occurrence log would then strip an imported EXDATE of the new master on a
-- restore (issue #86). The unique key holds one master row per calendar and
-- UID, so the insert of a master never meets a log of a master there.
-- +goose StatementBegin
CREATE TRIGGER events_master_insert_drops_stale_logs
AFTER INSERT ON events
WHEN NEW.recurrence_id = '' AND NEW.deleted_at IS NULL
BEGIN
    DELETE FROM event_series_deletes WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid;
    DELETE FROM event_exdate_deletes WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid;
END;
-- +goose StatementEnd

-- An override row that comes back, by any path, leaves the delete logs of its
-- UID. A restore by ID, an undo, an edit that revives the row, and a pull
-- import all clear deleted_at. The trigger drops the pruned marker of its
-- occurrence, and removes its ID from each series-delete log, each truncation
-- log, and each occurrence log. A log then names a row only while the row
-- stays in the trash from that delete. A series-delete log that names only
-- this row goes. A master of its calendar in the trash keeps that log,
-- because the log still records the series delete. A log that names no row
-- stays, because the row does not belong to that delete.
-- +goose StatementBegin
CREATE TRIGGER events_override_revive_drops_stale_logs
AFTER UPDATE OF deleted_at ON events
WHEN OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL AND NEW.recurrence_id != ''
BEGIN
    DELETE FROM event_exdate_deletes
    WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid AND recurrence_id = NEW.recurrence_id AND pruned = 1;
    DELETE FROM event_series_deletes
    WHERE uid = NEW.uid AND hidden_ids = CAST(NEW.id AS TEXT)
      AND NOT EXISTS (
        SELECT 1 FROM events AS m
        WHERE m.calendar_id = event_series_deletes.calendar_id AND m.uid = NEW.uid
          AND m.recurrence_id = '' AND m.deleted_at IS NOT NULL);
    UPDATE event_series_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || NEW.id || ',', ','), ',')
    WHERE uid = NEW.uid AND ',' || hidden_ids || ',' LIKE '%,' || NEW.id || ',%';
    UPDATE event_truncate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || NEW.id || ',', ','), ',')
    WHERE uid = NEW.uid AND hidden_ids IS NOT NULL AND ',' || hidden_ids || ',' LIKE '%,' || NEW.id || ',%';
    UPDATE event_exdate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ':' || NEW.id || ',', ':,'), ',')
    WHERE uid = NEW.uid AND ',' || hidden_ids || ',' LIKE '%:' || NEW.id || ',%';
END;
-- +goose StatementEnd

-- A new live override row drops the pruned marker of its occurrence too:
-- the import of a row after a purge of the pruned row.
-- +goose StatementBegin
CREATE TRIGGER events_override_insert_drops_prune_marker
AFTER INSERT ON events
WHEN NEW.recurrence_id != '' AND NEW.deleted_at IS NULL
BEGIN
    DELETE FROM event_exdate_deletes
    WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid AND recurrence_id = NEW.recurrence_id AND pruned = 1;
END;
-- +goose StatementEnd

-- A purge of an override row removes its ID from the delete logs of its
-- UID. Every purge path runs it: the purge by ID, the age purge, and the
-- purge of a series. A log then never names a row that is gone. The
-- occurrence log keeps the entry of the delete with no row.
-- +goose StatementBegin
CREATE TRIGGER events_purge_drops_row_from_logs
AFTER DELETE ON events
WHEN OLD.recurrence_id != ''
BEGIN
    UPDATE event_series_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || OLD.id || ',', ','), ',')
    WHERE uid = OLD.uid AND ',' || hidden_ids || ',' LIKE '%,' || OLD.id || ',%';
    UPDATE event_truncate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ',' || OLD.id || ',', ','), ',')
    WHERE uid = OLD.uid AND hidden_ids IS NOT NULL AND ',' || hidden_ids || ',' LIKE '%,' || OLD.id || ',%';
    UPDATE event_exdate_deletes
    SET hidden_ids = TRIM(REPLACE(',' || hidden_ids || ',', ':' || OLD.id || ',', ':,'), ',')
    WHERE uid = OLD.uid AND ',' || hidden_ids || ',' LIKE '%:' || OLD.id || ',%';
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS events_purge_drops_row_from_logs;
DROP TRIGGER IF EXISTS events_override_insert_drops_prune_marker;
DROP TRIGGER IF EXISTS events_override_revive_drops_stale_logs;
DROP TRIGGER IF EXISTS events_master_insert_drops_stale_logs;
DROP TRIGGER IF EXISTS events_master_revive_drops_series_log;
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
-- The old schema has no pruned marker, so a marker goes.
INSERT OR IGNORE INTO event_exdate_deletes_old (id, calendar_id, uid, recurrence_id, deleted_at)
SELECT id, calendar_id, uid, recurrence_id, deleted_at FROM event_exdate_deletes WHERE pruned = 0 ORDER BY id;
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
