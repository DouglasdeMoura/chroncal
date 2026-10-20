package event

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// twoCalendarSeries stores one daily series with the same UID in calendar
// 1 and in a second calendar. Each copy has an override of its second
// occurrence (issue #842). It returns the master rows and the second
// occurrence.
func twoCalendarSeries(t *testing.T, svc *Service) (masterA, masterB Event, second time.Time) {
	t.Helper()
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Other")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second = start.Add(24 * time.Hour)
	masters := make([]Event, 2)
	for i, cal := range []int64{1, calB} {
		title := "Standup A"
		if cal == calB {
			title = "Standup B"
		}
		m, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: "shared-series", CalendarID: cal, Title: title,
			StartTime: start, EndTime: start.Add(30 * time.Minute),
			RecurrenceRule: "FREQ=DAILY;COUNT=5",
		})
		if err != nil {
			t.Fatalf("upsert master in calendar %d: %v", cal, err)
		}
		if _, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: "shared-series", CalendarID: cal, Title: title + " (late)",
			StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
			RecurrenceID: second.Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("upsert override in calendar %d: %v", cal, err)
		}
		masters[i] = m
	}
	return masters[0], masters[1], second
}

// seriesState reads back the master row and the live overrides of the
// series in one calendar.
func seriesState(t *testing.T, svc *Service, calendarID int64) (master storage.Event, overrides []Event) {
	t.Helper()
	ctx := context.Background()
	m, err := svc.q.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
		CalendarID: calendarID, Uid: "shared-series",
	})
	if err != nil {
		t.Fatalf("get master in calendar %d: %v", calendarID, err)
	}
	o, err := svc.ListSeriesOverrides(ctx, calendarID, "shared-series")
	if err != nil {
		t.Fatalf("list overrides in calendar %d: %v", calendarID, err)
	}
	return m, o
}

// wantSeriesIntact fails when the series in calendarID lost its rule, its
// own override, or gained an EXDATE. A legacy override of the UID in
// another calendar is part of the series too, but it does not count here.
func wantSeriesIntact(t *testing.T, svc *Service, calendarID int64) {
	t.Helper()
	m, all := seriesState(t, svc, calendarID)
	var overrides []Event
	for _, o := range all {
		if o.CalendarID == calendarID {
			overrides = append(overrides, o)
		}
	}
	if m.DeletedAt != nil {
		t.Errorf("master in calendar %d is in the trash", calendarID)
	}
	if got := storage.NullableToString(m.RecurrenceRule); got != "FREQ=DAILY;COUNT=5" {
		t.Errorf("rule in calendar %d = %q, want the original rule", calendarID, got)
	}
	if got := storage.NullableToString(m.Exdates); got != "" {
		t.Errorf("exdates in calendar %d = %q, want none", calendarID, got)
	}
	if len(overrides) != 1 {
		t.Errorf("live overrides in calendar %d = %d, want 1", calendarID, len(overrides))
	}
}

func TestUpdateFromInstance_ScopedToCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, masterB, second := twoCalendarSeries(t, svc)

	split, err := svc.UpdateFromInstance(ctx, masterB.CalendarID, masterB.UID, second, UpdateParams{
		Title: "Standup B (new)", StartTime: second, EndTime: second.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}
	if split.CalendarID != masterB.CalendarID {
		t.Errorf("split series calendar = %d, want %d", split.CalendarID, masterB.CalendarID)
	}
	wantSeriesIntact(t, svc, 1)
	m, overrides := seriesState(t, svc, masterB.CalendarID)
	if got := storage.NullableToString(m.RecurrenceRule); !strings.Contains(got, "UNTIL=") {
		t.Errorf("rule in calendar B = %q, want a truncated rule", got)
	}
	if len(overrides) != 0 {
		t.Errorf("live overrides in calendar B = %d, want 0 after the split", len(overrides))
	}
}

func TestDeleteInstance_ScopedToCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, masterB, second := twoCalendarSeries(t, svc)

	if err := svc.DeleteInstance(ctx, masterB.CalendarID, masterB.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	wantSeriesIntact(t, svc, 1)
	m, overrides := seriesState(t, svc, masterB.CalendarID)
	if got := storage.NullableToString(m.Exdates); got == "" {
		t.Errorf("exdates in calendar B = %q, want the deleted occurrence", got)
	}
	if len(overrides) != 0 {
		t.Errorf("live overrides in calendar B = %d, want 0 after the delete", len(overrides))
	}
}

