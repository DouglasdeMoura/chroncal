package alarm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/douglasdemoura/tcal/internal/duration"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/storage"
	"github.com/douglasdemoura/tcal/internal/todo"
)

// TodoDueAlarm represents a due alarm for a todo
type TodoDueAlarm struct {
	Todo      todo.Todo
	Alarm     model.Alarm
	TriggerAt time.Time
	StateID   int64
}

// TodoAlarmLister defines the interface for listing todo alarms
type TodoAlarmLister interface {
	ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error)
}

// TodoService handles alarm operations for todos
type TodoService struct {
	db    *sql.DB
	q     *storage.Queries
	todos TodoAlarmLister
}

// NewTodoService creates a new TodoService
func NewTodoService(db *sql.DB, q *storage.Queries, todos TodoAlarmLister) *TodoService {
	return &TodoService{db: db, q: q, todos: todos}
}

// CheckTodos finds due alarms for todos within the stale threshold window
func (s *TodoService) CheckTodos(ctx context.Context, now time.Time) ([]TodoDueAlarm, error) {
	// Get all todos with due dates in window
	windowStart := now.Add(-StaleThreshold - 24*time.Hour)
	windowEnd := now.Add(StaleThreshold + 24*time.Hour)

	rows, err := s.q.ListTodosByDueDateRange(ctx, storage.ListTodosByDueDateRangeParams{
		DueDate:   windowStart.Format("2006-01-02"),
		DueDate_2: windowEnd.Format("2006-01-02"),
	})
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}

	var due []TodoDueAlarm

	for _, row := range rows {
		t := todoFromRow(row)

		// Skip completed/cancelled todos
		if t.Status == "COMPLETED" || t.Status == "CANCELLED" {
			continue
		}

		alarms, err := s.todos.ListAlarms(ctx, t.ID)
		if err != nil {
			continue
		}

		for _, a := range alarms {
			triggerAt, err := computeTodoTriggerTime(t, a)
			if err != nil {
				continue
			}

			// Check if due but not stale
			if triggerAt.After(now) {
				continue
			}
			if now.Sub(triggerAt) > StaleThreshold {
				continue
			}

			// Check if already fired
			triggerKey := triggerAt.UTC().Format(time.RFC3339)
			_, err = s.q.GetTodoAlarmState(ctx, storage.GetTodoAlarmStateParams{
				AlarmID:   a.ID,
				TriggerAt: triggerKey,
			})
			if err == nil {
				continue // Already has state row
			}

			due = append(due, TodoDueAlarm{
				Todo:      t,
				Alarm:     a,
				TriggerAt: triggerAt,
			})
		}
	}

	return due, nil
}

// todoFromRow converts a storage row to a todo.Todo
func todoFromRow(row storage.Todo) todo.Todo {
	return todo.Todo{
		ID:              row.ID,
		UID:             row.Uid,
		CalendarID:      row.CalendarID,
		Summary:         row.Summary,
		Description:     row.Description,
		Location:        row.Location,
		DueDate:         row.DueDate,
		StartDate:       row.StartDate,
		Duration:        row.Duration,
		CompletedAt:     row.CompletedAt,
		PercentComplete: row.PercentComplete,
		Status:          row.Status,
		Priority:        row.Priority,
		Class:           row.Class,
		URL:             row.Url,
		Categories:      row.Categories,
		RecurrenceRule:  row.RecurrenceRule,
		Timezone:        row.Timezone,
		Sequence:        row.Sequence,
		ExDates:         row.Exdates,
		RDates:          row.Rdates,
		RecurrenceID:    row.RecurrenceID,
		Geo:             row.Geo,
	}
}

// computeTodoTriggerTime calculates when a todo alarm should trigger
func computeTodoTriggerTime(t todo.Todo, alarm model.Alarm) (time.Time, error) {
	// Determine base time (due date or start date)
	base := t.ParseDueDate()
	if base.IsZero() {
		base = t.ParseStartDate()
	}
	if base.IsZero() {
		return time.Time{}, fmt.Errorf("todo has no due or start date")
	}

	if alarm.Related == "END" {
		// For todos, "END" means the due date
		// No duration adjustment needed
		_ = base
	}

	// Parse trigger
	if alarm.TriggerValue == "" {
		return base.Add(-15 * time.Minute), nil // Default 15 min before
	}

	// Handle relative duration
	if len(alarm.TriggerValue) > 0 && (alarm.TriggerValue[0] == '-' || alarm.TriggerValue[0] == 'P' || alarm.TriggerValue[0] == '+') {
		return duration.Add(base, alarm.TriggerValue), nil
	}

	// Absolute time
	return time.Parse(time.RFC3339, alarm.TriggerValue)
}

// MarkTodoAlarmFired records that a todo alarm has fired
func (s *TodoService) MarkTodoAlarmFired(ctx context.Context, alarmID, todoID int64, triggerAt time.Time) (int64, error) {
	triggerKey := triggerAt.UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)

	state, err := s.q.InsertTodoAlarmState(ctx, storage.InsertTodoAlarmStateParams{
		AlarmID:   alarmID,
		TodoID:    todoID,
		TriggerAt: triggerKey,
		FiredAt:   sql.NullString{String: now, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("insert todo alarm state: %w", err)
	}

	return state.ID, nil
}

// DismissTodoAlarm acknowledges a fired todo alarm
func (s *TodoService) DismissTodoAlarm(ctx context.Context, stateID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.UpdateTodoAlarmState(ctx, storage.UpdateTodoAlarmStateParams{
		ID:      stateID,
		AckedAt: sql.NullString{String: now, Valid: true},
	})
}

// SnoozeTodoAlarm reschedules a todo alarm
func (s *TodoService) SnoozeTodoAlarm(ctx context.Context, stateID int64, snoozeUntil time.Time) error {
	return s.q.UpdateTodoAlarmState(ctx, storage.UpdateTodoAlarmStateParams{
		ID:        stateID,
		SnoozedTo: sql.NullString{String: snoozeUntil.UTC().Format(time.RFC3339), Valid: true},
	})
}

// ListExpiredTodoSnoozed returns snoozed todo alarms that should re-fire
func (s *TodoService) ListExpiredTodoSnoozed(ctx context.Context, now time.Time) ([]TodoDueAlarm, error) {
	states, err := s.q.ListExpiredTodoSnoozed(ctx, sql.NullString{
		String: now.UTC().Format(time.RFC3339),
		Valid:  true,
	})
	if err != nil {
		return nil, err
	}

	var due []TodoDueAlarm
	for _, st := range states {
		row, err := s.q.GetTodo(ctx, st.TodoID)
		if err != nil {
			continue
		}
		t := todoFromRow(row)

		alarms, err := s.todos.ListAlarms(ctx, t.ID)
		if err != nil {
			continue
		}

		var matched model.Alarm
		for _, a := range alarms {
			if a.ID == st.AlarmID {
				matched = a
				break
			}
		}
		if matched.ID == 0 {
			continue
		}

		triggerAt, _ := time.Parse(time.RFC3339, st.SnoozedTo.String)

		due = append(due, TodoDueAlarm{
			Todo:      t,
			Alarm:     matched,
			TriggerAt: triggerAt,
			StateID:   st.ID,
		})
	}

	return due, nil
}
