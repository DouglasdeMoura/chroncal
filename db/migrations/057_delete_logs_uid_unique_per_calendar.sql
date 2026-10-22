-- +goose Up
-- delete_token names the soft-delete statement that put a row in the trash.
-- Each statement takes a fresh token from the one-row counter
-- event_delete_token, and writes it on every row that it hides. The undo of
-- a delete keeps its token (UndoMeta.DeleteToken): a row in the trash with
-- that token is still as the delete left it. A later delete, a pull
-- included, writes a newer token. The token matters only while deleted_at
-- holds a time, so a restore leaves it.
ALTER TABLE events ADD COLUMN delete_token INTEGER NOT NULL DEFAULT 0;
CREATE TABLE event_delete_token (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last INTEGER NOT NULL
);
INSERT INTO event_delete_token (id, last) VALUES (1, 0);

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
DROP INDEX IF EXISTS idx_event_exdate_deletes_calendar;
DROP INDEX IF EXISTS idx_event_exdate_deletes_deleted_at;
DROP TABLE event_exdate_deletes;
ALTER TABLE event_exdate_deletes_new RENAME TO event_exdate_deletes;
CREATE INDEX idx_event_exdate_deletes_calendar ON event_exdate_deletes(calendar_id);
CREATE INDEX idx_event_exdate_deletes_deleted_at ON event_exdate_deletes(deleted_at);
-- The triggers and the identity check of an undo read the logs of one UID in
-- every calendar.
CREATE INDEX idx_event_exdate_deletes_uid ON event_exdate_deletes(uid);

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
    delete_token INTEGER NOT NULL DEFAULT 0,
    UNIQUE (calendar_id, uid, cutoff_time, delete_token)
);
-- hidden_ids lists the override rows that the truncation hid, by row ID
-- (comma-separated). A restore un-hides exactly those rows, so a change
-- of the series owner after the truncation does not strand a row. A row
-- from before this migration has NULL: its restore reads hidden_overrides
-- (recurrence IDs) through the series rule.
-- delete_token is the delete token of the truncation (events.delete_token).
-- Each truncation gets its own row, also a second truncation at the same
-- cutoff. The row keeps the rule before its own truncation (previous_rrule),
-- and its undo restores its own rows only. A row from before this migration
-- has 0.
INSERT INTO event_truncate_deletes_new (id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates)
SELECT id, calendar_id, uid, cutoff_time, previous_rrule, deleted_at, hidden_overrides, removed_rdates FROM event_truncate_deletes;
DROP INDEX IF EXISTS idx_event_truncate_deletes_calendar;
DROP INDEX IF EXISTS idx_event_truncate_deletes_deleted_at;
DROP TABLE event_truncate_deletes;
ALTER TABLE event_truncate_deletes_new RENAME TO event_truncate_deletes;
CREATE INDEX idx_event_truncate_deletes_calendar ON event_truncate_deletes(calendar_id);
CREATE INDEX idx_event_truncate_deletes_deleted_at ON event_truncate_deletes(deleted_at);
CREATE INDEX idx_event_truncate_deletes_uid ON event_truncate_deletes(uid);

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
    had_master INTEGER NOT NULL DEFAULT 0,
    delete_token INTEGER NOT NULL DEFAULT 0
);
-- delete_token is the delete token of the master that the series delete hid
-- (events.delete_token). The revive of that master consumes this log only.
-- One row per series delete: the id names the delete. Two deletes of orphan
-- rows in one calendar keep two rows. A restore by UID reverses the newest
-- one, and the other one stays restorable.
CREATE INDEX idx_event_series_deletes_calendar ON event_series_deletes(calendar_id, uid);
CREATE INDEX idx_event_series_deletes_deleted_at ON event_series_deletes(deleted_at);
CREATE INDEX idx_event_series_deletes_uid ON event_series_deletes(uid);

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