func TestDeleteFromInstance_ScopedToCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, masterB, second := twoCalendarSeries(t, svc)

	if err := svc.DeleteFromInstance(ctx, masterB.CalendarID, masterB.UID, second); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	wantSeriesIntact(t, svc, 1)
	m, overrides := seriesState(t, svc, masterB.CalendarID)
	if got := storage.NullableToString(m.RecurrenceRule); !strings.Contains(got, "UNTIL=") {
		t.Errorf("rule in calendar B = %q, want a truncated rule", got)
	}
	if len(overrides) != 0 {
		t.Errorf("live overrides in calendar B = %d, want 0 after the truncation", len(overrides))
	}
}

func TestDeleteSeries_ScopedToCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, masterB, _ := twoCalendarSeries(t, svc)

	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	wantSeriesIntact(t, svc, 1)
	m, overrides := seriesState(t, svc, masterB.CalendarID)
	if m.DeletedAt == nil {
		t.Errorf("master in calendar B is live, want it in the trash")
	}
	if len(overrides) != 0 {
		t.Errorf("live overrides in calendar B = %d, want 0 after the delete", len(overrides))
	}

	// RestoreByUID follows the series rule: the live master in calendar A
	// names the series, and A has no row in the trash. The ID of the
	// master of B names the series in the trash.
	if err := svc.RestoreByUID(ctx, masterB.UID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByUID with a live copy in A err = %v, want ErrNotDeleted", err)
	}
	if err := svc.RestoreByID(ctx, masterB.ID); err != nil {
		t.Fatalf("RestoreByID(master B): %v", err)
	}
	wantSeriesIntact(t, svc, masterB.CalendarID)
	wantSeriesIntact(t, svc, 1)

	// With both series in the trash, the UID names no single series.
	for _, cal := range []int64{1, masterB.CalendarID} {
		if err := svc.DeleteSeries(ctx, cal, masterB.UID); err != nil {
			t.Fatalf("DeleteSeries(%d): %v", cal, err)
		}
	}
	if err := svc.RestoreByUID(ctx, masterB.UID); !errors.Is(err, ErrAmbiguousUID) {
		t.Fatalf("RestoreByUID with two trashed series err = %v, want ErrAmbiguousUID", err)
	}
	if err := svc.RestoreByID(ctx, masterB.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if m, _ := seriesState(t, svc, masterB.CalendarID); m.DeletedAt != nil {
		t.Errorf("master in calendar B is still in the trash after the restore")
	}
}

// TestSeriesEdits_RefuseAmbiguousUID is the refusal rule: with no calendar,
// a UID with a live master in two calendars gives ErrAmbiguousUID, and no
// row changes.
func TestSeriesEdits_RefuseAmbiguousUID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, masterB, second := twoCalendarSeries(t, svc)
	uid := masterB.UID

	calls := map[string]func() error{
		"DeleteInstance":     func() error { return svc.DeleteInstance(ctx, 0, uid, second) },
		"DeleteFromInstance": func() error { return svc.DeleteFromInstance(ctx, 0, uid, second) },
		"DeleteSeries":       func() error { return svc.DeleteSeries(ctx, 0, uid) },
		"UpdateFromInstance": func() error {
			_, err := svc.UpdateFromInstance(ctx, 0, uid, second, UpdateParams{
				Title: "x", StartTime: second, EndTime: second.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=2",
			})
			return err
		},
		"DeleteInstanceWithUndo": func() error {
			_, err := svc.DeleteInstanceWithUndo(ctx, 0, uid, second)
			return err
		},
		"DeleteFromInstanceWithUndo": func() error {
			_, err := svc.DeleteFromInstanceWithUndo(ctx, 0, uid, second)
			return err
		},
		"DeleteSeriesWithUndo": func() error {
			_, err := svc.DeleteSeriesWithUndo(ctx, 0, uid)
			return err
		},
		"GetByUID": func() error {
			_, err := svc.GetByUID(ctx, uid)
			return err
		},
		"GetByUIDAndRecurrenceID": func() error {
			_, err := svc.GetByUIDAndRecurrenceID(ctx, uid, second.Format(time.RFC3339))
			return err
		},
		"SeriesMaster": func() error {
			_, err := svc.SeriesMaster(ctx, 0, uid)
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrAmbiguousUID) {
			t.Errorf("%s err = %v, want ErrAmbiguousUID", name, err)
		}
	}
	wantSeriesIntact(t, svc, 1)
	wantSeriesIntact(t, svc, masterB.CalendarID)

	// A calendar with no row of the UID holds no series.
	calC := insertCalendar(t, svc, "Empty")
	if _, err := svc.SeriesMaster(ctx, calC, uid); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SeriesMaster(empty calendar) err = %v, want sql.ErrNoRows", err)
	}
}

