package event

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/uidmove"
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

	// RestoreByUID: the live series in calendar A has nothing in the
	// trash, so the one trashed master of the UID names the series.
	if err := svc.RestoreByUID(ctx, masterB.UID); err != nil {
		t.Fatalf("RestoreByUID with a live copy in A: %v", err)
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
	// The live series in the read-only calendar has nothing in the
	// trash, so the restore by UID targets the trashed series of
	// calendar 1 and guards that calendar only.
	if err := svc.RestoreByUID(ctx, "guarded-uid"); err != nil {
		t.Fatalf("RestoreByUID with a live read-only copy: %v", err)
	}
	master1, err := svc.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: 1, Uid: "guarded-uid"})
	if err != nil {
		t.Fatalf("master 1 after the restore: %v", err)
	}
	if master1.DeletedAt != nil {
		t.Errorf("master 1 is still in the trash after RestoreByUID")
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

	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); !errors.Is(err, ErrSeriesInTrash) {
		t.Fatalf("DeleteSeries(trashed B) err = %v, want ErrSeriesInTrash", err)
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

// singleSeriesWithOverrides stores one daily series in calendar 1 with
// an override of its second and its third occurrence.
func singleSeriesWithOverrides(t *testing.T, svc *Service) (master Event, second, third time.Time) {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second = start.Add(24 * time.Hour)
	third = second.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "single-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	for _, at := range []time.Time{second, third} {
		if _, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: "single-uid", CalendarID: 1, Title: "Late",
			StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("upsert override %s: %v", at, err)
		}
	}
	return master, second, third
}

// TestUndo_OverrideDeletedWhileMasterInTrash: an override that came back
// on its own and went again comes back with the undo. The undo never
// un-hides the master in the trash.
func TestUndo_OverrideDeletedWhileMasterInTrash(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID(override): %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, override.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(override): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if r, err := svc.Get(ctx, override.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("override after the undo = %+v, %v; want it live", r, err)
	}
	if m, err := svc.GetIncludingDeleted(ctx, master.ID); err != nil || m.DeletedAt == nil {
		t.Errorf("master after the undo = %+v, %v; want it still in the trash", m, err)
	}
}

// TestRestoreSeries_KeepsOwnDeletedOccurrence is the issue #287 rule on
// the series restore: an occurrence that the user deleted on its own
// before the series delete stays deleted, with its EXDATE and its log
// row. The restore by master ID and the undo both follow it.
func TestRestoreSeries_KeepsOwnDeletedOccurrence(t *testing.T) {
	for name, restore := range map[string]func(t *testing.T, svc *Service, master Event){
		"by master ID": func(t *testing.T, svc *Service, master Event) {
			if err := svc.DeleteSeries(context.Background(), 1, master.UID); err != nil {
				t.Fatalf("DeleteSeries: %v", err)
			}
			if err := svc.RestoreByID(context.Background(), master.ID); err != nil {
				t.Fatalf("RestoreByID(master): %v", err)
			}
		},
		"undo": func(t *testing.T, svc *Service, master Event) {
			meta, err := svc.DeleteSeriesWithUndo(context.Background(), 1, master.UID)
			if err != nil {
				t.Fatalf("DeleteSeriesWithUndo: %v", err)
			}
			if err := svc.RestoreUndo(context.Background(), meta); err != nil {
				t.Fatalf("RestoreUndo: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			master, second, third := singleSeriesWithOverrides(t, svc)
			if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
				t.Fatalf("DeleteInstance: %v", err)
			}
			restore(t, svc, master)

			m, err := svc.q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
			if err != nil {
				t.Fatalf("master after the restore: %v", err)
			}
			if got := storage.NullableToString(m.Exdates); !strings.Contains(got, second.Format(time.RFC3339)) {
				t.Errorf("exdates after the restore = %q, want the own deleted occurrence still excluded", got)
			}
			overrides, err := svc.ListSeriesOverrides(ctx, 1, master.UID)
			if err != nil {
				t.Fatalf("ListSeriesOverrides: %v", err)
			}
			if len(overrides) != 1 || overrides[0].RecurrenceID != third.Format(time.RFC3339) {
				t.Errorf("live overrides after the restore = %+v, want the third occurrence only", overrides)
			}
			logs, err := svc.q.ListEventExdateDeletesByCalendar(ctx, 1)
			if err != nil || len(logs) != 1 {
				t.Errorf("exdate log rows after the restore = %v, %v; want the own delete kept", logs, err)
			}
			// The trash still lists the own occurrence delete.
			entries, err := svc.ListTrash(ctx, 1)
			if err != nil {
				t.Fatalf("ListTrash: %v", err)
			}
			var instances int
			for _, e := range entries {
				if e.Kind == TrashKindInstance {
					instances++
				}
			}
			if instances != 1 {
				t.Errorf("instance entries in the trash = %d, want 1", instances)
			}
		})
	}
}

