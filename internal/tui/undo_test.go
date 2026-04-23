package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

func entry(title string, bytes int) UndoEntry {
	// Use a slice of the right length so EstimatedBytes reflects the desired
	// cost without depending on the exact constant overhead.
	data := make([]byte, bytes)
	return UndoEntry{
		Label:     "Deleted " + title,
		DeletedAt: time.Now(),
		Snapshot: event.DeletedSnapshot{
			Event: event.Event{
				Title:       title,
				Attachments: []model.Attachment{{Data: data}},
			},
		},
	}
}

func TestUndoStack_PushPopPeek(t *testing.T) {
	s := NewUndoStack()
	if _, ok := s.Peek(); ok {
		t.Fatal("Peek on empty stack should return ok=false")
	}
	if _, ok := s.Pop(); ok {
		t.Fatal("Pop on empty stack should return ok=false")
	}

	s.Push(entry("A", 100))
	s.Push(entry("B", 100))

	got, ok := s.Peek()
	if !ok || !strings.Contains(got.Label, "B") {
		t.Fatalf("Peek = %+v, want B", got)
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}

	popped, ok := s.Pop()
	if !ok || !strings.Contains(popped.Label, "B") {
		t.Fatalf("Pop = %+v, want B", popped)
	}
	if s.Len() != 1 {
		t.Fatalf("Len after pop = %d, want 1", s.Len())
	}

	popped, ok = s.Pop()
	if !ok || !strings.Contains(popped.Label, "A") {
		t.Fatalf("Pop = %+v, want A", popped)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after second pop = %d, want 0", s.Len())
	}
}

func TestUndoStack_DepthEviction(t *testing.T) {
	s := NewUndoStack()
	// Push UndoMaxDepth + 2 small entries; the two oldest should be evicted.
	for i := 0; i < UndoMaxDepth+2; i++ {
		s.Push(entry(labelFor(i), 0))
	}
	if s.Len() != UndoMaxDepth {
		t.Fatalf("Len = %d, want %d", s.Len(), UndoMaxDepth)
	}
	// Newest entry should still be on top.
	top, _ := s.Peek()
	if want := labelFor(UndoMaxDepth + 1); !strings.Contains(top.Label, want) {
		t.Fatalf("Peek label = %q, want contains %q", top.Label, want)
	}
}

func TestUndoStack_ByteBudgetEviction(t *testing.T) {
	s := NewUndoStack()
	// Two 3 MiB entries fit under the 5 MiB cap only if the older one is
	// evicted when the second lands.
	big := 3 * 1024 * 1024
	s.Push(entry("first", big))
	s.Push(entry("second", big))
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (byte budget should have evicted first)", s.Len())
	}
	top, _ := s.Peek()
	if !strings.Contains(top.Label, "second") {
		t.Fatalf("Peek = %q, want 'second'", top.Label)
	}
}

func labelFor(i int) string {
	return string([]byte{byte('A' + i)})
}