// TestSeriesMaster_LegacyOverrideFindsTheOneMaster covers a legacy
// override (see storage.LegacyOverride): an override row in a calendar
// with no master of its UID belongs to the one live master.
func TestSeriesMaster_LegacyOverrideFindsTheOneMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Other")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "legacy-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	second := start.Add(24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "legacy-uid", CalendarID: calB, Title: "Legacy override",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	got, err := svc.SeriesMaster(ctx, calB, "legacy-uid")
	if err != nil {
		t.Fatalf("SeriesMaster(legacy calendar): %v", err)
	}
	if got.ID != master.ID {
		t.Errorf("SeriesMaster(legacy calendar) = row %d, want the master %d", got.ID, master.ID)
	}
	// The legacy override is part of the series of calendar 1.
	overrides, err := svc.ListSeriesOverrides(ctx, 1, "legacy-uid")
	if err != nil {
		t.Fatalf("ListSeriesOverrides: %v", err)
	}
	if len(overrides) != 1 || overrides[0].CalendarID != calB {
		t.Errorf("ListSeriesOverrides(1) = %+v, want the legacy override of calendar %d", overrides, calB)
	}
}

func TestTrashRestore_ScopedToCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)
	fourth := third.Add(24 * time.Hour)

	// Occurrence deletes on different occurrences, and truncations at
	// different cutoffs, so each calendar keeps its own log row.
	if err := svc.DeleteInstance(ctx, masterA.CalendarID, masterA.UID, third); err != nil {
		t.Fatalf("DeleteInstance(A): %v", err)
	}
	if err := svc.DeleteInstance(ctx, masterB.CalendarID, masterB.UID, second); err != nil {
		t.Fatalf("DeleteInstance(B): %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, masterA.CalendarID, masterA.UID, fourth); err != nil {
		t.Fatalf("DeleteFromInstance(A): %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, masterB.CalendarID, masterB.UID, third); err != nil {
		t.Fatalf("DeleteFromInstance(B): %v", err)
	}

	entries, err := svc.ListTrash(ctx, masterB.CalendarID)
	if err != nil {
		t.Fatalf("ListTrash(B): %v", err)
	}
	// The trash of B holds the two log rows and the override row that
	// the occurrence delete hid. Each entry names calendar B only. The
	// log rows go first: a restore of the override row consumes the
	// EXDATE log of its occurrence.
	if len(entries) != 3 {
		t.Fatalf("trash of calendar B holds %d entries, want 3: %+v", len(entries), entries)
	}
	for _, kind := range []TrashKind{TrashKindTruncation, TrashKindInstance, TrashKindEvent} {
		for _, e := range entries {
			if e.Kind != kind {
				continue
			}
			if e.CalendarID != masterB.CalendarID {
				t.Errorf("trash entry %v is in calendar %d, want %d", e.Kind, e.CalendarID, masterB.CalendarID)
			}
			if e.Kind != TrashKindEvent && e.Title != "Standup B" {
				t.Errorf("trash entry %v names master %q, want the master of calendar B", e.Kind, e.Title)
			}
			if err := svc.RestoreTrash(ctx, e); err != nil {
				t.Fatalf("RestoreTrash(%v): %v", e.Kind, err)
			}
		}
	}

	wantSeriesIntact(t, svc, masterB.CalendarID)
	mA, overridesA := seriesState(t, svc, masterA.CalendarID)
	if got := storage.NullableToString(mA.RecurrenceRule); !strings.Contains(got, "UNTIL=") {
		t.Errorf("rule in calendar A = %q, want it still truncated", got)
	}
	if got := storage.NullableToString(mA.Exdates); got == "" {
		t.Errorf("exdates in calendar A = %q, want the deleted occurrence still excluded", got)
	}
	if len(overridesA) != 1 {
		t.Errorf("live overrides in calendar A = %d, want 1", len(overridesA))
	}
}

func TestUndoRestore_ScopedToCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)

	// A keeps a truncation of its own at another cutoff.
	if err := svc.DeleteFromInstance(ctx, masterA.CalendarID, masterA.UID, third); err != nil {
		t.Fatalf("DeleteFromInstance(A): %v", err)
	}

	for name, run := range map[string]func() (UndoMeta, error){
		"instance": func() (UndoMeta, error) {
			return svc.DeleteInstanceWithUndo(ctx, masterB.CalendarID, masterB.UID, second)
		},
		"tail": func() (UndoMeta, error) {
			return svc.DeleteFromInstanceWithUndo(ctx, masterB.CalendarID, masterB.UID, second)
		},
		"series": func() (UndoMeta, error) { return svc.DeleteSeriesWithUndo(ctx, masterB.CalendarID, masterB.UID) },
	} {
		meta, err := run()
		if err != nil {
			t.Fatalf("%s delete: %v", name, err)
		}
		if meta.CalendarID != masterB.CalendarID {
			t.Errorf("%s UndoMeta.CalendarID = %d, want %d", name, meta.CalendarID, masterB.CalendarID)
		}
		if err := svc.RestoreUndo(ctx, meta); err != nil {
			t.Fatalf("%s RestoreUndo: %v", name, err)
		}
		wantSeriesIntact(t, svc, masterB.CalendarID)
	}
	mA, overridesA := seriesState(t, svc, masterA.CalendarID)
	if got := storage.NullableToString(mA.RecurrenceRule); !strings.Contains(got, "UNTIL=") {
		t.Errorf("rule in calendar A = %q, want it still truncated", got)
	}
	if len(overridesA) != 1 {
		t.Errorf("live overrides in calendar A = %d, want 1", len(overridesA))
	}
}