// TestRestoreByUID_FallsBackToTrashedSeries: with a live series that has
// nothing in the trash, the restore by UID targets the one trashed master
// of the UID in another calendar. Two trashed masters need the master ID.
func TestRestoreByUID_FallsBackToTrashedSeries(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, _ := twoCalendarSeries(t, svc)
	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantSeriesIntact(t, svc, masterB.CalendarID)
	wantSeriesIntact(t, svc, masterA.CalendarID)

	calC := insertCalendar(t, svc, "Third")
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: masterA.UID, CalendarID: calC, Title: "Standup C",
		StartTime: masterA.StartTime, EndTime: masterA.EndTime, RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}); err != nil {
		t.Fatalf("upsert master C: %v", err)
	}
	for _, cal := range []int64{masterB.CalendarID, calC} {
		if err := svc.DeleteSeries(ctx, cal, masterA.UID); err != nil {
			t.Fatalf("DeleteSeries(%d): %v", cal, err)
		}
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); !errors.Is(err, ErrAmbiguousUID) {
		t.Fatalf("RestoreByUID with two trashed masters err = %v, want ErrAmbiguousUID", err)
	}
}

// TestSeriesOwnerView_MatchesLegacyOwner: the view event_series_rows and
// uidmove.LegacyOwner agree on the owner of a legacy override.
func TestSeriesOwnerView_MatchesLegacyOwner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		masters []struct {
			calendar string
			trashed  bool
		}
	}{
		{name: "none"},
		{name: "one live", masters: []struct {
			calendar string
			trashed  bool
		}{{"A", false}}},
		{name: "one trashed", masters: []struct {
			calendar string
			trashed  bool
		}{{"A", true}}},
		{name: "two live", masters: []struct {
			calendar string
			trashed  bool
		}{{"A", false}, {"B", false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
			second := start.Add(24 * time.Hour)
			for _, m := range tc.masters {
				cal := insertCalendar(t, svc, m.calendar)
				master, err := svc.UpsertByUID(ctx, UpsertParams{
					UID: "owner-uid", CalendarID: cal, Title: m.calendar,
					StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
				})
				if err != nil {
					t.Fatalf("upsert master %s: %v", m.calendar, err)
				}
				if m.trashed {
					if err := svc.q.SoftDeleteEvent(ctx, master.ID); err != nil {
						t.Fatalf("trash master %s: %v", m.calendar, err)
					}
				}
			}
			legacy, err := svc.UpsertByUID(ctx, UpsertParams{
				UID: "owner-uid", CalendarID: 1, Title: "Legacy",
				StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
			})
			if err != nil {
				t.Fatalf("upsert legacy override: %v", err)
			}
			rows, err := svc.q.ListEventMastersByUIDIncludingDeleted(ctx, "owner-uid")
			if err != nil {
				t.Fatalf("list masters: %v", err)
			}
			want, ok, _ := uidmove.LegacyOwner(uidMasters(rows))
			got, err := svc.q.SeriesOwnerCalendarIDs(ctx, legacy.ID)
			if err != nil {
				t.Fatalf("SeriesOwnerCalendarIDs: %v", err)
			}
			switch {
			case len(rows) == 0:
				// No master anywhere: the row is an orphan of its own
				// calendar in the view, and LegacyOwner names no master.
				if ok || len(got) != 1 || got[0] != legacy.CalendarID {
					t.Errorf("view owners = %v, LegacyOwner ok = %v; want the own calendar %d and no master", got, ok, legacy.CalendarID)
				}
			case !ok && len(got) != 0:
				t.Errorf("view owners = %v, want none (LegacyOwner ok = false)", got)
			case ok && (len(got) != 1 || got[0] != want):
				t.Errorf("view owners = %v, want [%d] (LegacyOwner)", got, want)
			}
		})
	}
}

