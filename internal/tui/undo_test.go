package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func entry(title string) UndoEntry {
	return UndoEntry{
		DeletedAt: time.Now(),
		Meta: event.UndoMeta{
			Kind:  event.UndoKindSingle,
			UID:   "uid-" + title,
			Label: "Deleted " + title,
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

	s.Push(entry("A"))
	s.Push(entry("B"))

	got, ok := s.Peek()
	if !ok || !strings.Contains(got.Meta.Label, "B") {
		t.Fatalf("Peek = %+v, want B", got)
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}

	popped, ok := s.Pop()
	if !ok || !strings.Contains(popped.Meta.Label, "B") {
		t.Fatalf("Pop = %+v, want B", popped)
	}
	if s.Len() != 1 {
		t.Fatalf("Len after pop = %d, want 1", s.Len())
	}

	popped, ok = s.Pop()
	if !ok || !strings.Contains(popped.Meta.Label, "A") {
		t.Fatalf("Pop = %+v, want A", popped)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after second pop = %d, want 0", s.Len())
	}
}

func TestUndoStack_DepthEviction(t *testing.T) {
	s := NewUndoStack()
	// Push UndoMaxDepth + 2 entries; the two oldest should be evicted.
	for i := 0; i < UndoMaxDepth+2; i++ {
		s.Push(entry(labelFor(i)))
	}
	if s.Len() != UndoMaxDepth {
		t.Fatalf("Len = %d, want %d", s.Len(), UndoMaxDepth)
	}
	// Newest entry should still be on top.
	top, _ := s.Peek()
	if want := labelFor(UndoMaxDepth + 1); !strings.Contains(top.Meta.Label, want) {
		t.Fatalf("Peek label = %q, want contains %q", top.Meta.Label, want)
	}
}

func labelFor(i int) string {
	return string([]byte{byte('A' + i)})
}