// TestUndo_SameOccurrenceDeletedInBothCalendars is the delete-log rule of
// migration 057: each calendar keeps its own log row, so the undo of the
// delete in B does not look up the row of A and does nothing.
func TestUndo_SameOccurrenceDeletedInBothCalendars(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)

	if _, err := svc.DeleteInstanceWithUndo(ctx, masterA.CalendarID, masterA.UID, second); err != nil {
		t.Fatalf("DeleteInstanceWithUndo(A): %v", err)
	}
	metaB, err := svc.DeleteInstanceWithUndo(ctx, masterB.CalendarID, masterB.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo(B): %v", err)
	}
	if err := svc.RestoreUndo(ctx, metaB); err != nil {
		t.Fatalf("RestoreUndo(B occurrence): %v", err)
	}
	wantSeriesIntact(t, svc, masterB.CalendarID)
	mA, overridesA := seriesState(t, svc, masterA.CalendarID)
	if got := storage.NullableToString(mA.Exdates); got == "" {
		t.Errorf("exdates in calendar A = %q, want the deleted occurrence still excluded", got)
	}
	if len(overridesA) != 0 {
		t.Errorf("live overrides in calendar A = %d, want 0", len(overridesA))
	}

	// The same cutoff in both calendars.
	if _, err := svc.DeleteFromInstanceWithUndo(ctx, masterA.CalendarID, masterA.UID, third); err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo(A): %v", err)
	}
	metaB, err = svc.DeleteFromInstanceWithUndo(ctx, masterB.CalendarID, masterB.UID, third)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo(B): %v", err)
	}
	if err := svc.RestoreUndo(ctx, metaB); err != nil {
		t.Fatalf("RestoreUndo(B tail): %v", err)
	}
	wantSeriesIntact(t, svc, masterB.CalendarID)
	mA, _ = seriesState(t, svc, masterA.CalendarID)
	if got := storage.NullableToString(mA.RecurrenceRule); !strings.Contains(got, "UNTIL=") {
		t.Errorf("rule in calendar A = %q, want it still truncated", got)
	}

	// The trash of A still lists both log rows of A.
	entries, err := svc.ListTrash(ctx, masterA.CalendarID)
	if err != nil {
		t.Fatalf("ListTrash(A): %v", err)
	}
	var logs int
	for _, e := range entries {
		if e.Kind == TrashKindInstance || e.Kind == TrashKindTruncation {
			logs++
		}
	}
	if logs != 2 {
		t.Errorf("log entries in the trash of A = %d, want 2", logs)
	}
}

