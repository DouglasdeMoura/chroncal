package trash

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestService_ListMergesAllThreeDomains wires the aggregator to real
// event/todo/journal services backed by a fresh in-memory DB, soft-
// deletes one row in each domain, and verifies List returns three
// entries with the right Kind values and newest-first ordering.
func TestService_ListMergesAllThreeDomains(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	svc := NewService(events, todos, journals)

	ctx := context.Background()

	ev, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Trash Event",
		StartTime:  time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	td, err := todos.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "Trash Todo",
		DueDate:    "2026-05-02",
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	j, err := journals.Create(ctx, journal.CreateParams{
		CalendarID: 1,
		Summary:    "Trash Journal",
		StartDate:  "2026-05-03",
	})
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}

	// Delete in order so newest-first sorting has a predictable result.
	if err := events.Delete(ctx, ev.ID); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := todos.Delete(ctx, td.ID); err != nil {
		t.Fatalf("delete todo: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := journals.Delete(ctx, j.ID); err != nil {
		t.Fatalf("delete journal: %v", err)
	}

	entries, err := svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (event + todo + journal)", len(entries))
	}
	wantKinds := []Kind{KindJournal, KindTodo, KindEvent}
	for i, want := range wantKinds {
		if entries[i].Kind != want {
			t.Errorf("entries[%d].Kind = %v, want %v (%s newest-first)", i, entries[i].Kind, want, want.Label())
		}
	}
}

// TestService_RestoreDispatchesByKind exercises the Restore path for
// each domain: the underlying service un-hides its own row and List no
// longer returns it.
func TestService_RestoreDispatchesByKind(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	svc := NewService(events, todos, journals)
	ctx := context.Background()

	td, err := todos.Create(ctx, todo.CreateParams{CalendarID: 1, Summary: "Restore Me"})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if err := todos.Delete(ctx, td.ID); err != nil {
		t.Fatalf("delete todo: %v", err)
	}

	entries, err := svc.List(ctx, 1)
	if err != nil || len(entries) != 1 {
		t.Fatalf("List after delete = (%+v, %v), want 1 entry", entries, err)
	}
	if entries[0].Kind != KindTodo {
		t.Fatalf("entries[0].Kind = %v, want KindTodo", entries[0].Kind)
	}

	if err := svc.Restore(ctx, entries[0]); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	entries, err = svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List after restore: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries after restore = %d, want 0", len(entries))
	}
	if _, err := todos.Get(ctx, td.ID); err != nil {
		t.Fatalf("todo not live after restore: %v", err)
	}
}

// TestService_PurgeDispatchesByKind hard-deletes across all three
// domains in one PurgeOld call and reports per-domain counts.
func TestService_PurgeDispatchesByKind(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	svc := NewService(events, todos, journals)
	ctx := context.Background()

	ev, _ := events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Ev",
		StartTime: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC),
	})
	td, _ := todos.Create(ctx, todo.CreateParams{CalendarID: 1, Summary: "Td"})
	j, _ := journals.Create(ctx, journal.CreateParams{CalendarID: 1, Summary: "J", StartDate: "2026-05-03"})

	for _, del := range []func() error{
		func() error { return events.Delete(ctx, ev.ID) },
		func() error { return todos.Delete(ctx, td.ID) },
		func() error { return journals.Delete(ctx, j.ID) },
	} {
		if err := del(); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}

	counts, err := svc.PurgeOld(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("PurgeOld: %v", err)
	}
	if counts.Events != 1 {
		t.Errorf("Events purged = %d, want 1", counts.Events)
	}
	if counts.Todos != 1 {
		t.Errorf("Todos purged = %d, want 1", counts.Todos)
	}
	if counts.Journals != 1 {
		t.Errorf("Journals purged = %d, want 1", counts.Journals)
	}

	entries, err := svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List after purge: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries after purge = %d, want 0", len(entries))
	}
}

// TestService_ListPopulatesCategories soft-deletes an event, todo, and
// journal that each carry categories, then verifies List surfaces those
// categories on the unified Entry. Without category population the trash
// detail "Tags" row renders blank (issue #224).
func TestService_ListPopulatesCategories(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	svc := NewService(events, todos, journals)
	ctx := context.Background()

	ev, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Cat Event",
		StartTime:  time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC),
		Categories: "work,urgent",
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	td, err := todos.Create(ctx, todo.CreateParams{CalendarID: 1, Summary: "Cat Todo"})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if err := todos.ReplaceCategories(ctx, td.ID, []string{"home", "chores"}); err != nil {
		t.Fatalf("todo categories: %v", err)
	}
	j, err := journals.Create(ctx, journal.CreateParams{CalendarID: 1, Summary: "Cat Journal", StartDate: "2026-05-03"})
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}
	if err := journals.ReplaceCategories(ctx, j.ID, []string{"notes", "personal"}); err != nil {
		t.Fatalf("journal categories: %v", err)
	}

	if err := events.Delete(ctx, ev.ID); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	if err := todos.Delete(ctx, td.ID); err != nil {
		t.Fatalf("delete todo: %v", err)
	}
	if err := journals.Delete(ctx, j.ID); err != nil {
		t.Fatalf("delete journal: %v", err)
	}

	entries, err := svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Categories come back normalized (sorted) by JoinCategoryList.
	want := map[Kind]string{
		KindEvent:   "urgent,work",
		KindTodo:    "chores,home",
		KindJournal: "notes,personal",
	}
	got := make(map[Kind]string, len(entries))
	for _, e := range entries {
		got[e.Kind] = e.Categories
	}
	for kind, wantCats := range want {
		if got[kind] != wantCats {
			t.Errorf("%s Categories = %q, want %q", kind.Label(), got[kind], wantCats)
		}
	}
}
