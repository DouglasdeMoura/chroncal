// Package uidmove holds the calendar-move rules that the event, todo, and
// journal services share (issue #828). One CalDAV object holds the master
// and all overrides of one UID, so the rows of a UID move as one unit. The
// package keeps the rules in one place, so a fix cannot drift across the
// three domains.
//
// Call each function inside the transaction of the update. A check outside
// the transaction can see a state that another write changes before the
// move.
package uidmove

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// Rows adapts the generated queries of one domain to the operations of a
// UID move. Each domain binds its sqlc methods to the transaction and to
// the UID.
type Rows struct {
	// Q is the Queries of the transaction. CheckTarget reads the calendar
	// name from it, and Record passes it to storage.RecordResourceMove.
	Q *storage.Queries
	// UID is the UID of the rows that move.
	UID string
	// ErrInstanceMove is the domain error for a refused move of one
	// override row.
	ErrInstanceMove error
	// ErrTargetHasUID is the domain error for a move into a calendar that
	// holds a row of the UID with the same recurrence ID.
	ErrTargetHasUID error
	// HasMaster reports whether calendarID holds a live master row of the
	// UID.
	HasMaster func(ctx context.Context, calendarID int64) (bool, error)
	// MasterCalendars returns the calendars that hold a live master row of
	// the UID. DirtyCalendar reads it.
	MasterCalendars func(ctx context.Context) ([]int64, error)
	// CountCollisions counts the rows (live or soft-deleted) in
	// toCalendarID that share the UID and a recurrence ID with a row in
	// fromCalendarID. Leave it nil when the schema of the domain keeps a
	// UID and a recurrence ID unique across all calendars. A collision then
	// cannot occur.
	CountCollisions func(ctx context.Context, fromCalendarID, toCalendarID int64) (int64, error)
	// MoveRows moves every row of the UID and its delete-log rows from one
	// calendar to another.
	MoveRows func(ctx context.Context, fromCalendarID, toCalendarID int64) error
}

// Found converts the error of a single-row lookup to a found flag.
// sql.ErrNoRows gives false and no error.
func Found(err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

// Check applies CheckOverrideMove and then CheckTarget to an update of one
// row by its ID. recurrenceID is the recurrence ID of that row.
func (r Rows) Check(ctx context.Context, recurrenceID string, fromCalendarID, toCalendarID int64) error {
	if err := r.CheckOverrideMove(ctx, recurrenceID, fromCalendarID, toCalendarID); err != nil {
		return err
	}
	return r.CheckTarget(ctx, fromCalendarID, toCalendarID)
}

// CheckOverrideMove refuses the calendar move of one override row. One
// CalDAV object holds the whole series, so an override cannot move alone.
// An older version saved an override in another calendar on a calendar
// change. Such a legacy override has no master in its own calendar, and it
// can move back to a calendar with a master. The rule does nothing for a
// master row (an empty recurrenceID), for an update that keeps the
// calendar, or for an update that names no calendar (toCalendarID 0).
func (r Rows) CheckOverrideMove(ctx context.Context, recurrenceID string, fromCalendarID, toCalendarID int64) error {
	if recurrenceID == "" || fromCalendarID == toCalendarID || toCalendarID == 0 {
		return nil
	}
	ownMaster, err := r.HasMaster(ctx, fromCalendarID)
	if err != nil {
		return fmt.Errorf("find master: %w", err)
	}
	targetMaster, err := r.HasMaster(ctx, toCalendarID)
	if err != nil {
		return fmt.Errorf("find master: %w", err)
	}
	if ownMaster || !targetMaster {
		return r.ErrInstanceMove
	}
	return nil
}

// CheckTarget refuses a calendar move of the rows of the UID when the
// target calendar already holds a row (live or soft-deleted) with the same
// UID and recurrence ID. Without the check the move fails on the UNIQUE key
// with a raw constraint error. It does nothing when the two calendars are
// the same, when toCalendarID is 0, or when CountCollisions is nil.
func (r Rows) CheckTarget(ctx context.Context, fromCalendarID, toCalendarID int64) error {
	if fromCalendarID == toCalendarID || toCalendarID == 0 || r.CountCollisions == nil {
		return nil
	}
	n, err := r.CountCollisions(ctx, fromCalendarID, toCalendarID)
	if err != nil {
		return fmt.Errorf("check move target: %w", err)
	}
	if n == 0 {
		return nil
	}
	name := fmt.Sprint(toCalendarID)
	if r.Q != nil {
		if cal, err := r.Q.GetCalendar(ctx, toCalendarID); err == nil {
			name = cal.Name
		}
	}
	return fmt.Errorf("%w: calendar %q already holds a row of UID %s; the row can be in the trash, so purge or restore it first",
		r.ErrTargetHasUID, name, r.UID)
}

// DirtyCalendar returns the calendar whose sync row a write of one row of
// the UID must mark dirty. recurrenceID and calendarID are the fields of
// the row after the write.
//
// The export of a UID holds the master and every override, in all
// calendars (issue #842). A legacy override is an override row in a
// calendar with no master of the UID. A dirty mark on its calendar uploads
// the whole series into that calendar a second time. For a legacy override
// with one master, the mark goes to the calendar of the master. Each other
// row (a master, an override beside its master, or a legacy override with
// no single master) marks its own calendar.
func (r Rows) DirtyCalendar(ctx context.Context, recurrenceID string, calendarID int64) (int64, error) {
	if recurrenceID == "" {
		return calendarID, nil
	}
	own, err := r.HasMaster(ctx, calendarID)
	if err != nil {
		return 0, fmt.Errorf("find master: %w", err)
	}
	if own {
		return calendarID, nil
	}
	cals, err := r.MasterCalendars(ctx)
	if err != nil {
		return 0, fmt.Errorf("find master calendars: %w", err)
	}
	if len(cals) == 1 {
		return cals[0], nil
	}
	return calendarID, nil
}

// Record records the move of the rows of the UID from one calendar to
// another. It does nothing when the two calendars are the same. See
// storage.RecordResourceMove.
func (r Rows) Record(ctx context.Context, fromCalendarID, toCalendarID int64) error {
	return storage.RecordResourceMove(ctx, r.Q, fromCalendarID, toCalendarID, r.UID, func(ctx context.Context) error {
		return r.MoveRows(ctx, fromCalendarID, toCalendarID)
	})
}
