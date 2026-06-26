package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

type mockTodoAlarmLister struct {
	alarms []model.Alarm
}

func (m *mockTodoAlarmLister) ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	return m.alarms, nil
}

func (m *mockTodoAlarmLister) ListAlarmsLean(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	return m.ListAlarms(ctx, todoID)
}

func TestCheckTodoAlarms_DueAlarm(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	// Create a todo with due date
	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	_, err := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Submit Report",
		DueDate:    base.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}

	// Check at 4:30 PM (alarm fires at 4:00 PM with -PT1H trigger)
	checkTime := time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC)

	mockLister := &mockTodoAlarmLister{
		alarms: []model.Alarm{{
			ID:           1,
			UID:          "todo-alarm-1",
			Action:       "DISPLAY",
			TriggerValue: "-PT1H",
			Description:  "Report due in 1 hour",
		}},
	}

	todoAlarmSvc := NewTodoService(db, q, mockLister)
	due, err := todoAlarmSvc.CheckTodos(context.Background(), checkTime)
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	if len(due) != 1 {
		t.Errorf("due alarms = %d, want 1", len(due))
	}

	if len(due) > 0 {
		expectedTrigger := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
		if !due[0].TriggerAt.Equal(expectedTrigger) {
			t.Errorf("trigger at = %v, want %v", due[0].TriggerAt, expectedTrigger)
		}
	}
}

// TestCheckTodoAlarms_TransientStateErrorDoesNotFire is the todo-side mirror of
// TestCheck_TransientStateErrorDoesNotFire: a transient (non-ErrNoRows) error
// from GetTodoAlarmState must abort and propagate, never be treated as "not
// fired". We drop todo_alarm_state to force a "no such table" error.
func TestCheckTodoAlarms_TransientStateErrorDoesNotFire(t *testing.T) {
	ctx := context.Background()
	db, q := newFileTestDB(t)

	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	if _, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "Submit Report",
		DueDate:    base.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("create todo: %v", err)
	}

	checkTime := time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC)
	triggerKey := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC).Format(time.RFC3339)
	const alarmID = 1
	mockLister := &mockTodoAlarmLister{
		alarms: []model.Alarm{{
			ID:           alarmID,
			UID:          "todo-alarm-1",
			Action:       "DISPLAY",
			TriggerValue: "-PT1H",
			Description:  "Report due in 1 hour",
		}},
	}

	// Poison row: todo_id holds TEXT, so GetTodoAlarmState's int64 Scan of that
	// row fails (stand-in for a transient DB error like SQLITE_BUSY).
	// snoozed_to stays NULL so ListExpiredTodoSnoozed (which filters snoozed_to
	// IS NOT NULL) skips it — isolating the failure to the in-loop
	// GetTodoAlarmState lookup.
	insertPoisonAlarmState(ctx, t, db,
		"INSERT INTO todo_alarm_state (alarm_id, todo_id, trigger_at) VALUES (?, 'not-an-int', ?)",
		alarmID, triggerKey)

	todoAlarmSvc := NewTodoService(db, q, mockLister)
	due, err := todoAlarmSvc.CheckTodos(ctx, checkTime)
	if err == nil {
		t.Fatal("expected error from transient GetTodoAlarmState failure, got nil")
	}
	if len(due) != 0 {
		t.Fatalf("got %d due todo alarms on transient error, want 0 (must not re-fire)", len(due))
	}
}

func TestCheckTodoAlarms_SkipsCompleted(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)

	// Create completed todo
	_, err := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Done Task",
		DueDate:    base.Format(time.RFC3339),
		Status:     "COMPLETED",
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}

	checkTime := time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC)

	mockLister := &mockTodoAlarmLister{
		alarms: []model.Alarm{{
			ID:           1,
			UID:          "completed-alarm",
			Action:       "DISPLAY",
			TriggerValue: "-PT1H",
		}},
	}

	todoAlarmSvc := NewTodoService(db, q, mockLister)
	due, err := todoAlarmSvc.CheckTodos(context.Background(), checkTime)
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	if len(due) != 0 {
		t.Errorf("expected 0 alarms for completed todo, got %d", len(due))
	}
}