// TestDeleteSeries_ReadOnlyCopyDoesNotBlock is the access rule: the guard
// covers the calendars of the series only, so a read-only copy of the UID
// in another calendar does not block the delete or the restore.
func TestDeleteSeries_ReadOnlyCopyDoesNotBlock(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	readOnly := insertGuardedCalendar(t, svc.db, "Read-Only", "read", "VEVENT")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	for _, cal := range []int64{1, readOnly} {
		if _, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: "guarded-uid", CalendarID: cal, Title: "Series",
			StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
		}); err != nil {
			t.Fatalf("upsert in calendar %d: %v", cal, err)
		}
	}
	if err := svc.DeleteSeries(ctx, readOnly, "guarded-uid"); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteSeries(read-only) err = %v, want ErrReadOnly", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, "guarded-uid")
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo(1): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo(1): %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, "guarded-uid"); err != nil {
		t.Fatalf("DeleteSeries(1): %v", err)
	}
	// The live master in the read-only calendar names the series for a
	// restore by UID, so that restore is refused. The ID of the master
	// in calendar 1 names the series in the trash.
	if err := svc.RestoreByUID(ctx, "guarded-uid"); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreByUID with a live read-only copy err = %v, want ErrReadOnly", err)
	}
	master1, err := svc.q.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{CalendarID: 1, Uid: "guarded-uid"})
	if err != nil {
		t.Fatalf("get master 1: %v", err)
	}
	if err := svc.RestoreByID(ctx, master1.ID); err != nil {
		t.Fatalf("RestoreByID(master 1): %v", err)
	}
}

// TestHasLiveOverrideFrom_ScopedToCalendar: an override after the cutoff in
// calendar A does not make a truncation of calendar B meaningful.
func TestHasLiveOverrideFrom_ScopedToCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	cutoff := second.Add(24 * time.Hour)
	fourth := cutoff.Add(24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: masterA.UID, CalendarID: masterA.CalendarID, Title: "A late",
		StartTime: fourth.Add(time.Hour), EndTime: fourth.Add(2 * time.Hour),
		RecurrenceID: fourth.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert override in A: %v", err)
	}
	for _, c := range []struct {
		cal  int64
		want bool
	}{{masterA.CalendarID, true}, {masterB.CalendarID, false}} {
		got, err := svc.HasLiveOverrideFrom(ctx, c.cal, masterA.UID, cutoff)
		if err != nil {
			t.Fatalf("HasLiveOverrideFrom(%d): %v", c.cal, err)
		}
		if got != c.want {
			t.Errorf("HasLiveOverrideFrom(%d) = %v, want %v", c.cal, got, c.want)
		}
	}
}

// TestSeriesOverride_ScopedAndLiveOnly: the override of an occurrence is
// the row of the series in the named calendar. A row of the other
// calendar, or a row in the trash, is not that override.
func TestSeriesOverride_ScopedAndLiveOnly(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	recID := second.Format(time.RFC3339)

	a, err := svc.SeriesOverride(ctx, masterA.CalendarID, masterA.UID, recID)
	if err != nil {
		t.Fatalf("SeriesOverride(A): %v", err)
	}
	if a.CalendarID != masterA.CalendarID {
		t.Errorf("SeriesOverride(A) is in calendar %d, want %d", a.CalendarID, masterA.CalendarID)
	}
	b, err := svc.SeriesOverride(ctx, masterB.CalendarID, masterB.UID, recID)
	if err != nil {
		t.Fatalf("SeriesOverride(B): %v", err)
	}
	if err := svc.Delete(ctx, b.ID); err != nil {
		t.Fatalf("Delete(B override): %v", err)
	}
	if _, err := svc.SeriesOverride(ctx, masterB.CalendarID, masterB.UID, recID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SeriesOverride(B) after the delete err = %v, want sql.ErrNoRows", err)
	}
	if _, err := svc.SeriesOverride(ctx, masterA.CalendarID, masterA.UID, recID); err != nil {
		t.Errorf("SeriesOverride(A) after the delete in B: %v", err)
	}
}

