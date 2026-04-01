package alarm

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

type mockAlarmLister struct {
	todoAlarms map[int64][]model.Alarm
}

func (m *mockAlarmLister) ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	if alarms, ok := m.todoAlarms[todoID]; ok {
		return alarms, nil
	}
	return nil, nil
}

func TestService_Check_BothEventAndTodoAlarms(t *testing.T) {
	db, q := testutil.NewTestDB(t)

	// Create services
	evtSvc := event.NewService(db, q)
	todoSvc := todo.NewService(db, q)

	// Create mock todo alarm lister
	mockLister := &mockAlarmLister{
		todoAlarms: make(map[int64][]model.Alarm),
	}

	// Create alarm service with both event and todo support
	svc := NewService(db, q, evtSvc, mockLister)

	// Create event with alarm
	eventStart := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	evt, err := evtSvc.Create(context.Background(), event.CreateParams{
		CalendarID: 1,
		Title:      "Event Alarm Test",
		StartTime:  eventStart,
		EndTime:    eventStart.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	err = evtSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Event alarm"},
	})
	if err != nil {
		t.Fatalf("add event alarm: %v", err)
	}

	// Create todo with alarm
	todoDue := time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC)
	newTodo, err := todoSvc.Create(context.Background(), todo.CreateParams{
		CalendarID: 1,
		Summary:    "Todo Alarm Test",
		DueDate:    todoDue.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create todo: %v", err)
	}

	// Create todo alarm in database
	todoAlarmUID := "todo-alarm-test"
	todoAlarmDesc := "Todo alarm"
	todoAlarm, err := q.CreateTodoAlarm(context.Background(), storage.CreateTodoAlarmParams{
		TodoID:       newTodo.ID,
		Uid:          &todoAlarmUID,
		Action:       "DISPLAY",
		TriggerValue: "-PT30M",
		Description:  &todoAlarmDesc,
	})
	if err != nil {
		t.Fatalf("create todo alarm: %v", err)
	}

	// Register the alarm with our mock lister
	mockLister.todoAlarms[newTodo.ID] = []model.Alarm{{
		ID:           todoAlarm.ID,
		UID:          "todo-alarm-test",
		Action:       "DISPLAY",
		TriggerValue: "-PT30M",
		Description:  "Todo alarm",
	}}

	// Check at 10:30 AM - both alarms should fire:
	// - Event alarm: 9:45 AM (fired, not stale)
	// - Todo alarm: 10:30 AM (firing now)
	checkTime := time.Date(2026, 4, 1, 10, 30, 0, 0, time.UTC)

	// The Check method now returns both event and todo alarms
	// We need to verify the new signature works
	eventAlarms, todoAlarms, err := svc.Check(context.Background(), checkTime)
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	// Should find the event alarm
	if len(eventAlarms) != 1 {
		t.Errorf("event alarms = %d, want 1", len(eventAlarms))
	}

	// Should find the todo alarm
	if len(todoAlarms) != 1 {
		t.Errorf("todo alarms = %d, want 1", len(todoAlarms))
	}
}