func TestMarkTodoAlarmFired(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	// First create a todo and alarm
	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	newTodo, err := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    base.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}

	// Create an alarm for the todo
	alarm, err := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          storage.StringToNullable("test-alarm-uid"),
		Action:       "DISPLAY",
		TriggerValue: "-PT1H",
		Related:      "START",
	})
	if err != nil {
		t.Fatalf("create alarm: %v", err)
	}

	todoAlarmSvc := NewTodoService(db, q, &mockTodoAlarmLister{})

	triggerAt := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
	stateID, err := todoAlarmSvc.MarkTodoAlarmFired(context.Background(), alarm.ID, newTodo.ID, triggerAt)
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	if stateID == 0 {
		t.Error("expected non-zero state ID")
	}
}

func TestDismissTodoAlarm(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	// Create todo and alarm first
	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	newTodo, _ := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    base.Format(time.RFC3339),
	})
	alarm, _ := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          storage.StringToNullable("test-alarm-uid"),
		Action:       "DISPLAY",
		TriggerValue: "-PT1H",
		Related:      "START",
	})

	todoAlarmSvc := NewTodoService(db, q, &mockTodoAlarmLister{})

	// First mark as fired
	triggerAt := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
	stateID, _ := todoAlarmSvc.MarkTodoAlarmFired(context.Background(), alarm.ID, newTodo.ID, triggerAt)

	// Then dismiss
	err := todoAlarmSvc.DismissTodoAlarm(context.Background(), stateID)
	if err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// Verify it's dismissed
	state, err := q.GetTodoAlarmState(context.Background(), storage.GetTodoAlarmStateParams{
		AlarmID:   alarm.ID,
		TriggerAt: triggerAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get state: %v", err)
	}

	if state.AckedAt == nil {
		t.Error("expected alarm to be dismissed (acked_at set)")
	}
}

func TestSnoozeTodoAlarm(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	// Create todo and alarm first
	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	newTodo, _ := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    base.Format(time.RFC3339),
	})
	alarm, _ := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          storage.StringToNullable("test-alarm-uid"),
		Action:       "DISPLAY",
		TriggerValue: "-PT1H",
		Related:      "START",
	})

	todoAlarmSvc := NewTodoService(db, q, &mockTodoAlarmLister{})

	triggerAt := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
	stateID, _ := todoAlarmSvc.MarkTodoAlarmFired(context.Background(), alarm.ID, newTodo.ID, triggerAt)

	snoozeUntil := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	err := todoAlarmSvc.SnoozeTodoAlarm(context.Background(), stateID, snoozeUntil)
	if err != nil {
		t.Fatalf("snooze: %v", err)
	}

	// Verify snooze time is set
	state, err := q.GetTodoAlarmState(context.Background(), storage.GetTodoAlarmStateParams{
		AlarmID:   alarm.ID,
		TriggerAt: triggerAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get state: %v", err)
	}

	if state.SnoozedTo == nil {
		t.Error("expected snooze time to be set")
	}
}

func TestDismissTodoAlarm_PreservesFiredAt(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	newTodo, _ := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    base.Format(time.RFC3339),
	})
	alarm, _ := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          storage.StringToNullable("test-alarm-uid"),
		Action:       "DISPLAY",
		TriggerValue: "-PT1H",
		Related:      "START",
	})

	todoAlarmSvc := NewTodoService(db, q, &mockTodoAlarmLister{})

	triggerAt := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
	stateID, _ := todoAlarmSvc.MarkTodoAlarmFired(context.Background(), alarm.ID, newTodo.ID, triggerAt)

	if err := todoAlarmSvc.DismissTodoAlarm(context.Background(), stateID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	state, err := q.GetTodoAlarmState(context.Background(), storage.GetTodoAlarmStateParams{
		AlarmID:   alarm.ID,
		TriggerAt: triggerAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get state: %v", err)
	}

	if state.AckedAt == nil {
		t.Error("expected alarm to be dismissed (acked_at set)")
	}
	if state.FiredAt == nil {
		t.Error("dismiss must preserve fired_at, but it was cleared")
	}
}

func TestSnoozeTodoAlarm_PreservesFiredAt(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	newTodo, _ := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    base.Format(time.RFC3339),
	})
	alarm, _ := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          storage.StringToNullable("test-alarm-uid"),
		Action:       "DISPLAY",
		TriggerValue: "-PT1H",
		Related:      "START",
	})

	todoAlarmSvc := NewTodoService(db, q, &mockTodoAlarmLister{})

	triggerAt := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
	stateID, _ := todoAlarmSvc.MarkTodoAlarmFired(context.Background(), alarm.ID, newTodo.ID, triggerAt)

	snoozeUntil := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	if err := todoAlarmSvc.SnoozeTodoAlarm(context.Background(), stateID, snoozeUntil); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	state, err := q.GetTodoAlarmState(context.Background(), storage.GetTodoAlarmStateParams{
		AlarmID:   alarm.ID,
		TriggerAt: triggerAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get state: %v", err)
	}

	if state.SnoozedTo == nil {
		t.Error("expected snooze time to be set")
	}
	if state.FiredAt == nil {
		t.Error("snooze must preserve fired_at, but it was cleared")
	}
}

