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

// TestRestore_PrefersLiveMasterOverTrashedCopy: an old copy of the UID in
// the trash of another calendar does not make a restore ambiguous.
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

// TestRestoreByUID_OwnDeletedOccurrenceIsNoCandidate: the restore by UID
// reverses a series delete. The trashed series of A is the one candidate.
// The occurrence of B that the user deleted on its own is no candidate,
// so it stays deleted with its EXDATE (issue #287).
func TestRestoreByUID_OwnDeletedOccurrenceIsNoCandidate(t *testing.T) {
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
	wantSeriesIntact(t, svc, masterA.CalendarID)
	mB, overridesB := seriesState(t, svc, masterB.CalendarID)
	if got := storage.NullableToString(mB.Exdates); !strings.Contains(got, second.Format(time.RFC3339)) {
		t.Errorf("exdates of B = %q, want the own deleted occurrence still excluded", got)
	}
	if len(overridesB) != 0 {
		t.Errorf("live overrides of B = %d, want 0", len(overridesB))
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
			// The production lookup of the view: the master that owns the
			// rows of the calendar of the legacy row, live or in the trash.
			owner, err := svc.q.GetSeriesMasterOfCalendar(ctx, storage.GetSeriesMasterOfCalendarParams{
				Uid: "owner-uid", CalendarID: legacy.CalendarID, IncludeDeleted: true,
			})
			found, err := storage.Found(err)
			if err != nil {
				t.Fatalf("GetSeriesMasterOfCalendar: %v", err)
			}
			switch {
			case found != ok:
				t.Errorf("view owner found = %v, LegacyOwner ok = %v; want them equal", found, ok)
			case ok && owner.CalendarID != want:
				t.Errorf("view owner = calendar %d, want %d (LegacyOwner)", owner.CalendarID, want)
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
	// The rows that stay are the moved rows, not the stale rows.
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

// TestPurgeByID_MasterDropsLogRows: a purge of a master drops the
// occurrence logs of its series in that calendar. The series-delete log
// stays, so the undo of the series delete still reverses it.
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
	for table, want := range map[string]int{"event_exdate_deletes": 0, "event_series_deletes": 1} {
		var n int
		if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE uid = ?`, master.UID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("%s rows after the purge of the master = %d, want %d", table, n, want)
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
	// The prune is a separate, earlier delete.
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, override.ID); err != nil {
		t.Fatalf("date the prune: %v", err)
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
// of one occurrence, both owned by the one master, give a delete error
// that names the rows. A lookup of the occurrence takes the lowest ID.
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
	if got, err := svc.SeriesOverride(ctx, 1, master.UID, second.Format(time.RFC3339)); err != nil || got.ID != ids[0] {
		t.Errorf("SeriesOverride = %d, %v; want the lowest ID %d", got.ID, err, ids[0])
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
	// The undo reversed its own delete only: the master of calendar 1
	// stays in the trash, and the log of the legacy calendar is consumed.
	m, err := svc.q.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{CalendarID: 1, Uid: "orphan-uid"})
	if err != nil || m.DeletedAt == nil {
		t.Errorf("master of calendar 1 after the undo = trashed %v, %v; want it still in the trash", m.DeletedAt != nil, err)
	}
	if _, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: calL, Uid: "orphan-uid"}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("series delete log of the legacy calendar after the undo err = %v, want sql.ErrNoRows", err)
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

// TestUndoSeries_OrphanRowsBesideMasterHiddenByPull is the reviewer's
// state: a pull hid the master of calendar 1 with no log, a legacy
// override lives in L. The undo of the series delete of L restores the
// legacy row from the log of L, leaves the master in the trash, and
// consumes the log. The same holds for a delete that names no calendar.
func TestUndoSeries_OrphanRowsBesideMasterHiddenByPull(t *testing.T) {
	for name, calendar := range map[string]bool{"named calendar": true, "calendar 0": false} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			calL := insertCalendar(t, svc, "Legacy")
			start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
			second := start.Add(24 * time.Hour)
			master, err := svc.UpsertByUID(ctx, UpsertParams{
				UID: "pull-uid", CalendarID: 1, Title: "Series",
				StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
			})
			if err != nil {
				t.Fatalf("upsert master: %v", err)
			}
			legacy, err := svc.UpsertByUID(ctx, UpsertParams{
				UID: "pull-uid", CalendarID: calL, Title: "Legacy",
				StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
			})
			if err != nil {
				t.Fatalf("upsert legacy override: %v", err)
			}
			// A pull hides the master with no log.
			if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: "pull-uid"}); err != nil {
				t.Fatalf("hide the master: %v", err)
			}
			target := calL
			if !calendar {
				target = 0
			}
			meta, err := svc.DeleteSeriesWithUndo(ctx, target, "pull-uid")
			if err != nil {
				t.Fatalf("DeleteSeriesWithUndo(%d): %v", target, err)
			}
			if meta.CalendarID != calL || meta.MasterID != 0 {
				t.Fatalf("meta = %+v, want the legacy calendar and no master", meta)
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
			if m, err := svc.GetIncludingDeleted(ctx, master.ID); err != nil || m.DeletedAt == nil {
				t.Errorf("master after the undo = trashed %v, %v; want it still in the trash", m.DeletedAt != nil, err)
			}
			if _, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: calL, Uid: "pull-uid"}); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("log of the legacy calendar after the undo err = %v, want sql.ErrNoRows", err)
			}
		})
	}
}

// TestRestoreByID_DropsRowFromLogs: a restore of one logged row by ID
// drops it from the series-delete log and from the truncation log, so a
// later independent delete of that row is not reversed by the restore
// of the series or of the truncation.
func TestRestoreByID_DropsRowFromLogs(t *testing.T) {
	t.Run("series", func(t *testing.T) {
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
		log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
		if err != nil {
			t.Fatalf("series delete log: %v", err)
		}
		if strings.Contains(","+log.HiddenIds+",", ","+strconv.FormatInt(override.ID, 10)+",") {
			t.Errorf("series delete log still names the restored row: %q", log.HiddenIds)
		}
		if err := svc.Delete(ctx, override.ID); err != nil {
			t.Fatalf("Delete(override) on its own: %v", err)
		}
		if err := svc.RestoreByID(ctx, master.ID); err != nil {
			t.Fatalf("RestoreByID(master): %v", err)
		}
		if r, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || r.DeletedAt == nil {
			t.Errorf("override after the series restore = %+v, %v; want it still deleted", r, err)
		}
	})
	t.Run("truncation", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		master, second, third := singleSeriesWithOverrides(t, svc)
		override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("get override: %v", err)
		}
		if err := svc.DeleteFromInstance(ctx, 1, master.UID, second); err != nil {
			t.Fatalf("DeleteFromInstance: %v", err)
		}
		if err := svc.RestoreByID(ctx, override.ID); err != nil {
			t.Fatalf("RestoreByID(override): %v", err)
		}
		if err := svc.Delete(ctx, override.ID); err != nil {
			t.Fatalf("Delete(override) on its own: %v", err)
		}
		entries, err := svc.ListTrash(ctx, 1)
		if err != nil {
			t.Fatalf("ListTrash: %v", err)
		}
		for _, e := range entries {
			if e.Kind == TrashKindTruncation {
				if err := svc.RestoreTrash(ctx, e); err != nil {
					t.Fatalf("RestoreTrash(truncation): %v", err)
				}
			}
		}
		if r, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || r.DeletedAt == nil {
			t.Errorf("override after the truncation restore = %+v, %v; want it still deleted", r, err)
		}
	})
}

// TestUndo_PurgedMasterBesideTrashedCopy: the undo of a master delete
// after a purge of the row has nothing to reverse, also when another
// calendar holds a copy of the UID in the trash.
func TestUndo_PurgedMasterBesideTrashedCopy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, _ := twoCalendarSeries(t, svc)
	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	for _, o := range mustListSeriesOverrides(t, svc, masterA.CalendarID, masterA.UID) {
		if err := svc.Delete(ctx, o.ID); err != nil {
			t.Fatalf("Delete(override A): %v", err)
		}
	}
	meta, err := svc.DeleteWithUndo(ctx, masterA.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(master A): %v", err)
	}
	if err := svc.PurgeByID(ctx, masterA.ID); err != nil {
		t.Fatalf("PurgeByID(master A): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo after the purge err = %v, want ErrNotDeleted", err)
	}
	if m, err := svc.GetIncludingDeleted(ctx, masterB.ID); err != nil || m.DeletedAt == nil {
		t.Errorf("master B after the undo = trashed %v, %v; want it still in the trash", m.DeletedAt != nil, err)
	}
}

func mustListSeriesOverrides(t *testing.T, svc *Service, calendarID int64, uid string) []Event {
	t.Helper()
	overrides, err := svc.ListSeriesOverrides(context.Background(), calendarID, uid)
	if err != nil {
		t.Fatalf("ListSeriesOverrides(%d): %v", calendarID, err)
	}
	return overrides
}

// TestOrphanDelete_MergesIntoSeriesLog: a delete of orphan rows beside
// the trashed master of the calendar joins the log of the earlier series
// delete, so the restore of the master brings every hidden row back.
func TestOrphanDelete_MergesIntoSeriesLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	o1, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get o1: %v", err)
	}
	o2, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get o2: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, o1.ID); err != nil {
		t.Fatalf("RestoreByID(o1): %v", err)
	}
	// o1 is an orphan beside the trashed master: its series delete logs
	// it next to o2, and does not touch the master.
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo(orphan o1): %v", err)
	}
	if meta.MasterID != 0 {
		t.Errorf("meta.MasterID = %d, want 0 (no live master)", meta.MasterID)
	}
	log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
	if err != nil {
		t.Fatalf("series delete log: %v", err)
	}
	ids, _ := parseRowIDs(log.HiddenIds)
	if len(ids) != 2 {
		t.Fatalf("series delete log names %v, want o1 and o2", ids)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	for _, id := range []int64{master.ID, o1.ID, o2.ID} {
		if r, err := svc.Get(ctx, id); err != nil || r.DeletedAt != nil {
			t.Errorf("row %d after the restore of the master = %+v, %v; want it live", id, r, err)
		}
	}
}

// TestUndoOrphanDelete_LeavesMasterInTrash: the undo of a delete of
// orphan rows never touches the trashed master of the calendar.
func TestUndoOrphanDelete_LeavesMasterInTrash(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	o1, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get o1: %v", err)
	}
	// A pull hid the series with no log.
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID}); err != nil {
		t.Fatalf("hide the series: %v", err)
	}
	if err := svc.RestoreByID(ctx, o1.ID); err != nil {
		t.Fatalf("RestoreByID(o1): %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo(orphan o1): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if r, err := svc.Get(ctx, o1.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("o1 after the undo = %+v, %v; want it live", r, err)
	}
	if m, err := svc.GetIncludingDeleted(ctx, master.ID); err != nil || m.DeletedAt == nil {
		t.Errorf("master after the undo = trashed %v, %v; want it still in the trash", m.DeletedAt != nil, err)
	}
}

// TestGetByUIDAndRecurrenceID_NoFallbackWithLiveMaster: with a live
// master and no override at the occurrence, the lookup finds nothing. A
// row of another calendar's trashed series is not the occurrence.
func TestGetByUIDAndRecurrenceID_NoFallbackWithLiveMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	ownA, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: masterA.CalendarID, Uid: masterA.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override A: %v", err)
	}
	if err := svc.Delete(ctx, ownA.ID); err != nil {
		t.Fatalf("Delete(override A): %v", err)
	}
	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if _, err := svc.GetByUIDAndRecurrenceID(ctx, masterA.UID, second.Format(time.RFC3339)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByUIDAndRecurrenceID err = %v, want sql.ErrNoRows (no override beside the live master)", err)
	}
}

// TestMoveSeries_TargetWithTrashedLegacyRow: a series move into a
// calendar that holds no master of the UID, only a trashed legacy row at
// another occurrence, lets the logs of the moved series win over the
// stale logs of the target.
func TestMoveSeries_TargetWithTrashedLegacyRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	calB := insertCalendar(t, svc, "Target")
	fourth := third.Add(24 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calB, Title: "Stale legacy",
		StartTime: fourth, EndTime: fourth.Add(time.Hour), RecurrenceID: fourth.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy row in the target: %v", err)
	}
	if err := svc.q.SoftDeleteEvent(ctx, legacy.ID); err != nil {
		t.Fatalf("trash the legacy row: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	var sourceLogID int64
	if err := svc.db.QueryRowContext(ctx, `SELECT id FROM event_exdate_deletes WHERE uid = ? AND calendar_id = 1`, master.UID).Scan(&sourceLogID); err != nil {
		t.Fatalf("source log id: %v", err)
	}
	// A stale log row of the target with the key of the moved row.
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (?, ?, ?)`, calB, master.UID, second.Format(time.RFC3339)); err != nil {
		t.Fatalf("insert stale target log: %v", err)
	}
	fresh, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := svc.Update(ctx, master.ID, UpdateParams{
		CalendarID: calB, Title: fresh.Title, StartTime: fresh.StartTime, EndTime: fresh.EndTime,
		RecurrenceRule: fresh.RecurrenceRule, ExDates: fresh.ExDates,
	}); err != nil {
		t.Fatalf("move the series: %v", err)
	}
	var movedID int64
	if err := svc.db.QueryRowContext(ctx, `SELECT id FROM event_exdate_deletes WHERE uid = ? AND calendar_id = ?`, master.UID, calB).Scan(&movedID); err != nil {
		t.Fatalf("log row in the target after the move: %v", err)
	}
	if movedID != sourceLogID {
		t.Errorf("log row in the target = id %d, want the moved row %d (the source logs win)", movedID, sourceLogID)
	}
}

// TestDeleteFromInstance_GuardsLegacyOverrideCalendar: a truncation hides
// the legacy overrides of the series after the cutoff, so the calendar
// of such a row takes the write, and a read-only calendar refuses the
// truncation before any change.
func TestDeleteFromInstance_GuardsLegacyOverrideCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	readOnly := insertGuardedCalendar(t, svc.db, "Read-Only", "read", "VEVENT")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	third := second.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guard-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "guard-uid", CalendarID: readOnly, Title: "Legacy",
		StartTime: third, EndTime: third.Add(time.Hour), RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, second); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteFromInstance with a read-only legacy row err = %v, want ErrReadOnly", err)
	}
	if r, err := svc.Get(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after the refused truncation = %+v, %v; want it live", r, err)
	}
	if m, err := svc.Get(ctx, master.ID); err != nil || m.RecurrenceRule != "FREQ=DAILY;COUNT=4" {
		t.Errorf("master after the refused truncation = rule %q, %v; want the rule unchanged", m.RecurrenceRule, err)
	}
}

