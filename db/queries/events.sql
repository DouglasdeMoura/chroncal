-- name: ListEventsByDateRange :many
SELECT * FROM events
WHERE start_time < ? AND end_time > ? AND deleted_at IS NULL
ORDER BY start_time;

-- name: ListEventsByCalendarAndDateRange :many
SELECT * FROM events
WHERE calendar_id = ? AND start_time < ? AND end_time > ? AND deleted_at IS NULL
ORDER BY start_time;

-- name: GetEvent :one
SELECT * FROM events WHERE id = ? AND deleted_at IS NULL;

-- name: GetEventByCalendarUID :one
SELECT * FROM events WHERE calendar_id = ? AND uid = ? AND recurrence_id = '' AND deleted_at IS NULL;

-- name: ListEventMastersByUIDIncludingDeleted :many
-- Every master row of one UID, live or soft-deleted, in all calendars. The
-- legacy-override rule reads it (see storage.LegacyOverride).
SELECT * FROM events WHERE uid = ? AND recurrence_id = '' ORDER BY id;

-- name: ListEventsByUIDAndRecurrenceID :many
SELECT * FROM events WHERE uid = ? AND recurrence_id = ? AND deleted_at IS NULL ORDER BY id;

-- name: GetEventByCalendarUIDAndRecurrenceID :one
SELECT * FROM events WHERE calendar_id = ? AND uid = ? AND recurrence_id = ? AND deleted_at IS NULL;

-- name: GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted :one
-- The row of one (calendar, UID, recurrence ID) key, live or soft-deleted.
-- A soft-deleted override keeps its UNIQUE key, so a new override of the
-- same occurrence must revive it.
SELECT * FROM events WHERE calendar_id = ? AND uid = ? AND recurrence_id = ?;

-- name: GetEventIncludingDeleted :one
SELECT * FROM events WHERE id = ?;