// TestMoveSeries_StaleLogRowInTarget: a log row of the UID left in the
// target calendar (a purge of the master keeps no log, but an older
// version did) does not block the calendar move. The moved log row takes
// its key.
func TestMoveSeries_StaleLogRowInTarget(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	calB := insertCalendar(t, svc, "Target")
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, third); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	// Stale rows in the target calendar with the keys of the moved rows.
	for _, stmt := range []string{
		`INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (?, ?, ?)`,
		`INSERT INTO event_truncate_deletes (calendar_id, uid, cutoff_time, previous_rrule) VALUES (?, ?, ?, 'FREQ=DAILY')`,
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids) VALUES (?, ?, '')`,
	} {
		args := []any{calB, master.UID, second.Format(time.RFC3339)}
		if strings.Contains(stmt, "truncate") {
			args[2] = third.Format(time.RFC3339)
		}
		if strings.Contains(stmt, "series") {
			args = args[:2]
		}
		if _, err := svc.db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatalf("insert stale row: %v", err)
		}
	}
	var sourceExdateID int64
	if err := svc.db.QueryRowContext(ctx, `SELECT id FROM event_exdate_deletes WHERE uid = ? AND calendar_id = 1`, master.UID).Scan(&sourceExdateID); err != nil {
		t.Fatalf("source exdate log id: %v", err)
	}
	fresh, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := svc.Update(ctx, master.ID, UpdateParams{
		CalendarID: calB, Title: fresh.Title, StartTime: fresh.StartTime, EndTime: fresh.EndTime,
		RecurrenceRule: fresh.RecurrenceRule, ExDates: fresh.ExDates,
	}); err != nil {
		t.Fatalf("move to the target calendar: %v", err)
	}
	for _, table := range []string{"event_exdate_deletes", "event_truncate_deletes"} {
		var n int
		if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE uid = ? AND calendar_id = ?`, master.UID, calB).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s rows in the target after the move = %d, want 1 (the moved row)", table, n)
		}
	}
	// The surviving rows are the moved rows, not the stale rows.
	var movedExdateID int64
	if err := svc.db.QueryRowContext(ctx, `SELECT id FROM event_exdate_deletes WHERE uid = ? AND calendar_id = ?`, master.UID, calB).Scan(&movedExdateID); err != nil {
		t.Fatalf("moved exdate log id: %v", err)
	}
	if movedExdateID != sourceExdateID {
		t.Errorf("exdate log row in the target = id %d, want the moved row %d", movedExdateID, sourceExdateID)
	}
	var rule, hiddenIDs string
	if err := svc.db.QueryRowContext(ctx, `SELECT previous_rrule, COALESCE(hidden_ids, '') FROM event_truncate_deletes WHERE uid = ? AND calendar_id = ?`, master.UID, calB).Scan(&rule, &hiddenIDs); err != nil {
		t.Fatalf("moved truncate log: %v", err)
	}
	if rule != "FREQ=DAILY;COUNT=4" || hiddenIDs == "" {
		t.Errorf("truncate log row in the target = rule %q, hidden_ids %q; want the moved row", rule, hiddenIDs)
	}
	var stale int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_series_deletes WHERE uid = ? AND calendar_id = ?`, master.UID, calB).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("stale series log rows in the target = %d, %v; want 0", stale, err)
	}
	// The log rows still restore after the move.
	entries, err := svc.ListTrash(ctx, calB)
	if err != nil {
		t.Fatalf("ListTrash(target): %v", err)
	}
	for _, e := range entries {
		if e.Kind == TrashKindInstance || e.Kind == TrashKindTruncation {
			if err := svc.RestoreTrash(ctx, e); err != nil {
				t.Errorf("RestoreTrash(%v) after the move: %v", e.Kind, err)
			}
		}
	}
}

// TestPurgeByID_MasterDropsLogRows: a purge of a master drops the delete
// logs of its series in that calendar.
func TestPurgeByID_MasterDropsLogRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.PurgeByID(ctx, master.ID); err != nil {
		t.Fatalf("PurgeByID(master): %v", err)
	}
	for _, table := range []string{"event_exdate_deletes", "event_series_deletes"} {
		var n int
		if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE uid = ?`, master.UID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s rows after the purge of the master = %d, want 0", table, n)
		}
	}
}

// TestRestoreSeries_KeepsPrunedOverride: an override that a sync prune
// hid without a log row stays hidden after the restore of the series.
// The series-delete log lists the overrides the series delete hid.
func TestRestoreSeries_KeepsPrunedOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	pruned, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	// A sync prune soft-deletes the row with no log.
	if err := svc.q.SoftDeleteEvent(ctx, pruned.ID); err != nil {
		t.Fatalf("prune override: %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
	if err != nil {
		t.Fatalf("series delete log: %v", err)
	}
	thirdRow, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get third override: %v", err)
	}
	if log.HiddenIds != strconv.FormatInt(thirdRow.ID, 10) {
		t.Errorf("series delete log lists %q, want the row of the third occurrence only (%d)", log.HiddenIds, thirdRow.ID)
	}
	for name, restore := range map[string]func() error{
		"undo":         func() error { return svc.RestoreUndo(ctx, meta) },
		"by master ID": func() error { return svc.RestoreByID(ctx, master.ID) },
	} {
		if err := restore(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		overrides, err := svc.ListSeriesOverrides(ctx, 1, master.UID)
		if err != nil {
			t.Fatalf("ListSeriesOverrides: %v", err)
		}
		if len(overrides) != 1 || overrides[0].RecurrenceID != third.Format(time.RFC3339) {
			t.Errorf("%s: live overrides = %+v, want the third occurrence only", name, overrides)
		}
		if r, err := svc.GetIncludingDeleted(ctx, pruned.ID); err != nil || r.DeletedAt == nil {
			t.Errorf("%s: pruned override = %+v, %v; want it still hidden", name, r, err)
		}
		if _, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID}); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("%s: series delete log after the restore err = %v, want sql.ErrNoRows", name, err)
		}
		if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
			t.Fatalf("%s: DeleteSeries again: %v", name, err)
		}
	}
}