// TestUndoOrphanDelete_Exact: the undo of a delete of orphan rows
// un-hides only the rows that delete hid, and leaves the rest of the
// series-delete log for the restore of the master.
func TestUndoOrphanDelete_Exact(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	o1, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get o1: %v", err)
	}
	o2, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get o2: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, o1.ID); err != nil {
		t.Fatalf("RestoreByID(o1): %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo(orphan o1): %v", err)
	}
	if meta.MasterID != 0 || len(meta.RowIDs) != 1 || meta.RowIDs[0] != o1.ID {
		t.Fatalf("meta = %+v, want no master and RowIDs [o1 %d]", meta, o1.ID)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if r, err := svc.Get(ctx, o1.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("o1 after the undo = %+v, %v; want it live", r, err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, o2.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("o2 after the undo = %+v, %v; want it still in the trash", r, err)
	}
	log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
	if err != nil {
		t.Fatalf("series delete log after the undo: %v", err)
	}
	if log.HiddenIds != strconv.FormatInt(o2.ID, 10) {
		t.Errorf("series delete log after the undo = %q, want o2 only (%d)", log.HiddenIds, o2.ID)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	if r, err := svc.Get(ctx, o2.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("o2 after the restore of the master = %+v, %v; want it live", r, err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("second RestoreUndo err = %v, want ErrNotDeleted", err)
	}
}

// TestRestoreUndo_SeriesWithoutRecord: undo data of a series delete with
// no master and no rows names no delete, so the undo changes nothing.
func TestRestoreUndo_SeriesWithoutRecord(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	meta := UndoMeta{Kind: UndoKindSeries, UID: master.UID, CalendarID: 1}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo without a record err = %v, want ErrNotDeleted", err)
	}
	if m, err := svc.GetIncludingDeleted(ctx, master.ID); err != nil || m.DeletedAt == nil {
		t.Errorf("master after the undo without a record = trashed %v, %v; want it still in the trash", m.DeletedAt != nil, err)
	}
}

// TestRestoreByUID_LeavesTruncatedRowsToTheirLog: the restore by UID
// leaves the rows that a truncation hid. The trash entry of the
// truncation restores them with the rule.
func TestRestoreByUID_LeavesTruncatedRowsToTheirLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	tail, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get tail override: %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, third); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("master after RestoreByUID: %v", err)
	}
	if !strings.Contains(m.RecurrenceRule, "UNTIL=") {
		t.Errorf("rule after RestoreByUID = %q, want the truncated rule", m.RecurrenceRule)
	}
	if r, err := svc.GetIncludingDeleted(ctx, tail.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("tail override after RestoreByUID = %+v, %v; want it still hidden", r, err)
	}
	if _, err := svc.SeriesOverride(ctx, 1, master.UID, second.Format(time.RFC3339)); err != nil {
		t.Errorf("override before the cutoff after RestoreByUID: %v, want it live", err)
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
		t.Fatal("no truncation entry in the trash after RestoreByUID")
	}
	if r, err := svc.Get(ctx, tail.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("tail override after the truncation restore = %+v, %v; want it live", r, err)
	}
	if m, err := svc.Get(ctx, master.ID); err != nil || strings.Contains(m.RecurrenceRule, "UNTIL=") {
		t.Errorf("rule after the truncation restore = %q, %v; want the original rule", m.RecurrenceRule, err)
	}
}

// TestDeleteSeries_FreshLogAfterRevivalByUpsert covers issue #287 on the
// series log. A pull or a re-import revives a trashed series by upsert,
// and the upsert keeps the old log. The next series delete with a live
// master writes a fresh log, so an override that the user deleted on its
// own after the revival stays deleted after the next restore.
func TestDeleteSeries_FreshLogAfterRevivalByUpsert(t *testing.T) {
	for name, revive := range map[string]func(t *testing.T, svc *Service, master Event, o1, o2 storage.Event){
		"pull upsert": func(t *testing.T, svc *Service, master Event, o1, o2 storage.Event) {
			ctx := context.Background()
			for _, r := range []storage.Event{o1, o2} {
				if _, err := svc.UpsertByUID(ctx, UpsertParams{
					UID: r.Uid, CalendarID: r.CalendarID, Title: r.Title, RecurrenceID: r.RecurrenceID,
					StartTime: parseStorageTime(r.StartTime), EndTime: parseStorageTime(r.EndTime),
				}); err != nil {
					t.Fatalf("upsert override: %v", err)
				}
			}
			if _, err := svc.UpsertByUID(ctx, UpsertParams{
				UID: master.UID, CalendarID: 1, Title: master.Title,
				StartTime: master.StartTime, EndTime: master.EndTime, RecurrenceRule: master.RecurrenceRule,
			}); err != nil {
				t.Fatalf("upsert master: %v", err)
			}
		},
		"raw revival": func(t *testing.T, svc *Service, master Event, _, _ storage.Event) {
			rows, err := svc.q.ListTrashedEventsByUID(context.Background(), master.UID)
			if err != nil {
				t.Fatalf("list trashed rows: %v", err)
			}
			ids := make([]int64, len(rows))
			for i, r := range rows {
				ids[i] = r.ID
			}
			if _, err := svc.q.RestoreEventsByIDs(context.Background(), ids); err != nil {
				t.Fatalf("revive rows: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			master, second, third := singleSeriesWithOverrides(t, svc)
			o1, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
			if err != nil {
				t.Fatalf("get o1: %v", err)
			}
			o2, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
			if err != nil {
				t.Fatalf("get o2: %v", err)
			}
			if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
				t.Fatalf("DeleteSeries: %v", err)
			}
			revive(t, svc, master, o1, o2)
			if err := svc.Delete(ctx, o1.ID); err != nil {
				t.Fatalf("Delete(o1) on its own: %v", err)
			}
			if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
				t.Fatalf("second DeleteSeries: %v", err)
			}
			log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
			if err != nil {
				t.Fatalf("series delete log: %v", err)
			}
			if log.HiddenIds != strconv.FormatInt(o2.ID, 10) {
				t.Errorf("series delete log = %q, want o2 only (%d)", log.HiddenIds, o2.ID)
			}
			if err := svc.RestoreByID(ctx, master.ID); err != nil {
				t.Fatalf("RestoreByID(master): %v", err)
			}
			if r, err := svc.GetIncludingDeleted(ctx, o1.ID); err != nil || r.DeletedAt == nil {
				t.Errorf("o1 after the restore = %+v, %v; want it still deleted", r, err)
			}
			if r, err := svc.Get(ctx, o2.ID); err != nil || r.DeletedAt != nil {
				t.Errorf("o2 after the restore = %+v, %v; want it live", r, err)
			}
		})
	}
}