// TestDeleteByID_LegacyOverrideWithTwoMasters: a legacy override of a UID
// with two live masters belongs to no series. Its delete by ID and its
// restore by ID work, and no master gets an EXDATE.
func TestDeleteByID_LegacyOverrideWithTwoMasters(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	calC := insertCalendar(t, svc, "Legacy")
	third := second.Add(24 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: masterA.UID, CalendarID: calC, Title: "Legacy",
		StartTime: third, EndTime: third.Add(time.Hour), RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.Delete(ctx, legacy.ID); err != nil {
		t.Fatalf("Delete(legacy): %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Fatalf("legacy row after the delete = %+v, %v; want it in the trash", r, err)
	}
	wantSeriesIntact(t, svc, masterA.CalendarID)
	wantSeriesIntact(t, svc, masterB.CalendarID)
	if err := svc.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Fatalf("legacy row after the restore = %+v, %v; want it live", r, err)
	}
}

// TestRestore_PrefersLiveMasterOverTrashedCopy: a purge-pending copy of the
// UID in the trash of another calendar does not make a restore ambiguous.
func TestRestore_PrefersLiveMasterOverTrashedCopy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	got, err := svc.SeriesMaster(ctx, 0, masterA.UID)
	if err != nil {
		t.Fatalf("SeriesMaster(0): %v", err)
	}
	if got.ID != masterA.ID {
		t.Errorf("SeriesMaster(0) = row %d, want the live master %d", got.ID, masterA.ID)
	}

	meta, err := svc.DeleteInstanceWithUndo(ctx, masterA.CalendarID, masterA.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo(A): %v", err)
	}
	// A legacy UndoMeta without a calendar takes the live master.
	meta.CalendarID = 0
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo(A, no calendar): %v", err)
	}
	wantSeriesIntact(t, svc, masterA.CalendarID)

	// A restore by ID of the trashed override in B finds its master in
	// the trash of B.
	row, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
		CalendarID: masterB.CalendarID, Uid: masterB.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("override row of B: %v", err)
	}
	if err := svc.RestoreByID(ctx, row.ID); err != nil {
		t.Fatalf("RestoreByID(B override): %v", err)
	}
}

// legacyOverrideOfTwoMasters stores a legacy override of the shared UID in
// a third calendar, beside the two live masters of twoCalendarSeries.
func legacyOverrideOfTwoMasters(t *testing.T, svc *Service, masterA Event, at time.Time) Event {
	t.Helper()
	calC := insertCalendar(t, svc, "Legacy")
	legacy, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID: masterA.UID, CalendarID: calC, Title: "Legacy",
		StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	return legacy
}

// TestUndo_LegacyOverrideWithTwoMasters: the undo of a delete of a legacy
// override of a UID with two live masters brings that one row back.
func TestUndo_LegacyOverrideWithTwoMasters(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	legacy := legacyOverrideOfTwoMasters(t, svc, masterA, second.Add(24*time.Hour))

	meta, err := svc.DeleteWithUndo(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(legacy): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo(legacy): %v", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Fatalf("legacy row after the undo = %+v, %v; want it live", r, err)
	}
	wantSeriesIntact(t, svc, masterA.CalendarID)
	wantSeriesIntact(t, svc, masterB.CalendarID)
}

// TestRestoreByID_MasterRestoresSeries: with the UID in the trash of two
// calendars, the ID of a master names one series, and its restore brings
// the overrides of that calendar back with it.
func TestRestoreByID_MasterRestoresSeries(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, _ := twoCalendarSeries(t, svc)
	for _, cal := range []int64{masterA.CalendarID, masterB.CalendarID} {
		if err := svc.DeleteSeries(ctx, cal, masterA.UID); err != nil {
			t.Fatalf("DeleteSeries(%d): %v", cal, err)
		}
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); !errors.Is(err, ErrAmbiguousUID) {
		t.Fatalf("RestoreByUID err = %v, want ErrAmbiguousUID", err)
	}
	if err := svc.RestoreByID(ctx, masterB.ID); err != nil {
		t.Fatalf("RestoreByID(master B): %v", err)
	}
	wantSeriesIntact(t, svc, masterB.CalendarID)
	mA, overridesA := seriesState(t, svc, masterA.CalendarID)
	if mA.DeletedAt == nil || len(overridesA) != 0 {
		t.Errorf("series A after the restore of B: master trashed %v, live overrides %d; want trashed and 0", mA.DeletedAt != nil, len(overridesA))
	}
}