// TestUndo_LegacyOccurrenceAfterSeriesDelete: the undo of an occurrence
// delete whose override is a legacy row in another calendar finds that
// row through the series, after the master went to the trash.
func TestUndo_LegacyOccurrenceAfterSeriesDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Master")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "legacy-uid", CalendarID: calB, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	second := start.Add(24 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "legacy-uid", CalendarID: 1, Title: "Legacy override",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	meta, err := svc.DeleteInstanceWithUndo(ctx, calB, "legacy-uid", second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if meta.CalendarID != calB {
		t.Fatalf("meta.CalendarID = %d, want the master calendar %d", meta.CalendarID, calB)
	}
	if err := svc.DeleteSeries(ctx, calB, "legacy-uid"); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy override after the undo = %+v, %v; want it live", r, err)
	}
	m, err := svc.GetIncludingDeleted(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if m.DeletedAt == nil {
		t.Errorf("the master came back with the undo, want it still in the trash")
	}
	if m.ExDates != "" {
		t.Errorf("exdates of the trashed master = %q, want the occurrence cleared", m.ExDates)
	}
}

// TestUpdateInstance_SeriesOfTheRowCalendar: the occurrence edit takes the
// series of the calendar of the row, so with two live masters the edit of
// an occurrence of B acts on B. With no calendar, the UID is ambiguous.
func TestUpdateInstance_SeriesOfTheRowCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)
	p := UpdateParams{Title: "B third", StartTime: third, EndTime: third.Add(time.Hour)}
	if _, err := svc.UpdateInstance(ctx, 0, masterB.UID, third, p); !errors.Is(err, ErrAmbiguousUID) {
		t.Fatalf("UpdateInstance(no calendar) err = %v, want ErrAmbiguousUID", err)
	}
	got, err := svc.UpdateInstance(ctx, masterB.CalendarID, masterB.UID, third, p)
	if err != nil {
		t.Fatalf("UpdateInstance(B): %v", err)
	}
	if got.CalendarID != masterB.CalendarID {
		t.Errorf("new override is in calendar %d, want %d", got.CalendarID, masterB.CalendarID)
	}
	wantSeriesIntact(t, svc, masterA.CalendarID)
	overrides, err := svc.ListSeriesOverrides(ctx, masterB.CalendarID, masterB.UID)
	if err != nil || len(overrides) != 2 {
		t.Errorf("overrides of B = %+v, %v; want 2", overrides, err)
	}
}

// TestRestoreSeries_LoggedRowAfterOwnerChange: the series delete logs the
// rows it hid by ID, so a legacy override comes back with the series even
// when another master took the ownership after the delete.
func TestRestoreSeries_LoggedRowAfterOwnerChange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Other")
	calC := insertCalendar(t, svc, "Legacy")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	masterA, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "owner-uid", CalendarID: 1, Title: "A",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master A: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "owner-uid", CalendarID: calC, Title: "Legacy",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, "owner-uid"); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Fatalf("legacy row after DeleteSeries(A) = %+v, %v; want it hidden", r, err)
	}
	// A new live master in B owns the legacy row now.
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "owner-uid", CalendarID: calB, Title: "B",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master B: %v", err)
	}
	if err := svc.RestoreByID(ctx, masterA.ID); err != nil {
		t.Fatalf("RestoreByID(A): %v", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after the restore of A = %+v, %v; want it live", r, err)
	}
}

// TestUndo_MasterDeleteAfterTrashRestore is the issue #287 rule on the
// undo of a master delete: delete one occurrence, delete the master with
// undo, restore the master from the trash, then undo. The undo has
// nothing to do, and the occurrence stays deleted.
func TestUndo_MasterDeleteAfterTrashRestore(t *testing.T) {
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
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, master.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(master): %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo after the trash restore err = %v, want ErrNotDeleted", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("own deleted occurrence after the undo = %+v, %v; want it still deleted", r, err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if !strings.Contains(m.ExDates, second.Format(time.RFC3339)) {
		t.Errorf("exdates after the undo = %q, want the own deleted occurrence still excluded", m.ExDates)
	}
}

