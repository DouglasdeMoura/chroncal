package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// healLegacyEventOverrides moves the legacy overrides of the local calendars
// into the calendar of the series that owns them (issue #862). A legacy
// override is an override row in a calendar with no master row of its UID
// (see LegacyOverride). An older version saved such a row on a calendar
// change. The series that owns it is the one that the view
// event_series_rows names (GetSeriesMasterOfCalendar).
//
// The heal moves the rows of one UID in one calendar as one unit, with
// their delete-log rows (MoveEventUIDRows). It moves the rows only when all
// of these conditions are true:
//   - The source calendar and the owner calendar are quiet (localQuietTx):
//     no account link, no read-only access, and no sync row, tombstone, or
//     open sync conflict of the UID. The heal then makes no server write,
//     and no pull can prune the moved rows. An older version did not always
//     put the rows into the server object of the owner.
//   - The calendars hold no open move conflict of the UID
//     (CountOpenMoveConflictsInMoveScope).
//   - The owner master is live. A series in the trash keeps its rows as
//     they are.
//   - The owner calendar holds no row, live or in the trash, with the UID
//     and a recurrence ID of a moved row. The move would break the UNIQUE
//     key. The tie rule (uidmove.TieBefore) keeps such a row in use.
//
// Else the rows stay, and the code paths for a legacy override handle them.
// The sync doctor lists the rows that stay, with the command that moves
// them. The checks use the queries of the move rules in uidmove: the owner
// rule of the view, the collision count, and the conflict counts. uidmove
// imports storage, so the heal cannot call it. The transaction of each UID
// reads every condition again, so a write of another process between the
// list and the move cannot pass.
//
// The heal then calls MarkResourceDirty for the owner calendar. A local
// calendar gets no sync row, but the call clears a stale edit_unowned flag
// of the moved rows.
//
// A failure is fatal to Open, like every startup heal. The function runs on
// every startup. It changes nothing when no local calendar holds a legacy
// override that it can move.
func healLegacyEventOverrides(ctx context.Context, conn *sql.DB, q *Queries) error {
	candidates, err := q.ListLocalLegacyEventOverrideUIDs(ctx)
	if err != nil {
		return fmt.Errorf("list legacy overrides: %w", err)
	}
	for _, c := range candidates {
		if err := healLegacyEventOverride(ctx, conn, q, c.CalendarID, c.Uid); err != nil {
			return fmt.Errorf("uid %s in calendar %d: %w", c.Uid, c.CalendarID, err)
		}
	}
	return nil
}

// healLegacyEventOverride moves the legacy overrides of uid in calendarID to
// the calendar of their owner, in one transaction. It does nothing when a
// condition of healLegacyEventOverrides is false.
func healLegacyEventOverride(ctx context.Context, conn *sql.DB, q *Queries, calendarID int64, uid string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := q.WithTx(tx)

	owner, err := qtx.GetSeriesMasterOfCalendar(ctx, GetSeriesMasterOfCalendarParams{
		CalendarID: calendarID, Uid: uid, IncludeDeleted: true,
	})
	if found, err := Found(err); err != nil {
		return fmt.Errorf("find owner: %w", err)
	} else if !found || owner.DeletedAt != nil || owner.CalendarID == calendarID {
		return nil
	}
	for _, id := range []int64{calendarID, owner.CalendarID} {
		if quiet, err := localQuietTx(ctx, qtx, id, uid); err != nil || !quiet {
			return err
		}
	}
	moves, err := qtx.CountOpenMoveConflictsInMoveScope(ctx, CountOpenMoveConflictsInMoveScopeParams{
		Uid: uid, FromCalendarID: calendarID, ToCalendarID: owner.CalendarID,
	})
	if err != nil {
		return fmt.Errorf("count open move conflicts: %w", err)
	}
	if moves > 0 {
		return nil
	}
	collisions, err := qtx.CountEventUIDMoveCollisions(ctx, CountEventUIDMoveCollisionsParams{
		ToCalendarID: owner.CalendarID, Uid: uid, FromCalendarID: calendarID,
	})
	if err != nil {
		return fmt.Errorf("count collisions: %w", err)
	}
	if collisions.Live > 0 || collisions.Trashed > 0 {
		return nil
	}
	if err := MoveEventUIDRows(ctx, qtx, MoveEventUIDToCalendarParams{
		FromCalendarID: calendarID, ToCalendarID: owner.CalendarID, Uid: uid,
	}, true); err != nil {
		return err
	}
	if err := MarkResourceDirty(ctx, tx, owner.CalendarID, uid, "event"); err != nil {
		return fmt.Errorf("mark owner dirty: %w", err)
	}
	return tx.Commit()
}

// localQuietTx reports whether calendarID is a local calendar with no sync
// state of uid: no account link, no read-only access (a user edit there
// refuses), and no sync row, tombstone, or open sync conflict of uid. An
// unlink can leave such state.
func localQuietTx(ctx context.Context, qtx *Queries, calendarID int64, uid string) (bool, error) {
	cal, err := qtx.GetCalendar(ctx, calendarID)
	if err != nil {
		return false, fmt.Errorf("get calendar %d: %w", calendarID, err)
	}
	if CalendarLinked(cal) || CalendarReadOnly(cal.RemoteAccess) {
		return false, nil
	}
	_, err = qtx.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	if found, err := Found(err); err != nil || found {
		return false, err
	}
	_, err = qtx.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid})
	if found, err := Found(err); err != nil || found {
		return false, err
	}
	open, err := qtx.CountOpenSyncConflicts(ctx, CountOpenSyncConflictsParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		return false, fmt.Errorf("count open conflicts: %w", err)
	}
	return open == 0, nil
}
