package trash

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestService_ListMergesAllThreeDomains wires the aggregator to real
// event/todo/journal services backed by a fresh in-memory DB. It soft-
// deletes one row in each domain. It verifies List returns three
// entries with the right Kind values and newest-first order.
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
// journal's Categories into the unified Entry. That matches fromTodo and
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
// each domain. The service underneath un-hides its own row. List no
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
// between event.TrashEntry and trash.Entry. Go struct literals
// do not require every field. An add of a field to event.TrashEntry would
// compile cleanly even if one converter forgot it. The field then zeros on
// the round-trip, in silence. RestoreTrash/PurgeTrashEntry target rows
// by InstanceTime/CutoffTime. A dropped discriminator could then act on the
// wrong instance.
//
// The test fills every event.TrashEntry field with a distinct non-zero
// value via reflection. It round-trips through fromEventTrash∘toEventTrash.
// It demands an exact match. A future field that one converter drops
// reverts to its zero value and fails the comparison. fillNonZero fails
// the test on any field it does not know how to populate. Whoever
// adds such a field must then extend this guard rather than slip past it.
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
		case []int64:
			f.Set(reflect.ValueOf([]int64{int64(i + 1), int64(i + 2)}))
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

// TestService_PurgeOldCountsSeriesLogs: PurgeOld reports the old
// series-delete logs that it drops.
func TestService_PurgeOldCountsSeriesLogs(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, deleted_at) VALUES (1, 'old-series', '', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed series log: %v", err)
	}
	counts, err := svc.PurgeOld(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("PurgeOld: %v", err)
	}
	if counts.EventSeriesLogs != 1 {
		t.Errorf("EventSeriesLogs = %d, want 1", counts.EventSeriesLogs)
	}
}

// keptSeriesFixture builds a series whose master is old in the trash while
// its series still owns a live override. The trashed override, the series
// log, and the occurrence log are old too. It returns the master ID and the
// ID of the trashed override.
func keptSeriesFixture(t *testing.T, db *sql.DB, events *event.Service) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := events.UpsertByUID(ctx, event.UpsertParams{
		UID: "kept-series", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	overrides := make([]int64, 0, 2)
	for _, days := range []int{1, 2} {
		at := start.AddDate(0, 0, days)
		o, err := events.UpsertByUID(ctx, event.UpsertParams{
			UID: master.UID, CalendarID: 1, Title: "Late",
			StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert override: %v", err)
		}
		overrides = append(overrides, o.ID)
	}
	if err := events.DeleteInstance(ctx, 1, master.UID, start.AddDate(0, 0, 3)); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := events.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET deleted_at = NULL WHERE id = ?`, overrides[1]); err != nil {
		t.Fatalf("revive the live override as a pull does: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE events SET deleted_at = '2020-01-01T00:00:00Z' WHERE uid = 'kept-series' AND deleted_at IS NOT NULL`,
		`UPDATE event_series_deletes SET deleted_at = '2020-01-01T00:00:00Z' WHERE uid = 'kept-series'`,
		`UPDATE event_exdate_deletes SET deleted_at = '2020-01-01T00:00:00Z' WHERE uid = 'kept-series'`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("age the series: %v", err)
		}
	}
	return master.ID, overrides[0]
}