// TestRestoreByID_MasterWithoutLogComesBackAlone: a master with no
// series-delete log row (a plain delete) comes back alone. An override
// that a sync prune hid with no log stays hidden.
func TestRestoreByID_MasterWithoutLogComesBackAlone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "pruned-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "pruned-uid", CalendarID: 1, Title: "Late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	if err := svc.q.SoftDeleteEvent(ctx, override.ID); err != nil {
		t.Fatalf("prune override: %v", err)
	}
	if err := svc.Delete(ctx, master.ID); err != nil {
		t.Fatalf("Delete(master): %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	if m, err := svc.Get(ctx, master.ID); err != nil || m.DeletedAt != nil {
		t.Errorf("master after the restore = %+v, %v; want it live", m, err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("pruned override after the restore = %+v, %v; want it still hidden", r, err)
	}
}

// TestUndoSeries_AfterTrashRestore: the undo of a series delete after a
// restore from the trash has nothing to do.
func TestUndoSeries_AfterTrashRestore(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo after the trash restore err = %v, want ErrNotDeleted", err)
	}
	overrides, err := svc.ListSeriesOverrides(ctx, 1, master.UID)
	if err != nil || len(overrides) != 2 {
		t.Errorf("live overrides = %+v, %v; want both", overrides, err)
	}
}

// TestDeleteSeries_TrashedCalendarBesideTwoLiveMasters: a calendar whose
// rows of the UID are all in the trash holds no live series, so a series
// delete there reports ErrSeriesInTrash, not an ambiguity, also when two
// other calendars hold live masters.
func TestDeleteSeries_TrashedCalendarBesideTwoLiveMasters(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, _, _ := twoCalendarSeries(t, svc)
	calC := insertCalendar(t, svc, "Third")
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: masterA.UID, CalendarID: calC, Title: "Standup C",
		StartTime: masterA.StartTime, EndTime: masterA.EndTime, RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}); err != nil {
		t.Fatalf("upsert master C: %v", err)
	}
	if err := svc.DeleteSeries(ctx, calC, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(C): %v", err)
	}
	if _, err := svc.SeriesMaster(ctx, calC, masterA.UID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SeriesMaster(trashed C) err = %v, want sql.ErrNoRows", err)
	}
	if err := svc.DeleteSeries(ctx, calC, masterA.UID); !errors.Is(err, ErrSeriesInTrash) {
		t.Errorf("DeleteSeries(trashed C) err = %v, want ErrSeriesInTrash", err)
	}
}

// TestGetByUID_AllMastersTrashed: a UID whose masters are all in the
// trash, in two calendars, is not found by a live lookup.
func TestGetByUID_AllMastersTrashed(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	for _, cal := range []int64{masterA.CalendarID, masterB.CalendarID} {
		if err := svc.DeleteSeries(ctx, cal, masterA.UID); err != nil {
			t.Fatalf("DeleteSeries(%d): %v", cal, err)
		}
	}
	if _, err := svc.GetByUID(ctx, masterA.UID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByUID err = %v, want sql.ErrNoRows", err)
	}
	if _, err := svc.SeriesMaster(ctx, 0, masterA.UID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SeriesMaster(0) err = %v, want sql.ErrNoRows", err)
	}
	if err := svc.DeleteInstance(ctx, 0, masterA.UID, second); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteInstance(0) err = %v, want sql.ErrNoRows", err)
	}
	// A restore still sees both trashed masters as ambiguous.
	if err := svc.RestoreByUID(ctx, masterA.UID); !errors.Is(err, ErrAmbiguousUID) {
		t.Errorf("RestoreByUID err = %v, want ErrAmbiguousUID", err)
	}
}

