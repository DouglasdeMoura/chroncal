package alarm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TodoDueAlarm represents a due alarm for a todo
type TodoDueAlarm struct {
	Todo      todo.Todo
	Alarm     model.Alarm
	TriggerAt time.Time
	StateID   int64
}

// TodoAlarmLister defines the interface for listing todo alarms.
// ListAlarmsLean omits X-properties (round-trip-only metadata) — the check
// loop calls it per todo every tick and never reads them.
type TodoAlarmLister interface {
	ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error)
	ListAlarmsLean(ctx context.Context, todoID int64) ([]model.Alarm, error)
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
	// Widen the forward window for lead times beyond the base window so todos
	// far in the future (e.g. -P1W on a todo due in 7 days) still fire.
	triggers, err := s.q.ListDistinctTodoAlarmTriggers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list todo alarm triggers: %w", err)
	}
	forward := baseForwardWindow + maxLeadTime(triggers)
	windowStart := now.Add(-StaleThreshold - 24*time.Hour)
	windowEnd := now.Add(forward)

	rows, err := s.q.ListAllTodos(ctx)
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}

	// Build per-UID suppression sets from override rows so the master's RRULE
	// expansion skips any slot that has been overridden. The override row itself
	// is still iterated below and produces the correct single instance at its own
	// (possibly rescheduled) time with its own alarm definition.
	overrideKeys := buildOverrideSuppressionKeys(rows)

	var due []TodoDueAlarm

	for _, row := range rows {
		t := todoFromRow(row)

		// Skip completed/cancelled todos
		if t.Status == "COMPLETED" || t.Status == "CANCELLED" {
			continue
		}

		alarms, err := s.todos.ListAlarmsLean(ctx, t.ID)
		if err != nil || len(alarms) == 0 {
			continue
		}

		// Expand recurring instances (returns single instance for non-recurring)
		instances := recurrence.ExpandTodo(t, windowStart, windowEnd)

		// For recurring masters, suppress occurrences that have been overridden.
		// Without this, the master fires its alarm for the overridden slot while
		// the override row also fires its own alarm — causing a duplicate.
		if t.RecurrenceRule != "" && t.RecurrenceID == "" {
			if suppressed := overrideKeys[t.UID]; len(suppressed) > 0 {
				kept := instances[:0]
				for _, inst := range instances {
					if _, ok := suppressed[inst.InstanceTime.UTC().Format(time.RFC3339)]; !ok {
						kept = append(kept, inst)
					}
				}
				instances = kept
			}
		}

		for _, inst := range instances {
			for _, a := range alarms {
				triggerAt, err := computeTodoTriggerTimeForInstance(inst, a)
				if err != nil {
					continue
				}

				triggers := buildRepeatTriggers(triggerAt, a.Repeat, a.Duration)

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
						slog.Debug("skipping stale todo alarm",
							"alarm_id", a.ID,
							"todo", instanceTodo.Summary,
							"trigger_at", tt.UTC().Format(time.RFC3339),
							"age", now.Sub(tt).Round(time.Minute).String(),
						)
						continue
					}

					triggerKey := tt.UTC().Format(time.RFC3339)
					_, err = s.q.GetTodoAlarmState(ctx, storage.GetTodoAlarmStateParams{
						AlarmID:   a.ID,
						TriggerAt: triggerKey,
					})
					if err == nil {
						continue // already fired/acknowledged
					}
					if !errors.Is(err, sql.ErrNoRows) {
						// Transient DB error (e.g. SQLITE_BUSY): we can't
						// tell whether this alarm already fired, so abort
						// rather than risk re-firing it. Propagate.
						return nil, fmt.Errorf("get todo alarm state: %w", err)
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

// buildOverrideSuppressionKeys returns a per-UID set of canonical instance-time
// keys (UTC RFC 3339) derived from override rows in the full todo list. When
// expanding a recurring master, any instance whose time is in the set for its
// UID must be skipped: the slot has been overridden and the override row itself
// carries the alarm definition to fire at its own (possibly rescheduled) time.
// A malformed recurrence_id is silently skipped — the master will fire for
// that slot, which is preferable to silently dropping a legitimate alarm.
func buildOverrideSuppressionKeys(rows []storage.Todo) map[string]map[string]struct{} {
	m := make(map[string]map[string]struct{})
	for _, row := range rows {
		if row.RecurrenceID == "" {
			continue
		}
		t, err := timeutil.ParseRecurrenceID(row.RecurrenceID)
		if err != nil {
			continue
		}
		key := t.UTC().Format(time.RFC3339)
		if m[row.Uid] == nil {
			m[row.Uid] = make(map[string]struct{})
		}
		m[row.Uid][key] = struct{}{}
	}
	return m
}

// todoFromRow converts a storage view row to a todo.Todo
func todoFromRow(row storage.Todo) todo.Todo {
	return todo.Todo{
		ID:              row.ID,
		UID:             row.Uid,
		CalendarID:      row.CalendarID,
		Summary:         row.Summary,
		Description:     storage.NullableToString(row.Description),
		Location:        storage.NullableToString(row.Location),
		DueDate:         storage.NullableToString(row.DueDate),
		StartDate:       storage.NullableToString(row.StartDate),
		Duration:        storage.NullableToString(row.Duration),
		CompletedAt:     storage.NullableToString(row.CompletedAt),
		PercentComplete: row.PercentComplete,
		Status:          row.Status,
		Priority:        row.Priority,
		Class:           row.Class,
		URL:             storage.NullableToString(row.Url),
		RecurrenceRule:  storage.NullableToString(row.RecurrenceRule),
		Timezone:        storage.NullableToString(row.Timezone),
		Sequence:        row.Sequence,
		ExDates:         storage.NullableToString(row.Exdates),
		RDates:          storage.NullableToString(row.Rdates),
		RecurrenceID:    row.RecurrenceID,
		Geo:             storage.NullableToString(row.Geo),
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
	if alarm.Related == "END" && inst.Duration != "" {
		base = duration.Add(base, inst.Duration)
	}

	if alarm.TriggerValue == "" {
		return base.Add(-15 * time.Minute), nil
	}

	// Duration trigger (relative)
	if duration.Validate(alarm.TriggerValue) == nil {
		anchor := base
		if inst.Timezone != "" {
			if loc, err := time.LoadLocation(inst.Timezone); err == nil {
				anchor = anchor.In(loc)
			}
		}
		return duration.Add(anchor, alarm.TriggerValue), nil
	}

	// Absolute triggers
	return model.ParseAbsoluteTime(alarm.TriggerValue, inst.Timezone)
}

// MarkTodoAlarmFired records that a todo alarm has fired
func (s *TodoService) MarkTodoAlarmFired(ctx context.Context, alarmID, todoID int64, triggerAt time.Time) (int64, error) {
	triggerKey := triggerAt.UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)

	state, err := s.q.InsertTodoAlarmState(ctx, storage.InsertTodoAlarmStateParams{
		AlarmID:   alarmID,
		TodoID:    todoID,
		TriggerAt: triggerKey,
		FiredAt:   &now,
	})
	if err != nil {
		return 0, fmt.Errorf("insert todo alarm state: %w", err)
	}

	return state.ID, nil
}

// DismissTodoAlarm acknowledges a fired todo alarm
func (s *TodoService) DismissTodoAlarm(ctx context.Context, stateID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.AcknowledgeTodoAlarmState(ctx, storage.AcknowledgeTodoAlarmStateParams{
		AckedAt: &now,
		ID:      stateID,
	})
}

// SnoozeTodoAlarm reschedules a todo alarm
func (s *TodoService) SnoozeTodoAlarm(ctx context.Context, stateID int64, snoozeUntil time.Time) error {
	snoozeStr := snoozeUntil.UTC().Format(time.RFC3339)
	return s.q.SnoozeTodoAlarmState(ctx, storage.SnoozeTodoAlarmStateParams{
		SnoozedTo: &snoozeStr,
		ID:        stateID,
	})
}

// ListExpiredTodoSnoozed returns snoozed todo alarms that should re-fire
func (s *TodoService) ListExpiredTodoSnoozed(ctx context.Context, now time.Time) ([]TodoDueAlarm, error) {
	nowStr := now.UTC().Format(time.RFC3339)
	states, err := s.q.ListExpiredTodoSnoozed(ctx, &nowStr)
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

		alarms, err := s.todos.ListAlarmsLean(ctx, t.ID)
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

		triggerAt, _ := time.Parse(time.RFC3339, storage.NullableToString(st.SnoozedTo))

		due = append(due, TodoDueAlarm{
			Todo:      t,
			Alarm:     matched,
			TriggerAt: triggerAt,
			StateID:   st.ID,
		})
	}

	return due, nil
}
