package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
//   - The source calendar has no account link, and no sync row or
//     tombstone of the UID. The heal then makes no server write.
//   - The owner master is live. A series in the trash keeps its rows as
//     they are.
//   - The owner calendar is not read-only. Its server object can lack the
//     rows, and the next pull would then prune them.
//   - The owner calendar has no open sync conflict of the UID.
//   - The owner calendar holds no row, live or in the trash, with the UID
//     and a recurrence ID of a moved row. The move would break the UNIQUE
//     key. The tie rule (uidmove.TieBefore) keeps such a row in use.
//
// Else the rows stay, and the code paths for a legacy override handle them.
// A linked owner calendar gets the dirty mark, so its server object holds the
// moved rows. Its export held them before the move, so the body does not
// change.
//
// Each UID moves in its own transaction. A failure is fatal to Open, like
// every startup heal. The function runs on every startup. It changes nothing
// when no local calendar holds a legacy override that it can move.
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
	cal, err := qtx.GetCalendar(ctx, owner.CalendarID)
	if err != nil {
		return fmt.Errorf("get owner calendar: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(cal.RemoteAccess), "read") {
		return nil
	}
	open, err := qtx.CountOpenSyncConflicts(ctx, CountOpenSyncConflictsParams{CalendarID: owner.CalendarID, Uid: uid})
	if err != nil {
		return fmt.Errorf("count open conflicts: %w", err)
	}
	if open > 0 {
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
	if CalendarLinked(cal) {
		if err := MarkResourceDirty(ctx, tx, owner.CalendarID, uid, "event"); err != nil {
			return fmt.Errorf("mark owner dirty: %w", err)
		}
	}
	return tx.Commit()
}
