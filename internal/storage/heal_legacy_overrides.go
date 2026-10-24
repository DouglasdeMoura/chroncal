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
//   - The source calendar and the owner calendar are quiet (localQuiet):
//     no account link, no read-only access, and no sync row, tombstone, or
//     open sync conflict of the UID. The heal then makes no server write,
//     and no pull can prune the moved rows. An older version did not always
//     put the rows into the server object of the owner.
//   - The owner master is live. A series in the trash keeps its rows as
//     they are.
//   - No rule of a user move refuses the move (LegacyMoveRefusal): no open
//     move conflict of the UID, and no row of the owner calendar with the
//     UID and a recurrence ID of a moved row. Such a row would break the
//     UNIQUE key. The tie rule (uidmove.TieBefore) keeps it in use.
//
// Else the rows stay, and the code paths for a legacy override handle them.
// The sync doctor lists the rows that stay, with the reason. It gives the
// command that moves them only when no rule of a user move refuses it. The checks use the queries of the move rules in uidmove: the owner
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
//
// The checks run first with no transaction. A row that stays then takes no
// write lock on startup: the connection opens each transaction with
// _txlock=immediate, and a sync of another process can hold the lock. The
// transaction of the move runs the checks again.
func healLegacyEventOverride(ctx context.Context, conn *sql.DB, q *Queries, calendarID int64, uid string) error {
	if _, ok, err := legacyMoveTarget(ctx, q, calendarID, uid); err != nil || !ok {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := q.WithTx(tx)
	ownerCalendarID, ok, err := legacyMoveTarget(ctx, qtx, calendarID, uid)
	if err != nil || !ok {
		return err
	}
	if err := MoveEventUIDRows(ctx, qtx, MoveEventUIDToCalendarParams{
		FromCalendarID: calendarID, ToCalendarID: ownerCalendarID, Uid: uid,
	}, true); err != nil {
		return err
	}
	if err := MarkResourceDirty(ctx, tx, ownerCalendarID, uid, "event"); err != nil {
		return fmt.Errorf("mark owner dirty: %w", err)
	}
	return tx.Commit()
}

// legacyMoveTarget checks the conditions of healLegacyEventOverrides for the
// legacy overrides of uid in calendarID. ok reports whether the heal moves
// them. ownerCalendarID is the calendar of the owner.
func legacyMoveTarget(ctx context.Context, q *Queries, calendarID int64, uid string) (ownerCalendarID int64, ok bool, err error) {
	owner, err := q.GetSeriesMasterOfCalendar(ctx, GetSeriesMasterOfCalendarParams{
		CalendarID: calendarID, Uid: uid, IncludeDeleted: true,
	})
	if found, err := Found(err); err != nil {
		return 0, false, fmt.Errorf("find owner: %w", err)
	} else if !found || owner.DeletedAt != nil || owner.CalendarID == calendarID {
		return 0, false, nil
	}
	for _, id := range []int64{calendarID, owner.CalendarID} {
		if quiet, err := localQuiet(ctx, q, id, uid); err != nil || !quiet {
			return 0, false, err
		}
	}
	if refusal, err := LegacyMoveRefusal(ctx, q, calendarID, owner.CalendarID, uid); err != nil || refusal != "" {
		return 0, false, err
	}
	return owner.CalendarID, true, nil
}

// LegacyMoveRefusal names the rule that refuses the move of the legacy
// overrides of uid from calendarID to ownerCalendarID. It returns "" when
// no rule refuses the move. The rules are the rules of a user move
// (uidmove.Rows.Update and RecordResourceMove):
//   - Each calendar holds no open sync conflict of the UID.
//   - The calendars hold no open move conflict of the UID.
//   - The owner calendar holds no row, live or in the trash, with the UID
//     and a recurrence ID of a moved row.
//
// The access rule (calendaraccess.EnsureWritable) is not here, because
// calendaraccess imports storage. The startup heal checks the read-only
// access in localQuiet. The sync doctor checks it with calendaraccess.
func LegacyMoveRefusal(ctx context.Context, q *Queries, calendarID, ownerCalendarID int64, uid string) (string, error) {
	for _, id := range []int64{calendarID, ownerCalendarID} {
		open, err := q.CountOpenSyncConflicts(ctx, CountOpenSyncConflictsParams{CalendarID: id, Uid: uid})
		if err != nil {
			return "", fmt.Errorf("count open conflicts: %w", err)
		}
		if open > 0 {
			return fmt.Sprintf("calendar %q holds an open sync conflict of the UID; resolve it with chroncal sync resolve first",
				CalendarLabel(ctx, q, id)), nil
		}
	}
	moves, err := q.CountOpenMoveConflictsInMoveScope(ctx, CountOpenMoveConflictsInMoveScopeParams{
		Uid: uid, FromCalendarID: calendarID, ToCalendarID: ownerCalendarID,
	})
	if err != nil {
		return "", fmt.Errorf("count open move conflicts: %w", err)
	}
	if moves > 0 {
		return "an open move conflict of the UID waits for the user; resolve it with chroncal sync resolve first", nil
	}
	collisions, err := q.CountEventUIDMoveCollisions(ctx, CountEventUIDMoveCollisionsParams{
		ToCalendarID: ownerCalendarID, Uid: uid, FromCalendarID: calendarID,
	})
	if err != nil {
		return "", fmt.Errorf("count collisions: %w", err)
	}
	owner := func() string { return CalendarLabel(ctx, q, ownerCalendarID) }
	switch {
	case collisions.Live > 0 && collisions.Trashed > 0:
		return fmt.Sprintf("calendar %q holds a live row and a row in the trash at the same occurrence; delete the live row, and purge or restore the row in the trash first", owner()), nil
	case collisions.Live > 0:
		return fmt.Sprintf("calendar %q holds a live row at the same occurrence; delete or move that row first", owner()), nil
	case collisions.Trashed > 0:
		return fmt.Sprintf("calendar %q holds a row in the trash at the same occurrence; purge or restore that row first", owner()), nil
	}
	return "", nil
}

// localQuiet reports whether calendarID is a local calendar with no sync
// state of uid: no account link, no read-only access (a user edit there
// refuses), and no sync row, tombstone, or open sync conflict of uid. An
// unlink can leave such state.
func localQuiet(ctx context.Context, q *Queries, calendarID int64, uid string) (bool, error) {
	cal, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		return false, fmt.Errorf("get calendar %d: %w", calendarID, err)
	}
	if CalendarLinked(cal) || CalendarReadOnly(cal.RemoteAccess) {
		return false, nil
	}
	_, err = q.GetSyncResource(ctx, GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	if found, err := Found(err); err != nil || found {
		return false, err
	}
	_, err = q.GetTombstoneByCalendarAndUID(ctx, GetTombstoneByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid})
	if found, err := Found(err); err != nil || found {
		return false, err
	}
	open, err := q.CountOpenSyncConflicts(ctx, CountOpenSyncConflictsParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		return false, fmt.Errorf("count open conflicts: %w", err)
	}
	return open == 0, nil
}
