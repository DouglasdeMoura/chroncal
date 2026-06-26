package trash

import (
	"context"
	"reflect"
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

// TestFromJournalCopiesCategories guards that fromJournal carries the
// journal's Categories into the unified Entry, mirroring fromTodo and
// fromEventTrash. Without it the trash detail "Tags" row can never render
// for journals even after categories are populated upstream (issue #291).
func TestFromJournalCopiesCategories(t *testing.T) {
	j := journal.Journal{
		ID:         7,
		CalendarID: 1,
		UID:        "journal-uid",
		Summary:    "Tagged Journal",
		Categories: "notes,personal",
	}

	got := fromJournal(j)
	if got.Categories != j.Categories {
		t.Errorf("fromJournal Categories = %q, want %q", got.Categories, j.Categories)
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

// TestEventTrashRoundTripPreservesAllFields guards the symmetry of
// fromEventTrash/toEventTrash. Both are manual field-by-field copies
// between event.TrashEntry and trash.Entry; because Go struct literals
// don't require every field, adding a field to event.TrashEntry would
// compile cleanly even if one converter forgot it — silently zeroing the
// field on the round-trip. Since RestoreTrash/PurgeTrashEntry target rows
// by InstanceTime/CutoffTime, a dropped discriminator could act on the
// wrong instance.
//
// The test fills every event.TrashEntry field with a distinct non-zero
// value via reflection, round-trips through fromEventTrash∘toEventTrash,
// and demands an exact match. A future field that one converter drops
// reverts to its zero value and fails the comparison. fillNonZero fails
// the test on any field it doesn't know how to populate, forcing whoever
// adds such a field to extend this guard rather than slip past it.
func TestEventTrashRoundTripPreservesAllFields(t *testing.T) {
	// The non-Kind fields are seeded once; only Kind varies per case so the
	// map/unmapEventKind round-trip is exercised for every defined value.
	var template event.TrashEntry
	fillNonZero(t, reflect.ValueOf(&template).Elem(), "Kind")

	for _, kind := range []event.TrashKind{
		event.TrashKindEvent,
		event.TrashKindInstance,
		event.TrashKindTruncation,
	} {
		original := template
		original.Kind = kind

		got := toEventTrash(fromEventTrash(original))
		if !reflect.DeepEqual(got, original) {
			t.Errorf("round-trip dropped or altered a field for kind %d:\n got  %+v\n want %+v", kind, got, original)
		}
	}
}

// fillNonZero sets every exported field of struct value v (except the one
// named skip) to a deterministic, field-distinct non-zero value. It fails
// the test on any unhandled field type so the round-trip guard can't be
// defeated by adding a field whose type it silently ignores.
func fillNonZero(t *testing.T, v reflect.Value, skip string) {
	t.Helper()
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == skip {
			continue
		}
		f := v.Field(i)
		if !f.CanSet() {
			// Unexported fields would panic on Interface()/Set below; fail
			// loudly so the guard stays honest as the struct grows.
			t.Fatalf("fillNonZero: cannot set field %s; the round-trip guard only handles exported fields", field.Name)
		}
		switch f.Interface().(type) {
		case time.Time:
			// Distinct per field so a dropped or swapped time is caught.
			f.Set(reflect.ValueOf(time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)))
		case string:
			f.SetString(field.Name)
		case bool:
			f.SetBool(true)
		case int64:
			f.SetInt(int64(i + 1))
		default:
			t.Fatalf("fillNonZero: unhandled type %s for field %s; extend the round-trip guard", f.Type(), field.Name)
		}
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