// TestUndo_OrphanOverride: the undo of a delete of an orphan override
// (no master anywhere) un-hides that row, reports a purged row, and does
// nothing twice.
func TestUndo_OrphanOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	orphan, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "orphan-uid", CalendarID: 1, Title: "Orphan",
		StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert orphan override: %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, orphan.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if r, err := svc.Get(ctx, orphan.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("orphan after the undo = %+v, %v; want it live", r, err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("second RestoreUndo err = %v, want ErrNotDeleted", err)
	}
	if err := svc.Delete(ctx, orphan.ID); err != nil {
		t.Fatalf("Delete again: %v", err)
	}
	if err := svc.PurgeByID(ctx, orphan.ID); err != nil {
		t.Fatalf("PurgeByID: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("RestoreUndo of a purged row err = %v, want ErrNotDeleted", err)
	}
}

// TestDeleteInstance_TwoLegacyOverridesAtOneOccurrence: two legacy rows
// of one occurrence, both owned by the one master, give an error that
// names the rows.
func TestDeleteInstance_TwoLegacyOverridesAtOneOccurrence(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "twin-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	ids := make([]int64, 0, 2)
	for _, name := range []string{"Legacy C", "Legacy D"} {
		cal := insertCalendar(t, svc, name)
		row, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: "twin-uid", CalendarID: cal, Title: name,
			StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		ids = append(ids, row.ID)
	}
	err = svc.DeleteInstance(ctx, 1, master.UID, second)
	if !errors.Is(err, ErrAmbiguousOverride) {
		t.Fatalf("DeleteInstance err = %v, want ErrAmbiguousOverride", err)
	}
	for _, id := range ids {
		if !strings.Contains(err.Error(), strconv.FormatInt(id, 10)) {
			t.Errorf("error %q does not name row %d", err, id)
		}
	}
	if _, err := svc.SeriesOverride(ctx, 1, master.UID, second.Format(time.RFC3339)); !errors.Is(err, ErrAmbiguousOverride) {
		t.Errorf("SeriesOverride err = %v, want ErrAmbiguousOverride", err)
	}
}

// TestMoveLegacyBack_KeepsMasterLogs: a legacy override that goes back to
// the calendar of its master leaves the delete logs of that calendar in
// place. Only a target with no row of the UID loses its stale logs.
func TestMoveLegacyBack_KeepsMasterLogs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	// The legacy row is the first occurrence, before the occurrence
	// delete and the truncation below, so both leave it live.
	first := master.StartTime
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: first, EndTime: first.Add(time.Hour), RecurrenceID: first.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, third); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids) VALUES (1, ?, '')`, master.UID); err != nil {
		t.Fatalf("insert series log: %v", err)
	}
	// A log row in the legacy calendar that collides with one of the master.
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (?, ?, ?)`, calL, master.UID, second.Format(time.RFC3339)); err != nil {
		t.Fatalf("insert colliding log: %v", err)
	}
	if _, err := svc.Update(ctx, legacy.ID, UpdateParams{
		CalendarID: 1, Title: legacy.Title, StartTime: legacy.StartTime, EndTime: legacy.EndTime,
	}); err != nil {
		t.Fatalf("move the legacy override back: %v", err)
	}
	for table, want := range map[string]int{"event_exdate_deletes": 1, "event_truncate_deletes": 1, "event_series_deletes": 1} {
		var n int
		if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE uid = ? AND calendar_id = 1`, master.UID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("%s rows of the master calendar after the move = %d, want %d", table, n, want)
		}
	}
	var stray int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_exdate_deletes WHERE uid = ? AND calendar_id = ?`, master.UID, calL).Scan(&stray); err != nil || stray != 0 {
		t.Errorf("log rows left in the legacy calendar = %d, %v; want 0", stray, err)
	}
}

// TestRestoreByUID_RestoresLoggedRowsFirst: the restore by UID un-hides
// the rows of the series-delete log by ID and consumes the log.
func TestRestoreByUID_RestoresLoggedRowsFirst(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	at := master.StartTime.Add(72 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after RestoreByUID = %+v, %v; want it live", r, err)
	}
	if _, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("series delete log after RestoreByUID err = %v, want sql.ErrNoRows", err)
	}
	if m, err := svc.Get(ctx, master.ID); err != nil || m.DeletedAt != nil {
		t.Errorf("master after RestoreByUID = %+v, %v; want it live", m, err)
	}
	overrides, err := svc.ListSeriesOverrides(ctx, 1, master.UID)
	if err != nil || len(overrides) != 3 {
		t.Errorf("live overrides after RestoreByUID = %d, %v; want 3 (two own and the legacy row)", len(overrides), err)
	}
}

// TestSeriesMaster_LiveLegacyBesideTwoTrashedMasters: two masters in the
// trash make no ambiguity for a live edit of a legacy override.
func TestSeriesMaster_LiveLegacyBesideTwoTrashedMasters(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)
	legacy := legacyOverrideOfTwoMasters(t, svc, masterA, third)
	for _, cal := range []int64{masterA.CalendarID, masterB.CalendarID} {
		if err := svc.DeleteSeries(ctx, cal, masterA.UID); err != nil {
			t.Fatalf("DeleteSeries(%d): %v", cal, err)
		}
	}
	if _, err := svc.SeriesMaster(ctx, legacy.CalendarID, masterA.UID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SeriesMaster(legacy calendar) err = %v, want sql.ErrNoRows", err)
	}
	if err := svc.DeleteInstance(ctx, legacy.CalendarID, masterA.UID, third); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteInstance(legacy calendar) err = %v, want sql.ErrNoRows", err)
	}
}

