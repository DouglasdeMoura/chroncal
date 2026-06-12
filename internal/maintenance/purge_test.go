package maintenance

import (
	"context"
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
