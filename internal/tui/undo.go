package tui

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// UndoMaxDepth bounds the number of deletions the stack will remember. A
// deeper stack does not match the "oops" mental model an undo shortcut
// conveys; the user expects a shallow, recent window.
const UndoMaxDepth = 10

// UndoEntry is a single reversible event delete. Entries hold compact undo
// metadata (UID + kind + optional RRULE pre-state); the actual rows live in
// the database with deleted_at set and get un-hidden by Service.RestoreUndo.
// Since entries are tiny (no snapshots, no blobs), a byte budget is no longer
// needed.
type UndoEntry struct {
	Meta      event.UndoMeta
	DeletedAt time.Time
}

// UndoStack is a bounded LIFO of event deletions awaiting possible undo.
// It is not safe for concurrent use — the TUI owns a single instance on the
// main update loop.
type UndoStack struct {
	entries []UndoEntry
}

// NewUndoStack returns an empty stack.
func NewUndoStack() *UndoStack {
	return &UndoStack{}
}

// Push appends a new undo entry, evicting the oldest entries until the depth
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
// soft-delete as meta, returning whether a match was found. It exists because
// a restore runs asynchronously: a delete landing between the Peek (when the
// undo key is pressed) and the restore's success message can push a new entry
// onto the stack, so the success handler must remove the entry it actually
// restored by identity rather than blindly popping the top.
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
// soft-delete operation. Kind + UID + RecurrenceID uniquely identifies a
// reversible delete: a series and a single-instance delete of the same UID
// differ by Kind, and distinct overrides differ by RecurrenceID.
func sameUndoTarget(a, b event.UndoMeta) bool {
	return a.Kind == b.Kind && a.UID == b.UID && a.RecurrenceID == b.RecurrenceID
}

// Len returns the current depth.
func (s *UndoStack) Len() int { return len(s.entries) }
