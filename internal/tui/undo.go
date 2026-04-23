package tui

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// UndoMaxDepth bounds the number of deletions the stack will remember. A
// deeper stack does not match the "oops" mental model an undo shortcut
// conveys; the user expects a shallow, recent window.
const UndoMaxDepth = 10

// UndoMaxBytes caps the cumulative in-memory cost of the stack. Large inline
// attachments can dominate footprint, so eviction runs on either the depth or
// the byte budget, whichever is hit first.
const UndoMaxBytes = 5 * 1024 * 1024 // 5 MiB

// UndoEntry is a single reversible event delete. Entries hold data, not
// closures: what to do with the snapshot is decided by the caller that pops,
// which keeps the stack trivially testable and free of service dependencies.
type UndoEntry struct {
	Snapshot  event.DeletedSnapshot
	Label     string
	DeletedAt time.Time
}

// UndoStack is a bounded LIFO of event deletions awaiting possible undo.
// It is not safe for concurrent use — the TUI owns a single instance on the
// main update loop.
type UndoStack struct {
	entries []UndoEntry
	bytes   int
}

// NewUndoStack returns an empty stack.
func NewUndoStack() *UndoStack {
	return &UndoStack{}
}

// Push appends a new undo entry, evicting the oldest entries until both the
// depth and byte budgets are satisfied.
func (s *UndoStack) Push(e UndoEntry) {
	s.entries = append(s.entries, e)
	s.bytes += e.Snapshot.EstimatedBytes()
	s.evict()
}

// evict drops oldest entries until both caps are satisfied.
func (s *UndoStack) evict() {
	for len(s.entries) > UndoMaxDepth || (s.bytes > UndoMaxBytes && len(s.entries) > 0) {
		s.bytes -= s.entries[0].Snapshot.EstimatedBytes()
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
	s.bytes -= last.Snapshot.EstimatedBytes()
	if s.bytes < 0 {
		s.bytes = 0
	}
	return last, true
}

// Len returns the current depth.
func (s *UndoStack) Len() int { return len(s.entries) }

// Bytes returns the current accumulated byte estimate.
func (s *UndoStack) Bytes() int { return s.bytes }
