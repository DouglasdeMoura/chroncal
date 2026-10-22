-- name: CreateSyncConflict :execrows
-- Record a conflict. A NULL move_source_calendar_id records an edit
-- conflict. A source calendar records a move conflict. The row lives on the
-- target calendar of a local move whose source object changed on the server
-- (issue #841). A record of one kind replaces an open row of the same kind
-- and a resolved row of either kind. An open row of the other kind stays as
-- it is. An open move conflict needs the choice of the user. An open edit
-- conflict of the moved copy holds the remote edit of the target object.
-- CreateMoveConflictReplacingEdit replaces an open edit conflict too. The
-- move tombstone waits for its resolution (the view move_wait_tombstones).
-- A resolved row of either kind, abandoned included, becomes the new
-- conflict: a new refusal is a new state. A trashed copy never reaches a
-- move record (its export fails), so an abandoned row comes back only for a
-- copy that lives again. A move conflict starts in the state 'open'.
INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, move_source_calendar_id, move_state)
VALUES (
    sqlc.arg(calendar_id), sqlc.arg(owner_type), sqlc.arg(owner_id), sqlc.arg(uid),
    sqlc.arg(local_ical), sqlc.arg(server_ical), sqlc.arg(server_etag), sqlc.arg(move_source_calendar_id),
    CASE WHEN sqlc.arg(move_source_calendar_id) IS NULL THEN '' ELSE 'open' END
)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    owner_type = excluded.owner_type,
    owner_id = excluded.owner_id,
    local_ical = excluded.local_ical,
    server_ical = excluded.server_ical,
    server_etag = excluded.server_etag,
    detected_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    resolved_at = NULL,
    resolution = NULL,
    move_source_calendar_id = excluded.move_source_calendar_id,
    move_state = excluded.move_state,
    warned_etag = '',
    warned_hash = '',
    cancel_source_rev = 0,
    cancel_source_row_id = 0
WHERE sync_conflicts.resolved_at IS NOT NULL
   OR (sync_conflicts.move_source_calendar_id IS NULL) = (excluded.move_source_calendar_id IS NULL);

-- name: CreateMoveConflictReplacingEdit :execrows
-- Record a move conflict as CreateSyncConflict does, and also replace an
-- open edit conflict of the row. The organizer gate of a server-wins pass
-- uses it. The close of the edit conflict and the move record commit in one
-- statement, so a failed record keeps the edit conflict. See issue #841.
INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, move_source_calendar_id, move_state)
VALUES (
    sqlc.arg(calendar_id), sqlc.arg(owner_type), sqlc.arg(owner_id), sqlc.arg(uid),
    sqlc.arg(local_ical), sqlc.arg(server_ical), sqlc.arg(server_etag), sqlc.arg(move_source_calendar_id),
    CASE WHEN sqlc.arg(move_source_calendar_id) IS NULL THEN '' ELSE 'open' END
)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    owner_type = excluded.owner_type,
    owner_id = excluded.owner_id,
    local_ical = excluded.local_ical,
    server_ical = excluded.server_ical,
    server_etag = excluded.server_etag,
    detected_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    resolved_at = NULL,
    resolution = NULL,
    move_source_calendar_id = excluded.move_source_calendar_id,
    move_state = excluded.move_state,
    warned_etag = '',
    warned_hash = '',
    cancel_source_rev = 0,
    cancel_source_row_id = 0
WHERE sync_conflicts.resolved_at IS NOT NULL
   OR (sync_conflicts.move_source_calendar_id IS NULL) = (excluded.move_source_calendar_id IS NULL)
   OR (sync_conflicts.move_source_calendar_id IS NULL AND excluded.move_source_calendar_id IS NOT NULL);

-- name: CloseMoveConflict :exec
-- Close a move conflict whose moved copy is gone: the state becomes
-- abandoned. An open row takes the resolution server-auto in the same
-- statement. A resolved row (a local pick) keeps its resolution and
-- resolved_at. Both picks refuse an abandoned move. No move back ran, and
-- the trashed copy can still hold the UID. The update touches the states
-- open and local-picked. It touches a failed cancel only when
-- allow_failed_cancel is 1. The moot step closes a failed cancel whose item
-- the user deleted. Every other path leaves it for its replay. See issue
-- #841.
UPDATE sync_conflicts
SET move_state = 'abandoned',
    resolved_at = COALESCE(resolved_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    resolution = COALESCE(resolution, 'server-auto')
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid) AND move_source_calendar_id IS NOT NULL
  AND (move_state IN ('open', 'local-picked')
       OR (CAST(sqlc.arg(allow_failed_cancel) AS INTEGER) = 1 AND move_state = 'cancel-failed'));