-- A log from before this migration records the deletes of one occurrence,
-- and those deletes recorded the EXDATE. A later delete of the occurrence
-- moved the time of the log. The backfill writes the log in the entry form:
-- delete 1 names the overrides that the deletes hid, or no row. The code
-- then has one log format. The old schema has no pruned marker. The rows
-- are the trashed rows of the series of the log at its occurrence. The view
-- adds the legacy overrides in other calendars by the owner rule. The old
-- restore brought back each such row by its recurrence ID, so the backfill
-- takes the same rows, with three exceptions:
--   - a row that went to the trash more than one second after the log: a
--     later delete hid it.
--   - a row that went to the trash with the master before the log. A series
--     delete hides the master and its rows in one statement. The old data
--     has no delete token. So a row in the same second as the log counts.
--     An occurrence delete can hide it just before a series delete in that
--     second. The old restore brought it back by recurrence ID.
--   - a row that a truncation hid at the same time: its truncation log
--     owns it.
-- The times compare as whole seconds (unixepoch, SQLite 3.38 or later).
-- unixepoch drops a fraction of a second, so the bound of one second is
-- exact.
-- The log owns its EXDATE only while no live override of the series sits at
-- the occurrence (see softdelete.SettleOccurrence).
UPDATE event_exdate_deletes SET delete_seq = 1,
    owns_exdate = NOT EXISTS (
        SELECT 1 FROM event_series_rows AS live
        WHERE live.uid = event_exdate_deletes.uid
          AND live.owner_calendar_id = event_exdate_deletes.calendar_id
          AND live.recurrence_id = event_exdate_deletes.recurrence_id
          AND live.deleted_at IS NULL),
    hidden_ids = COALESCE((
        SELECT GROUP_CONCAT('1:' || s.id, ',') FROM event_series_rows AS s
        WHERE s.uid = event_exdate_deletes.uid
          AND s.owner_calendar_id = event_exdate_deletes.calendar_id
          AND s.recurrence_id = event_exdate_deletes.recurrence_id
          AND s.deleted_at IS NOT NULL
          AND unixepoch(s.deleted_at) <= unixepoch(event_exdate_deletes.deleted_at) + 1
          AND NOT EXISTS (
            SELECT 1 FROM events AS m
            WHERE m.uid = s.uid AND m.calendar_id = s.owner_calendar_id
              AND m.recurrence_id = '' AND m.deleted_at IS NOT NULL
              AND unixepoch(m.deleted_at) = unixepoch(s.deleted_at)
              AND unixepoch(s.deleted_at) < unixepoch(event_exdate_deletes.deleted_at))
          AND NOT EXISTS (
            SELECT 1 FROM event_truncate_deletes AS t
            WHERE t.uid = s.uid AND t.calendar_id = s.owner_calendar_id
              AND unixepoch(t.cutoff_time) <= unixepoch(s.recurrence_id)
              AND unixepoch(t.deleted_at) = unixepoch(s.deleted_at))), '1:');

-- A master that comes back from the trash by any path consumes the
-- series-delete log of its delete: the log with the delete token of the
-- master. The restore of the series reads and drops the log before it
-- un-hides the master. A pull, an import, or a re-import un-hides the master
-- with no log read. That log is stale, and a later restore must not replay it
-- (issue #287). The other logs of the calendar stay.
-- +goose StatementBegin
CREATE TRIGGER events_master_revive_drops_series_log
AFTER UPDATE OF deleted_at ON events
WHEN OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL AND NEW.recurrence_id = ''
BEGIN
    DELETE FROM event_series_deletes
    WHERE calendar_id = NEW.calendar_id AND uid = NEW.uid AND delete_token = OLD.delete_token;
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
DROP TABLE IF EXISTS event_delete_token;
DROP INDEX IF EXISTS idx_event_exdate_deletes_uid;
DROP INDEX IF EXISTS idx_event_truncate_deletes_uid;
DROP INDEX IF EXISTS idx_event_series_deletes_uid;
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
ALTER TABLE events DROP COLUMN delete_token;
