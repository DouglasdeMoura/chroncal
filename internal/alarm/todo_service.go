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

// TodoAlarmLister defines the interface for a list of todo alarms. The
// Lean variants omit X-properties (round-trip-only metadata) — the check
// loop never reads them. The Fireable variants also exclude preserved
// sync-only actions in SQL. The check loop reads every todo on each tick,
// so it uses the batch variant and pays one query for the whole set. The
// snooze lister uses the unfiltered per-todo list, because its own state
// query already filters on the current action.
type TodoAlarmLister interface {
	ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error)
	ListAlarmsLean(ctx context.Context, todoID int64) ([]model.Alarm, error)
	ListFireableAlarmsByTodoIDs(ctx context.Context, todoIDs []int64) (map[int64][]model.Alarm, error)
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

	// Read the alarms of every open todo in one query. A read per todo
	// costs one query per todo on each tick (issue #586).
	openIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if !todoIsOpen(row) {
			continue
		}
		openIDs = append(openIDs, row.ID)
	}
	alarmMap, err := s.todos.ListFireableAlarmsByTodoIDs(ctx, openIDs)
	if err != nil {
		return nil, fmt.Errorf("list todo alarms: %w", err)
	}

	var due []TodoDueAlarm

	for _, row := range rows {
		t := todoFromRow(row)

		// Skip a completed or a cancelled todo.
		if !todoIsOpen(row) {
			continue
		}

		alarms := alarmMap[t.ID]
		if len(alarms) == 0 {
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
					// A refused anchor drops a reminder. Log it like
					// the stale branch below, so the state-dir log
					// shows why the alarm never fired.
					slog.Debug("skipping todo alarm with an unusable trigger",
						"alarm_id", a.ID,
						"todo", t.Summary,
						"error", err)
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
// a recurring master expands, skip any instance whose time is in the set for
// its UID. The slot is overridden. The override row itself carries the alarm
// definition to fire at its own (possibly rescheduled) time.
// A malformed recurrence_id is skipped in silence. The master will fire for
// that slot. That is better than a silent drop of a legitimate alarm.
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
// todoIsOpen reports whether a todo can still fire an alarm. A completed
// or a cancelled todo cannot. The batch alarm read and the loops that
// process the rows share this test, so a new terminal status cannot reach
// one of them alone and silently stop an alarm.
func todoIsOpen(row storage.Todo) bool {
	return row.Status != "COMPLETED" && row.Status != "CANCELLED"
}

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

	// For RELATED=END, anchor at the occurrence's end. ExpandTodo leaves the
	// embedded Todo's DueDate/StartDate as the master's (first-occurrence)
	// values and only advances InstanceTime, so the end must be derived from
	// InstanceTime — not read from the stored DueDate, which would pin every
	// occurrence to the master's DUE (issue #489).
	//
	// InstanceTime is the occurrence's START when DTSTART is present, else its
	// DUE. So: with DTSTART+DUE, shift the DUE−START span onto InstanceTime
	// (issue #367: a VTODO with DTSTART+DUE has an empty Duration field, so the
	// span must come from the dates); DUE-only occurrences are already anchored
	// at DUE; otherwise fall back to START+DURATION.
	if alarm.Related == "END" {
		start := inst.ParseStartDate()
		due := inst.ParseDueDate()
		switch {
		case !start.IsZero() && !due.IsZero():
			base = inst.InstanceTime.Add(due.Sub(start))
		case !due.IsZero():
			base = inst.InstanceTime
		case inst.Duration != "":
			// A legacy invalid, negative, or out-of-range Duration must
			// not anchor the alarm. Refuse instead. A backward anchor
			// fires the alarm at the wrong time, and the callers already
			// skip an alarm on error.
			if err := duration.ValidateSpan(inst.Duration); err != nil {
				return time.Time{}, fmt.Errorf("invalid todo duration for the END anchor: %w", err)
			}
			base = duration.Add(base, inst.Duration)
		}
		// A todo with neither an end nor a span keeps the START anchor.
		// RFC 5545 gives such a VTODO no end, so START is the only time
		// the alarm can bind to. A refusal here would stop a reminder
		// that fires correctly today.
	}

	if alarm.TriggerValue == "" {
		// Empty trigger is always a data defect (CLI validates non-empty,
		// import gates on it, the TUI builders always attach one). The event
		// path skips empties; defaulting to -15m here would fire a fabricated
		// alarm, so refuse and let the caller skip it instead.
		return time.Time{}, fmt.Errorf("empty trigger value")
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

// MarkTodoAlarmFired records that a todo alarm has fired. It returns
// ErrNotFireable when the stored action is sync-only. The insert reads the
// action in the same statement, so a sync pull that disables the alarm
// after the check loop reads it cannot leave a fired state behind.
func (s *TodoService) MarkTodoAlarmFired(ctx context.Context, alarmID, todoID int64, triggerAt time.Time) (int64, error) {
	triggerKey := triggerAt.UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)

	state, err := s.q.InsertTodoAlarmState(ctx, storage.InsertTodoAlarmStateParams{
		AlarmID:   alarmID,
		TodoID:    todoID,
		TriggerAt: triggerKey,
		FiredAt:   &now,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFireable
	}
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
		// ListExpiredSnoozedTodoAlarmStates joins todo_alarms and
		// filters on the current action, so a snoozed alarm a pull
		// rewrote to a sync-only action never reaches here. Keep that
		// filter: this loop does not test the action itself.

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