func TestListExpiredTodoSnoozed(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	// Create todo and alarm first
	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	newTodo, _ := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    base.Format(time.RFC3339),
	})
	alarm, _ := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          storage.StringToNullable("test-alarm-uid"),
		Action:       "DISPLAY",
		TriggerValue: "-PT1H",
		Related:      "START",
	})

	todoAlarmSvc := NewTodoService(db, q, &mockTodoAlarmLister{
		alarms: []model.Alarm{{ID: alarm.ID, UID: "test-alarm-uid", Action: "DISPLAY", TriggerValue: "-PT1H"}},
	})

	// Create a fired and snoozed alarm
	triggerAt := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
	stateID, _ := todoAlarmSvc.MarkTodoAlarmFired(context.Background(), alarm.ID, newTodo.ID, triggerAt)

	snoozeTime := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	todoAlarmSvc.SnoozeTodoAlarm(context.Background(), stateID, snoozeTime)

	// Check after snooze time expires
	checkTime := time.Date(2026, 4, 1, 17, 30, 0, 0, time.UTC)
	expired, err := todoAlarmSvc.ListExpiredTodoSnoozed(context.Background(), checkTime)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}

	if len(expired) != 1 {
		t.Errorf("expired snoozed = %d, want 1", len(expired))
	}
}

// TestListExpiredTodoSnoozed_SkipsDismissed guards issue #295: a fired todo
// alarm that is snoozed and then dismissed must NOT re-fire once the snooze
// expires. DismissTodoAlarm sets acked_at but leaves snoozed_to intact, so
// without an acked_at guard the expired-snooze scan returns the dismissed row
// and a phantom notification fires for an alarm the user explicitly dismissed.
// The event path is immune because ListExpiredSnoozedAlarmStates carries
// acked_at IS NULL; this asserts the todo query matches that contract.
func TestListExpiredTodoSnoozed_SkipsDismissed(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	todoSvc := todo.NewService(db, q)
	base := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	newTodo, _ := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Test Todo",
		DueDate:    base.Format(time.RFC3339),
	})
	alarm, _ := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          storage.StringToNullable("test-alarm-uid"),
		Action:       "DISPLAY",
		TriggerValue: "-PT1H",
		Related:      "START",
	})

	todoAlarmSvc := NewTodoService(db, q, &mockTodoAlarmLister{
		alarms: []model.Alarm{{ID: alarm.ID, UID: "test-alarm-uid", Action: "DISPLAY", TriggerValue: "-PT1H"}},
	})

	// Fire, snooze, then dismiss the alarm.
	triggerAt := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)
	stateID, _ := todoAlarmSvc.MarkTodoAlarmFired(context.Background(), alarm.ID, newTodo.ID, triggerAt)

	snoozeTime := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	if err := todoAlarmSvc.SnoozeTodoAlarm(context.Background(), stateID, snoozeTime); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	if err := todoAlarmSvc.DismissTodoAlarm(context.Background(), stateID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// After the snooze expires, the dismissed alarm must not re-fire.
	checkTime := time.Date(2026, 4, 1, 17, 30, 0, 0, time.UTC)
	expired, err := todoAlarmSvc.ListExpiredTodoSnoozed(context.Background(), checkTime)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}

	if len(expired) != 0 {
		t.Errorf("expired snoozed = %d, want 0 (dismissed alarm must not re-fire, issue #295)", len(expired))
	}
}

