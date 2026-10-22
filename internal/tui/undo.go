package tui

import (
	"slices"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// UndoMaxDepth bounds the number of deletions the stack will remember. A
// deeper stack does not match the "oops" mental model an undo shortcut
// conveys; the user expects a shallow, recent window.
const UndoMaxDepth = 10

// UndoEntry is a single reversible event delete. Entries hold compact undo
// metadata (UID + kind + optional RRULE pre-state). The actual rows live in
// the database with deleted_at set. Service.RestoreUndo un-hides them.
// Entries are tiny (no snapshots, no blobs). A byte budget is not needed.
type UndoEntry struct {
	Meta      event.UndoMeta
	DeletedAt time.Time
}

// UndoStack is a bounded LIFO of event deletions that wait for a possible undo.
// It is not safe for concurrent use. The TUI owns a single instance on the
// main update loop.
type UndoStack struct {
	entries []UndoEntry
}

// NewUndoStack returns an empty stack.
func NewUndoStack() *UndoStack {
	return &UndoStack{}
}

// Push appends a new undo entry. It evicts the oldest entries until the depth
// budget is satisfied.
func (s *UndoStack) Push(e UndoEntry) {
	s.entries = append(s.entries, e)
	for len(s.entries) > UndoMaxDepth {
		s.entries = s.entries[1:]
	}
}

// Peek returns the most recent entry and whether the stack was non-empty.
// It does not remove the entry; callers pop only after a successful restore.
func (s *UndoStack) Peek() (UndoEntry, bool) {
	if len(s.entries) == 0 {
		return UndoEntry{}, false
	}
	return s.entries[len(s.entries)-1], true
}

// Pop removes and returns the most recent entry. Callers use this after a
// successful restore; on failure they should leave the entry in place so
// the user can try again.
func (s *UndoStack) Pop() (UndoEntry, bool) {
	if len(s.entries) == 0 {
		return UndoEntry{}, false
	}
	last := s.entries[len(s.entries)-1]
	s.entries = s.entries[:len(s.entries)-1]
	return last, true
}

// Remove deletes the most recent entry whose metadata identifies the same
// soft-delete as meta. It reports whether a match was found.
//
// A restore runs asynchronously. A delete can land between the Peek (when
// the undo key is pressed) and the restore's success message. That delete
// can push a new entry onto the stack. The success handler must then
// remove the restored entry by identity. It must not pop the top in
// silence.
func (s *UndoStack) Remove(meta event.UndoMeta) bool {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if sameUndoTarget(s.entries[i].Meta, meta) {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

// sameUndoTarget reports whether two UndoMeta values describe the same
// soft-delete operation. Every field that names the delete must match:
//   - Kind, UID, and CalendarID name the series. Two calendars can hold one
//     UID (issue #842).
//   - RecurrenceID names the occurrence. CutoffTime names the truncation:
//     a truncation has no RecurrenceID, and two truncations of one series
//     differ by their cutoff (issue #514). CutoffTime is the zero time for
//     every other Kind, so the compare is always safe.
//   - RowID, RowIDs, and MasterID name the rows that the delete hid.
//   - ExdateLogID, ExdateDeleteSeq, and SeriesLogID name the log entry of
//     the delete.
//   - DeleteToken names the statement of the delete. Two deletes of one
//     occurrence, or two truncations at one cutoff, differ by it.
func sameUndoTarget(a, b event.UndoMeta) bool {
	return a.Kind == b.Kind && a.UID == b.UID && a.CalendarID == b.CalendarID &&
		a.RecurrenceID == b.RecurrenceID && a.CutoffTime.Equal(b.CutoffTime) &&
		a.RowID == b.RowID && a.MasterID == b.MasterID && slices.Equal(a.RowIDs, b.RowIDs) &&
		a.ExdateLogID == b.ExdateLogID && a.ExdateDeleteSeq == b.ExdateDeleteSeq &&
		a.SeriesLogID == b.SeriesLogID && a.DeleteToken == b.DeleteToken
}

// Len returns the current depth.
func (s *UndoStack) Len() int { return len(s.entries) }
