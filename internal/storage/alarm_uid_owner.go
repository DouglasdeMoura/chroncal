package storage

import "context"

// Alarm UIDs are unique per table: idx_event_alarms_uid and
// idx_todo_alarms_uid are partial unique indexes on the UID column. An
// import that meets a file UID already stored on another owner cannot keep
// it, so the alarm service mints a local UID for the row it writes
// (issue #915). The import comparison needs the same knowledge before it
// decides that a file changes nothing, so these helpers answer whether a
// UID belongs to another owner.

// AlarmUIDTakenByAnotherEvent reports whether uid is stored on an alarm of
// an event other than eventID.
func (q *Queries) AlarmUIDTakenByAnotherEvent(ctx context.Context, uid string, eventID int64) (bool, error) {
	return q.rowExists(ctx, `SELECT EXISTS(SELECT 1 FROM event_alarms WHERE uid = ? AND event_id <> ?)`, uid, eventID)
}

// AlarmUIDTakenByAnotherTodo reports whether uid is stored on an alarm of a
// todo other than todoID.
func (q *Queries) AlarmUIDTakenByAnotherTodo(ctx context.Context, uid string, todoID int64) (bool, error) {
	return q.rowExists(ctx, `SELECT EXISTS(SELECT 1 FROM todo_alarms WHERE uid = ? AND todo_id <> ?)`, uid, todoID)
}

// rowExists runs a query that yields 1 when at least one row matches, and
// 0 otherwise.
func (q *Queries) rowExists(ctx context.Context, query string, args ...any) (bool, error) {
	var found int64
	if err := q.db.QueryRowContext(ctx, query, args...).Scan(&found); err != nil {
		return false, err
	}
	return found != 0, nil
}