-- name: CreateEvent :one
INSERT INTO events (
    uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule,
    timezone, status, transp, sequence, priority,
    class, url, exdates, rdates, recurrence_id, geo, duration, dtstamp,
    conference_uri
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEvent :one
UPDATE events SET
    title = ?, description = ?, location = ?,
    start_time = ?, end_time = ?, all_day = ?,
    recurrence_rule = ?, calendar_id = ?,
    timezone = ?, status = ?, transp = ?,
    sequence = sequence + 1, priority = ?,
    class = ?, url = ?,
    exdates = ?, rdates = ?, geo = ?,
    duration = ?, dtstamp = ?,
    conference_uri = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING *;

-- name: UpsertEventByUID :one
-- NOTE: ON CONFLICT matches (calendar_id, uid, recurrence_id). The same
-- UID on a second calendar inserts a new row (issue #756). ON CONFLICT
-- UPDATE clears deleted_at. This query resurrects soft-deleted rows.
-- Callers outside the pull path in the sync engine must know this. The
-- pull path is safe. The engine excludes tombstoned UIDs before this
-- query runs (engine.go loads tombstones first and skips them during pull).
INSERT INTO events (
    uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule,
    timezone, status, transp, sequence, priority,
    class, url, exdates, rdates, recurrence_id, geo, duration, dtstamp,
    conference_uri
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(calendar_id, uid, recurrence_id) DO UPDATE SET
    title = excluded.title, description = excluded.description,
    location = excluded.location, start_time = excluded.start_time,
    end_time = excluded.end_time, all_day = excluded.all_day,
    recurrence_rule = excluded.recurrence_rule,
    timezone = excluded.timezone, status = excluded.status,
    transp = excluded.transp,
    sequence = MAX(excluded.sequence, events.sequence + 1),
    priority = excluded.priority, class = excluded.class,
    url = excluded.url,
    exdates = excluded.exdates, rdates = excluded.rdates,
    geo = excluded.geo,
    duration = excluded.duration,
    dtstamp = excluded.dtstamp,
    conference_uri = excluded.conference_uri,
    deleted_at = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: UpdateEventExdates :exec
UPDATE events SET exdates = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: UpdateEventRdates :exec
UPDATE events SET rdates = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: UpdateEventRecurrenceRule :exec
UPDATE events SET recurrence_rule = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: SoftDeleteEvent :exec
UPDATE events SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NULL;

-- name: SoftDeleteEventsByCalendarUID :exec
-- Soft-delete every live row of one UID in one calendar. A pull that sees
-- the server object of the UID gone in that calendar uses it. Another
-- calendar can hold the same UID (a move, or a copy), so the delete stays
-- in the pulled calendar.
UPDATE events SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE calendar_id = ? AND uid = ? AND deleted_at IS NULL;

-- name: SoftDeleteEventsByUID :exec
UPDATE events SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NULL;

-- name: RestoreEvent :exec
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NOT NULL;

-- name: PurgeSoftDeletedEvents :execrows
DELETE FROM events WHERE deleted_at IS NOT NULL AND deleted_at < ?;

-- name: PurgeEventByID :execrows
DELETE FROM events WHERE id = ? AND deleted_at IS NOT NULL;

-- name: ListDeletedEventsByCalendar :many
SELECT * FROM events
WHERE calendar_id = ? AND deleted_at IS NOT NULL
ORDER BY deleted_at DESC;

-- name: ListRecurringEvents :many
SELECT * FROM events WHERE (recurrence_rule IS NOT NULL OR (rdates IS NOT NULL AND rdates != '')) AND recurrence_id = '' AND deleted_at IS NULL;

-- name: CountEventsByCalendar :one
SELECT COUNT(*) FROM events WHERE calendar_id = ? AND deleted_at IS NULL;

-- name: ReassignEventsCalendar :execrows
-- Bulk-move every event (live or soft-deleted) from one calendar to another.
-- Used by calendar migration ("Move to Account"): the destination calendar is
-- created/linked inside the same transaction before this runs, so the
-- calendar_id FK always resolves. No deleted_at filter - soft-deleted rows
-- move too so the trash view and purge keep working after migration.
UPDATE events SET
    calendar_id = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE calendar_id = ?;

-- name: CountEventIdentitiesByCalendar :one
-- Distinct live event identities on a calendar. A recurring master and its
-- overrides share a uid, so a series counts once - the number a
-- "Moved N events" summary should report.
SELECT COUNT(DISTINCT uid) FROM events WHERE calendar_id = ? AND uid != '' AND deleted_at IS NULL;

-- name: MarkEventIdentitiesDirtyForMigration :exec
-- Flags every distinct live event identity on the source calendar dirty on
-- the destination so the first sync after a migration uploads it. Runs before
-- reassignment, while the source rows still identify exactly what moves, as
-- one set-based statement instead of a per-UID loop. The destination is
-- always account-linked (migration targets a discovered collection), so the
-- account gate in MarkResourceDirty is not needed here.
INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
SELECT ?, uid, 'event', 1, 'sync-token'
FROM events AS src
WHERE src.calendar_id = ? AND src.deleted_at IS NULL AND src.uid != ''
GROUP BY uid
ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, rev = rev + 1;

-- name: CountEventUIDMoveCollisions :one
-- Count the live rows and the soft-deleted rows in the target calendar
-- that share the UID and the recurrence ID of a row that
-- MoveEventUIDToCalendar moves. A nonzero count means the move breaks the
-- UNIQUE key.
SELECT
    CAST(COALESCE(SUM(dst.deleted_at IS NULL), 0) AS INTEGER) AS live,
    CAST(COALESCE(SUM(dst.deleted_at IS NOT NULL), 0) AS INTEGER) AS trashed
FROM events AS dst
WHERE dst.calendar_id = sqlc.arg(to_calendar_id) AND dst.uid = sqlc.arg(uid)
  AND dst.recurrence_id IN (
    SELECT src.recurrence_id FROM events AS src
    WHERE src.calendar_id = sqlc.arg(from_calendar_id) AND src.uid = sqlc.arg(uid)
  );

-- name: MoveEventUIDToCalendar :exec
-- Move every row of one UID (live or soft-deleted) from one calendar to
-- another. A local calendar move of a resource calls it. See issue #828.
UPDATE events SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);

-- name: EventUIDMasterCalendars :one
-- Count the calendars with a live master row of one UID, and give the
-- lowest of them. With one calendar, that calendar is the only one.
SELECT
    COUNT(DISTINCT calendar_id) AS calendars,
    CAST(COALESCE(MIN(calendar_id), 0) AS INTEGER) AS calendar_id
FROM events
WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL;

-- name: GetEventByCalendarUIDIncludingDeleted :one
-- The master row of one UID in one calendar, live or soft-deleted. A
-- restore reads it: the master can be in the trash.
SELECT * FROM events WHERE calendar_id = ? AND uid = ? AND recurrence_id = '' LIMIT 1;

-- name: ListSeriesOverridesInCalendar :many
-- The live override rows of the series of one UID in one calendar. These are
-- the override rows of that calendar, plus the legacy overrides of the UID
-- that the series owns. The view event_series_rows in migration 057 and
-- uidmove.LegacyOwner hold the rule. Two calendars can hold the same UID, so
-- the override rows of the other master stay out (issue #842). Every series
-- query below takes its rows from the same view. Within one recurrence ID,
-- the order is the tie rule of the view column beside_master: the row beside
-- the master first, then the lowest ID.
SELECT events.* FROM events
JOIN event_series_rows AS s ON s.id = events.id
WHERE events.recurrence_id != '' AND events.deleted_at IS NULL
  AND s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id)
ORDER BY events.recurrence_id, s.beside_master DESC, events.id;

-- name: HasLiveSeriesOverrideAtOrAfter :one
-- Report whether the series of one UID in one calendar holds a live
-- override at or after the cutoff (see ListSeriesOverridesInCalendar). The
-- CLI and the TUI read it before a truncation.
SELECT CAST(EXISTS (
    SELECT 1 FROM events
    WHERE events.recurrence_id != '' AND events.recurrence_id >= sqlc.arg(recurrence_id) AND events.deleted_at IS NULL
      AND events.id IN (
        SELECT s.id FROM event_series_rows AS s
        WHERE s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id))
) AS BOOLEAN) AS has_override;

-- name: SoftDeleteEventsByIDs :exec
-- Soft-delete a set of live rows by ID in one statement. The truncation of
-- a series reads its rows once and hides them with it.
UPDATE events SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id IN (sqlc.slice(ids)) AND deleted_at IS NULL;

-- name: RestoreSeriesOverridesAtOrAfter :exec
-- Un-hide the soft-deleted overrides of the series in one calendar at or
-- after the cutoff (see ListSeriesOverridesInCalendar).
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE events.recurrence_id != '' AND events.recurrence_id >= sqlc.arg(recurrence_id) AND events.deleted_at IS NOT NULL
  AND events.id IN (
    SELECT s.id FROM event_series_rows AS s
    WHERE s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id));