// TestRestoreByUID_CandidatesAreSeriesDeletes: the restore by UID takes
// only a series delete as its target. A live series with an occurrence
// deleted on its own, or with rows that a truncation hid, is no
// candidate. With no candidate the restore reports ErrNotDeleted. With one
// it restores that series. With two it needs the ID of the master.
func TestRestoreByUID_CandidatesAreSeriesDeletes(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	third := second.Add(24 * time.Hour)
	// B: an occurrence deleted on its own. A: a truncation.
	if err := svc.DeleteInstance(ctx, masterB.CalendarID, masterB.UID, second); err != nil {
		t.Fatalf("DeleteInstance(B): %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, masterA.CalendarID, masterA.UID, third); err != nil {
		t.Fatalf("DeleteFromInstance(A): %v", err)
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreByUID with no series delete err = %v, want ErrNotDeleted", err)
	}
	mA, _ := seriesState(t, svc, masterA.CalendarID)
	if got := storage.NullableToString(mA.RecurrenceRule); !strings.Contains(got, "UNTIL=") {
		t.Errorf("rule of A after the refused restore = %q, want it still truncated", got)
	}
	// A third series with a series delete is the one candidate.
	calC := insertCalendar(t, svc, "Third")
	masterC, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: masterA.UID, CalendarID: calC, Title: "Standup C",
		StartTime: masterA.StartTime, EndTime: masterA.EndTime, RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("upsert master C: %v", err)
	}
	if err := svc.DeleteSeries(ctx, calC, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(C): %v", err)
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); err != nil {
		t.Fatalf("RestoreByUID with one series delete: %v", err)
	}
	if m, err := svc.Get(ctx, masterC.ID); err != nil || m.DeletedAt != nil {
		t.Errorf("master C after the restore = %+v, %v; want it live", m, err)
	}
	if got := mustListSeriesOverrides(t, svc, masterB.CalendarID, masterB.UID); len(got) != 0 {
		t.Errorf("live overrides of B after the restore of C = %d, want 0", len(got))
	}
	// Two series deletes: the UID names no single series.
	for _, cal := range []int64{masterB.CalendarID, calC} {
		if err := svc.DeleteSeries(ctx, cal, masterA.UID); err != nil {
			t.Fatalf("DeleteSeries(%d): %v", cal, err)
		}
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); !errors.Is(err, ErrAmbiguousUID) {
		t.Errorf("RestoreByUID with two series deletes err = %v, want ErrAmbiguousUID", err)
	}
}

// TestRestoreByID_StaleLogAfterRevival is the five-step sequence of the
// stale series log: a series delete logs its rows, a pull revives the
// master (the trigger drops the log), the user deletes an override on its
// own, a pull hides the series with no log, and the restore by ID of the
// master brings the master back alone.
func TestRestoreByID_StaleLogAfterRevival(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	o1, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get o1: %v", err)
	}
	o2, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get o2: %v", err)
	}
	// 1. The series delete logs o1 and o2.
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	// 2. A pull revives the series by upsert.
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Title: master.Title,
		StartTime: master.StartTime, EndTime: master.EndTime, RecurrenceRule: master.RecurrenceRule,
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	if _, err := svc.q.RestoreEventsByIDs(ctx, []int64{o1.ID, o2.ID}); err != nil {
		t.Fatalf("revive overrides: %v", err)
	}
	if _, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("series log after the revival err = %v, want sql.ErrNoRows (dropped by the trigger)", err)
	}
	// 3. The user deletes o1 on its own.
	if err := svc.Delete(ctx, o1.ID); err != nil {
		t.Fatalf("Delete(o1): %v", err)
	}
	// 4. A pull hides the series with no log.
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID}); err != nil {
		t.Fatalf("hide the series: %v", err)
	}
	// 5. The restore by ID of the master replays no stale log. It brings
	// back the rows that the pull hid with the master (o2). o1, which the
	// user deleted on its own, stays deleted.
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	if m, err := svc.Get(ctx, master.ID); err != nil || m.DeletedAt != nil {
		t.Errorf("master after the restore = %+v, %v; want it live", m, err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, o1.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("o1 after the restore = %+v, %v; want it still deleted", r, err)
	}
	if r, err := svc.Get(ctx, o2.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("o2 after the restore = %+v, %v; want it live", r, err)
	}
}

// TestRestoreByUID_LeavesRowsOfOldTruncationLogs: a truncation log from
// before the row IDs names its rows by recurrence ID, and one from before
// issue #287 names none. The restore by UID leaves those rows for the
// truncation restore in both cases.
func TestRestoreByUID_LeavesRowsOfOldTruncationLogs(t *testing.T) {
	for name, update := range map[string]string{
		"recurrence IDs": `UPDATE event_truncate_deletes SET hidden_ids = NULL WHERE uid = ?`,
		"no list":        `UPDATE event_truncate_deletes SET hidden_ids = NULL, hidden_overrides = NULL WHERE uid = ?`,
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			master, _, third := singleSeriesWithOverrides(t, svc)
			tail, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
			if err != nil {
				t.Fatalf("get tail: %v", err)
			}
			if err := svc.DeleteFromInstance(ctx, 1, master.UID, third); err != nil {
				t.Fatalf("DeleteFromInstance: %v", err)
			}
			if _, err := svc.db.ExecContext(ctx, update, master.UID); err != nil {
				t.Fatalf("age the truncation log: %v", err)
			}
			if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
				t.Fatalf("DeleteSeries: %v", err)
			}
			if err := svc.RestoreByUID(ctx, master.UID); err != nil {
				t.Fatalf("RestoreByUID: %v", err)
			}
			if r, err := svc.GetIncludingDeleted(ctx, tail.ID); err != nil || r.DeletedAt == nil {
				t.Errorf("tail after RestoreByUID = %+v, %v; want it still hidden", r, err)
			}
		})
	}
}

// TestDeleteSeries_OrphanRowsOfSyncedCalendarTombstone: the delete of
// orphan rows of a synced calendar queues the delete of their server
// object.
func TestDeleteSeries_OrphanRowsOfSyncedCalendarTombstone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "orphan-uid", CalendarID: 1, Title: "Orphan",
		StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert orphan: %v", err)
	}
	if err := svc.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: 1, Uid: "orphan-uid", OwnerType: "event",
		RemoteUrl: "https://example.com/cal/orphan.ics", Etag: "etag-1", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("upsert sync resource: %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, "orphan-uid")
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	tombstones, err := svc.q.ListTombstonesByCalendar(ctx, 1)
	if err != nil || len(tombstones) != 1 {
		t.Fatalf("tombstones after the orphan delete = %d, %v; want 1", len(tombstones), err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if tombstones, err := svc.q.ListTombstonesByCalendar(ctx, 1); err != nil || len(tombstones) != 0 {
		t.Errorf("tombstones after the undo = %d, %v; want 0", len(tombstones), err)
	}
}

// TestSeriesMaster_OwnTrashedMasterBesideTwoLiveMasters: a calendar with
// its own master in the trash and a live row owns that row. Two live
// masters elsewhere make no ambiguity, and a series delete there reports
// ErrSeriesInTrash.
func TestSeriesMaster_OwnTrashedMasterBesideTwoLiveMasters(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, _, second := twoCalendarSeries(t, svc)
	calC := insertCalendar(t, svc, "Third")
	masterC, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: masterA.UID, CalendarID: calC, Title: "Standup C",
		StartTime: masterA.StartTime, EndTime: masterA.EndTime, RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("upsert master C: %v", err)
	}
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: masterA.UID, CalendarID: calC, Title: "C late",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert override C: %v", err)
	}
	if err := svc.q.SoftDeleteEvent(ctx, masterC.ID); err != nil {
		t.Fatalf("trash master C: %v", err)
	}
	if _, err := svc.SeriesMaster(ctx, calC, masterA.UID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SeriesMaster(C) err = %v, want sql.ErrNoRows", err)
	}
	if err := svc.DeleteSeries(ctx, calC, masterA.UID); !errors.Is(err, ErrSeriesInTrash) {
		t.Errorf("DeleteSeries(C) err = %v, want ErrSeriesInTrash", err)
	}
}