// TestCheckTodos_OverrideAwareness is the regression test for issue #366:
// when a recurring todo has an override (a row with the same UID but a
// non-empty recurrence_id), CheckTodos must not fire the master's alarm for
// the overridden slot in addition to the override's own alarm.
//
// Without the fix, ExpandTodo on the master still produces the overridden
// occurrence (no EXDATE is set when a CalDAV override arrives), while
// ExpandTodo on the override row emits a single instance at the same time.
// Because the master and override have distinct alarm_ids, the unique
// (alarm_id, trigger_at) state index does not prevent the double firing.
func TestCheckTodos_OverrideAwareness(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	ctx := context.Background()
	todoSvc := todo.NewService(db, q)

	// Master: daily recurring, first occurrence Apr 1 17:00 UTC.
	firstOcc := time.Date(2026, 4, 1, 17, 0, 0, 0, time.UTC)
	master, err := todoSvc.UpsertByUID(ctx, todo.UpsertParams{
		UID:            "override-alarm-test",
		CalendarID:     1,
		Summary:        "Daily Stand-up",
		DueDate:        firstOcc.Format(time.RFC3339),
		RecurrenceRule: "FREQ=DAILY",
	})
	if err != nil {
		t.Fatalf("upsert master todo: %v", err)
	}
	if err := todoSvc.ReplaceAlarms(ctx, master.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT1H"},
	}); err != nil {
		t.Fatalf("replace master alarms: %v", err)
	}

	// Override for the Apr 2 occurrence at the same due time, different summary.
	// No EXDATE is added to the master — matching CalDAV sync behaviour where the
	// server pushes an override without also updating the master's EXDATE list.
	apr2 := time.Date(2026, 4, 2, 17, 0, 0, 0, time.UTC)
	override, err := todoSvc.UpsertByUID(ctx, todo.UpsertParams{
		UID:          "override-alarm-test",
		CalendarID:   1,
		Summary:      "Daily Stand-up (rescheduled)",
		DueDate:      apr2.Format(time.RFC3339),
		RecurrenceID: apr2.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert override todo: %v", err)
	}
	if err := todoSvc.ReplaceAlarms(ctx, override.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT1H"},
	}); err != nil {
		t.Fatalf("replace override alarms: %v", err)
	}

	// Check at Apr 2 17:30 UTC.  The Apr 2 16:00 trigger is 1.5 h old, well
	// within StaleThreshold=24 h.  The master expands Apr 2 via RRULE (no EXDATE),
	// so without the fix both the master alarm and the override alarm fire — 2 due
	// alarms.  With the fix the master's Apr 2 slot is suppressed and only the
	// override's alarm fires — 1 due alarm.
	checkTime := time.Date(2026, 4, 2, 17, 30, 0, 0, time.UTC)
	todoAlarmSvc := NewTodoService(db, q, todoSvc)
	due, err := todoAlarmSvc.CheckTodos(ctx, checkTime)
	if err != nil {
		t.Fatalf("check todos: %v", err)
	}

	if len(due) != 1 {
		t.Errorf("CheckTodos = %d due alarms, want 1; master must not fire for overridden slot (issue #366)", len(due))
		for i, d := range due {
			t.Logf("  [%d] todo_id=%d summary=%q trigger=%v", i, d.Todo.ID, d.Todo.Summary, d.TriggerAt)
		}
		return
	}
	if due[0].Todo.ID != override.ID {
		t.Errorf("due alarm is for todo ID %d (master=%d), want override ID %d", due[0].Todo.ID, master.ID, override.ID)
	}
}

// TestCheckTodos_FiresLongLeadTimeAlarm guards issue #98 on the todo path: a
// todo due 7 days out with a "1 week before" alarm must fire now even though
// the todo instance is far past the base forward window. Uses the real todo
// service so the DB-backed trigger scan that sizes the window is exercised.
func TestCheckTodos_FiresLongLeadTimeAlarm(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	todoSvc := todo.NewService(db, q)
	ctx := context.Background()

	due := time.Now().Add(7 * 24 * time.Hour)
	td, err := todoSvc.Create(ctx, todo.CreateParams{
		CalendarID: 1,
		Summary:    "Far Deadline",
		DueDate:    due.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if err := todoSvc.ReplaceAlarms(ctx, td.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-P1W", Description: "1 week reminder"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}

	todoAlarmSvc := NewTodoService(db, q, todoSvc)
	got, err := todoAlarmSvc.CheckTodos(ctx, time.Now())
	if err != nil {
		t.Fatalf("check todos: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d due todo alarms, want 1 (long-lead-time alarm missed)", len(got))
	}
}