-- name: RestoreSeriesOverrideByRecurrenceID :execrows
-- Un-hide the soft-deleted override of one occurrence of the series in one
-- calendar (see ListSeriesOverridesInCalendar).
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE events.recurrence_id = sqlc.arg(recurrence_id) AND events.deleted_at IS NOT NULL
  AND events.id IN (
    SELECT s.id FROM event_series_rows AS s
    WHERE s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id));

-- name: SoftDeleteSeriesRowsInCalendar :exec
-- Soft-delete every live row of the series of one UID in one calendar. The
-- series holds the master, the overrides of that calendar, and the legacy
-- overrides it owns (see ListSeriesOverridesInCalendar). A delete of a whole
-- series uses it.
UPDATE events SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE events.deleted_at IS NULL
  AND events.id IN (
    SELECT s.id FROM event_series_rows AS s
    WHERE s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id));

-- name: ListSeriesCalendarIDs :many
-- The calendars that hold a row of the series of one UID in one calendar.
-- These are that calendar, plus each calendar with a legacy override that the
-- series owns (see ListSeriesOverridesInCalendar). Rows in the trash count.
-- The access guard of a series delete or restore reads it.
SELECT DISTINCT s.calendar_id FROM event_series_rows AS s
WHERE s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id)
ORDER BY s.calendar_id;

-- name: ListEventCalendarIDsByUID :many
-- Every calendar that holds a row of one UID, live or in the trash. The
-- access guard of a UID-wide delete or restore reads it.
SELECT DISTINCT calendar_id FROM events WHERE uid = ? ORDER BY calendar_id;

-- name: ListSeriesOverridesAtRecurrenceID :many
-- The live override rows of one occurrence of the series in one calendar (see
-- ListSeriesOverridesInCalendar). These are the row of the calendar of the
-- master, or the legacy rows the series owns. The order is the tie rule of
-- the view column beside_master, so the first row wins.
SELECT events.* FROM events
JOIN event_series_rows AS s ON s.id = events.id
WHERE events.recurrence_id = sqlc.arg(recurrence_id) AND events.deleted_at IS NULL
  AND s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id)
ORDER BY s.beside_master DESC, events.id;

-- name: ListLiveOverridesInCalendarByUID :many
-- The live override rows of one UID in one calendar, with no series
-- rule. The export of a calendar that holds orphan instances reads it:
-- the UID has no live master anywhere.
SELECT * FROM events
WHERE calendar_id = ? AND uid = ? AND recurrence_id != '' AND deleted_at IS NULL
ORDER BY recurrence_id;

-- name: ListLiveSeriesOverridesAtOrAfter :many
-- The live override rows that a truncation of the series in one calendar
-- is about to hide (see ListSeriesOverridesInCalendar).
-- The truncation log records their IDs and their recurrence IDs.
SELECT * FROM events
WHERE events.recurrence_id != '' AND events.recurrence_id >= sqlc.arg(recurrence_id) AND events.deleted_at IS NULL
  AND events.id IN (
    SELECT s.id FROM event_series_rows AS s
    WHERE s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id))
