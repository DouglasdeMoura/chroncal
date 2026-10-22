-- name: UpsertSyncResource :exec
INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    remote_url = excluded.remote_url,
    etag = excluded.etag,
    dirty = MAX(sync_resources.dirty, excluded.dirty),
    sync_strategy = excluded.sync_strategy;

-- name: GetSyncResource :one
SELECT * FROM sync_resources WHERE calendar_id = ? AND uid = ?;

-- name: ListSyncResourcesByCalendar :many
SELECT * FROM sync_resources WHERE calendar_id = ? ORDER BY id;

-- name: ListDirtySyncResources :many
SELECT * FROM sync_resources WHERE calendar_id = ? AND dirty = 1 ORDER BY id;

-- name: ListDirtySyncResourcesWithConflicts :many
-- The dirty rows of one calendar with their open conflict, in one read.
-- has_open_conflict is 1 when an open conflict row exists, and
-- open_move_source is its source calendar for a move conflict. The push
-- reads it once per pass (issue #841).
SELECT
    sqlc.embed(sr),
    CAST(sc.id IS NOT NULL AS INTEGER) AS has_open_conflict,
    sc.move_source_calendar_id AS open_move_source
FROM sync_resources sr
LEFT JOIN sync_conflicts sc
    ON sc.calendar_id = sr.calendar_id AND sc.uid = sr.uid AND sc.resolved_at IS NULL
WHERE sr.calendar_id = ? AND sr.dirty = 1
ORDER BY sr.id;

-- name: MarkSyncResourceDirty :exec
UPDATE sync_resources SET dirty = 1, rev = rev + 1 WHERE calendar_id = ? AND uid = ?;

-- name: MarkSyncResourceDirtyWithEtag :exec
UPDATE sync_resources SET dirty = 1, rev = rev + 1, etag = ? WHERE calendar_id = ? AND uid = ?;

-- name: ClearSyncResourceDirty :exec
UPDATE sync_resources SET dirty = 0, etag = ? WHERE calendar_id = ? AND uid = ?;

-- name: FinalizePushedResource :execrows
-- Records the new server ETag after a successful PUT and optimistically
-- clears the dirty flag. The count of the affected rows tells the caller
-- when the row went away during the PUT (a delete of the item landed). The
-- server object then needs a tombstone (issue #841). The ETag always
-- advances to the server's current version so the next push's If-Match does
-- not 412 against a change we just made. Dirty is cleared only when rev
-- still matches the value captured before the body was exported and PUT. A
-- local edit that landed during the PUT round-trip bumps rev (via
-- MarkSyncResourceDirty / MarkResourceDirty). Dirty then stays 1, and the
-- edit survives to the next push. See #92. A create passes the href of the
-- new object in remote_url, so the href commits with the clear of the dirty
-- flag. A delete after the finalize then sees a pushed row and writes a
-- tombstone. An empty or a NULL remote_url keeps the href of the row (issue
-- #841). A non-zero id must match the row id that the caller read. A row
-- that a delete removed and an import created again has a new id, and its
-- rev can start at the same value. The finalize then counts it as a gone
-- row (issue #841).
UPDATE sync_resources
SET etag = sqlc.arg(etag),
    dirty = CASE WHEN rev = sqlc.arg(rev) THEN 0 ELSE dirty END,
    remote_url = CASE WHEN COALESCE(CAST(sqlc.arg(remote_url) AS TEXT), '') = '' THEN remote_url ELSE CAST(sqlc.arg(remote_url) AS TEXT) END
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid)
  AND (CAST(sqlc.arg(row_id) AS INTEGER) = 0 OR id = CAST(sqlc.arg(row_id) AS INTEGER));

-- name: RecordSyncResourcePushFailure :exec
-- One more consecutive failed push attempt for this resource. The engine
-- and the sync doctor call this after a push attempt that fails on an
-- export, a parse, or a PUT. The doctor reads the counter to show how long
-- a resource stayed wedged.
-- A failure replaces a wait, so it clears the wait columns.
UPDATE sync_resources
SET push_fail_count = push_fail_count + 1,
    last_push_error = ?,
    wait_kind = 0,
    wait_holder_calendar_id = 0
WHERE calendar_id = ? AND uid = ?;

-- name: ListSyncResourcePushFailures :many
-- The sync rows of one calendar with a failed push attempt. The sync status
-- lists them, so a push failure that keeps an edit local is visible.
SELECT uid, owner_type, push_fail_count, last_push_error FROM sync_resources
WHERE calendar_id = ? AND push_fail_count > 0
ORDER BY uid;

-- name: ClearSyncResourcePushFailure :exec
-- Reset the failure bookkeeping after a successful push. The engine clears
-- it next to FinalizePushedResource. The sync doctor clears it after its
-- escape-hatch PUT lands.
UPDATE sync_resources SET push_fail_count = 0, last_push_error = '', wait_kind = 0, wait_holder_calendar_id = 0
WHERE calendar_id = ? AND uid = ?;

-- name: DeleteSyncResource :exec
DELETE FROM sync_resources WHERE calendar_id = ? AND uid = ?;

-- name: DeleteSyncResourcesByCalendar :exec
DELETE FROM sync_resources WHERE calendar_id = ?;

-- name: DetachSyncResourcesByCalendar :exec
-- Preserve local resources when an account is removed, but erase every
-- server-specific identity so a later link cannot reuse stale hrefs or ETags.
-- Dirty=1 makes the retained local objects first-time creates on a new server.
UPDATE sync_resources
SET remote_url = '', etag = '', dirty = 1, rev = rev + 1
WHERE calendar_id = ?;

-- name: CreateTombstone :exec
-- moved_to_calendar_id names the target of a move, or NULL for a plain
-- delete. See issue #841.
-- A new tombstone is never a tombstone from before the upgrade, so the
-- upsert resets legacy_move.
-- A plain write never erases a move target: the upsert keeps the target
-- of the row when the new value is NULL. Only ClearMoveTargetOfTombstones
-- and the guarded ClearMoveTargetOfTombstone clear a target.
-- A new write can name a new server object, so the upsert resets
-- source_gone.
INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (?, ?, ?, ?)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    remote_url = excluded.remote_url,
    deleted_at = excluded.deleted_at,
    moved_to_calendar_id = COALESCE(excluded.moved_to_calendar_id, tombstones.moved_to_calendar_id),
    legacy_move = 0,
    source_gone = 0,
    version = tombstones.version + 1;

-- name: DeletePlainTombstoneOfUID :exec
-- Delete the plain tombstone of one UID in one calendar. A tombstone that
-- names a move target stays: the ical import does not undo a move. See
-- issue #841.
DELETE FROM tombstones WHERE calendar_id = ? AND uid = ? AND moved_to_calendar_id IS NULL;

-- name: ClearMoveTargetOfTombstone :execrows
-- Turn one move tombstone into a plain delete, with three guards:
--   - the move target and the version are the ones that the caller read;
--   - no open move conflict of a calendar other than the move target holds
--     the tombstone (the view open_move_holders).
-- The organizer gate uses it for a gone copy and for an ended move. Zero
-- rows means that a write changed the tombstone or that a conflict holds
-- it, and the caller skips it. A held tombstone keeps its move target, so
-- the conflict that holds it still names the move. See issue #841.
UPDATE tombstones SET moved_to_calendar_id = NULL, version = version + 1
WHERE id = sqlc.arg(id) AND moved_to_calendar_id = sqlc.arg(moved_to_calendar_id) AND version = sqlc.arg(version)
  AND NOT EXISTS (
    SELECT 1 FROM open_move_holders h
    WHERE h.source_calendar_id = tombstones.calendar_id AND h.uid = tombstones.uid
      AND h.calendar_id IS NOT tombstones.moved_to_calendar_id
  );

-- name: ListMoveTombstonesOfTarget :many
-- List every move tombstone of one UID that names calendar_id as the
-- target. Unlike ListSiblingTombstones, it keeps a read-only source and a
-- source of another account. The end of a gated move clears each of them
-- through ClearMoveTargetOfTombstone. See issue #841.
SELECT * FROM tombstones WHERE uid = sqlc.arg(uid) AND moved_to_calendar_id = sqlc.arg(calendar_id)
ORDER BY calendar_id;

-- name: ClearMoveTargetOfTombstones :exec
-- Turn the move tombstones of one UID that name calendar_id as the target
-- into plain deletes. Every caller follows one rule: the move of the UID
-- into calendar_id is over, and calendar_id holds no copy to protect. One
-- caller is the delete of an unpushed moved copy. Another is the organizer
-- gate, for an ended move or a gone copy. The last is a move back whose
-- destination holds no live copy. See issue #841.
UPDATE tombstones SET moved_to_calendar_id = NULL, version = version + 1
WHERE uid = sqlc.arg(uid) AND moved_to_calendar_id = sqlc.arg(calendar_id);

-- name: SetSyncResourceCreateConflictRef :exec
-- Mark a row without a server object dirty with the href and the etag of
-- the object at its planned href. The local pick of an edit conflict on a
-- create uses it, so the push sends If-Match. See issue #841.
UPDATE sync_resources SET remote_url = ?, etag = ?, dirty = 1, rev = rev + 1
WHERE calendar_id = ? AND uid = ? AND remote_url = '';

-- name: ClearSyncResourceDirtyFlag :exec
-- Clear the dirty flag of one row and keep its etag. The cancel of a move
-- that failed in its import clears the source row. No push then sends a
-- half-imported body before the replay (issue #841).
UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = ?;

-- name: ListTombstonesByCalendar :many
SELECT * FROM tombstones WHERE calendar_id = ? ORDER BY deleted_at;

-- name: DeleteTombstone :exec
DELETE FROM tombstones WHERE id = ?;

-- name: DeleteTombstonesByCalendar :exec
DELETE FROM tombstones WHERE calendar_id = ?;

-- name: DeleteTombstonesByCalendarAndUID :exec
DELETE FROM tombstones WHERE calendar_id = ? AND uid = ?;

-- name: DeleteStaleTombstones :exec
-- Delete the tombstones older than 30 days. A tombstone that a calendar
-- move still needs stays (the view move_wait_tombstones). See issue #841.
DELETE FROM tombstones
WHERE deleted_at < strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-30 days')
  AND NOT EXISTS (SELECT 1 FROM move_wait_tombstones w WHERE w.id = tombstones.id);

-- name: GetTombstoneByCalendarAndUID :one
SELECT * FROM tombstones WHERE calendar_id = ? AND uid = ?;

-- name: RetargetMoveTombstones :exec
-- Point the move tombstones of one UID that name from_calendar_id as the
-- target at to_calendar_id. A chain move (A to B, then B to C before any
-- sync) leaves the tombstone of A with the target B. B then holds no row of
-- the UID any more. See issue #841.
UPDATE tombstones SET moved_to_calendar_id = sqlc.arg(to_calendar_id), version = version + 1
WHERE uid = sqlc.arg(uid) AND moved_to_calendar_id = sqlc.arg(from_calendar_id);

-- name: ClearPushedSyncResourceDirty :exec
-- Clear the dirty flag of a sync row that has a server object. The etag
-- stays. A calendar move calls it on the source row. See issue #828.
UPDATE sync_resources SET dirty = 0
WHERE calendar_id = ? AND uid = ? AND remote_url != '';

-- name: DeleteUnpushedSyncResource :execrows
-- Delete a sync row that has no server object and still has the rev the
-- caller read. A push of that row creates a copy on the server. A
-- restore or an edit that landed after the read bumps rev, and the row
-- then keeps its fresh dirty mark (issue #841). See issue #828.
DELETE FROM sync_resources
WHERE calendar_id = ? AND uid = ? AND remote_url = '' AND rev = ?;

-- name: SetSyncResourceEtag :exec
-- Replace the last known etag of a sync row. The dirty flag and the rev
-- stay. A resolved move conflict sets the etag of the source row to the
-- etag of the remote edit, so the conditional source DELETE matches. See
-- issue #841.
UPDATE sync_resources SET etag = ? WHERE calendar_id = ? AND uid = ?;

-- name: CountMoveWaitTombstones :one
-- Count the tombstones of one UID in calendar_id that a calendar move
-- still needs (the view move_wait_tombstones). The source delete of the
-- move waits for them. See storage.MoveSourceDeleteSafe and issue #841.
SELECT COUNT(*) FROM move_wait_tombstones WHERE calendar_id = ? AND uid = ?;

-- name: ListMoveDeleteWaitUIDs :many
-- List the UIDs of the tombstones of calendar_id that a calendar move
-- still needs (the view move_wait_tombstones). The tombstone pass reads
-- it once per pass through storage.MoveDeleteWaitUIDs. See issue #841.
SELECT uid FROM move_wait_tombstones WHERE calendar_id = ?;

-- name: ListSiblingTombstonesAny :many
-- The tombstones of one UID in the other remote calendars of one account,
-- with or without a move target. Each row has the name and the remote
-- access of its calendar. A refused create whose new href holds no object
-- reads it, for a tombstone that names a third calendar or a plain delete.
-- A pending delete in a writable sibling calendar goes out on the next pass
-- of that calendar. The create then lands (issue #841). held marks a
-- tombstone that the wait view holds: it waits for another move, so the
-- create does not wait for it.
SELECT
    sqlc.embed(t),
    c.name AS calendar_name,
    c.remote_access AS calendar_remote_access,
    CAST(EXISTS (SELECT 1 FROM move_wait_tombstones w WHERE w.id = t.id) AS INTEGER) AS held
FROM tombstones t
JOIN calendars c ON c.id = t.calendar_id
WHERE c.account_id = (SELECT own.account_id FROM calendars own WHERE own.id = sqlc.arg(calendar_id))
  AND c.account_id != 0
  AND c.id != sqlc.arg(calendar_id)
  AND c.remote_url IS NOT NULL AND c.remote_url != ''
  AND t.uid = sqlc.arg(uid)
ORDER BY c.display_order, c.name, t.deleted_at;

-- name: ListSiblingTombstones :many
-- The unsent tombstones of one UID in the other calendars of one account,
-- whose target is calendar_id or that name no target. Each row has the last
-- known etag of its source object. A calendar without a remote_url has no
-- server object to delete. The caller drops the read-only calendars. A
-- local move between two calendars of one account leaves a tombstone in the
-- source calendar that names the target (issue #828). A tombstone without a
-- target (a move from before the upgrade, or a plain delete) also frees the
-- UID for a refused create. A 412 on its delete follows the plain
-- delete-vs-edit rule, never a move conflict (issue #841).
SELECT
    sqlc.embed(t),
    c.remote_url AS calendar_remote_url,
    c.remote_access AS calendar_remote_access,
    COALESCE(sr.etag, '') AS etag
FROM tombstones t
JOIN calendars c ON c.id = t.calendar_id
LEFT JOIN sync_resources sr ON sr.calendar_id = t.calendar_id AND sr.uid = t.uid
WHERE c.account_id = (SELECT own.account_id FROM calendars own WHERE own.id = sqlc.arg(calendar_id))
  AND c.account_id != 0
  AND c.id != sqlc.arg(calendar_id)
  AND c.remote_url IS NOT NULL AND c.remote_url != ''
  AND t.uid = sqlc.arg(uid)
  AND (t.moved_to_calendar_id = sqlc.arg(calendar_id) OR t.moved_to_calendar_id IS NULL)
ORDER BY c.display_order, c.name, t.deleted_at;

-- name: CountPendingMovesOfUIDTouchingCalendar :one
-- Count the move tombstones of one UID whose source or target is
-- calendar_id. The import of a todo or a journal reads it. Their key spans
-- calendars, and an upsert of a row in another calendar must not move a row
-- of a pending move. See issue #841.
SELECT COUNT(*) FROM tombstones
WHERE uid = sqlc.arg(uid) AND moved_to_calendar_id IS NOT NULL
  AND (calendar_id = sqlc.arg(calendar_id) OR moved_to_calendar_id = sqlc.arg(calendar_id));

-- name: ListCalendarsOfTombstonesMovedTo :many
-- The calendars whose tombstones name one calendar as the move target. An
-- unlink or a reset of the target changes how those tombstones wait, so
-- the caller locks them. See issue #841.
SELECT DISTINCT calendar_id FROM tombstones
WHERE moved_to_calendar_id = sqlc.arg(target_calendar_id)
ORDER BY calendar_id;

-- name: ListMoveTargetsOfOwnTombstones :many
-- The target calendars of the move tombstones of one calendar. A reset or
-- an unlink of the calendar changes those moves, so it locks the targets.
-- See issue #841.
SELECT DISTINCT moved_to_calendar_id FROM tombstones
WHERE calendar_id = sqlc.arg(calendar_id) AND moved_to_calendar_id IS NOT NULL
ORDER BY moved_to_calendar_id;

-- name: DeleteTombstonesForReset :exec
-- Delete the tombstones of one calendar for a reset, except the move
-- tombstones whose target holds a live copy (the view live_move_copies).
-- The source DELETE of such a move still goes out after the reset, so the
-- source object does not come back on the next pull. See issue #841.
DELETE FROM tombstones
WHERE tombstones.calendar_id = sqlc.arg(calendar_id)
  AND NOT (tombstones.moved_to_calendar_id IS NOT NULL AND EXISTS (
    SELECT 1 FROM live_move_copies l
    WHERE l.calendar_id = tombstones.moved_to_calendar_id AND l.uid = tombstones.uid
  ));

-- name: DeleteSyncResourcesForReset :exec
-- Delete the sync rows of one calendar for a reset, except two kinds of
-- row:
--   - the rows of the UIDs whose tombstone the reset keeps
--     (DeleteTombstonesForReset), so the kept DELETE stays conditional on
--     the last etag
--   - the rows of the moved copies that a move tombstone of another
--     calendar names. An unpushed copy keeps its create. A landed copy
--     keeps its href and etag, so no second create of it goes out.
-- A reset refuses a calendar with an open move conflict, so no such
-- conflict needs a kept row.
-- See issue #841.
DELETE FROM sync_resources
WHERE sync_resources.calendar_id = sqlc.arg(calendar_id)
  AND NOT EXISTS (
    SELECT 1 FROM tombstones t
    WHERE (t.calendar_id = sync_resources.calendar_id OR t.moved_to_calendar_id = sync_resources.calendar_id)
      AND t.uid = sync_resources.uid
  );

-- name: IsTombstoneWaiting :one
-- Count whether one tombstone is in the view move_wait_tombstones. The
-- tombstone pass reads it right before the DELETE, with the tombstone
-- itself. See issue #841.
SELECT COUNT(*) FROM move_wait_tombstones WHERE id = sqlc.arg(id);

-- name: ListCalendarsOfTombstonesMovedToForUID :many
-- The calendars whose tombstones of one UID name one calendar as the move
-- target. The resolve of a move conflict locks them: the move back
-- rewrites those tombstones. See issue #841.
SELECT DISTINCT calendar_id FROM tombstones
WHERE moved_to_calendar_id = sqlc.arg(target_calendar_id) AND uid = sqlc.arg(uid)
ORDER BY calendar_id;

-- name: DeleteTombstoneIfVersion :execrows
-- Delete one tombstone only when its version is the one that the tombstone
-- pass read before its DELETE. Zero rows means that a write changed the
-- tombstone while the server handled the DELETE. See issue #841.
DELETE FROM tombstones WHERE id = sqlc.arg(id) AND version = sqlc.arg(version);

-- name: ClearSyncResourceRemote :exec
-- Record that the server object of a sync row is gone. The row loses its
-- href and its etag, and it becomes dirty with a new rev. A detach does the
-- same (DetachSyncResourcesByCalendar). Its live rows then go out as a create
-- on the next push. See issue #841.
UPDATE sync_resources SET remote_url = '', etag = '', dirty = 1, rev = rev + 1
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid);

-- name: ClearSyncResourceHref :exec
-- Drop the href and the etag of a sync row whose server object is gone,
-- and leave its dirty flag as it is. The clear of a tombstone uses it on a
-- version miss when no live row of the UID remains. See issue #841.
UPDATE sync_resources SET remote_url = '', etag = ''
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid);

-- name: ListTombstoneHoldingCalendars :many
-- List the calendars whose open move conflict names the calendar of one
-- tombstone as the source. A conflict on the target of the move does not
-- count, and a conflict on exclude_calendar_id does not count. The push
-- passes the calendar that it serves there, also for a tombstone without
-- a move target. A conflict on a calendar that an account delete left
-- unlinked does not count (the view open_move_holders). The user
-- must resolve such a conflict before the source of the move can go. The
-- settle of a moved resource reads the list before each source DELETE. The
-- clear of a source that is gone reads it in its transaction. See issue
-- #841.
SELECT DISTINCT CAST(h.calendar_id AS INTEGER) AS id, CAST(h.calendar_name AS TEXT) AS name FROM tombstones t
JOIN open_move_holders h ON h.source_calendar_id = t.calendar_id AND h.uid = t.uid
WHERE t.id = sqlc.arg(id)
  AND h.calendar_id IS NOT t.moved_to_calendar_id
  AND h.calendar_id != sqlc.arg(exclude_calendar_id)
ORDER BY h.calendar_id;

-- name: MarkTombstoneSourceGone :execrows
-- Mark the server object of one tombstone as gone, at the version that the
-- caller read. An open move conflict of a third calendar holds the
-- tombstone, so the clear keeps it. The flag does not change the version.
-- See issue #841.
UPDATE tombstones SET source_gone = 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version);

-- name: SetSyncResourceWait :exec
-- Record that the push of a sync row waits for another step. wait_kind is 1
-- for a hold by an open move conflict of wait_holder_calendar_id, and 2 for
-- a wait for a source delete. A wait is no failure, so push_fail_count and
-- last_push_error stay as they are. See issue #841.
UPDATE sync_resources
SET wait_kind = sqlc.arg(wait_kind), wait_holder_calendar_id = sqlc.arg(wait_holder_calendar_id)
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid);

-- name: ListWaitingSyncResources :many
-- List the sync rows of every calendar whose push waits, while the cause of
-- the wait still holds:
--   - kind 1: an open move conflict of the holder calendar names a source
--     calendar S. S holds a tombstone of the UID that the row waits on.
--     When S is the calendar of the row, the row waits for its source
--     delete. Otherwise S is a sibling calendar of the same account, and
--     the row is dirty: it waits for its create.
--   - kind 2: the row is dirty and has no server object. A tombstone of
--     the UID waits in a sibling calendar of the same account.
-- The cause decides, so no path must clear a wait when the cause ends: a
-- pick, the moot step, a restore, or a gate clear. holder_name is the name
-- of the holder calendar, or its ID as text. The sync doctor and the sync
-- status show these rows as waits, not as failures. See issue #841.
SELECT sr.calendar_id, sr.uid, sr.owner_type, sr.wait_kind,
       CAST(COALESCE(hc.name, CAST(sr.wait_holder_calendar_id AS TEXT)) AS TEXT) AS holder_name
FROM sync_resources sr
JOIN calendars own ON own.id = sr.calendar_id
LEFT JOIN calendars hc ON hc.id = sr.wait_holder_calendar_id
WHERE (sr.wait_kind = 1
    AND EXISTS (
      SELECT 1 FROM open_move_holders h
      JOIN tombstones t ON t.calendar_id = h.source_calendar_id AND t.uid = h.uid
      JOIN calendars tc ON tc.id = t.calendar_id
      WHERE h.calendar_id = sr.wait_holder_calendar_id AND h.uid = sr.uid
        AND (t.calendar_id = sr.calendar_id
          OR (sr.dirty = 1 AND tc.account_id = own.account_id))
    ))
  OR (sr.wait_kind = 2
    AND sr.dirty = 1 AND sr.remote_url = ''
    AND EXISTS (
      SELECT 1 FROM tombstones t JOIN calendars c ON c.id = t.calendar_id
      WHERE t.uid = sr.uid AND t.calendar_id != sr.calendar_id AND c.account_id = own.account_id
    ))
ORDER BY sr.calendar_id, sr.uid;

-- name: ClearHrefOfGoneSource :exec
-- Drop the href and the etag of a sync row whose tombstone has the
-- source_gone mark, and mark the row dirty with a new rev. A restore, an
-- import, or a move back clears such a tombstone. The server object is
-- gone, so the next push must create the item again. plain_only limits the check to a tombstone without a move
-- target, as the import clears only such a tombstone. See issue #841.
UPDATE sync_resources SET remote_url = '', etag = '', dirty = 1, rev = rev + 1
WHERE sync_resources.calendar_id = sqlc.arg(calendar_id) AND sync_resources.uid = sqlc.arg(uid)
  AND EXISTS (
    SELECT 1 FROM tombstones t
    WHERE t.calendar_id = sync_resources.calendar_id AND t.uid = sync_resources.uid
      AND t.source_gone = 1
      AND (CAST(sqlc.arg(plain_only) AS INTEGER) = 0 OR t.moved_to_calendar_id IS NULL)
  );

-- name: UpsertDirtySyncResourceOfCalendar :exec
-- Mark the sync row of uid in exactly calendar_id dirty with a new rev, and
-- create the row when it does not exist. It does not move the mark to
-- another calendar. MarkResourceDirty runs it after its own redirect, and
-- the settle of a gone move source runs it for the row of the moved copy.
-- So one statement holds the write. See issue #841.
INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
VALUES (sqlc.arg(calendar_id), sqlc.arg(uid), sqlc.arg(owner_type), 1, 'sync-token')
ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, rev = rev + 1;

-- name: HasMoveTombstonesOfTarget :one
-- Report whether a move tombstone of uid names calendar_id as the target.
-- The organizer gate uses it for its early return. See issue #841.
SELECT EXISTS (
  SELECT 1 FROM tombstones WHERE uid = sqlc.arg(uid) AND moved_to_calendar_id = sqlc.arg(calendar_id)
) AS has_tombstone;

-- name: MarkMovedCopiesDirtyInCalendar :exec
-- Give each live moved copy in a linked calendar a dirty sync row. The query
-- starts from the moves into the calendar: the move tombstones that name it
-- as the target, and the open move conflicts on it. A UID of such a move
-- with a live row in the calendar is a moved copy. A copy in a local
-- calendar has no sync row. After a link or a reset, the copy then pushes,
-- and the view live_move_copies counts it. An existing row stays as it is.
-- See issue #841.
INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
SELECT c.id, m.uid, x.owner_type, 1, 'sync-token'
FROM calendars c
JOIN (
    SELECT t.uid FROM tombstones t WHERE t.moved_to_calendar_id = CAST(sqlc.arg(calendar_id) AS INTEGER)
    UNION
    SELECT sc.uid FROM sync_conflicts sc
    WHERE sc.calendar_id = sqlc.arg(calendar_id) AND sc.move_source_calendar_id IS NOT NULL AND sc.resolved_at IS NULL
) m
JOIN (
    SELECT e.uid, 'event' AS owner_type FROM events e WHERE e.calendar_id = sqlc.arg(calendar_id) AND e.deleted_at IS NULL
    UNION
    SELECT td.uid, 'todo' AS owner_type FROM todos td WHERE td.calendar_id = sqlc.arg(calendar_id) AND td.deleted_at IS NULL
    UNION
    SELECT j.uid, 'journal' AS owner_type FROM journals j WHERE j.calendar_id = sqlc.arg(calendar_id) AND j.deleted_at IS NULL
) x ON x.uid = m.uid
WHERE c.id = sqlc.arg(calendar_id) AND c.account_id IS NOT NULL AND c.account_id != 0
ON CONFLICT(calendar_id, uid) DO NOTHING;