-- name: ListMootFailedCancels :many
-- The failed cancels whose target or source is calendar_id and whose source
-- calendar holds no live row of the UID any more. The user deleted or
-- purged the item after the cancel moved it back. See issue #841.
SELECT sqlc.embed(sc)
FROM sync_conflicts sc
WHERE sc.resolved_at IS NULL
  AND sc.move_source_calendar_id IS NOT NULL
  AND sc.move_state = 'cancel-failed'
  AND (sc.calendar_id = sqlc.arg(calendar_id) OR sc.move_source_calendar_id = sqlc.arg(calendar_id))
  AND NOT EXISTS (
    SELECT 1 FROM events e WHERE sc.owner_type = 'event' AND e.calendar_id = sc.move_source_calendar_id AND e.uid = sc.uid AND e.deleted_at IS NULL
    UNION ALL
    SELECT 1 FROM todos td WHERE sc.owner_type = 'todo' AND td.calendar_id = sc.move_source_calendar_id AND td.uid = sc.uid AND td.deleted_at IS NULL
    UNION ALL
    SELECT 1 FROM journals j WHERE sc.owner_type = 'journal' AND j.calendar_id = sc.move_source_calendar_id AND j.uid = sc.uid AND j.deleted_at IS NULL
  )
ORDER BY sc.id;

-- name: ListSettledFailedCancels :many
-- The failed cancels whose target or source is calendar_id and whose source
-- row already carries the server side. The row is clean, has a server
-- object, and has a rev other than the rev at the failure, or another row
-- id. A pull import changes the rev, and the failed cancel does not. A row
-- that a delete removed and an import created again has a new id. An etag that matches proves
-- nothing, because the cancel can write it before the import fails. The
-- replay has nothing to restore then. See issue #841.
SELECT sqlc.embed(sc), sr.rev AS source_rev, sr.id AS source_row_id
FROM sync_conflicts sc
JOIN sync_resources sr ON sr.calendar_id = sc.move_source_calendar_id AND sr.uid = sc.uid
WHERE sc.resolved_at IS NULL
  AND sc.move_source_calendar_id IS NOT NULL
  AND sc.move_state = 'cancel-failed'
  AND (sc.calendar_id = sqlc.arg(calendar_id) OR sc.move_source_calendar_id = sqlc.arg(calendar_id))
  AND sr.dirty = 0 AND sr.remote_url != '' AND sr.etag != ''
  AND (sr.rev != sc.cancel_source_rev
       OR (sc.cancel_source_row_id != 0 AND sr.id != sc.cancel_source_row_id))
ORDER BY sc.id;

-- name: CloseSettledFailedCancel :exec
-- Close a failed cancel whose source row carries the server side: the
-- state becomes cancelled, as after a cancel that succeeded. The rev of
-- the source row becomes the cancel rev, so a later local pick restores
-- only when the source does not change again. See issue #841.
UPDATE sync_conflicts
SET move_state = 'cancelled',
    resolved_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    resolution = 'server-auto',
    cancel_source_rev = sqlc.arg(cancel_source_rev),
    cancel_source_row_id = sqlc.arg(cancel_source_row_id)
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid) AND move_source_calendar_id IS NOT NULL
  AND move_state = 'cancel-failed' AND resolved_at IS NULL;

-- name: CloseOpenEditConflict :exec
-- Close the open edit conflict of one UID in one calendar as server-auto.
-- The delete of an unpushed item calls it: the conflict has no local side
-- any more. See issue #841.
UPDATE sync_conflicts
SET resolved_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    resolution = 'server-auto'
WHERE calendar_id = ? AND uid = ? AND move_source_calendar_id IS NULL AND resolved_at IS NULL;

-- name: MovedCopyExists :one
-- Report whether calendar_id holds a live copy of the UID (the view
-- live_move_copies). See issue #841.
SELECT EXISTS (
    SELECT 1 FROM live_move_copies
    WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid) AND owner_type = sqlc.arg(owner_type)
) AS present;