ORDER BY events.recurrence_id;

-- name: ListEventCalendarIDsByIDs :many
-- The calendars of a set of rows, live or in the trash. The access guard
-- of a log-driven restore reads it in one query.
SELECT DISTINCT calendar_id FROM events WHERE id IN (sqlc.slice(ids)) ORDER BY calendar_id;

-- name: RestoreEventsByIDs :execrows
-- Un-hide a set of soft-deleted rows by ID in one statement. The count
-- tells the rows that came back from the rows that were live or purged.
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id IN (sqlc.slice(ids)) AND deleted_at IS NOT NULL;

-- name: ListLiveEventCalendarIDsByUID :many
-- Every calendar that holds a live row of one UID. A series delete with
-- no calendar and no live master picks the one calendar of the rows.
SELECT DISTINCT calendar_id FROM events WHERE uid = ? AND deleted_at IS NULL ORDER BY calendar_id;

-- name: ListTrashedSeriesRowsInCalendar :many
-- Every soft-deleted row of the series of one UID in one calendar (see
-- SoftDeleteSeriesRowsInCalendar). The restore by UID reads it, so it
-- can leave the rows that a truncation hid to the restore of that
-- truncation.
SELECT * FROM events
WHERE events.deleted_at IS NOT NULL
  AND events.id IN (
    SELECT s.id FROM event_series_rows AS s
    WHERE s.uid = sqlc.arg(uid) AND s.owner_calendar_id = sqlc.arg(calendar_id))
ORDER BY events.id;

-- name: GetSeriesMasterOfCalendar :one
-- The master whose series owns the rows of one UID in one calendar (see
-- the view event_series_rows). A calendar with its own master gives that
-- master. A calendar with a legacy override gives the owner of that
-- override. Else no row. Without include_deleted, the master and the rows
-- of the calendar must be live. With it, a restore finds them in the
-- trash too.
SELECT m.* FROM events AS m
WHERE m.uid = sqlc.arg(uid) AND m.recurrence_id = ''
  AND (m.deleted_at IS NULL OR CAST(sqlc.arg(include_deleted) AS BOOLEAN))
  AND m.calendar_id IN (
    SELECT s.owner_calendar_id FROM event_series_rows AS s
    WHERE s.uid = sqlc.arg(uid) AND s.calendar_id = sqlc.arg(calendar_id)
      AND (s.deleted_at IS NULL OR CAST(sqlc.arg(include_deleted) AS BOOLEAN)));

-- name: CountEventUIDRowsInCalendar :one
-- Count the master rows and the override rows of one UID in one calendar.
-- Without include_deleted, the count takes the live rows only. With it,
-- the rows in the trash count too. The dirty-mark rule for a legacy
-- override, the move rule, and the series rule read it.
SELECT
    CAST(COALESCE(SUM(recurrence_id = ''), 0) AS INTEGER) AS masters,
    CAST(COALESCE(SUM(recurrence_id != ''), 0) AS INTEGER) AS overrides,
    CAST(COALESCE(SUM(recurrence_id = '' AND deleted_at IS NULL), 0) AS INTEGER) AS live_masters
FROM events
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid)
  AND (deleted_at IS NULL OR CAST(sqlc.arg(include_deleted) AS BOOLEAN));

-- name: ListTrashedEventsByUID :many
-- The rows of one UID in the trash, in every calendar. The restore by UID
-- reads it to find the logs that still name a row, and the orphan rows of
-- the newest delete.
SELECT * FROM events WHERE uid = ? AND deleted_at IS NOT NULL ORDER BY id;

-- name: IsSeriesRowOfCalendar :one
-- Report whether the series of one calendar owns one row (see the view
-- event_series_rows). A row of that calendar, or a legacy override that
-- the series owns, gives true.
SELECT CAST(EXISTS (
    SELECT 1 FROM event_series_rows AS s WHERE s.id = sqlc.arg(id) AND s.owner_calendar_id = sqlc.arg(calendar_id)
) AS BOOLEAN) AS owned;

-- name: PurgeTrashedOverridesInCalendar :execrows
-- Hard-delete the override rows of one UID in one calendar that are in the
-- trash. The purge of a master takes them along, and the live rows stay.
DELETE FROM events
WHERE calendar_id = ? AND uid = ? AND recurrence_id != '' AND deleted_at IS NOT NULL;

-- name: ListTrashedMastersBefore :many
-- The master rows in the trash with deleted_at before the cutoff. The purge
-- by age applies the rule of the purge of one master to each of them.
SELECT calendar_id, uid FROM events
WHERE recurrence_id = '' AND deleted_at IS NOT NULL AND deleted_at < ?;