// TestUndoKey_LegacyOverrideSameForRowAndOccurrenceDelete: the delete of a
// legacy override by its row and the delete of its occurrence record the
// same undo calendar, the calendar of the master.
func TestUndoKey_LegacyOverrideSameForRowAndOccurrenceDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB := insertCalendar(t, svc, "Master")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "key-uid", CalendarID: calB, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "key-uid", CalendarID: 1, Title: "Legacy",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	byRow, err := svc.DeleteWithUndo(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo: %v", err)
	}
	if byRow.CalendarID != calB || byRow.RowID != legacy.ID {
		t.Errorf("row delete meta = calendar %d row %d, want calendar %d row %d", byRow.CalendarID, byRow.RowID, calB, legacy.ID)
	}
	if err := svc.RestoreUndo(ctx, byRow); err != nil {
		t.Fatalf("RestoreUndo(row): %v", err)
	}
	byOccurrence, err := svc.DeleteInstanceWithUndo(ctx, calB, "key-uid", second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if byOccurrence.CalendarID != byRow.CalendarID || byOccurrence.RecurrenceID != byRow.RecurrenceID {
		t.Errorf("occurrence delete meta = calendar %d rec %q, want the row delete key %d %q", byOccurrence.CalendarID, byOccurrence.RecurrenceID, byRow.CalendarID, byRow.RecurrenceID)
	}
}

// TestRestoreMaster_NoLogBringsSameDeleteRows covers the restore of a
// master with no series-delete log. A series delete from before the log
// and the series delete of a pull hide the master and its overrides in
// one statement. The restore brings back the overrides of that statement.
// An occurrence that the user deleted on its own stays deleted.
func TestRestoreMaster_NoLogBringsSameDeleteRows(t *testing.T) {
	for name, hide := range map[string]func(t *testing.T, svc *Service, uid string){
		"series delete from before the log": func(t *testing.T, svc *Service, uid string) {
			if err := svc.DeleteSeries(context.Background(), 1, uid); err != nil {
				t.Fatalf("DeleteSeries: %v", err)
			}
			if _, err := svc.db.ExecContext(context.Background(), `DELETE FROM event_series_deletes WHERE uid = ?`, uid); err != nil {
				t.Fatalf("drop the log: %v", err)
			}
		},
		"pull": func(t *testing.T, svc *Service, uid string) {
			if err := svc.q.SoftDeleteEventsByCalendarUID(context.Background(), storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: uid}); err != nil {
				t.Fatalf("hide the series: %v", err)
			}
		},
	} {
		for restoreName, restore := range map[string]func(svc *Service, master Event) error{
			"by ID":  func(svc *Service, master Event) error { return svc.RestoreByID(context.Background(), master.ID) },
			"by UID": func(svc *Service, master Event) error { return svc.RestoreByUID(context.Background(), master.UID) },
		} {
			t.Run(name+" "+restoreName, func(t *testing.T) {
				svc := newTestService(t)
				ctx := context.Background()
				master, second, third := singleSeriesWithOverrides(t, svc)
				o1, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
				if err != nil {
					t.Fatalf("get o1: %v", err)
				}
				o2, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
				if err != nil {
					t.Fatalf("get o2: %v", err)
				}
				// o1 goes on its own, earlier.
				if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
					t.Fatalf("DeleteInstance(o1): %v", err)
				}
				if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, o1.ID); err != nil {
					t.Fatalf("date the own delete: %v", err)
				}
				hide(t, svc, master.UID)
				if err := restore(svc, master); err != nil {
					t.Fatalf("restore: %v", err)
				}
				if m, err := svc.Get(ctx, master.ID); err != nil || m.DeletedAt != nil {
					t.Errorf("master after the restore = %+v, %v; want it live", m, err)
				}
				if r, err := svc.Get(ctx, o2.ID); err != nil || r.DeletedAt != nil {
					t.Errorf("o2 after the restore = %+v, %v; want it live", r, err)
				}
				if r, err := svc.GetIncludingDeleted(ctx, o1.ID); err != nil || r.DeletedAt == nil {
					t.Errorf("o1 after the restore = %+v, %v; want it still deleted", r, err)
				}
			})
		}
	}
}

// TestListSeriesOverrides_OneRowPerOccurrence: the series list holds one
// row per occurrence. The row of the calendar of the master wins over a
// legacy row of the same recurrence ID.
func TestListSeriesOverrides_OneRowPerOccurrence(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	own, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get own override: %v", err)
	}
	calL := insertCalendar(t, svc, "Legacy")
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy twin",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy twin: %v", err)
	}
	overrides := mustListSeriesOverrides(t, svc, 1, master.UID)
	var at []Event
	for _, o := range overrides {
		if o.RecurrenceID == second.Format(time.RFC3339) {
			at = append(at, o)
		}
	}
	if len(at) != 1 || at[0].ID != own.ID {
		t.Errorf("rows at the occurrence = %+v, want the own row %d only", at, own.ID)
	}
}

// TestUndoOverrideOfTrashedMaster_KeepsTombstone: the undo of an override
// delete while the master is in the trash keeps the series tombstone, as
// the restore by ID does. The row stays local until the master comes back.
func TestUndoOverrideOfTrashedMaster_KeepsTombstone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: 1, Uid: master.UID, OwnerType: "event",
		RemoteUrl: "https://example.com/cal/series.ics", Etag: "etag-1", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("upsert sync resource: %v", err)
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
	// A second series state: the tombstone of the series delete is back.
	if _, err := storage.CreateTombstoneIfSynced(ctx, svc.db, 1, master.UID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	// The master stays in the trash, so the tombstone of the series stays.
	tombstones, err := svc.q.ListTombstonesByCalendar(ctx, 1)
	if err != nil || len(tombstones) != 1 {
		t.Errorf("tombstones after the undo = %d, %v; want 1 (the series is in the trash)", len(tombstones), err)
	}
}

// TestMoveLegacyBack_DropsSourceSeriesLog: a legacy row that goes home
// carries no series delete, so the series-delete log of its calendar does
// not move into the calendar of the master.
func TestMoveLegacyBack_DropsSourceSeriesLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	first := master.StartTime
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: first, EndTime: first.Add(time.Hour), RecurrenceID: first.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids) VALUES (?, ?, '999')`, calL, master.UID); err != nil {
		t.Fatalf("seed the source log: %v", err)
	}
	if _, err := svc.Update(ctx, legacy.ID, UpdateParams{
		CalendarID: 1, Title: legacy.Title, StartTime: legacy.StartTime, EndTime: legacy.EndTime,
	}); err != nil {
		t.Fatalf("move the legacy row home: %v", err)
	}
	var n int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_series_deletes WHERE uid = ?`, master.UID).Scan(&n); err != nil {
		t.Fatalf("count series logs: %v", err)
	}
	if n != 0 {
		t.Errorf("series logs after the move = %d, want 0", n)
	}
}

// TestRestoreUndo_SingleWithoutRecord: undo data of a single-row delete
// with no master and no row names no delete.
func TestRestoreUndo_SingleWithoutRecord(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	meta := UndoMeta{Kind: UndoKindSingle, UID: master.UID, CalendarID: 1, RecurrenceID: second.Format(time.RFC3339)}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo without a record err = %v, want ErrNotDeleted", err)
	}
}