// TestUndoOccurrence_RestoresTheHiddenRowOnly: the undo of an occurrence
// delete un-hides the row that the delete hid, not a row of the same
// occurrence that an earlier delete hid on its own.
func TestUndoOccurrence_RestoresTheHiddenRowOnly(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	twin, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy twin",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy twin: %v", err)
	}
	if err := svc.Delete(ctx, twin.ID); err != nil {
		t.Fatalf("Delete(twin): %v", err)
	}
	own, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get own override: %v", err)
	}
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if meta.RowID != own.ID {
		t.Errorf("meta.RowID = %d, want the own override %d", meta.RowID, own.ID)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if r, err := svc.Get(ctx, own.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("own override after the undo = %+v, %v; want it live", r, err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, twin.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("legacy twin after the undo = %+v, %v; want it still in the trash", r, err)
	}
}

// TestDeleteSeries_OrphanRowsBesideTrashedMaster: with the one master of
// the UID in the trash of another calendar, a series delete from the
// calendar of a live legacy override hides that calendar's own rows. A
// second delete finds nothing.
func TestDeleteSeries_OrphanRowsBesideTrashedMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calL := insertCalendar(t, svc, "Legacy")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "orphan-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "orphan-uid", CalendarID: calL, Title: "Legacy",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, "orphan-uid"); err != nil {
		t.Fatalf("DeleteSeries(1): %v", err)
	}
	// The series delete of calendar 1 hid the legacy row with it, so
	// bring it back alone to get the exact state.
	if err := svc.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	if err := svc.DeleteSeries(ctx, calL, "orphan-uid"); err != nil {
		t.Fatalf("DeleteSeries(legacy calendar): %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("legacy row after the delete = %+v, %v; want it hidden", r, err)
	}
	if err := svc.DeleteSeries(ctx, calL, "orphan-uid"); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("second DeleteSeries(legacy calendar) err = %v, want ErrNotDeleted", err)
	}
	if _, err := svc.DeleteSeriesWithUndo(ctx, calL, "orphan-uid"); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("DeleteSeriesWithUndo(legacy calendar) err = %v, want ErrNotDeleted", err)
	}
}

// TestRestoreTruncation_LoggedRowAfterOwnerChange: the truncation log
// names the hidden rows by ID, so a legacy override comes back with the
// truncation restore even when another master took its ownership.
func TestRestoreTruncation_LoggedRowAfterOwnerChange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Other")
	calL := insertCalendar(t, svc, "Legacy")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	third := second.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "trunc-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "trunc-uid", CalendarID: calL, Title: "Legacy",
		StartTime: third, EndTime: third.Add(time.Hour), RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Fatalf("legacy row after the truncation = %+v, %v; want it hidden", r, err)
	}
	// A second live master takes the ownership of the legacy row away.
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "trunc-uid", CalendarID: calB, Title: "Other",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	}); err != nil {
		t.Fatalf("upsert master B: %v", err)
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	var restored bool
	for _, e := range entries {
		if e.Kind == TrashKindTruncation {
			if err := svc.RestoreTrash(ctx, e); err != nil {
				t.Fatalf("RestoreTrash(truncation): %v", err)
			}
			restored = true
		}
	}
	if !restored {
		t.Fatal("no truncation entry in the trash")
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after the truncation restore = %+v, %v; want it live", r, err)
	}
}

// TestDeleteInstance_GuardsLegacyOverrideCalendar: the occurrence delete
// writes the calendar of a legacy override, so a read-only calendar of
// that row refuses the delete.
func TestDeleteInstance_GuardsLegacyOverrideCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	readOnly := insertGuardedCalendar(t, svc.db, "Read-Only", "read", "VEVENT")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guard-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guard-uid", CalendarID: readOnly, Title: "Legacy",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteInstance with a read-only legacy row err = %v, want ErrReadOnly", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after the refused delete = %+v, %v; want it live", r, err)
	}
	if m, err := svc.Get(ctx, master.ID); err != nil || m.ExDates != "" {
		t.Errorf("master after the refused delete = exdates %q, %v; want none", m.ExDates, err)
	}
}

// TestRestoreSeries_GuardsHiddenRowCalendars: the restore of a series
// writes the calendar of each hidden legacy row, so a read-only calendar
// of such a row refuses the restore.
func TestRestoreSeries_GuardsHiddenRowCalendars(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calL := insertCalendar(t, svc, "Legacy")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guard-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guard-uid", CalendarID: calL, Title: "Legacy",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = ?`, calL); err != nil {
		t.Fatalf("make the legacy calendar read-only: %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreByID with a read-only hidden row err = %v, want ErrReadOnly", err)
	}
	if m, err := svc.GetIncludingDeleted(ctx, master.ID); err != nil || m.DeletedAt == nil {
		t.Errorf("master after the refused restore = %+v, %v; want it still in the trash", m, err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("legacy row after the refused restore = %+v, %v; want it still in the trash", r, err)
	}
}