// TestDeleteSeries_TrashedMasterCalendarRefuses: a calendar whose master
// is in the trash holds no live series, so a series delete named with it
// has no target. The legacy overrides of the live master stay.
func TestDeleteSeries_TrashedMasterCalendarRefuses(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	legacy := legacyOverrideOfTwoMasters(t, svc, masterA, second.Add(24*time.Hour))

	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteSeries(trashed B) err = %v, want sql.ErrNoRows", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Fatalf("legacy override after the refused delete = %+v, %v; want it live", r, err)
	}
	wantSeriesIntact(t, svc, masterA.CalendarID)
}

// TestSeriesMaster_TrashedLegacyOverrideDoesNotJoin: a calendar that holds
// a legacy override in the trash only is not in the series for a live
// edit. A restore still finds the master of that row.
func TestSeriesMaster_TrashedLegacyOverrideDoesNotJoin(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Other")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "legacy-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	second := start.Add(24 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "legacy-uid", CalendarID: calB, Title: "Legacy override",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.Delete(ctx, legacy.ID); err != nil {
		t.Fatalf("Delete(legacy): %v", err)
	}
	if _, err := svc.SeriesMaster(ctx, calB, "legacy-uid"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SeriesMaster(calendar with a trashed legacy override) err = %v, want sql.ErrNoRows", err)
	}
	if err := svc.DeleteInstance(ctx, calB, "legacy-uid", second); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteInstance(calendar with a trashed legacy override) err = %v, want sql.ErrNoRows", err)
	}
	if err := svc.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	got, err := svc.SeriesMaster(ctx, calB, "legacy-uid")
	if err != nil || got.ID != master.ID {
		t.Errorf("SeriesMaster(calendar with a live legacy override) = %d, %v; want the master %d", got.ID, err, master.ID)
	}
}

// TestDeleteByID_LegacyOverrideGuardsMasterCalendar: the delete of a legacy
// override writes the EXDATE onto its master, so a read-only master
// calendar refuses the delete.
func TestDeleteByID_LegacyOverrideGuardsMasterCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	readOnly := insertGuardedCalendar(t, svc.db, "Read-Only", "read", "VEVENT")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guarded-uid", CalendarID: readOnly, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	second := start.Add(24 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guarded-uid", CalendarID: 1, Title: "Legacy override",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.Delete(ctx, legacy.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Delete(legacy of a read-only master) err = %v, want ErrReadOnly", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Fatalf("legacy row after the refused delete = %+v, %v; want it live", r, err)
	}
	m, err := svc.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: readOnly, Uid: "guarded-uid"})
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if got := storage.NullableToString(m.Exdates); got != "" {
		t.Errorf("exdates of the read-only master = %q, want none", got)
	}
}

// TestLegacyOverride_TwoLiveMastersOwnNothing is the owner rule
// (uidmove.LegacyOwner) on each series path: with two live masters, a
// legacy override belongs to no series. Once one master is in the trash,
// the other owns it.
func TestLegacyOverride_TwoLiveMastersOwnNothing(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)
	legacy := legacyOverrideOfTwoMasters(t, svc, masterA, third)

	for _, cal := range []int64{masterA.CalendarID, masterB.CalendarID} {
		overrides, err := svc.ListSeriesOverrides(ctx, cal, masterA.UID)
		if err != nil {
			t.Fatalf("ListSeriesOverrides(%d): %v", cal, err)
		}
		if len(overrides) != 1 || overrides[0].CalendarID != cal {
			t.Errorf("ListSeriesOverrides(%d) = %+v, want the own override only", cal, overrides)
		}
		if _, err := svc.SeriesOverride(ctx, cal, masterA.UID, third.Format(time.RFC3339)); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("SeriesOverride(%d, legacy occurrence) err = %v, want sql.ErrNoRows", cal, err)
		}
	}
	if err := svc.DeleteSeries(ctx, masterA.CalendarID, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Fatalf("legacy row after DeleteSeries(A) = %+v, %v; want it live", r, err)
	}
	// B is the one live master now, so it owns the legacy row.
	overrides, err := svc.ListSeriesOverrides(ctx, masterB.CalendarID, masterB.UID)
	if err != nil {
		t.Fatalf("ListSeriesOverrides(B): %v", err)
	}
	if len(overrides) != 2 {
		t.Errorf("ListSeriesOverrides(B) with A in the trash = %d rows, want 2 (own and legacy)", len(overrides))
	}
}