// TestRestoreOverrideByID_TrashedOwnMasterKeepsTombstone: a restore by ID
// of an override beside its own master in the trash keeps the series
// tombstone and gives the UID no dirty mark.
func TestRestoreOverrideByID_TrashedOwnMasterKeepsTombstone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: 1, Uid: master.UID, OwnerType: "event",
		RemoteUrl: "https://example.com/cal/series.ics", Etag: "etag-1", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("upsert sync resource: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if tombstones, err := svc.q.ListTombstonesByCalendar(ctx, 1); err != nil || len(tombstones) != 1 {
		t.Fatalf("tombstones after the series delete = %d, %v; want 1", len(tombstones), err)
	}
	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID(override): %v", err)
	}
	if tombstones, err := svc.q.ListTombstonesByCalendar(ctx, 1); err != nil || len(tombstones) != 1 {
		t.Errorf("tombstones after the restore = %d, %v; want 1 (the master is in the trash)", len(tombstones), err)
	}
	var dirty int64
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_resources WHERE calendar_id = 1 AND uid = ? AND dirty = 1`, master.UID).Scan(&dirty); err != nil || dirty != 0 {
		t.Errorf("dirty sync rows after the restore = %d, %v; want 0", dirty, err)
	}
}

// liveRowBesideTrashedMaster adds a series of uid to a new calendar,
// moves the series to the trash, and restores its override at at by ID.
// The calendar then holds one live row outside the live series.
func liveRowBesideTrashedMaster(t *testing.T, svc *Service, name, uid string, start, at time.Time) Event {
	t.Helper()
	ctx := context.Background()
	cal := insertCalendar(t, svc, name)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: cal, Title: name,
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	}); err != nil {
		t.Fatalf("upsert master %s: %v", name, err)
	}
	row, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: cal, Title: name + " late",
		StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert override %s: %v", name, err)
	}
	if err := svc.DeleteSeries(ctx, cal, uid); err != nil {
		t.Fatalf("DeleteSeries(%s): %v", name, err)
	}
	if err := svc.RestoreByID(ctx, row.ID); err != nil {
		t.Fatalf("RestoreByID(%s override): %v", name, err)
	}
	return row
}

// TestGetByUIDAndRecurrenceID_RowOutsideLiveSeries: with a live master and
// no override at the occurrence, the one live row of another calendar at
// the occurrence is the result. Two such rows give an
// *AmbiguousOverrideError.
func TestGetByUIDAndRecurrenceID_RowOutsideLiveSeries(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "outside-uid"
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: 1, Title: "Live",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	}); err != nil {
		t.Fatalf("upsert live master: %v", err)
	}
	rowB := liveRowBesideTrashedMaster(t, svc, "B", uid, start, second)
	got, err := svc.GetByUIDAndRecurrenceID(ctx, uid, second.Format(time.RFC3339))
	if err != nil || got.ID != rowB.ID {
		t.Fatalf("GetByUIDAndRecurrenceID = %d, %v; want the row %d of calendar B", got.ID, err, rowB.ID)
	}
	rowC := liveRowBesideTrashedMaster(t, svc, "C", uid, start, second)
	_, err = svc.GetByUIDAndRecurrenceID(ctx, uid, second.Format(time.RFC3339))
	var amb *AmbiguousOverrideError
	if !errors.As(err, &amb) || len(amb.IDs) != 2 || amb.IDs[0] != rowB.ID || amb.IDs[1] != rowC.ID {
		t.Errorf("GetByUIDAndRecurrenceID err = %v; want an AmbiguousOverrideError with IDs %d and %d", err, rowB.ID, rowC.ID)
	}
}

// TestTwoLegacyRowsAtOneOccurrence_OneWinner: with two legacy rows of one
// occurrence, the lookup, the edit, and the export list take the same row:
// the lowest ID.
func TestTwoLegacyRowsAtOneOccurrence_OneWinner(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "winner-uid", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	ids := make([]int64, 0, 2)
	for _, name := range []string{"Legacy C", "Legacy D"} {
		cal := insertCalendar(t, svc, name)
		row, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: master.UID, CalendarID: cal, Title: name,
			StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		ids = append(ids, row.ID)
	}
	listed := mustListSeriesOverrides(t, svc, 1, master.UID)
	if len(listed) != 1 || listed[0].ID != ids[0] {
		t.Fatalf("ListSeriesOverrides = %+v, want the row %d once", listed, ids[0])
	}
	edited, err := svc.UpdateInstance(ctx, 1, master.UID, second, UpdateParams{
		Title: "Edited", StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
	})
	if err != nil || edited.ID != ids[0] {
		t.Fatalf("UpdateInstance = %d, %v; want the row %d", edited.ID, err, ids[0])
	}
	other, err := svc.Get(ctx, ids[1])
	if err != nil || other.Title != "Legacy D" {
		t.Errorf("the other row = %q, %v; want it unchanged", other.Title, err)
	}
}

// TestInsertMaster_DropsOrphanSeriesLog: a new live master in a calendar
// consumes the series-delete log of its orphan rows. A later series delete
// then writes a fresh log, and its restore leaves the old orphan rows in
// the trash.
func TestInsertMaster_DropsOrphanSeriesLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-then-master"
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	orphan, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: 1, Title: "Orphan",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert orphan: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, uid); err != nil {
		t.Fatalf("DeleteSeries(orphan rows): %v", err)
	}
	logs := func() int64 {
		t.Helper()
		var n int64
		if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_series_deletes WHERE calendar_id = 1 AND uid = ?`, uid).Scan(&n); err != nil {
			t.Fatalf("count logs: %v", err)
		}
		return n
	}
	if n := logs(); n != 1 {
		t.Fatalf("series logs after the orphan delete = %d, want 1", n)
	}
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: 1, Title: "Master",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	if n := logs(); n != 0 {
		t.Fatalf("series logs after the master insert = %d, want 0", n)
	}
	if err := svc.DeleteSeries(ctx, 1, uid); err != nil {
		t.Fatalf("DeleteSeries(master): %v", err)
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, orphan.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("old orphan row after the restore = trashed %v, %v; want it still in the trash", r.DeletedAt != nil, err)
	}
}

// TestRestoreByUID_LogBeatsTrashedCopy: a calendar with a series-delete log
// wins over a calendar with a master in the trash and no log, for example
// an old copy whose log the purge dropped.
func TestRestoreByUID_LogBeatsTrashedCopy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, _ := twoCalendarSeries(t, svc)
	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `DELETE FROM event_series_deletes WHERE calendar_id = ?`, masterB.CalendarID); err != nil {
		t.Fatalf("purge the log of B: %v", err)
	}
	if err := svc.DeleteSeries(ctx, masterA.CalendarID, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if a, err := svc.GetIncludingDeleted(ctx, masterA.ID); err != nil || a.DeletedAt != nil {
		t.Errorf("master A after the restore = trashed %v, %v; want it live", a.DeletedAt != nil, err)
	}
	if b, err := svc.GetIncludingDeleted(ctx, masterB.ID); err != nil || b.DeletedAt == nil {
		t.Errorf("master B after the restore = trashed %v, %v; want it still in the trash", b.DeletedAt != nil, err)
	}
	// With no log anywhere, two trashed masters stay ambiguous.
	if err := svc.DeleteSeries(ctx, masterA.CalendarID, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(A) again: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `DELETE FROM event_series_deletes`); err != nil {
		t.Fatalf("purge the logs: %v", err)
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); !errors.Is(err, ErrAmbiguousUID) {
		t.Errorf("RestoreByUID with two trashed masters and no log = %v, want ErrAmbiguousUID", err)
	}
}

// TestRestoreByUID_NoMasterReconcilesRestoredCalendarsOnly: the restore of
// a UID with no master row reconciles the calendars where rows came back.
// The tombstone of a calendar where no row came back stays.
func TestRestoreByUID_NoMasterReconcilesRestoredCalendarsOnly(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-reconcile"
	calY := insertCalendar(t, svc, "Y")
	at := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	rows := make(map[int64]int64, 2)
	for _, cal := range []int64{1, calY} {
		r, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: uid, CalendarID: cal, Title: "Orphan",
			StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert orphan in %d: %v", cal, err)
		}
		rows[cal] = r.ID
		if err := svc.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID: cal, Uid: uid, OwnerType: "event",
			RemoteUrl: "https://example.com/" + strconv.FormatInt(cal, 10) + ".ics", Etag: "e", SyncStrategy: "sync-token",
		}); err != nil {
			t.Fatalf("upsert sync resource: %v", err)
		}
		if _, err := storage.CreateTombstoneIfSynced(ctx, svc.db, cal, uid); err != nil {
			t.Fatalf("tombstone: %v", err)
		}
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE id = ?`, rows[1]); err != nil {
		t.Fatalf("trash the row of calendar 1: %v", err)
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	for cal, want := range map[int64]int{1: 0, calY: 1} {
		if got, err := svc.q.ListTombstonesByCalendar(ctx, cal); err != nil || len(got) != want {
			t.Errorf("tombstones of calendar %d = %d, %v; want %d", cal, len(got), err, want)
		}
	}
}

// TestDeleteSeries_OrphanRowsBesideReadOnlyTrashedCopy: a series delete
// with no calendar resolves the calendar of the orphan rows first. The
// access guard then reads that calendar, so a read-only copy in the trash
// does not block the delete.
func TestDeleteSeries_OrphanRowsBesideReadOnlyTrashedCopy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	readOnly := insertGuardedCalendar(t, svc.db, "Read-Only", "read", "VEVENT")
	const uid = "read-only-trashed"
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: readOnly, Title: "Copy",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert read-only master: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE id = ?`, master.ID); err != nil {
		t.Fatalf("trash the read-only master: %v", err)
	}
	row, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: 1, Title: "Orphan",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert orphan row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 0, uid); err != nil {
		t.Fatalf("DeleteSeries(0) beside a read-only trashed copy: %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, row.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("orphan row after the delete = trashed %v, %v; want it in the trash", r.DeletedAt != nil, err)
	}
}

// TestTruncation_SecondAtSameCutoffMergesLog: a second truncation at the
// same cutoff extends the log of the first. The log names the rows of both
// truncations, so a restore brings back all of them.
func TestTruncation_SecondAtSameCutoffMergesLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	truncate := func() {
		t.Helper()
		tx, err := svc.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		qtx := svc.q.WithTx(tx)
		m, err := qtx.GetEventIncludingDeleted(ctx, master.ID)
		if err != nil {
			t.Fatalf("get master: %v", err)
		}
		if err := softDeleteOverridesAndRecordTruncation(ctx, qtx, m, second, master.RecurrenceRule); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	truncate()
	fourth := third.Add(24 * time.Hour)
	late, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Late import",
		StartTime: fourth, EndTime: fourth.Add(time.Hour), RecurrenceID: fourth.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert late override: %v", err)
	}
	truncate()
	log, err := svc.q.GetEventTruncateDeleteByCalendarUIDAndCutoff(ctx, storage.GetEventTruncateDeleteByCalendarUIDAndCutoffParams{
		CalendarID: 1, Uid: master.UID, CutoffTime: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get truncation log: %v", err)
	}
	ids, err := parseRowIDs(storage.NullableToString(log.HiddenIds))
	if err != nil {
		t.Fatalf("parse hidden ids: %v", err)
	}
	if len(ids) != 3 || !strings.Contains(storage.NullableToString(log.HiddenIds), strconv.FormatInt(late.ID, 10)) {
		t.Errorf("hidden_ids = %q, want the two first rows and the late row %d", storage.NullableToString(log.HiddenIds), late.ID)
	}
	recIDs := strings.Split(storage.NullableToString(log.HiddenOverrides), ",")
	if len(recIDs) != 3 || !strings.Contains(storage.NullableToString(log.HiddenOverrides), fourth.Format(time.RFC3339)) {
		t.Errorf("hidden_overrides = %q, want three recurrence IDs with %s", storage.NullableToString(log.HiddenOverrides), fourth.Format(time.RFC3339))
	}
}

// countTombstones counts the tombstones of uid in calendarID.
func countTombstones(t *testing.T, svc *Service, calendarID int64, uid string) int64 {
	t.Helper()
	n, err := svc.q.CountTombstonesByCalendarAndUID(context.Background(), storage.CountTombstonesByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		t.Fatalf("count tombstones: %v", err)
	}
	return n
}

// seedTombstone gives calendarID a synced sync row and a tombstone of uid.
func seedTombstone(t *testing.T, svc *Service, calendarID int64, uid string) {
	t.Helper()
	ctx := context.Background()
	if err := svc.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: uid, OwnerType: "event",
		RemoteUrl: "https://example.com/" + strconv.FormatInt(calendarID, 10) + "/" + uid + ".ics", Etag: "e", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("upsert sync resource: %v", err)
	}
	if _, err := storage.CreateTombstoneIfSynced(ctx, svc.db, calendarID, uid); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
}