-- name: DeleteResolvedMoveConflictsOfUID :exec
-- Delete the resolved move conflict rows of one UID in the calendars of a
-- new move: the two calendars, and every calendar of their accounts. A new
-- move of the UID starts a new history there: a stale cancelled row must
-- not restore an old body beside the moved item. A copy of the UID in
-- another account keeps its rows. See issue #841.
DELETE FROM sync_conflicts
WHERE uid = sqlc.arg(uid)
  AND move_source_calendar_id IS NOT NULL
  AND resolved_at IS NOT NULL
  AND (
    calendar_id IN (sqlc.arg(from_calendar_id), sqlc.arg(to_calendar_id))
    OR calendar_id IN (
      SELECT c.id FROM calendars c
      WHERE c.account_id != 0 AND c.account_id IN (
        SELECT m.account_id FROM calendars m
        WHERE m.id IN (sqlc.arg(from_calendar_id), sqlc.arg(to_calendar_id))
      )
    )
  );

-- name: CountMoveRecordBlockers :one
-- Count the rows that keep a pass from a new move record of one UID on one
-- calendar. Such a row is a move conflict row (open, or resolved by the
-- user), or an open edit conflict. The read-only and the gated target paths
-- read it. See issue #841.
SELECT COUNT(*) FROM sync_conflicts
WHERE calendar_id = ? AND uid = ? AND (move_source_calendar_id IS NOT NULL OR resolved_at IS NULL);

-- name: CountOpenMoveConflictsInMoveScope :one
-- Count the open move conflicts of one UID in the calendars of a new move:
-- the two calendars, and every calendar of their accounts. A failed cancel
-- is open too. A new move of the UID waits until the user resolves them.
-- See issue #841.
SELECT COUNT(*) FROM sync_conflicts
WHERE uid = sqlc.arg(uid)
  AND move_source_calendar_id IS NOT NULL
  AND resolved_at IS NULL
  AND (
    calendar_id IN (sqlc.arg(from_calendar_id), sqlc.arg(to_calendar_id))
    OR move_source_calendar_id IN (sqlc.arg(from_calendar_id), sqlc.arg(to_calendar_id))
    OR calendar_id IN (
      SELECT c.id FROM calendars c
      WHERE c.account_id != 0 AND c.account_id IN (
        SELECT m.account_id FROM calendars m
        WHERE m.id IN (sqlc.arg(from_calendar_id), sqlc.arg(to_calendar_id))
      )
    )
  );

-- name: SetMoveConflictCancelRev :exec
-- Record the rev and the id of the source sync row after a cancel of the
-- move. See the columns cancel_source_rev and cancel_source_row_id
-- (migration 058).
UPDATE sync_conflicts SET cancel_source_rev = ?, cancel_source_row_id = ?
WHERE calendar_id = ? AND uid = ? AND move_source_calendar_id = ?;

-- name: SeedMoveConflictWarned :exec
-- Seed the etag of the moved copy when a move conflict is recorded. The
-- copy at that etag is the state that the user moved, so the pull warns
-- only about a later change. See issue #841.
UPDATE sync_conflicts SET warned_etag = sqlc.arg(etag)
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid)
  AND resolved_at IS NULL AND move_source_calendar_id IS NOT NULL;

-- name: MarkMoveConflictWarnedHash :execrows
-- Record that the pull warned about the moved copy with the body hash. A
-- server that gives no etag uses it in place of MarkMoveConflictWarned:
-- the update changes nothing when the last warning named the same body.
-- See issue #841.
UPDATE sync_conflicts SET warned_hash = sqlc.arg(hash)
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid)
  AND resolved_at IS NULL AND move_source_calendar_id IS NOT NULL
  AND warned_hash != sqlc.arg(hash);

-- name: MarkMoveConflictWarned :execrows
-- Record that the pull warned about the moved copy at etag. The update
-- changes nothing when the last warning named the same etag, so the pull
-- warns once per new etag of the copy. See issue #841.
UPDATE sync_conflicts SET warned_etag = sqlc.arg(etag)
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid)
  AND resolved_at IS NULL AND move_source_calendar_id IS NOT NULL
  AND warned_etag != sqlc.arg(etag);

-- name: SetMoveConflictState :execrows
-- Write the state of the move of one move conflict with the expected
-- source calendar. Zero rows means that the conflict changed. See
-- migration 058.
UPDATE sync_conflicts SET move_state = ?
WHERE calendar_id = ? AND uid = ? AND move_source_calendar_id = ?;

