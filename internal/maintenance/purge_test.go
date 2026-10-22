package maintenance

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

// seedAlarmState creates an alarm_state row with a controlled trigger time
// and optionally acknowledges it.
func seedAlarmState(t *testing.T, q *storage.Queries, alarmID, eventID int64, triggerAt time.Time, acked bool) int64 {
	t.Helper()
	ctx := context.Background()
	fired := triggerAt.UTC().Format(time.RFC3339)
	st, err := q.CreateAlarmState(ctx, storage.CreateAlarmStateParams{
		AlarmID:   alarmID,
		EventID:   eventID,
		TriggerAt: triggerAt.UTC().Format(time.RFC3339),
		FiredAt:   &fired,
	})
	if err != nil {
		t.Fatalf("create alarm state: %v", err)
	}
	if acked {
		ackedAt := triggerAt.Add(time.Minute).UTC().Format(time.RFC3339)
		if err := q.AcknowledgeAlarmState(ctx, storage.AcknowledgeAlarmStateParams{
			AckedAt: &ackedAt,
			ID:      st.ID,
		}); err != nil {
			t.Fatalf("ack alarm state: %v", err)
		}
	}
	return st.ID
}

func TestPurger_RunOnce_PurgesOnlyAckedOldAlarmStates(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()

	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	trashSvc := trash.NewService(events, todos, journals)

	e, err := events.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Purge Fixture",
		StartTime:  time.Now().Add(time.Hour),
		EndTime:    time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := events.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}
	alarms, err := events.ListAlarms(ctx, e.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("list alarms: %v (n=%d)", err, len(alarms))
	}
	alarmID := alarms[0].ID

	const retentionDays = 30
	old := time.Now().Add(-retentionDays*24*time.Hour - 48*time.Hour)
	recent := time.Now().Add(-time.Hour)

	veryOld := time.Now().Add(-time.Duration(retentionDays*staleUnackedMultiplier)*24*time.Hour - 48*time.Hour)

	ackedOld := seedAlarmState(t, q, alarmID, e.ID, old, true)
	ackedRecent := seedAlarmState(t, q, alarmID, e.ID, recent, true)
	unackedOld := seedAlarmState(t, q, alarmID, e.ID, old.Add(time.Minute), false)
	unackedVeryOld := seedAlarmState(t, q, alarmID, e.ID, veryOld, false)

	p := NewPurger(trashSvc, q, retentionDays, nil)
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if _, err := q.GetAlarmStateByID(ctx, ackedOld); err == nil {
		t.Error("acked state older than retention must be purged")
	}
	if _, err := q.GetAlarmStateByID(ctx, ackedRecent); err != nil {
		t.Errorf("acked state inside retention must survive: %v", err)
	}
	if _, err := q.GetAlarmStateByID(ctx, unackedOld); err != nil {
		t.Errorf("unacked state inside the stale window must survive (backs 'alarm list'): %v", err)
	}
	if _, err := q.GetAlarmStateByID(ctx, unackedVeryOld); err == nil {
		t.Errorf("unacked state older than %dx retention must be purged", staleUnackedMultiplier)
	}
}

func TestNewPurger_NilLoggerIsSilent(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	p := NewPurger(nil, nil, 30, nil)
	p.logger.Info("soft-delete purge")

	if buf.Len() > 0 {
		t.Fatalf("nil-logger purger wrote to slog.Default (stderr in production, which corrupts the TUI): %q", buf.String())
	}
}

func TestPurger_RunOnce_NilQueriesSkipsAlarmStateCleanup(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	trashSvc := trash.NewService(events, todos, journals)

	p := NewPurger(trashSvc, nil, 30, nil)
	if _, err := p.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce with nil queries: %v", err)
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

// TestPurger_LogsAKeptMasterOncePerPurger: each Purger logs a kept master
// once, at Info. A later run of the same Purger logs it at Debug. A second
// Purger, as the next launch of the TUI makes, logs it at Info again. No run
// logs at Warn.
func TestPurger_LogsAKeptMasterOncePerPurger(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	trashSvc := trash.NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	keptSeriesFixture(t, db, events)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	for i := 0; i < 2; i++ {
		p := NewPurger(trashSvc, nil, 30, logger)
		for j := 0; j < 2; j++ {
			if _, err := p.RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce %d.%d: %v", i, j, err)
			}
		}
	}
	out := buf.String()
	if n := strings.Count(out, "level=WARN"); n != 0 {
		t.Errorf("warnings = %d, want 0:\n%s", n, out)
	}
	if n := strings.Count(out, `level=INFO msg="soft-delete purge kept a master`); n != 2 {
		t.Errorf("Info records of the kept master = %d, want 1 for each Purger:\n%s", n, out)
	}
	if n := strings.Count(out, `level=DEBUG msg="soft-delete purge kept a master`); n != 2 {
		t.Errorf("Debug records of the kept master = %d, want 2:\n%s", n, out)
	}
}

// TestTrashList_NamesTheRowsThatKeepAMaster: the trash entry of a master that
// the purge keeps names the live override rows that keep it.
func TestTrashList_NamesTheRowsThatKeepAMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	events := event.NewService(db, q)
	trashSvc := trash.NewService(events, todo.NewService(db, q), journal.NewService(db, q))
	masterID, _ := keptSeriesFixture(t, db, events)
	ctx := context.Background()
	if _, err := NewPurger(trashSvc, nil, 30, nil).RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	var live int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM events WHERE uid = 'kept-series' AND recurrence_id != '' AND deleted_at IS NULL`).Scan(&live); err != nil {
		t.Fatalf("live override: %v", err)
	}
	entries, err := trashSvc.List(ctx, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.Kind != trash.KindEvent || e.ID != masterID {
			if e.KeptNote() != "" {
				t.Errorf("entry %d (%v) note = %q, want none", e.ID, e.Kind, e.KeptNote())
			}
			continue
		}
		want := fmt.Sprintf("kept: the series still has live overrides, rows %d", live)
		if got := e.KeptNote(); got != want {
			t.Errorf("note of the kept master = %q, want %q", got, want)
		}
		return
	}
	t.Fatalf("no trash entry of the kept master %d", masterID)
}