// TestRestoreLegacyOverrideByID_ReconcilesMasterCalendar: the restore by ID
// of a legacy override reconciles the calendar of its master. The tombstone
// of the calendar of the row stays, because the old server object there
// still needs its delete.
func TestRestoreLegacyOverrideByID_ReconcilesMasterCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	fourth := second.Add(48 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: fourth, EndTime: fourth.Add(time.Hour), RecurrenceID: fourth.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	if err := svc.Delete(ctx, legacy.ID); err != nil {
		t.Fatalf("Delete(legacy): %v", err)
	}
	seedTombstone(t, svc, 1, master.UID)
	seedTombstone(t, svc, calL, master.UID)
	if err := svc.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	if n := countTombstones(t, svc, 1, master.UID); n != 0 {
		t.Errorf("tombstones of the master calendar = %d, want 0 (reconciled)", n)
	}
	if n := countTombstones(t, svc, calL, master.UID); n != 1 {
		t.Errorf("tombstones of the legacy calendar = %d, want 1 (kept)", n)
	}
}

// TestReviveOverride_LeavesTheTruncationLog: an override that a truncation
// hid, and that an edit then revived, leaves the truncation log. A delete
// of the row on its own then survives the restore of the tail.
func TestReviveOverride_LeavesTheTruncationLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	// The user widens the rule again, so the occurrence of the hidden row
	// is part of the series.
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET recurrence_rule = 'FREQ=DAILY;COUNT=4' WHERE id = ?`, master.ID); err != nil {
		t.Fatalf("widen the rule: %v", err)
	}
	revived, err := svc.UpdateInstance(ctx, 1, master.UID, third, UpdateParams{
		Title: "Revived", StartTime: third.Add(time.Hour), EndTime: third.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("UpdateInstance (revive): %v", err)
	}
	if err := svc.Delete(ctx, revived.ID); err != nil {
		t.Fatalf("Delete(revived): %v", err)
	}
	logs, err := svc.q.ListEventTruncateDeletesByCalendarUID(ctx, storage.ListEventTruncateDeletesByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
	if err != nil || len(logs) != 1 {
		t.Fatalf("truncation logs = %d, %v; want 1", len(logs), err)
	}
	if err := svc.restoreTruncationByLogID(ctx, logs[0].ID); err != nil {
		t.Fatalf("restore the tail: %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, revived.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("revived row after the tail restore = trashed %v, %v; want it still in the trash", r.DeletedAt != nil, err)
	}
}

// TestDeleteOverrideBesideTrashedMaster_NoDirtyMark: a delete of an
// override beside its own master in the trash marks no calendar dirty, as
// the restore by ID of that row does.
func TestDeleteOverrideBesideTrashedMaster_NoDirtyMark(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	account, err := svc.q.CreateAccount(ctx, storage.CreateAccountParams{Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET account_id = ? WHERE id = 1`, account.ID); err != nil {
		t.Fatalf("link calendar: %v", err)
	}
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID(override): %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 0`); err != nil {
		t.Fatalf("clear dirty: %v", err)
	}
	if err := svc.Delete(ctx, override.ID); err != nil {
		t.Fatalf("Delete(override): %v", err)
	}
	var dirty int64
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_resources WHERE uid = ? AND dirty = 1`, master.UID).Scan(&dirty); err != nil || dirty != 0 {
		t.Errorf("dirty sync rows after the delete = %d, %v; want 0", dirty, err)
	}
}

// TestRestoreByUID_LogOfPurgedRowsIsNoCandidate: a series-delete log that
// names only purged rows, in a calendar with no master in the trash, is no
// candidate. The restore by UID reverses the trashed master of the other
// calendar.
func TestRestoreByUID_LogOfPurgedRowsIsNoCandidate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "stale-log"
	calB := insertCalendar(t, svc, "B")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	masterB, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: calB, Title: "B",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master B: %v", err)
	}
	if err := svc.DeleteSeries(ctx, calB, uid); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `DELETE FROM event_series_deletes WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("purge the log of B: %v", err)
	}
	orphan, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: 1, Title: "Orphan",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert orphan row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, uid); err != nil {
		t.Fatalf("DeleteSeries(orphan rows): %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, orphan.ID); err != nil {
		t.Fatalf("purge the orphan row: %v", err)
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if b, err := svc.GetIncludingDeleted(ctx, masterB.ID); err != nil || b.DeletedAt != nil {
		t.Errorf("master B after the restore = trashed %v, %v; want it live", b.DeletedAt != nil, err)
	}
}

// TestRestoreTruncationByLogID_GuardsReadOnlyCalendar: the restore of a
// truncation writes the calendar of its log, so a read-only calendar
// refuses it.
func TestRestoreTruncationByLogID_GuardsReadOnlyCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	readOnly := insertGuardedCalendar(t, svc.db, "Read-Only", "read", "VEVENT")
	const uid = "read-only-tail"
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: readOnly, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;UNTIL=20260505T000000Z",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	res, err := svc.db.ExecContext(ctx, `INSERT INTO event_truncate_deletes (calendar_id, uid, cutoff_time, previous_rrule, hidden_overrides, removed_rdates, hidden_ids)
		VALUES (?, ?, '2026-05-05T09:00:00Z', 'FREQ=DAILY;COUNT=3', '', '', '')`, readOnly, uid)
	if err != nil {
		t.Fatalf("seed the log: %v", err)
	}
	logID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("log id: %v", err)
	}
	if err := svc.restoreTruncationByLogID(ctx, logID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Errorf("restoreTruncationByLogID err = %v, want ErrReadOnly", err)
	}
}

// TestSeriesView_BesideMaster: the view column beside_master is 1 for an
// override beside its master and 0 for a legacy override.
func TestSeriesView_BesideMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: second, EndTime: second.Add(time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	own, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get own override: %v", err)
	}
	for id, want := range map[int64]int64{own.ID: 1, legacy.ID: 0} {
		var got int64
		if err := svc.db.QueryRowContext(ctx, `SELECT beside_master FROM event_series_rows WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("beside_master of row %d = %d, %v; want %d", id, got, err, want)
		}
	}
}

// TestTruncation_SecondOverLogWithoutRowIDs: a second truncation at the
// cutoff of a log from before migration 057, with no hidden_ids, resolves
// the recurrence IDs of that log to its trashed rows. The merged log then
// names the rows of both truncations.
func TestTruncation_SecondOverLogWithoutRowIDs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	truncate := func() {
		t.Helper()
		tx, err := svc.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		qtx := svc.q.WithTx(tx)
		m, err := qtx.GetEventIncludingDeleted(ctx, master.ID)
		if err != nil {
			t.Fatalf("get master: %v", err)
		}
		if err := softDeleteOverridesAndRecordTruncation(ctx, qtx, m, second, master.RecurrenceRule); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	truncate()
	if _, err := svc.db.ExecContext(ctx, `UPDATE event_truncate_deletes SET hidden_ids = NULL WHERE uid = ?`, master.UID); err != nil {
		t.Fatalf("make the log pre-057: %v", err)
	}
	fourth := third.Add(24 * time.Hour)
	late, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Late import",
		StartTime: fourth, EndTime: fourth.Add(time.Hour), RecurrenceID: fourth.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert late override: %v", err)
	}
	truncate()
	log, err := svc.q.GetEventTruncateDeleteByCalendarUIDAndCutoff(ctx, storage.GetEventTruncateDeleteByCalendarUIDAndCutoffParams{
		CalendarID: 1, Uid: master.UID, CutoffTime: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get truncation log: %v", err)
	}
	ids, err := parseRowIDs(storage.NullableToString(log.HiddenIds))
	if err != nil {
		t.Fatalf("parse hidden ids: %v", err)
	}
	if len(ids) != 3 || !strings.Contains(storage.NullableToString(log.HiddenIds), strconv.FormatInt(late.ID, 10)) {
		t.Errorf("hidden_ids = %q, want the two first rows and the late row %d", storage.NullableToString(log.HiddenIds), late.ID)
	}
}

// TestUndoLegacyOverrideDelete_ClearsMasterEXDATE: the undo of a delete of
// a legacy override names the master that the delete wrote, so it clears
// the EXDATE on that master.
func TestUndoLegacyOverrideDelete_ClearsMasterEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	fourth := second.Add(48 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: fourth, EndTime: fourth.Add(time.Hour), RecurrenceID: fourth.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(legacy): %v", err)
	}
	if meta.MasterID != master.ID || meta.CalendarID != 1 {
		t.Fatalf("undo meta = master %d, calendar %d; want master %d, calendar 1", meta.MasterID, meta.CalendarID, master.ID)
	}
	exdates := func() string {
		t.Helper()
		m, err := svc.GetIncludingDeleted(ctx, master.ID)
		if err != nil {
			t.Fatalf("get master: %v", err)
		}
		return m.ExDates
	}
	if !strings.Contains(exdates(), fourth.Format("20060102")) && !strings.Contains(exdates(), fourth.Format(time.RFC3339)) {
		t.Fatalf("master EXDATE after the delete = %q, want the occurrence", exdates())
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if got := exdates(); got != "" {
		t.Errorf("master EXDATE after the undo = %q, want none", got)
	}
	if r, err := svc.GetIncludingDeleted(ctx, legacy.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("legacy row after the undo = trashed %v, %v; want it live", r.DeletedAt != nil, err)
	}
}

