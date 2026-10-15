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
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
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
	// Masters returns the master rows of the UID in all calendars, live
	// and in the trash (Master.Trashed). Check reads it.
	Masters func(ctx context.Context) ([]Master, error)
	// CountCollisions counts the live rows and the soft-deleted rows in
	// toCalendarID that share the UID and a recurrence ID with a row in
	// fromCalendarID. Leave it nil when the schema of the domain keeps a
	// UID and a recurrence ID unique across all calendars. A collision then
	// cannot occur.
	CountCollisions func(ctx context.Context, fromCalendarID, toCalendarID int64) (live, trashed int64, err error)
	// MoveRows moves every row of the UID and its delete-log rows from one
	// calendar to another.
	MoveRows func(ctx context.Context, fromCalendarID, toCalendarID int64) error
}

// Master names one master row of the UID.
type Master struct {
	ID         int64
	CalendarID int64
	// Trashed is true for a soft-deleted master row.
	Trashed bool
}

// OccurrenceMoveError reports a refused calendar move of one override row.
// It wraps the domain error (Rows.ErrInstanceMove), so errors.Is still
// matches that error. Masters lists the masters that the row can belong
// to: the master in the calendar of the row, or else every master of the
// UID. A caller can name the series with it, for example in a CLI hint.
type OccurrenceMoveError struct {
	Err     error
	Masters []Master
}

func (e *OccurrenceMoveError) Error() string { return e.Err.Error() }

func (e *OccurrenceMoveError) Unwrap() error { return e.Err }

// Check applies the override rule (CheckOverrideMove) and then
// CheckTarget to an update of one row by its ID. recurrenceID is the
// recurrence ID of that row. It reads the masters of the UID only when the
// override rule applies.
func (r Rows) Check(ctx context.Context, recurrenceID string, fromCalendarID, toCalendarID int64) error {
	var masters []Master
	if moveNeedsCheck(recurrenceID, fromCalendarID, toCalendarID) {
		var err error
		if masters, err = r.Masters(ctx); err != nil {
			return fmt.Errorf("find masters: %w", err)
		}
	}
	return r.CheckOf(ctx, recurrenceID, fromCalendarID, toCalendarID, masters)
}

// CheckOverrideMove refuses the calendar move of one override row.
// masters holds every master row of the UID, live and in the trash. The
// legacy rule needs the masters in the trash too. One CalDAV object holds the
// whole series, so an override cannot move alone. An older version saved
// an override in another calendar on a calendar change. Such a legacy
// override has no master in its own calendar, and it can move back to a
// calendar with a master. The rule does nothing for a master row (an empty
// recurrenceID), for an update that keeps the calendar, or for an update
// that names no calendar (toCalendarID 0). A fromCalendarID of 0 says that
// the calendar of the occurrence is not known. Calendar 0 holds no master,
// so the rule then refuses a move to a calendar with no master, and the
// refusal names every master.
//
// A refusal is an *OccurrenceMoveError. The function reads nothing.
func (r Rows) CheckOverrideMove(recurrenceID string, fromCalendarID, toCalendarID int64, masters []Master) error {
	if !moveNeedsCheck(recurrenceID, fromCalendarID, toCalendarID) {
		return nil
	}
	if overrideMoveAllowed(HasMasterIn(masters, fromCalendarID), hasLiveMasterIn(masters, toCalendarID)) {
		return nil
	}
	return r.occurrenceMoveError(fromCalendarID, masters)
}

// CheckOf is Check for a caller that already holds every master row of
// the UID, live and in the trash. It reads the target calendar only (CheckTarget).
func (r Rows) CheckOf(ctx context.Context, recurrenceID string, fromCalendarID, toCalendarID int64, masters []Master) error {
	if err := r.CheckOverrideMove(recurrenceID, fromCalendarID, toCalendarID, masters); err != nil {
		return err
	}
	return r.CheckTarget(ctx, fromCalendarID, toCalendarID)
}

// moveNeedsCheck reports whether the override rule applies: an override row
// (recurrenceID not empty) that an update sends to another named calendar.
func moveNeedsCheck(recurrenceID string, fromCalendarID, toCalendarID int64) bool {
	return recurrenceID != "" && fromCalendarID != toCalendarID && toCalendarID != 0
}

// overrideMoveAllowed is the override rule. A row beside its master (live
// or in the trash) cannot leave it. A legacy row can go only to a calendar
// with a live master.
func overrideMoveAllowed(ownMaster, targetLiveMaster bool) bool {
	return !ownMaster && targetLiveMaster
}

// HasMasterIn reports whether calendarID holds a master row of the UID,
// live or in the trash. An override row in a calendar without one is a
// legacy override. storage.LegacyOverride holds the definition.
func HasMasterIn(masters []Master, calendarID int64) bool {
	var n int64
	for _, m := range masters {
		if m.CalendarID == calendarID {
			n++
		}
	}
	return !storage.LegacyOverride(n)
}

func hasLiveMasterIn(masters []Master, calendarID int64) bool {
	for _, m := range masters {
		if m.CalendarID == calendarID && !m.Trashed {
			return true
		}
	}
	return false
}