// TestRestoreSeries_LeavesLegacyOverrideOfLiveMaster: the restore of a
// trashed series does not un-hide a legacy override that the live master
// of another calendar owns and excluded.
func TestRestoreSeries_LeavesLegacyOverrideOfLiveMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)
	legacy := legacyOverrideOfTwoMasters(t, svc, masterA, third)

	if err := svc.DeleteSeries(ctx, masterA.CalendarID, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	// B owns the legacy row now: its occurrence delete hides the row.
	if err := svc.DeleteInstance(ctx, masterB.CalendarID, masterB.UID, third); err != nil {
		t.Fatalf("DeleteInstance(B, legacy occurrence): %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Fatalf("legacy row after DeleteInstance(B) = %+v, %v; want it in the trash", r, err)
	}

	if err := svc.RestoreByID(ctx, masterA.ID); err != nil {
		t.Fatalf("RestoreByID(master A): %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("legacy row after the restore of A = %+v, %v; want it still in the trash", r, err)
	}
	mB, _ := seriesState(t, svc, masterB.CalendarID)
	if got := storage.NullableToString(mB.Exdates); got == "" {
		t.Errorf("exdates in calendar B = %q, want the legacy occurrence still excluded", got)
	}
}

// TestRestoreByUID_PrefersLiveMaster: a stale trashed copy of the UID in
// calendar A does not take the restore of a deleted override of the live
// series in calendar B.
func TestRestoreByUID_PrefersLiveMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	if err := svc.DeleteSeries(ctx, masterA.CalendarID, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if err := svc.DeleteInstance(ctx, masterB.CalendarID, masterB.UID, second); err != nil {
		t.Fatalf("DeleteInstance(B): %v", err)
	}
	if err := svc.RestoreByUID(ctx, masterB.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantSeriesIntact(t, svc, masterB.CalendarID)
	if mA, _ := seriesState(t, svc, masterA.CalendarID); mA.DeletedAt == nil {
		t.Errorf("the stale copy in A is live after the restore of B, want it still in the trash")
	}
}

// TestRestoreOverrideByID_ThenMaster: the restore of an override while its
// master is in the trash clears the EXDATE on the trashed master, so the
// restore of the master shows the occurrence.
func TestRestoreOverrideByID_ThenMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "single-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "single-uid", CalendarID: 1, Title: "Late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, "single-uid", second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, "single-uid"); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID(override): %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	m, err := svc.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: 1, Uid: "single-uid"})
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if got := storage.NullableToString(m.Exdates); got != "" {
		t.Errorf("exdates after both restores = %q, want none", got)
	}
	logs, err := svc.q.ListEventExdateDeletesByCalendar(ctx, 1)
	if err != nil || len(logs) != 0 {
		t.Errorf("exdate log rows after both restores = %v, %v; want none", logs, err)
	}
	if r, err := svc.Get(ctx, override.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("override after both restores = %+v, %v; want it live", r, err)
	}
}

// TestRestoreLegacyOverride_GuardsMasterCalendar: the restore of a legacy
// override rewrites the EXDATE of its master, so a read-only master
// calendar refuses the undo and the restore by ID.
func TestRestoreLegacyOverride_GuardsMasterCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Master")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guarded-uid", CalendarID: calB, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	second := start.Add(24 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guarded-uid", CalendarID: 1, Title: "Legacy override",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(legacy): %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make the master calendar read-only: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Errorf("RestoreUndo(legacy of a read-only master) err = %v, want ErrReadOnly", err)
	}
	if err := svc.RestoreByID(ctx, legacy.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Errorf("RestoreByID(legacy of a read-only master) err = %v, want ErrReadOnly", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("legacy row after the refused restores = %+v, %v; want it still in the trash", r, err)
	}
}