// TestRestoreByUID_OrphanFallbackTakesNewestDelete: with no master and no
// series-delete log, the restore by UID takes the one calendar with rows in
// the trash, and brings back its rows of the newest delete. An older delete
// and a row that an occurrence log names stay in the trash. Two calendars
// give ErrAmbiguousUID.
func TestRestoreByUID_OrphanFallbackTakesNewestDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-newest"
	base := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	ids := make([]int64, 0, 4)
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * 24 * time.Hour)
		r, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: uid, CalendarID: 1, Title: "Orphan",
			StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert orphan %d: %v", i, err)
		}
		ids = append(ids, r.ID)
	}
	trash := func(id int64, at string) {
		t.Helper()
		if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = ? WHERE id = ?`, at, id); err != nil {
			t.Fatalf("trash row %d: %v", id, err)
		}
	}
	trash(ids[0], "2026-05-01T10:00:00Z")
	for _, id := range ids[1:] {
		trash(id, "2026-05-02T10:00:00Z")
	}
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (1, ?, ?)`,
		uid, base.Add(72*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("seed the occurrence log: %v", err)
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	for i, wantLive := range []bool{false, true, true, false} {
		r, err := svc.GetIncludingDeleted(ctx, ids[i])
		if err != nil {
			t.Fatalf("get row %d: %v", i, err)
		}
		if live := r.DeletedAt == nil; live != wantLive {
			t.Errorf("row %d live = %v, want %v", i, live, wantLive)
		}
	}
	calY := insertCalendar(t, svc, "Y")
	other, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: calY, Title: "Orphan Y",
		StartTime: base, EndTime: base.Add(time.Hour), RecurrenceID: base.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert orphan in Y: %v", err)
	}
	trash(other.ID, "2026-05-03T10:00:00Z")
	if err := svc.RestoreByUID(ctx, uid); !errors.Is(err, ErrAmbiguousUID) {
		t.Errorf("RestoreByUID with trashed rows in two calendars = %v, want ErrAmbiguousUID", err)
	}
}

// TestCommaList_EmptyFields: the comma-list helpers skip an empty field and
// keep the order of the union.
func TestCommaList_EmptyFields(t *testing.T) {
	ids, err := parseRowIDs("1,,2,")
	if err != nil || len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("parseRowIDs = %v, %v; want [1 2]", ids, err)
	}
	merged, err := unionCommaList([]int64{2, 3}, "1,,2", parseRowID)
	if err != nil || formatRowIDs(merged) != "2,3,1" {
		t.Errorf("unionCommaList = %v, %v; want 2,3,1", merged, err)
	}
	recIDs, err := unionCommaList([]string{"b"}, ",a,b", parseRecurrenceID)
	if err != nil || strings.Join(recIDs, ",") != "b,a" {
		t.Errorf("unionCommaList(recurrence IDs) = %v, %v; want b,a", recIDs, err)
	}
}

// TestUndoOrphanDeleteBesideTrashedMaster_KeepsTombstone: the undo of a
// series delete of the live rows beside a master in the trash finds no
// calendar for a dirty mark. The tombstone of the series then stays, so the
// sync state of the series is not lost.
func TestUndoOrphanDeleteBesideTrashedMaster_KeepsTombstone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	account, err := svc.q.CreateAccount(ctx, storage.CreateAccountParams{Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET account_id = ? WHERE id = 1`, account.ID); err != nil {
		t.Fatalf("link calendar: %v", err)
	}
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	seedTombstone(t, svc, 1, master.UID)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID(override): %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo(orphan rows): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if n := countTombstones(t, svc, 1, master.UID); n != 1 {
		t.Errorf("tombstones after the undo = %d, want 1 (no dirty mark, so the tombstone stays)", n)
	}
}

// orphanRows adds n live orphan rows of uid to calendar 1, one per day.
func orphanRows(t *testing.T, svc *Service, uid string, n int) []Event {
	t.Helper()
	base := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	rows := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * 24 * time.Hour)
		r, err := svc.UpsertByUID(context.Background(), UpsertParams{
			UID: uid, CalendarID: 1, Title: "Orphan",
			StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert orphan %d: %v", i, err)
		}
		rows = append(rows, r)
	}
	return rows
}

// TestRestoreByUID_SkipsOrphanRowDeletedByID: a delete by ID of an orphan
// row writes its own record. A restore by UID then brings back the rows of
// the other delete only, and the trash lists the row once.
func TestRestoreByUID_SkipsOrphanRowDeletedByID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-single"
	rows := orphanRows(t, svc, uid, 2)
	// A pull hid the second row earlier.
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T10:00:00Z' WHERE id = ?`, rows[1].ID); err != nil {
		t.Fatalf("trash row 1: %v", err)
	}
	if err := svc.Delete(ctx, rows[0].ID); err != nil {
		t.Fatalf("Delete(row 0): %v", err)
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("trash entries = %d, want 2 (one per row, no log entry)", len(entries))
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	for i, wantLive := range []bool{false, true} {
		r, err := svc.GetIncludingDeleted(ctx, rows[i].ID)
		if err != nil {
			t.Fatalf("get row %d: %v", i, err)
		}
		if live := r.DeletedAt == nil; live != wantLive {
			t.Errorf("row %d live = %v, want %v", i, live, wantLive)
		}
	}
}

// TestRestoreOrphanRowByID_DropsItsRecord: a restore by ID of an orphan row
// that a delete by ID hid drops the record of that delete. A later delete of
// the row by a pull then comes back with a restore by UID.
func TestRestoreOrphanRowByID_DropsItsRecord(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-record"
	rows := orphanRows(t, svc, uid, 1)
	if err := svc.Delete(ctx, rows[0].ID); err != nil {
		t.Fatalf("Delete(row): %v", err)
	}
	if err := svc.RestoreByID(ctx, rows[0].ID); err != nil {
		t.Fatalf("RestoreByID(row): %v", err)
	}
	logs, err := svc.q.ListEventExdateDeletesByCalendarUID(ctx, storage.ListEventExdateDeletesByCalendarUIDParams{CalendarID: 1, Uid: uid})
	if err != nil || len(logs) != 0 {
		t.Fatalf("occurrence logs after the restore = %d, %v; want 0", len(logs), err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T10:00:00Z' WHERE id = ?`, rows[0].ID); err != nil {
		t.Fatalf("trash the row: %v", err)
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, rows[0].ID); err != nil || r.DeletedAt != nil {
		t.Errorf("row after the restore by UID = trashed %v, %v; want it live", r.DeletedAt != nil, err)
	}
}

// TestOrphanDeleteLog_NewMasterKeepsImportedEXDATE: the occurrence log of a
// delete by ID of an orphan row goes when a master of the UID arrives in
// the calendar. A restore of the row then keeps the EXDATE that the import
// of the master brought (issue #86).
func TestOrphanDeleteLog_NewMasterKeepsImportedEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-then-import"
	rows := orphanRows(t, svc, uid, 1)
	at := rows[0].StartTime
	if err := svc.Delete(ctx, rows[0].ID); err != nil {
		t.Fatalf("Delete(orphan row): %v", err)
	}
	start := at.Add(-24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: 1, Title: "Imported",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
		ExDates: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	logs, err := svc.q.ListEventExdateDeletesByCalendarUID(ctx, storage.ListEventExdateDeletesByCalendarUIDParams{CalendarID: 1, Uid: uid})
	if err != nil || len(logs) != 0 {
		t.Fatalf("occurrence logs after the master insert = %d, %v; want 0", len(logs), err)
	}
	if err := svc.RestoreByID(ctx, rows[0].ID); err != nil {
		t.Fatalf("RestoreByID(row): %v", err)
	}
	m, err := svc.GetIncludingDeleted(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if m.ExDates == "" {
		t.Errorf("master EXDATE after the restore = %q, want the imported EXDATE kept", m.ExDates)
	}
}

// TestRestoreTruncation_MasterInTrash: the restore of a truncation whose
// master is in the trash returns ErrMasterInTrash, not ErrNotDeleted.
func TestRestoreTruncation_MasterInTrash(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	logs, err := svc.q.ListEventTruncateDeletesByCalendarUID(ctx, storage.ListEventTruncateDeletesByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
	if err != nil || len(logs) != 1 {
		t.Fatalf("truncation logs = %d, %v; want 1", len(logs), err)
	}
	if err := svc.restoreTruncationByLogID(ctx, logs[0].ID); !errors.Is(err, ErrMasterInTrash) || errors.Is(err, ErrNotDeleted) {
		t.Errorf("restoreTruncationByLogID err = %v, want ErrMasterInTrash", err)
	}
}

// TestOrphanLog_RestoreWithMasterElsewhereDropsIt: the orphan log of a row
// lives in the calendar of the row. When a master of the UID arrives in
// another calendar, the restore of the row still drops that log, and the
// trash of the row's calendar lists nothing.
func TestOrphanLog_RestoreWithMasterElsewhereDropsIt(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-master-elsewhere"
	calL := insertCalendar(t, svc, "Legacy")
	at := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	row, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: calL, Title: "Orphan",
		StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert orphan: %v", err)
	}
	if err := svc.Delete(ctx, row.ID); err != nil {
		t.Fatalf("Delete(orphan): %v", err)
	}
	start := at.Add(-24 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: uid, CalendarID: 1, Title: "Master",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	}); err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	if err := svc.RestoreByID(ctx, row.ID); err != nil {
		t.Fatalf("RestoreByID(row): %v", err)
	}
	logs, err := svc.q.ListEventExdateDeletesByCalendarUID(ctx, storage.ListEventExdateDeletesByCalendarUIDParams{CalendarID: calL, Uid: uid})
	if err != nil || len(logs) != 0 {
		t.Errorf("occurrence logs of the row calendar = %d, %v; want 0", len(logs), err)
	}
	entries, err := svc.ListTrash(ctx, calL)
	if err != nil || len(entries) != 0 {
		t.Errorf("trash entries of the row calendar = %d, %v; want 0", len(entries), err)
	}
}

// TestOrphanDelete_NoMasterWritesFreshLog: a series delete of orphan rows
// in a calendar with no master writes a fresh log. A restore by UID then
// brings back the rows of that delete only.
func TestOrphanDelete_NoMasterWritesFreshLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-fresh-log"
	rows := orphanRows(t, svc, uid, 2)
	if err := svc.DeleteSeries(ctx, 1, uid); err != nil {
		t.Fatalf("first DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, rows[0].ID); err != nil {
		t.Fatalf("RestoreByID(row 0): %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, uid); err != nil {
		t.Fatalf("second DeleteSeries: %v", err)
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	for i, wantLive := range []bool{true, false} {
		r, err := svc.GetIncludingDeleted(ctx, rows[i].ID)
		if err != nil {
			t.Fatalf("get row %d: %v", i, err)
		}
		if live := r.DeletedAt == nil; live != wantLive {
			t.Errorf("row %d live = %v, want %v (only the second delete comes back)", i, live, wantLive)
		}
	}
}

// TestEmptiedSeriesLog_StaysBesideTrashedMaster: a restore by ID of the
// last row of a series-delete log keeps the log, with an empty list, while
// the master of its calendar is in the trash. RestoreByUID then still
// ranks that calendar above a trashed copy with no log.
func TestEmptiedSeriesLog_StaysBesideTrashedMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	masterA, masterB, second := twoCalendarSeries(t, svc)
	if err := svc.DeleteSeries(ctx, masterB.CalendarID, masterB.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `DELETE FROM event_series_deletes WHERE calendar_id = ?`, masterB.CalendarID); err != nil {
		t.Fatalf("purge the log of B: %v", err)
	}
	overrideA, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: masterA.CalendarID, Uid: masterA.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override A: %v", err)
	}
	if err := svc.DeleteSeries(ctx, masterA.CalendarID, masterA.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if err := svc.RestoreByID(ctx, overrideA.ID); err != nil {
		t.Fatalf("RestoreByID(override A): %v", err)
	}
	log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: masterA.CalendarID, Uid: masterA.UID})
	if err != nil || log.HiddenIds != "" {
		t.Fatalf("series log of A = %q, %v; want an empty list that stays", log.HiddenIds, err)
	}
	if err := svc.RestoreByUID(ctx, masterA.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if a, err := svc.GetIncludingDeleted(ctx, masterA.ID); err != nil || a.DeletedAt != nil {
		t.Errorf("master A after the restore = trashed %v, %v; want it live", a.DeletedAt != nil, err)
	}
}

// TestMoveLegacyHome_DropsSourceOccurrenceLogs: a legacy row that goes home
// to its master leaves the occurrence logs of its calendar. They record no
// delete of the master, and in the master calendar they would strip an
// imported EXDATE (issue #86).
func TestMoveLegacyHome_DropsSourceOccurrenceLogs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	fourth := third.Add(24 * time.Hour)
	legacy, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: fourth, EndTime: fourth.Add(time.Hour), RecurrenceID: fourth.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	stale := second.Add(-24 * time.Hour).Format(time.RFC3339)
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (?, ?, ?)`, calL, master.UID, stale); err != nil {
		t.Fatalf("seed the source log: %v", err)
	}
	if _, err := svc.Update(ctx, legacy.ID, UpdateParams{
		CalendarID: 1, Title: legacy.Title, StartTime: legacy.StartTime, EndTime: legacy.EndTime,
	}); err != nil {
		t.Fatalf("move the legacy row home: %v", err)
	}
	for _, cal := range []int64{1, calL} {
		if _, err := svc.q.GetEventExdateDeleteByCalendarUIDRecurrence(ctx, storage.GetEventExdateDeleteByCalendarUIDRecurrenceParams{
			CalendarID: cal, Uid: master.UID, RecurrenceID: stale,
		}); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("source log in calendar %d after the move: err = %v, want none", cal, err)
		}
	}
}

// TestRestoreByID_LiveMasterIsAlreadyRestored: a restore by ID of a live
// master returns ErrAlreadyRestored, which still matches ErrNotDeleted.
func TestRestoreByID_LiveMasterIsAlreadyRestored(t *testing.T) {
	svc := newTestService(t)
	master, _, _ := singleSeriesWithOverrides(t, svc)
	err := svc.RestoreByID(context.Background(), master.ID)
	if !errors.Is(err, ErrAlreadyRestored) || !errors.Is(err, ErrNotDeleted) {
		t.Errorf("RestoreByID(live master) err = %v, want ErrAlreadyRestored and ErrNotDeleted", err)
	}
}

// TestListTrash_HidesStaleOccurrenceLog: an occurrence log in a calendar with
// no master of its UID and no row names nothing to restore. The trash lists
// no entry for it.
func TestListTrash_HidesStaleOccurrenceLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calL := insertCalendar(t, svc, "Stale")
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id) VALUES (?, 'stale-uid', '2026-05-05T09:00:00Z')`, calL); err != nil {
		t.Fatalf("seed the stale log: %v", err)
	}
	entries, err := svc.ListTrash(ctx, calL)
	if err != nil || len(entries) != 0 {
		t.Errorf("trash entries = %d, %v; want 0", len(entries), err)
	}
}

