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

// A legacy override is an override row in a calendar that holds no master
// row of its UID, live or in the trash. An older version saved such a row
// on a calendar change (issue #828). A master in the trash still owns the
// overrides of its calendar: a delete of a series soft-deletes the master
// and the overrides together, and a restore brings them back together.
//
// This is the one definition of the term. CheckOverrideMove, the override
// lookup of the event service (findInstanceOverrideTx), and the dirty-mark
// rule of storage.MarkResourceDirty use it. storage cannot import this
// package, so its SQL states the same rule.

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
// masters holds the live masters of the UID. One CalDAV object holds the
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

// CheckOf is Check for a caller that already holds the live masters of
// the UID. It reads the target calendar only (CheckTarget).
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
// legacy override.
func HasMasterIn(masters []Master, calendarID int64) bool {
	for _, m := range masters {
		if m.CalendarID == calendarID {
			return true
		}
	}
	return false
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

// Record records the move of the rows of the UID from one calendar to
// another. It does nothing when the two calendars are the same. See
// storage.RecordResourceMove.
func (r Rows) Record(ctx context.Context, fromCalendarID, toCalendarID int64) error {
	return storage.RecordResourceMove(ctx, r.Q, fromCalendarID, toCalendarID, r.UID, func(ctx context.Context) error {
		return r.MoveRows(ctx, fromCalendarID, toCalendarID)
	})
}
