package event

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

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
// override, or gained an EXDATE.
func wantSeriesIntact(t *testing.T, svc *Service, calendarID int64) {
	t.Helper()
	m, overrides := seriesState(t, svc, calendarID)
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

	// RestoreByUID cannot choose the series, so the restore goes by ID.
	if err := svc.RestoreByUID(ctx, masterB.UID); !errors.Is(err, ErrAmbiguousUID) {
		t.Fatalf("RestoreByUID err = %v, want ErrAmbiguousUID", err)
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