// purgedMasterSeries deletes the series of calendar 1 with undo data, and
// then removes its master row as an older purge did. The trashed override at
// second stays. It returns the undo data and the override row.
func purgedMasterSeries(t *testing.T, svc *Service) (UndoMeta, storage.Event, Event) {
	t.Helper()
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, master.ID); err != nil {
		t.Fatalf("purge the master row: %v", err)
	}
	return meta, override, master
}

// TestRestorePurgedMasterRows_RefusedBesideLiveMaster: the rows of a purged
// master do not come back while another calendar holds a live master of the
// UID. That unrelated series would take them over as legacy overrides.
func TestRestorePurgedMasterRows_RefusedBesideLiveMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	meta, override, master := purgedMasterSeries(t, svc)
	calB := insertCalendar(t, svc, "B")
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calB, Title: "Other series",
		StartTime: master.StartTime, EndTime: master.EndTime, RecurrenceRule: master.RecurrenceRule,
	}); err != nil {
		t.Fatalf("upsert master B: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrMasterPurged) {
		t.Errorf("RestoreUndo err = %v, want ErrMasterPurged", err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); !errors.Is(err, ErrMasterPurged) {
		t.Errorf("RestoreByUID err = %v, want ErrMasterPurged", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("override after the refusals = trashed %v, %v; want it still in the trash", r.DeletedAt != nil, err)
	}
}

// TestPurgeMaster_PurgesItsTrashedOverrides: the purge of a master takes its
// trashed overrides in its calendar along, and keeps the live ones.
func TestPurgeMaster_PurgesItsTrashedOverrides(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	get := func(at time.Time) storage.Event {
		t.Helper()
		r, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339)})
		if err != nil {
			t.Fatalf("get override: %v", err)
		}
		return r
	}
	trashed, live := get(second), get(third)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, live.ID); err != nil {
		t.Fatalf("RestoreByID(live override): %v", err)
	}
	if err := svc.PurgeByID(ctx, master.ID); err != nil {
		t.Fatalf("PurgeByID(master): %v", err)
	}
	if _, err := svc.GetIncludingDeleted(ctx, trashed.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("trashed override after the purge: err = %v, want it purged", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, live.ID); err != nil || r.DeletedAt != nil {
		t.Errorf("live override after the purge = trashed %v, %v; want it live", r.DeletedAt != nil, err)
	}
}

// TestUndoOverrideDelete_PurgedMasterHasNothingToRestore: the undo of an
// override delete whose master a purge took has nothing to restore. The row
// stays in the trash, and the tombstone of the series stays.
func TestUndoOverrideDelete_PurgedMasterHasNothingToRestore(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, override.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo(override): %v", err)
	}
	seedTombstone(t, svc, 1, master.UID)
	if _, err := svc.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, master.ID); err != nil {
		t.Fatalf("purge the master row: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo err = %v, want ErrNotDeleted", err)
	}
	if r, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || r.DeletedAt == nil {
		t.Errorf("override after the undo = trashed %v, %v; want it in the trash", r.DeletedAt != nil, err)
	}
	if n := countTombstones(t, svc, 1, master.UID); n != 1 {
		t.Errorf("tombstones after the undo = %d, want 1", n)
	}
}

// TestPruneMarker_RestoreByIDDropsIt: a restore by ID of a pruned row drops
// the pruned marker of its occurrence.
func TestPruneMarker_RestoreByIDDropsIt(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T10:00:00Z' WHERE id = ?`, override.ID); err != nil {
		t.Fatalf("prune the row: %v", err)
	}
	if err := svc.q.RecordEventExdatePrune(ctx, storage.RecordEventExdatePruneParams{CalendarID: 1, Uid: master.UID, RecurrenceID: override.RecurrenceID}); err != nil {
		t.Fatalf("write the marker: %v", err)
	}
	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	var n int64
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_exdate_deletes WHERE uid = ? AND pruned = 1`, master.UID).Scan(&n); err != nil || n != 0 {
		t.Errorf("pruned markers after the restore = %d, %v; want 0", n, err)
	}
}

// TestReconcileAfterRestore_LegacyKeepsItsTombstone: the restore of a legacy
// override marks the calendar of its master. The tombstone of the calendar
// of the row stays, because the server object there still needs its delete.
func TestReconcileAfterRestore_LegacyKeepsItsTombstone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "Legacy")
	fourth := second.Add(48 * time.Hour)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: calL, Title: "Legacy",
		StartTime: fourth, EndTime: fourth.Add(time.Hour), RecurrenceID: fourth.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy row: %v", err)
	}
	seedTombstone(t, svc, calL, master.UID)
	if err := storage.ReconcileAfterRestore(ctx, svc.db, calL, master.UID, "event"); err != nil {
		t.Fatalf("ReconcileAfterRestore: %v", err)
	}
	if n := countTombstones(t, svc, calL, master.UID); n != 1 {
		t.Errorf("tombstones of the legacy calendar = %d, want 1 (the mark went to the master calendar)", n)
	}
}
