-- name: ListEventsByDateRange :many
SELECT * FROM events
WHERE start_time < ? AND end_time > ? AND deleted_at IS NULL
ORDER BY start_time;

-- name: ListEventsByCalendarAndDateRange :many
SELECT * FROM events
WHERE calendar_id = ? AND start_time < ? AND end_time > ? AND deleted_at IS NULL
ORDER BY start_time;

-- name: ListOverridesByUID :many
SELECT * FROM events
WHERE uid = ? AND recurrence_id != '' AND deleted_at IS NULL
ORDER BY recurrence_id;

-- name: ListDeletedOverrideRecurrenceIDs :many
SELECT recurrence_id FROM events
WHERE uid = ? AND recurrence_id != '' AND deleted_at IS NOT NULL
ORDER BY recurrence_id;

-- name: GetEvent :one
SELECT * FROM events WHERE id = ? AND deleted_at IS NULL;

-- name: GetEventByUID :one
SELECT * FROM events WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL;

-- name: GetEventByUIDAndRecurrenceID :one
SELECT * FROM events WHERE uid = ? AND recurrence_id = ? AND deleted_at IS NULL;

-- name: GetEventIncludingDeleted :one
SELECT * FROM events WHERE id = ?;

-- name: GetEventByUIDIncludingDeleted :one
SELECT * FROM events WHERE uid = ? AND recurrence_id = '' LIMIT 1;

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
-- NOTE: ON CONFLICT UPDATE clears deleted_at. This query resurrects
-- soft-deleted rows. Callers outside the pull path in the sync engine
-- must know this. The pull path is safe. The engine excludes
-- tombstoned UIDs before this query runs (engine.go loads tombstones
-- first and skips them during pull).
INSERT INTO events (
    uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule,
    timezone, status, transp, sequence, priority,
    class, url, exdates, rdates, recurrence_id, geo, duration, dtstamp,
    conference_uri
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    calendar_id = excluded.calendar_id,
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

-- name: SoftDeleteEventsByUID :exec
UPDATE events SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NULL;

-- name: ListLiveOverrideRecurrenceIDsAtOrAfter :many
-- The recurrence_ids a truncation is about to hide: live overrides at/after
-- the cutoff. Captured before SoftDeleteOverridesAtOrAfter so restore can
-- re-show only these and not overrides deleted independently (issue #287).
SELECT recurrence_id FROM events
WHERE uid = ? AND recurrence_id != '' AND recurrence_id >= ? AND deleted_at IS NULL;

-- name: SoftDeleteOverridesAtOrAfter :exec
UPDATE events SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND recurrence_id != '' AND recurrence_id >= ? AND deleted_at IS NULL;

-- name: RestoreEvent :exec
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NOT NULL;

-- name: RestoreEventsByUID :execrows
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NOT NULL;

-- name: RestoreOverridesAtOrAfter :exec
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND recurrence_id != '' AND recurrence_id >= ? AND deleted_at IS NOT NULL;

-- name: RestoreEventByUIDAndRecurrenceID :exec
UPDATE events SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND recurrence_id = ? AND deleted_at IS NOT NULL;

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