// TestPurgeOld_KeepsTheRowsAndLogsOfAKeptMaster: a master that the purge
// keeps keeps its whole series in the same run. That series holds its old
// trashed override, its series log, and its occurrence log.
func TestPurgeOld_KeepsTheRowsAndLogsOfAKeptMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()
	masterID, trashedID := keptSeriesFixture(t, db, events)
	counts, err := svc.PurgeOld(ctx, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PurgeOld: %v", err)
	}
	if len(counts.EventMastersSkipped) != 1 || counts.EventMastersSkipped[0].ID != masterID {
		t.Fatalf("skipped = %+v, want the master %d", counts.EventMastersSkipped, masterID)
	}
	for _, id := range []int64{masterID, trashedID} {
		if _, err := events.GetIncludingDeleted(ctx, id); err != nil {
			t.Errorf("row %d after the purge: %v, want it kept", id, err)
		}
	}
	for _, table := range []string{"event_series_deletes", "event_exdate_deletes"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE uid = 'kept-series'`).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s rows after the purge = %d, %v; want 1", table, n, err)
		}
	}
}

// TestPurgeOld_OneTransactionForRowsAndLogs: the purge of the event rows and
// the purge of the delete logs share one transaction. A failure in the log
// purge rolls back the row purge. Thus the purge cannot keep a master and
// lose its logs.
func TestPurgeOld_OneTransactionForRowsAndLogs(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	single, err := events.UpsertByUID(ctx, event.UpsertParams{
		UID: "old-single", CalendarID: 1, Title: "Old",
		StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE events SET deleted_at = '2020-01-01T00:00:00Z' WHERE id = ` + strconv.FormatInt(single.ID, 10),
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, deleted_at, had_master)
		 VALUES (1, 'boom', '', '2020-01-01T00:00:00Z', 0)`,
		`CREATE TRIGGER test_boom BEFORE DELETE ON event_series_deletes WHEN OLD.uid = 'boom'
		 BEGIN SELECT RAISE(ABORT, 'boom'); END`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := svc.PurgeOld(ctx, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("PurgeOld: got nil, want the error of the log purge")
	}
	if _, err := events.GetIncludingDeleted(ctx, single.ID); err != nil {
		t.Errorf("old row after the failed purge: %v, want the rollback to keep it", err)
	}
}

// TestService_RestoreAllRestoresTruncationsInAnyOrder selects two
// truncations of one series with the blocked one first. Restore of that
// entry refuses with event.ErrNewerTruncation, so RestoreAll tries it again
// after the newer one. Alone, the blocked entry still refuses.
func TestService_RestoreAllRestoresTruncationsInAnyOrder(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	for _, day := range []int{5, 3} {
		if err := events.DeleteFromInstance(ctx, 1, master.UID, start.AddDate(0, 0, day)); err != nil {
			t.Fatalf("truncate at day %d: %v", day, err)
		}
	}
	listTruncations := func() (blocked, free []Entry) {
		t.Helper()
		entries, err := svc.List(ctx, 1)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, e := range entries {
			switch {
			case e.Kind != KindEventSeriesTail:
			case e.NewerTruncationID != 0:
				blocked = append(blocked, e)
			default:
				free = append(free, e)
			}
		}
		return blocked, free
	}
	blocked, free := listTruncations()
	if len(blocked) != 1 || len(free) != 1 {
		t.Fatalf("truncations = %d blocked, %d free; want 1 and 1", len(blocked), len(free))
	}

	if n, err := svc.RestoreAll(ctx, blocked); !errors.Is(err, event.ErrNewerTruncation) || n != 0 {
		t.Fatalf("RestoreAll(blocked alone) = %d, %v; want 0, ErrNewerTruncation", n, err)
	}
	if n, err := svc.RestoreAll(ctx, append(blocked, free...)); err != nil || n != 2 {
		t.Fatalf("RestoreAll(blocked first) = %d, %v; want 2, nil", n, err)
	}
	if blocked, free := listTruncations(); len(blocked)+len(free) != 0 {
		t.Errorf("truncations after RestoreAll = %d, want 0", len(blocked)+len(free))
	}
	got, err := events.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if got.RecurrenceRule != "FREQ=DAILY;COUNT=10" {
		t.Errorf("RRULE after RestoreAll = %q, want the rule before the truncations", got.RecurrenceRule)
	}
}

// TestService_RestoreAllRestoresARowBeforeItsMaster selects a deleted
// occurrence before the master of its series, both in the trash. The
// restore of the occurrence refuses with event.ErrMasterInTrash, so
// RestoreAll tries it again after the master.
func TestService_RestoreAllRestoresARowBeforeItsMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	if err := events.DeleteInstance(ctx, 1, master.UID, start.AddDate(0, 0, 2)); err != nil {
		t.Fatalf("delete occurrence: %v", err)
	}
	if err := events.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	entries, err := svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var ordered []Entry
	for _, e := range entries {
		if e.Kind == KindEventInstance {
			ordered = append([]Entry{e}, ordered...)
		} else {
			ordered = append(ordered, e)
		}
	}
	if len(ordered) != 2 || ordered[0].Kind != KindEventInstance {
		t.Fatalf("entries = %+v, want an occurrence and a master", entries)
	}
	if err := svc.Restore(ctx, ordered[0]); !errors.Is(err, event.ErrMasterInTrash) {
		t.Fatalf("Restore(occurrence first) = %v, want ErrMasterInTrash", err)
	}
	if n, err := svc.RestoreAll(ctx, ordered); err != nil || n != 2 {
		t.Fatalf("RestoreAll(occurrence first) = %d, %v; want 2, nil", n, err)
	}
	if left, err := svc.List(ctx, 1); err != nil || len(left) != 0 {
		t.Errorf("trash after RestoreAll = %+v, %v; want empty", left, err)
	}
}

// TestService_RestoreAllCountsRowsThatTheMasterBroughtBack marks the master
// and the override of a deleted series. The restore of the master brings the
// override back with it, so the entry of the override then gives
// event.ErrNotDeleted. RestoreAll counts it as restored and reports no
// error.
func TestService_RestoreAllCountsRowsThatTheMasterBroughtBack(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	inst := start.AddDate(0, 0, 2)
	if _, err := events.UpdateInstance(ctx, 1, master.UID, inst, event.UpdateParams{CalendarID: 1, Title: "Late", StartTime: inst, EndTime: inst.Add(time.Hour)}); err != nil {
		t.Fatalf("create override: %v", err)
	}
	if err := events.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	entries, err := svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var ordered []Entry
	for _, e := range entries {
		if e.ID == master.ID {
			ordered = append([]Entry{e}, ordered...)
		} else {
			ordered = append(ordered, e)
		}
	}
	if len(ordered) != 2 || ordered[0].ID != master.ID {
		t.Fatalf("entries = %+v, want the master and the override", entries)
	}
	if n, err := svc.RestoreAll(ctx, ordered); err != nil || n != 2 {
		t.Fatalf("RestoreAll(master first) = %d, %v; want 2, nil", n, err)
	}
}

// TestService_RestoreAllCountsAnEntryThatIsNoLongerInTheTrash marks two
// deleted events, and the second one is already back. Its entry gives
// event.ErrNotDeleted. The trash no longer lists it, so RestoreAll counts it
// as restored, in either order of the selection.
func TestService_RestoreAllCountsAnEntryThatIsNoLongerInTheTrash(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	ids := make([]int64, 0, 2)
	for _, title := range []string{"A", "B"} {
		e, err := events.Create(ctx, event.CreateParams{CalendarID: 1, Title: title, StartTime: start, EndTime: start.Add(time.Hour)})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		ids = append(ids, e.ID)
	}
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		for _, id := range ids {
			if err := events.Delete(ctx, id); err != nil {
				t.Fatalf("delete %d: %v", id, err)
			}
		}
		entries, err := svc.List(ctx, 1)
		if err != nil || len(entries) != 2 {
			t.Fatalf("List = %+v, %v; want 2 entries", entries, err)
		}
		byID := map[int64]Entry{}
		for _, e := range entries {
			byID[e.ID] = e
		}
		if err := svc.Restore(ctx, byID[ids[1]]); err != nil {
			t.Fatalf("restore B: %v", err)
		}
		sel := []Entry{byID[ids[order[0]]], byID[ids[order[1]]]}
		if n, err := svc.RestoreAll(ctx, sel); err != nil || n != 2 {
			t.Fatalf("RestoreAll(order %v) = %d, %v; want 2, nil", order, n, err)
		}
	}
}

// TestService_RestoreAllReportsTheWaitFirst marks an older truncation without
// its newer truncation. The error names the truncation to restore first: the
// user can act on it.
func TestService_RestoreAllReportsTheWaitFirst(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	for _, day := range []int{5, 3} {
		if err := events.DeleteFromInstance(ctx, 1, master.UID, start.AddDate(0, 0, day)); err != nil {
			t.Fatalf("truncate at day %d: %v", day, err)
		}
	}
	entries, err := svc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var blocked Entry
	for _, e := range entries {
		if e.Kind == KindEventSeriesTail && e.NewerTruncationID != 0 {
			blocked = e
		}
	}
	if blocked.ID == 0 {
		t.Fatalf("entries = %+v, want a blocked truncation", entries)
	}
	n, err := svc.RestoreAll(ctx, []Entry{blocked})
	if !errors.Is(err, event.ErrNewerTruncation) || n != 0 {
		t.Fatalf("RestoreAll(blocked) = %d, %v; want 0, ErrNewerTruncation", n, err)
	}
}

// TestService_RestoreAllCountsALegacyOverrideOfARestoredMaster marks a master
// in calendar 1 and its legacy override in calendar 2 (an override in a
// calendar with no master of its UID). One series delete hid both. The
// restore of the master brings the override back, so its own entry gives
// event.ErrNotDeleted. The trash no longer lists it, so RestoreAll counts it.
func TestService_RestoreAllCountsALegacyOverrideOfARestoredMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	svc := NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	ctx := context.Background()

	other, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Other"})
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	inst := start.AddDate(0, 0, 2)
	override, err := events.UpdateInstance(ctx, 1, master.UID, inst, event.UpdateParams{CalendarID: 1, Title: "Late", StartTime: inst, EndTime: inst.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET calendar_id = ? WHERE id = ?`, other.ID, override.ID); err != nil {
		t.Fatalf("make a legacy override: %v", err)
	}
	if err := events.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	var sel []Entry
	for _, calID := range []int64{other.ID, 1} {
		entries, err := svc.List(ctx, calID)
		if err != nil {
			t.Fatalf("List %d: %v", calID, err)
		}
		sel = append(sel, entries...)
	}
	if len(sel) != 2 || sel[0].ID != override.ID {
		t.Fatalf("entries = %+v, want the legacy override, then the master", sel)
	}
	if n, err := svc.RestoreAll(ctx, sel); err != nil || n != 2 {
		t.Fatalf("RestoreAll(legacy override, master) = %d, %v; want 2, nil", n, err)
	}
}