// occurrenceMoveError builds the refusal of a move of an override row in
// calendarID. It names the live master in that calendar, or else every
// live master. A master in the trash is not a series that a move can take.
func (r Rows) occurrenceMoveError(calendarID int64, masters []Master) error {
	var own, live []Master
	for _, m := range masters {
		if m.Trashed {
			continue
		}
		live = append(live, m)
		if m.CalendarID == calendarID {
			own = append(own, m)
		}
	}
	if len(own) > 0 {
		live = own
	}
	return &OccurrenceMoveError{Err: r.ErrInstanceMove, Masters: live}
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
	live, trashed, err := r.CountCollisions(ctx, fromCalendarID, toCalendarID)
	if err != nil {
		return fmt.Errorf("check move target: %w", err)
	}
	if live == 0 && trashed == 0 {
		return nil
	}
	name := fmt.Sprint(toCalendarID)
	if r.Q != nil {
		if cal, err := r.Q.GetCalendar(ctx, toCalendarID); err == nil {
			name = cal.Name
		}
	}
	switch {
	case live > 0 && trashed > 0:
		return fmt.Errorf("%w: calendar %q holds a live copy of UID %s and a copy in the trash; delete or move the live copy, and purge or restore the trashed copy first",
			r.ErrTargetHasUID, name, r.UID)
	case live > 0:
		return fmt.Errorf("%w: calendar %q holds a live copy of UID %s; delete or move that copy first",
			r.ErrTargetHasUID, name, r.UID)
	default:
		return fmt.Errorf("%w: calendar %q holds UID %s in the trash; purge or restore the trashed copy first",
			r.ErrTargetHasUID, name, r.UID)
	}
}

// MoveStep moves one kind of row of the UID (the item rows, or a
// delete-log table) from one calendar to another. Name names the table in
// an error.
type MoveStep struct {
	Name string
	Move func(ctx context.Context, fromCalendarID, toCalendarID int64) error
}

// SingleMasterRows builds Rows for a domain whose schema keeps the key
// UNIQUE(uid, recurrence_id) across all calendars (todos, journals). A UID
// of such a domain has at most one master row, and a move cannot collide
// with a row of the target calendar, so CountCollisions stays nil.
//
// master returns the master row of the UID, live or in the trash. found is
// false (with a nil error) when no master row exists. MoveRows runs steps
// in order.
func SingleMasterRows(q *storage.Queries, uid string, errInstanceMove error, master func(ctx context.Context) (m Master, found bool, err error), steps ...MoveStep) Rows {
	return Rows{
		Q:               q,
		UID:             uid,
		ErrInstanceMove: errInstanceMove,
		Masters: func(ctx context.Context) ([]Master, error) {
			m, found, err := master(ctx)
			if err != nil || !found {
				return nil, err
			}
			return []Master{m}, nil
		},
		MoveRows: func(ctx context.Context, fromCalendarID, toCalendarID int64) error {
			for _, step := range steps {
				if err := step.Move(ctx, fromCalendarID, toCalendarID); err != nil {
					return fmt.Errorf("move %s: %w", step.Name, err)
				}
			}
			return nil
		},
	}
}

// Update runs the update of one row of the UID by its ID, in the
// transaction of r.Q, in one fixed order:
//
//  1. calendaraccess.EnsureWritable on the calendar of the row, and on
//     the target calendar of a move (component names the iCalendar
//     component)
//  2. Check: the override rule and the target collision
//  3. write: the row update and its child rows
//  4. Record: the move of the other rows of the UID and the source
//     tombstone
//  5. the dirty mark of the target calendar (storage.MarkResourceDirty
//     sends the mark of a legacy override to the calendar of its master)
//
// The dirty mark runs in the same transaction. A move commits the source
// tombstone, so a dirty mark after the commit that fails would delete the
// server object and never create the target object. toCalendarID 0 keeps
// the calendar of the row.
func (r Rows) Update(ctx context.Context, component, ownerType, recurrenceID string, fromCalendarID, toCalendarID int64, write func(ctx context.Context) error) error {
	if toCalendarID == 0 {
		toCalendarID = fromCalendarID
	}
	if err := calendaraccess.EnsureWritable(ctx, r.Q, fromCalendarID, component); err != nil {
		return err
	}
	if toCalendarID != fromCalendarID {
		if err := calendaraccess.EnsureWritable(ctx, r.Q, toCalendarID, component); err != nil {
			return err
		}
	}
	if err := r.Check(ctx, recurrenceID, fromCalendarID, toCalendarID); err != nil {
		return err
	}
	if err := write(ctx); err != nil {
		return err
	}
	// A move to another calendar takes the other rows of the UID along and
	// queues the delete of the source server object (issue #828).
	if err := r.Record(ctx, fromCalendarID, toCalendarID); err != nil {
		return fmt.Errorf("record calendar move: %w", err)
	}
	if err := r.Q.MarkResourceDirty(ctx, toCalendarID, r.UID, ownerType); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return nil
}

// Record records the move of the rows of the UID from one calendar to
// another. It does nothing when the two calendars are the same. See
// storage.RecordResourceMove.
func (r Rows) Record(ctx context.Context, fromCalendarID, toCalendarID int64) error {
	return storage.RecordResourceMove(ctx, r.Q, fromCalendarID, toCalendarID, r.UID, func(ctx context.Context) error {
		return r.MoveRows(ctx, fromCalendarID, toCalendarID)
	})
}
