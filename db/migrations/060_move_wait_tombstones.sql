-- +goose Up
-- The tombstones that a calendar move still needs. The tombstone pass
-- keeps them, the push after a move waits for them, and the stale
-- tombstone cleanup skips them. A tombstone is in the view when one of
-- these holds:
--   - an open move conflict names its calendar as the source. A conflict
--     on a calendar that an account delete left unlinked does not count.
--     Such a calendar has no account, but it keeps its remote_url for a
--     re-link.
--   - the target row of its move has no server object, and the target
--     calendar has an account
--   - the target row of its move has an open conflict of any kind, and
--     the target calendar has an account
--   - it is a tombstone from before the upgrade (legacy_move). Also, a
--     sibling calendar of the same account holds a row of the UID without a
--     server object. That row still waits for a push (dirty). A move from
--     before the upgrade looks like that, so its source delete waits for
--     the create. A row that the organizer gate cleared never pushes, so it
--     does not hold the delete. The push of the sibling sends that delete
--     itself when the server refuses the create (settleMovedSources). A new
--     plain delete never waits.
-- A local target (an unlinked calendar included) falls back to the rule
-- of a live copy (the view live_move_copies) in the tombstone pass. The
-- three queries that read the view share this one predicate. See issue
-- #841.
-- The open move conflicts that hold the source of a move: calendar_id
-- holds the conflict, and source_calendar_id is the source of the move. A
-- conflict on a calendar that an account delete left unlinked does not
-- count. Such a calendar has no account, but it keeps its remote_url for a
-- re-link. The wait view below, the hold check of the clear, and the list
-- of push waits read this one view.
CREATE VIEW open_move_holders AS
SELECT sc.calendar_id, sc.uid, sc.move_source_calendar_id AS source_calendar_id, cc.name AS calendar_name
FROM sync_conflicts sc
JOIN calendars cc ON cc.id = sc.calendar_id
WHERE sc.resolved_at IS NULL
  AND sc.move_source_calendar_id IS NOT NULL
  AND NOT ((cc.account_id IS NULL OR cc.account_id = 0) AND cc.remote_url IS NOT NULL AND cc.remote_url != '');

CREATE VIEW move_wait_tombstones AS
SELECT t.id, t.calendar_id, t.uid, t.moved_to_calendar_id
FROM tombstones t
WHERE EXISTS (
    SELECT 1 FROM open_move_holders h
    WHERE h.source_calendar_id = t.calendar_id
      AND h.uid = t.uid
)
OR EXISTS (
    SELECT 1 FROM sync_resources sr
    JOIN calendars tc ON tc.id = sr.calendar_id
    WHERE sr.calendar_id = t.moved_to_calendar_id
      AND sr.uid = t.uid
      AND sr.remote_url = ''
      AND tc.account_id IS NOT NULL AND tc.account_id != 0
)
OR EXISTS (
    SELECT 1 FROM sync_conflicts sc
    JOIN calendars tc ON tc.id = sc.calendar_id
    WHERE sc.calendar_id = t.moved_to_calendar_id
      AND sc.uid = t.uid
      AND sc.resolved_at IS NULL
      AND tc.account_id IS NOT NULL AND tc.account_id != 0
)
OR (t.moved_to_calendar_id IS NULL AND t.legacy_move = 1 AND EXISTS (
    SELECT 1
    FROM sync_resources sr
    JOIN calendars c ON c.id = sr.calendar_id
    JOIN calendars own ON own.id = t.calendar_id
    WHERE c.account_id = own.account_id
      AND c.account_id != 0
      AND c.id != t.calendar_id
      AND sr.uid = t.uid
      AND sr.remote_url = ''
      AND sr.dirty = 1
));

-- The live copies of a UID in a calendar: live rows exist, and no tombstone
-- of the UID waits there. A remote calendar also holds the sync row of the
-- UID: a copy without it never pushes. A moved copy is gone when its target
-- calendar is not in this view. The resolver (MovedCopyExists), the settle,
-- and the moot step (ListMootMoveConflicts) read the view, so all apply one
-- rule. The link of a calendar and a sync reset give each moved copy its
-- sync row (MarkMovedCopiesDirtyInCalendar).
CREATE VIEW live_move_copies AS
SELECT x.calendar_id, x.uid, x.owner_type
FROM (
    SELECT calendar_id, uid, 'event' AS owner_type FROM events WHERE deleted_at IS NULL
    UNION ALL
    SELECT calendar_id, uid, 'todo' AS owner_type FROM todos WHERE deleted_at IS NULL
    UNION ALL
    SELECT calendar_id, uid, 'journal' AS owner_type FROM journals WHERE deleted_at IS NULL
) x
JOIN calendars c ON c.id = x.calendar_id
WHERE NOT EXISTS (SELECT 1 FROM tombstones t WHERE t.calendar_id = x.calendar_id AND t.uid = x.uid)
  AND (c.account_id IS NULL OR c.account_id = 0
       OR EXISTS (SELECT 1 FROM sync_resources sr WHERE sr.calendar_id = x.calendar_id AND sr.uid = x.uid));

-- The tombstone pass and the resolve read tombstones by UID.
CREATE INDEX idx_tombstones_uid ON tombstones(uid);

-- wait_kind names the step that the push of a sync row waits for. 0 is no
-- wait. 1 is a hold by an open move conflict of the calendar
-- wait_holder_calendar_id. 2 is a wait for the source delete of the UID in
-- another calendar. A wait is no failure, so it does not change
-- push_fail_count. The list of waits (ListWaitingSyncResources) shows a row
-- only while the cause of its wait still holds, so no other path must clear
-- the columns. The push resets them on a success or on a failure.
ALTER TABLE sync_resources ADD COLUMN wait_kind INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_resources ADD COLUMN wait_holder_calendar_id INTEGER NOT NULL DEFAULT 0;


-- +goose Down
ALTER TABLE sync_resources DROP COLUMN wait_holder_calendar_id;
ALTER TABLE sync_resources DROP COLUMN wait_kind;
DROP INDEX IF EXISTS idx_tombstones_uid;
DROP VIEW IF EXISTS live_move_copies;
DROP VIEW IF EXISTS move_wait_tombstones;
DROP VIEW IF EXISTS open_move_holders;