// TestUndo_PurgedMasterReportsNotDeleted: the undo of a master delete
// after a purge of the row has nothing to restore.
func TestUndo_PurgedMasterReportsNotDeleted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	meta, err := svc.DeleteWithUndo(ctx, e.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo: %v", err)
	}
	if err := svc.PurgeByID(ctx, e.ID); err != nil {
		t.Fatalf("PurgeByID: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo after the purge err = %v, want ErrNotDeleted", err)
	}
}

// TestRestoreInstance_MasterInTrash: the restore of an occurrence delete
// while the master is in the trash clears the EXDATE on the trashed
// master, so the restore of the master shows the occurrence.
func TestRestoreInstance_MasterInTrash(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	var checked bool
	for _, e := range entries {
		if e.Kind == TrashKindInstance {
			if err := svc.RestoreTrash(ctx, e); err != nil {
				t.Errorf("RestoreTrash(instance) with the master in the trash: %v", err)
			}
			checked = true
		}
	}
	if !checked {
		t.Fatal("no instance entry in the trash")
	}
	m, err := svc.GetIncludingDeleted(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if m.DeletedAt == nil {
		t.Errorf("the master came back with the instance restore, want it still in the trash")
	}
	if strings.Contains(m.ExDates, second.Format(time.RFC3339)) {
		t.Errorf("exdates of the trashed master = %q, want the occurrence cleared", m.ExDates)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	if m, err := svc.Get(ctx, master.ID); err != nil || strings.Contains(m.ExDates, second.Format(time.RFC3339)) {
		t.Errorf("master after the series restore = exdates %q, %v; want the occurrence visible", m.ExDates, err)
	}
}

// TestUndoSeries_OrphanRows: a series delete of orphan rows logs the rows
// it hid, so the undo brings them back.
func TestUndoSeries_OrphanRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calL := insertCalendar(t, svc, "Legacy")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "orphan-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "orphan-uid", CalendarID: calL, Title: "Legacy",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, "orphan-uid"); err != nil {
		t.Fatalf("DeleteSeries(1): %v", err)
	}
	if err := svc.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, calL, "orphan-uid")
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo(legacy calendar): %v", err)
	}
	if meta.CalendarID != calL || meta.Label != "Legacy" || meta.MasterID != 0 {
		t.Errorf("meta = %+v, want the legacy calendar, its label, and no master", meta)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt == nil {
		t.Fatalf("legacy row after the delete = %+v, %v; want it hidden", r, err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after the undo = %+v, %v; want it live", r, err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("second RestoreUndo err = %v, want ErrNotDeleted", err)
	}
}

// TestUndoMeta_MasterID: the delete records the master of the series,
// and the undo reverses the delete on that row.
func TestUndoMeta_MasterID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if meta.MasterID != master.ID {
		t.Errorf("meta.MasterID = %d, want %d", meta.MasterID, master.ID)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	overrides, err := svc.ListSeriesOverrides(ctx, 1, master.UID)
	if err != nil || len(overrides) != 2 {
		t.Errorf("live overrides after the undo = %d, %v; want 2", len(overrides), err)
	}
	// Older undo data without the master ID still finds the master.
	meta, err = svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("second DeleteInstanceWithUndo: %v", err)
	}
	meta.MasterID = 0
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo without a master ID: %v", err)
	}
	override, err := svc.DeleteWithUndo(ctx, overrides[0].ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(override): %v", err)
	}
	if override.MasterID != master.ID {
		t.Errorf("DeleteWithUndo(override).MasterID = %d, want %d", override.MasterID, master.ID)
	}
}

// TestGetByUIDAndRecurrenceID_TwoLegacyRowsNameThem: the lookup of one
// occurrence by UID names the two legacy rows of the occurrence.
func TestGetByUIDAndRecurrenceID_TwoLegacyRowsNameThem(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "twin-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	for _, name := range []string{"Legacy C", "Legacy D"} {
		cal := insertCalendar(t, svc, name)
		if _, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: "twin-uid", CalendarID: cal, Title: name,
			StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}
	_, err := svc.GetByUIDAndRecurrenceID(ctx, "twin-uid", second.Format(time.RFC3339))
	if !errors.Is(err, ErrAmbiguousOverride) {
		t.Fatalf("GetByUIDAndRecurrenceID err = %v, want ErrAmbiguousOverride", err)
	}
}
