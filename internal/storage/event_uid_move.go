package storage

import (
	"context"
	"fmt"
)

// MoveEventUIDRows moves the event rows and the delete-log rows of one UID to
// another calendar. uidmove.Rows.Record calls it, after the write of the
// moved row. The heal of legacy overrides (healLegacyEventOverrides) calls
// it too. The log keys are per calendar (migration 057). targetHoldsMaster
// reports whether the target holds a master row of the UID, live or in the
// trash, before that write. With a master, the move is a legacy override that
// goes back to its master. The logs of the target are the logs of that
// series, so they stay. The source rows whose keys collide go. Without a
// master, the move is a series move. The logs of the moved series win, so the
// target rows whose keys collide go. The rest of the target (a stale row of
// another key) stays.
func MoveEventUIDRows(ctx context.Context, q *Queries, arg MoveEventUIDToCalendarParams, targetHoldsMaster bool) error {
	if err := q.MoveEventUIDToCalendar(ctx, arg); err != nil {
		return fmt.Errorf("move events: %w", err)
	}
	if targetHoldsMaster {
		if err := deleteCollidingEventLogRows(ctx, q, arg); err != nil {
			return err
		}
		// A legacy row that goes home carries no series delete: the
		// series of the target owns it. The series-delete log of the
		// source stays out of the target.
		if err := q.DeleteEventSeriesDeletesByCalendarUID(ctx, DeleteEventSeriesDeletesByCalendarUIDParams{CalendarID: arg.FromCalendarID, Uid: arg.Uid}); err != nil {
			return fmt.Errorf("delete source event_series_deletes: %w", err)
		}
		// The source calendar holds no master, so its occurrence logs record
		// no delete of the target master. Moved into the target, they would
		// strip an imported EXDATE of that master (issue #86). They go.
		if err := q.DeleteEventExdateDeletesByCalendarUID(ctx, DeleteEventExdateDeletesByCalendarUIDParams{CalendarID: arg.FromCalendarID, Uid: arg.Uid}); err != nil {
			return fmt.Errorf("delete source event_exdate_deletes: %w", err)
		}
	} else {
		// The same queries with the calendars swapped drop the target
		// rows whose keys the source holds. A series-delete log in a
		// calendar with no master of the UID is stale in any case: the
		// moved series brings its own, or none.
		if err := deleteCollidingEventLogRows(ctx, q, MoveEventUIDToCalendarParams{
			FromCalendarID: arg.ToCalendarID, ToCalendarID: arg.FromCalendarID, Uid: arg.Uid,
		}); err != nil {
			return err
		}
		if err := q.DeleteEventSeriesDeletesByCalendarUID(ctx, DeleteEventSeriesDeletesByCalendarUIDParams{CalendarID: arg.ToCalendarID, Uid: arg.Uid}); err != nil {
			return fmt.Errorf("delete stale event_series_deletes: %w", err)
		}
	}
	if err := q.MoveEventExdateDeletesUIDToCalendar(ctx, MoveEventExdateDeletesUIDToCalendarParams(arg)); err != nil {
		return fmt.Errorf("move event_exdate_deletes: %w", err)
	}
	if err := q.MoveEventTruncateDeletesUIDToCalendar(ctx, MoveEventTruncateDeletesUIDToCalendarParams(arg)); err != nil {
		return fmt.Errorf("move event_truncate_deletes: %w", err)
	}
	if err := q.MoveEventSeriesDeletesUIDToCalendar(ctx, MoveEventSeriesDeletesUIDToCalendarParams(arg)); err != nil {
		return fmt.Errorf("move event_series_deletes: %w", err)
	}
	return nil
}

// deleteCollidingEventLogRows drops the delete-log rows of the source
// calendar of arg whose keys the target calendar of arg holds. A call
// with the calendars swapped drops the target rows instead.
func deleteCollidingEventLogRows(ctx context.Context, q *Queries, arg MoveEventUIDToCalendarParams) error {
	if err := q.DeleteEventExdateDeletesCollidingWithMove(ctx, DeleteEventExdateDeletesCollidingWithMoveParams{FromCalendarID: arg.FromCalendarID, ToCalendarID: arg.ToCalendarID, Uid: arg.Uid}); err != nil {
		return fmt.Errorf("delete colliding event_exdate_deletes: %w", err)
	}
	if err := q.DeleteEventTruncateDeletesCollidingWithMove(ctx, DeleteEventTruncateDeletesCollidingWithMoveParams{FromCalendarID: arg.FromCalendarID, ToCalendarID: arg.ToCalendarID, Uid: arg.Uid}); err != nil {
		return fmt.Errorf("delete colliding event_truncate_deletes: %w", err)
	}
	return nil
}
