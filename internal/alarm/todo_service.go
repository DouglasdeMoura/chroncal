package alarm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/douglasdemoura/tcal/internal/duration"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/recurrence"
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

// CheckTodos finds due alarms for todos within the stale threshold window.
// Recurring todos are expanded via RRULE so alarms fire for each occurrence.
func (s *TodoService) CheckTodos(ctx context.Context, now time.Time) ([]TodoDueAlarm, error) {
	windowStart := now.Add(-StaleThreshold - 24*time.Hour)
	windowEnd := now.Add(StaleThreshold + 24*time.Hour)

	rows, err := s.q.ListAllTodos(ctx)
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
		if err != nil || len(alarms) == 0 {
			continue
		}

		// Expand recurring instances (returns single instance for non-recurring)
		instances := recurrence.ExpandTodo(t, windowStart, windowEnd)

		for _, inst := range instances {
			for _, a := range alarms {
				triggerAt, err := computeTodoTriggerTimeForInstance(inst, a)
				if err != nil {
					continue
				}

				// Build trigger list: initial + REPEAT firings.
				triggers := []time.Time{triggerAt}
				if a.Repeat > 0 && a.Duration != "" {
					for i := 1; i <= a.Repeat; i++ {
						repeatTrigger := triggerAt
						for j := 0; j < i; j++ {
							repeatTrigger = duration.Add(repeatTrigger, a.Duration)
						}
						if !repeatTrigger.IsZero() && repeatTrigger.After(triggerAt) {
							triggers = append(triggers, repeatTrigger)
						}
					}
				}

				// Use instance time for the todo's due/start date
				instanceTodo := t
				if t.DueDate != "" {
					instanceTodo.DueDate = inst.InstanceTime.Format(time.RFC3339)
				} else if t.StartDate != "" {
					instanceTodo.StartDate = inst.InstanceTime.Format(time.RFC3339)
				}

				for _, tt := range triggers {
					if tt.After(now) {
						continue
					}
					if now.Sub(tt) > StaleThreshold {
						continue
					}

					triggerKey := tt.UTC().Format(time.RFC3339)
					_, err = s.q.GetTodoAlarmState(ctx, storage.GetTodoAlarmStateParams{
						AlarmID:   a.ID,
						TriggerAt: triggerKey,
					})
					if err == nil {
						continue
					}

					due = append(due, TodoDueAlarm{
						Todo:      instanceTodo,
						Alarm:     a,
						TriggerAt: tt,
					})
				}
			}
		}
	}

	// Snoozed todo alarms whose snooze-until time has expired.
	snoozed, err := s.ListExpiredTodoSnoozed(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("list expired snoozed todo alarms: %w", err)
	}
	due = append(due, snoozed...)

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

// computeTodoTriggerTimeForInstance calculates when a todo alarm should trigger
// for a specific recurrence instance.
func computeTodoTriggerTimeForInstance(inst recurrence.ExpandedTodo, alarm model.Alarm) (time.Time, error) {
	base := inst.InstanceTime
	if base.IsZero() {
		return time.Time{}, fmt.Errorf("todo instance has no anchor time")
	}

	// For RELATED=END on a todo with duration, offset from end of task.
	if alarm.Related == "END" && inst.Todo.Duration != "" {
		base = duration.Add(base, inst.Todo.Duration)
	}

	if alarm.TriggerValue == "" {
		return base.Add(-15 * time.Minute), nil
	}

	// Duration trigger (relative)
	if duration.Validate(alarm.TriggerValue) == nil {
		anchor := base
		if inst.Todo.Timezone != "" {
			if loc, err := time.LoadLocation(inst.Todo.Timezone); err == nil {
				anchor = anchor.In(loc)
			}
		}
		return duration.Add(anchor, alarm.TriggerValue), nil
	}

	// Absolute triggers
	if t, err := time.Parse("20060102T150405Z", alarm.TriggerValue); err == nil {
		return t, nil
	}
	if t, err := time.Parse("20060102T150405", alarm.TriggerValue); err == nil {
		if inst.Todo.Timezone != "" {
			if loc, lerr := time.LoadLocation(inst.Todo.Timezone); lerr == nil {
				return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
			}
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, alarm.TriggerValue); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("invalid trigger format: %q", alarm.TriggerValue)
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