-- name: FinishMoveConflict :exec
-- Mark the move of a move conflict finished: the source object is gone from
-- the server. calendar_id is the target of the move. An open row takes the
-- resolution 'local' too: only the create of the moved copy remains. The
-- push of the target must not wait for a pick. A row that the user
-- cancelled stays as it is. See issue #841.
UPDATE sync_conflicts
SET move_state = 'finished',
    resolved_at = COALESCE(resolved_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    resolution = COALESCE(resolution, 'local')
WHERE calendar_id = ? AND uid = ? AND move_source_calendar_id = ?
  AND move_state IN ('open', 'local-picked');

-- name: FinishMoveCancel :execrows
-- Mark a cancel complete after the import of the server body. The state
-- becomes cancelled, and the row takes the resolution 'server'. The row
-- also records the rev of the source row after the import. One statement
-- writes all of them, so no cancelled row exists without its rev. The
-- cancel transaction leaves the row open in the state 'cancel-failed'
-- until this statement runs. See issue #841.
UPDATE sync_conflicts
SET move_state = 'cancelled',
    resolved_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    resolution = 'server',
    cancel_source_rev = sqlc.arg(cancel_source_rev),
    cancel_source_row_id = sqlc.arg(cancel_source_row_id)
WHERE calendar_id = sqlc.arg(calendar_id) AND uid = sqlc.arg(uid)
  AND move_source_calendar_id = sqlc.arg(move_source_calendar_id)
  AND move_state = 'cancel-failed' AND resolved_at IS NULL;

-- name: GetOpenConflictImportBlock :one
-- The open conflict of one UID in one calendar, in a slim form. The row
-- holds the source calendar of a move conflict and the dirty flag of the
-- sync row of the calendar.
-- The pull reads it to decide whether the conflict blocks an import. See
-- issue #841.
SELECT sc.move_source_calendar_id,
       CAST(COALESCE(sr.dirty, 0) AS INTEGER) AS row_dirty
FROM sync_conflicts sc
LEFT JOIN sync_resources sr ON sr.calendar_id = sc.calendar_id AND sr.uid = sc.uid
WHERE sc.calendar_id = sqlc.arg(calendar_id) AND sc.uid = sqlc.arg(uid) AND sc.resolved_at IS NULL;

-- name: ReopenCancelledMoveConflict :execrows
-- Open the row again as a failed cancel at the end of the cancel
-- transaction. The transaction resolved the row for the guard of the move
-- back, and the import of the server body comes after the commit. The row
-- waits in the state 'cancel-failed' until FinishMoveCancel. A failed
-- import keeps it there for the replay. The cancel stamps the state
-- 'cancel-failed' at its start, so the guard names that one state. See
-- issue #841.
UPDATE sync_conflicts SET resolved_at = NULL, resolution = NULL, move_state = 'cancel-failed'
WHERE calendar_id = ? AND uid = ? AND move_source_calendar_id = ?
  AND move_state = 'cancel-failed';

-- name: UpdateSyncConflictCancelRecord :execrows
-- Record the state of a move at its cancel. The live body of the moved copy
-- becomes the local side, and the current source object becomes the server
-- side. A later local pick restores that body with that etag, so its push
-- matches the source object. See issue #841.
UPDATE sync_conflicts
SET local_ical = ?,
    server_ical = ?,
    server_etag = ?
WHERE calendar_id = ? AND uid = ? AND move_source_calendar_id = ?;

-- name: CountOpenMoveConflictsTouchingCalendar :one
-- Count the open move conflicts (a failed cancel included) whose target or
-- source is calendar_id. The moot step reads its views only when the count
-- is above zero, so a pass without a move conflict pays one cheap read.
-- See issue #841.
SELECT COUNT(*) FROM sync_conflicts
WHERE resolved_at IS NULL AND move_source_calendar_id IS NOT NULL
  AND (calendar_id = sqlc.arg(calendar_id) OR move_source_calendar_id = sqlc.arg(calendar_id));

-- name: GetMoveConflictOfUID :one
-- The move conflict row of one UID on one calendar, open or resolved. See
-- issue #841.
SELECT * FROM sync_conflicts
WHERE calendar_id = ? AND uid = ? AND move_source_calendar_id IS NOT NULL;

-- name: ListMootMoveConflicts :many
-- The open move conflicts whose target or source is calendar_id and whose
-- moved copy is gone. The target calendar then holds no live copy of the
-- UID (the view live_move_copies). A cancel whose import failed waits for
-- its replay, so it is not moot. One read serves the moot step of each
-- pass. See issue #841.
SELECT sqlc.embed(sc)
FROM sync_conflicts sc
WHERE sc.resolved_at IS NULL
  AND sc.move_source_calendar_id IS NOT NULL
  AND sc.move_state != 'cancel-failed'
  AND (sc.calendar_id = sqlc.arg(calendar_id) OR sc.move_source_calendar_id = sqlc.arg(calendar_id))
  AND NOT EXISTS (
    SELECT 1 FROM live_move_copies l
    WHERE l.calendar_id = sc.calendar_id AND l.uid = sc.uid AND l.owner_type = sc.owner_type
  )
ORDER BY sc.id;

-- name: GetOpenSyncConflict :one
SELECT * FROM sync_conflicts WHERE calendar_id = ? AND uid = ? AND resolved_at IS NULL;

-- name: CountOpenSyncConflicts :one
SELECT COUNT(*) FROM sync_conflicts WHERE calendar_id = ? AND uid = ? AND resolved_at IS NULL;

-- name: ListSyncConflicts :many
SELECT * FROM sync_conflicts WHERE resolved_at IS NULL ORDER BY detected_at DESC;

-- name: ListSyncConflictsByCalendar :many
SELECT * FROM sync_conflicts WHERE calendar_id = ? AND resolved_at IS NULL ORDER BY detected_at DESC;

-- name: ListResolvedSyncConflicts :many
SELECT * FROM sync_conflicts WHERE resolved_at IS NOT NULL ORDER BY resolved_at DESC;

-- name: MarkSyncConflictResolved :exec
UPDATE sync_conflicts SET resolved_at = ?, resolution = ? WHERE calendar_id = ? AND uid = ?;

-- name: GetSyncConflict :one
SELECT * FROM sync_conflicts WHERE id = ?;

-- name: DeleteSyncConflict :exec
DELETE FROM sync_conflicts WHERE id = ?;

-- name: DeleteSyncConflictsByCalendar :exec
-- Delete the conflicts of one calendar for a reset or an unlink. An open
-- failed cancel stays: its replay writes only the source calendar, and a
-- delete loses the recorded server body that the replay needs. See issue
-- #841.
DELETE FROM sync_conflicts
WHERE calendar_id = ?
  AND NOT (move_state = 'cancel-failed' AND resolved_at IS NULL);

-- name: UpdateSyncConflictServerBody :execrows
-- The update leaves detected_at alone. A refresh records fresher server
-- data; it does not re-date how old the divergence is. A move conflict
-- keeps its body. Its server side is the object of the source calendar, and
-- the pull of the target calendar fetches the target object (issue #841).
UPDATE sync_conflicts
SET server_ical = ?,
    server_etag = ?
WHERE calendar_id = ? AND uid = ? AND resolved_at IS NULL AND move_source_calendar_id IS NULL;

-- name: ListMoveSourcesOfTarget :many
-- The source calendars of the move conflicts stored on one calendar, open
-- or resolved. A reset or an unlink of the calendar deletes those rows,
-- so it locks the sources. See issue #841.
SELECT DISTINCT move_source_calendar_id FROM sync_conflicts
WHERE calendar_id = sqlc.arg(calendar_id) AND move_source_calendar_id IS NOT NULL
ORDER BY move_source_calendar_id;

-- name: ListMoveTargetsOfSource :many
-- The target calendars of the live move conflicts whose source is one
-- calendar. A reset of the source locks them. See issue #841.
SELECT DISTINCT calendar_id FROM sync_conflicts
WHERE move_source_calendar_id = sqlc.arg(source_calendar_id)
  AND move_state IN ('open', 'local-picked', 'cancel-failed')
ORDER BY calendar_id;

-- name: DetachMoveConflictsOfSource :exec
-- End the live move conflicts whose source is one calendar as detached. An
-- unlink of the source deletes its tombstones, so the move can never
-- finish. A failed cancel ends too: its replay cannot reach a server any
-- more. A resolved row keeps its resolution and resolved_at. A reset does
-- not call it: the source stays linked, and the reset keeps the move
-- tombstones of live moves. See issue #841.
UPDATE sync_conflicts
SET move_state = 'detached',
    resolved_at = COALESCE(resolved_at, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    resolution = COALESCE(resolution, 'server-auto')
WHERE move_source_calendar_id = sqlc.arg(source_calendar_id)
  AND move_state IN ('open', 'local-picked', 'cancel-failed');
