package alarm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// StaleThreshold is the maximum age of an unfired alarm before it is skipped.
const StaleThreshold = 24 * time.Hour

// DueAlarm represents an alarm that should fire now.
type DueAlarm struct {
	Event     event.Event
	Alarm     model.Alarm
	TriggerAt time.Time
	StateID   int64 // non-zero for re-fired snoozed alarms
}

type Service struct {
	db     *sql.DB
	q      *storage.Queries
	events *event.Service
	todos  TodoAlarmLister
}

func NewService(db *sql.DB, q *storage.Queries, events *event.Service, todos TodoAlarmLister) *Service {
	return &Service{db: db, q: q, events: events, todos: todos}
}

// Check finds all alarms that are due at the given time.
// Returns both event alarms and todo alarms separately.
func (s *Service) Check(ctx context.Context, now time.Time) ([]DueAlarm, []TodoDueAlarm, error) {
	eventAlarms, err := s.checkEventAlarms(ctx, now)
	if err != nil {
		return nil, nil, fmt.Errorf("check event alarms: %w", err)
	}

	todoAlarms, err := s.checkTodoAlarms(ctx, now)
	if err != nil {
		return nil, nil, fmt.Errorf("check todo alarms: %w", err)
	}

	return eventAlarms, todoAlarms, nil
}

// MissedAlarm represents an alarm that was never fired because it became stale.
type MissedAlarm struct {
	EventTitle string
	AlarmID    int64
	TriggerAt  time.Time
	Age        time.Duration
}

// MissedTodoAlarm represents a todo alarm that was never fired because it became stale.
type MissedTodoAlarm struct {
	TodoSummary string
	AlarmID     int64
	TriggerAt   time.Time
	Age         time.Duration
}

// CheckMissed returns alarms from the last `lookback` that were never fired
// (no alarm_state / todo_alarm_state entry) and are past the stale threshold.
func (s *Service) CheckMissed(ctx context.Context, now time.Time, lookback time.Duration) ([]MissedAlarm, []MissedTodoAlarm, error) {
	windowStart := now.Add(-lookback)
	windowEnd := now

	// --- Event alarms ---
	recurSvc := recurrence.NewService(s.db, s.q)
	expanded, err := recurSvc.ListExpandedEvents(ctx, windowStart, windowEnd, recurrence.SkipCategories())
	if err != nil {
		return nil, nil, err
	}

	// Batch fetch alarms for all unique parent event IDs to avoid N+1 queries.
	uniqueIDs := make([]int64, 0, len(expanded))
	seen := make(map[int64]struct{}, len(expanded))
	for _, expEvt := range expanded {
		if _, ok := seen[expEvt.ID]; !ok {
			seen[expEvt.ID] = struct{}{}
			uniqueIDs = append(uniqueIDs, expEvt.ID)
		}
	}
	alarmMap, err := s.events.ListAlarmsByEventIDs(ctx, uniqueIDs)
	if err != nil {
		return nil, nil, err
	}

	var missed []MissedAlarm
	for _, expEvt := range expanded {
		for _, a := range alarmMap[expEvt.ID] {
			triggerAt, err := computeTriggerTimeForInstance(expEvt, a)
			if err != nil {
				continue
			}

			triggers := buildRepeatTriggers(triggerAt, a.Repeat, a.Duration)

			for _, t := range triggers {
				if t.After(now) || now.Sub(t) <= StaleThreshold {
					continue // not stale yet
				}
				triggerKey := t.UTC().Format(time.RFC3339)
				_, err := s.q.GetAlarmState(ctx, storage.GetAlarmStateParams{
					AlarmID:   a.ID,
					TriggerAt: triggerKey,
				})
				if err == nil {
					continue // already fired/acknowledged
				}
				if !errors.Is(err, sql.ErrNoRows) {
					continue // real DB error, skip rather than false-positive
				}
				missed = append(missed, MissedAlarm{
					EventTitle: expEvt.Title,
					AlarmID:    a.ID,
					TriggerAt:  t,
					Age:        now.Sub(t),
				})
			}
		}
	}

	// --- Todo alarms ---
	var missedTodos []MissedTodoAlarm
	if s.todos != nil {
		rows, err := s.q.ListAllTodos(ctx)
		if err != nil {
			return missed, nil, err
		}

		for _, row := range rows {
			td := todoFromRow(row)
			if td.Status == "COMPLETED" || td.Status == "CANCELLED" {
				continue
			}

			alarms, err := s.todos.ListAlarms(ctx, td.ID)
			if err != nil || len(alarms) == 0 {
				continue
			}

			instances := recurrence.ExpandTodo(td, windowStart, windowEnd)
			for _, inst := range instances {
				for _, a := range alarms {
					triggerAt, err := computeTodoTriggerTimeForInstance(inst, a)
					if err != nil {
						continue
					}

					triggers := buildRepeatTriggers(triggerAt, a.Repeat, a.Duration)

					for _, t := range triggers {
						if t.After(now) || now.Sub(t) <= StaleThreshold {
							continue
						}
						triggerKey := t.UTC().Format(time.RFC3339)
						_, err := s.q.GetTodoAlarmState(ctx, storage.GetTodoAlarmStateParams{
							AlarmID:   a.ID,
							TriggerAt: triggerKey,
						})
						if err == nil {
							continue
						}
						if !errors.Is(err, sql.ErrNoRows) {
							continue
						}
						missedTodos = append(missedTodos, MissedTodoAlarm{
							TodoSummary: td.Summary,
							AlarmID:     a.ID,
							TriggerAt:   t,
							Age:         now.Sub(t),
						})
					}
				}
			}
		}
	}

	return missed, missedTodos, nil
}

// checkEventAlarms finds due event alarms
func (s *Service) checkEventAlarms(ctx context.Context, now time.Time) ([]DueAlarm, error) {
	windowStart := now.Add(-StaleThreshold - 24*time.Hour)
	windowEnd := now.Add(StaleThreshold + 24*time.Hour)

	recurSvc := recurrence.NewService(s.db, s.q)
	expandedEvents, err := recurSvc.ListExpandedEvents(ctx, windowStart, windowEnd, recurrence.SkipCategories())
	if err != nil {
		return nil, fmt.Errorf("list expanded events: %w", err)
	}

	// Batch fetch alarms for all unique parent event IDs to avoid N+1 queries.
	// For recurring events, multiple expanded instances share the same parent event ID.
	uniqueParentIDs := make([]int64, 0, len(expandedEvents))
	seenIDs := make(map[int64]struct{}, len(expandedEvents))
	for _, expEvt := range expandedEvents {
		if _, seen := seenIDs[expEvt.ID]; !seen {
			seenIDs[expEvt.ID] = struct{}{}
			uniqueParentIDs = append(uniqueParentIDs, expEvt.ID)
		}
	}
	alarmMap, err := s.events.ListAlarmsByEventIDs(ctx, uniqueParentIDs)
	if err != nil {
		return nil, fmt.Errorf("fetch alarms: %w", err)
	}

	var due []DueAlarm

	for _, expEvt := range expandedEvents {
		alarms := alarmMap[expEvt.ID] // nil if no alarms for this event

		for _, a := range alarms {
			triggerAt, err := computeTriggerTimeForInstance(expEvt, a)
			if err != nil {
				continue
			}

			triggers := buildRepeatTriggers(triggerAt, a.Repeat, a.Duration)

			instanceEvent := expEvt.Event
			instanceEvent.StartTime = expEvt.InstanceTime
			instanceEvent.EndTime = expEvt.InstanceTime.Add(expEvt.Span())

			for _, t := range triggers {
				if t.After(now) {
					continue
				}
				if now.Sub(t) > StaleThreshold {
					slog.Debug("skipping stale alarm",
						"alarm_id", a.ID,
						"event", instanceEvent.Title,
						"trigger_at", t.UTC().Format(time.RFC3339),
						"age", now.Sub(t).Round(time.Minute).String(),
					)
					continue
				}

				triggerKey := t.UTC().Format(time.RFC3339)
				_, err = s.q.GetAlarmState(ctx, storage.GetAlarmStateParams{
					AlarmID:   a.ID,
					TriggerAt: triggerKey,
				})
				if err == nil {
					continue
				}

				due = append(due, DueAlarm{
					Event:     instanceEvent,
					Alarm:     a,
					TriggerAt: t,
				})
			}
		}
	}

	// 2. Snoozed alarms whose snooze-until time has expired.
	snoozed, err := s.ListExpiredSnoozed(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("list expired snoozed alarms: %w", err)
	}
	due = append(due, snoozed...)

	return due, nil
}

// checkTodoAlarms finds due todo alarms using TodoService
func (s *Service) checkTodoAlarms(ctx context.Context, now time.Time) ([]TodoDueAlarm, error) {
	if s.todos == nil {
		return nil, nil
	}

	todoSvc := NewTodoService(s.db, s.q, s.todos)
	return todoSvc.CheckTodos(ctx, now)
}

// computeTriggerTimeForInstance calculates trigger time for a specific event instance
func computeTriggerTimeForInstance(expEvt recurrence.ExpandedEvent, alarm model.Alarm) (time.Time, error) {
	trigger := alarm.TriggerValue
	if trigger == "" {
		return time.Time{}, fmt.Errorf("empty trigger value")
	}

	// Duration triggers: anchor-relative (RELATED=START or END).
	if duration.Validate(trigger) == nil {
		anchor := expEvt.InstanceTime
		if alarm.Related == "END" {
			anchor = expEvt.InstanceTime.Add(expEvt.Span())
		}
		// Convert to event's named timezone so that day-level arithmetic
		// (P1D, P1W) handles DST transitions correctly.
		if expEvt.Event.Timezone != "" {
			if loc, err := time.LoadLocation(expEvt.Event.Timezone); err == nil {
				anchor = anchor.In(loc)
			}
		}
		return duration.Add(anchor, trigger), nil
	}

	return parseAbsoluteTrigger(trigger, expEvt.Event.Timezone)
}

// parseAbsoluteTrigger parses an absolute trigger value (iCal UTC, floating, or RFC 3339).
// Floating times are interpreted in the given timezone if non-empty.
func parseAbsoluteTrigger(trigger, timezone string) (time.Time, error) {
	if t, err := time.Parse("20060102T150405Z", trigger); err == nil {
		return t, nil
	}
	if t, err := time.Parse("20060102T150405", trigger); err == nil {
		if timezone != "" {
			if loc, lerr := time.LoadLocation(timezone); lerr == nil {
				return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
			}
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, trigger); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid trigger format: %q", trigger)
}

// ListExpiredSnoozed returns snoozed alarms whose snooze-until time is at or
// before now. The caller should re-fire and mark them via MarkRefired.
func (s *Service) ListExpiredSnoozed(ctx context.Context, now time.Time) ([]DueAlarm, error) {
	nowStr := now.UTC().Format(time.RFC3339)
	states, err := s.q.ListExpiredSnoozedAlarmStates(ctx, &nowStr)
	if err != nil {
		return nil, err
	}

	var due []DueAlarm
	for _, st := range states {
		evt, err := s.events.Get(ctx, st.EventID)
		if err != nil {
			continue // event may have been deleted
		}
		alarms, err := s.events.ListAlarms(ctx, evt.ID)
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
			continue // alarm definition was removed
		}

		triggerAt, _ := time.Parse(time.RFC3339, storage.NullableToString(st.SnoozedTo))

		due = append(due, DueAlarm{
			Event:     evt,
			Alarm:     matched,
			TriggerAt: triggerAt,
			StateID:   st.ID,
		})
	}
	return due, nil
}

// MarkFired records that an alarm has been fired and returns the new state ID.
func (s *Service) MarkFired(ctx context.Context, da DueAlarm) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	st, err := s.q.CreateAlarmState(ctx, storage.CreateAlarmStateParams{
		AlarmID:   da.Alarm.ID,
		EventID:   da.Event.ID,
		TriggerAt: da.TriggerAt.UTC().Format(time.RFC3339),
		FiredAt:   &now,
	})
	if err != nil {
		return 0, err
	}
	return st.ID, nil
}

// MarkTodoFired records that a todo alarm has been fired and returns the new state ID.
func (s *Service) MarkTodoFired(ctx context.Context, tda TodoDueAlarm) (int64, error) {
	todoSvc := NewTodoService(s.db, s.q, s.todos)
	return todoSvc.MarkTodoAlarmFired(ctx, tda.Alarm.ID, tda.Todo.ID, tda.TriggerAt)
}

// MarkTodoRefired re-fires a snoozed todo alarm, clearing the snooze.
func (s *Service) MarkTodoRefired(ctx context.Context, stateID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.RefireTodoAlarmState(ctx, storage.RefireTodoAlarmStateParams{
		FiredAt: &now,
		ID:      stateID,
	})
}

// Dismiss acknowledges a fired alarm so it won't show as pending.
// Returns an error if the state ID does not exist or is already dismissed.
func (s *Service) Dismiss(ctx context.Context, stateID int64) error {
	st, err := s.q.GetAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("alarm state %d not found", stateID)
	}
	if err != nil {
		return fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return fmt.Errorf("alarm state %d already dismissed", stateID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.AcknowledgeAlarmState(ctx, storage.AcknowledgeAlarmStateParams{
		AckedAt: &now,
		ID:      stateID,
	})
}

// MarkRefired updates a snoozed alarm's fired_at and clears snoozed_to.
func (s *Service) MarkRefired(ctx context.Context, stateID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.RefireAlarmState(ctx, storage.RefireAlarmStateParams{
		FiredAt: &now,
		ID:      stateID,
	})
}

// SnoozeResult describes what happened when computing a snooze time.
type SnoozeResult struct {
	Until      time.Time
	Capped     bool // true if the snooze was capped at event end
	PastStart  bool // true if the snooze fires after event start
	EventStart time.Time
	EventEnd   time.Time
}

// ComputeSnooze calculates the snooze-until time, capped at event end.
// It returns metadata about the computation so the CLI can display warnings.
func (s *Service) ComputeSnooze(ctx context.Context, stateID int64, dur time.Duration, now time.Time) (SnoozeResult, error) {
	if dur <= 0 {
		return SnoozeResult{}, fmt.Errorf("snooze duration must be positive")
	}

	st, err := s.q.GetAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return SnoozeResult{}, fmt.Errorf("alarm state %d not found (use 'chroncal alarm list' to see pending alarms)", stateID)
	}
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return SnoozeResult{}, fmt.Errorf("alarm state %d is already dismissed", stateID)
	}

	evt, err := s.events.Get(ctx, st.EventID)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get event %d: %w", st.EventID, err)
	}

	// Reject if the event has already ended.
	if !evt.EndTime.IsZero() && evt.EndTime.Before(now) {
		return SnoozeResult{}, fmt.Errorf("event %q has already ended", evt.Title)
	}

	until := now.Add(dur)

	res := SnoozeResult{
		Until:      until,
		EventStart: evt.StartTime,
		EventEnd:   evt.EndTime,
	}

	// Cap at event end — no point snoozing past when the event is over.
	// Skip capping for all-day events with zero EndTime.
	if !evt.EndTime.IsZero() && until.After(evt.EndTime) {
		res.Until = evt.EndTime
		res.Capped = true
	}

	// Note if the snooze fires after the event has started.
	if res.Until.After(evt.StartTime) {
		res.PastStart = true
	}

	return res, nil
}

// SnoozeUntilStart snoozes an alarm to fire at the event's start time.
func (s *Service) SnoozeUntilStart(ctx context.Context, stateID int64, now time.Time) (SnoozeResult, error) {
	st, err := s.q.GetAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return SnoozeResult{}, fmt.Errorf("alarm state %d not found (use 'chroncal alarm list' to see pending alarms)", stateID)
	}
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return SnoozeResult{}, fmt.Errorf("alarm state %d is already dismissed", stateID)
	}

	evt, err := s.events.Get(ctx, st.EventID)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get event %d: %w", st.EventID, err)
	}

	if now.After(evt.StartTime) {
		return SnoozeResult{}, fmt.Errorf("event %q has already started", evt.Title)
	}

	res := SnoozeResult{
		Until:      evt.StartTime,
		EventStart: evt.StartTime,
		EventEnd:   evt.EndTime,
	}
	return res, nil
}

// Snooze reschedules a fired alarm to fire again at the given time.
func (s *Service) Snooze(ctx context.Context, stateID int64, until time.Time) error {
	snoozeStr := until.UTC().Format(time.RFC3339)
	return s.q.SnoozeAlarmState(ctx, storage.SnoozeAlarmStateParams{
		SnoozedTo: &snoozeStr,
		ID:        stateID,
	})
}

// ListPending returns all fired-but-not-acknowledged alarms.
func (s *Service) ListPending(ctx context.Context) ([]storage.AlarmState, error) {
	return s.q.ListPendingAlarmStates(ctx)
}

// ListPendingTodoAlarms returns all fired-but-not-acknowledged todo alarms.
func (s *Service) ListPendingTodoAlarms(ctx context.Context) ([]storage.TodoAlarmState, error) {
	return s.q.ListPendingTodoAlarmStates(ctx)
}

// DismissTodoAlarm acknowledges a fired todo alarm so it won't show as pending.
func (s *Service) DismissTodoAlarm(ctx context.Context, stateID int64) error {
	st, err := s.q.GetTodoAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("todo alarm state %d not found", stateID)
	}
	if err != nil {
		return fmt.Errorf("get todo alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return fmt.Errorf("todo alarm state %d already dismissed", stateID)
	}
	todoSvc := NewTodoService(s.db, s.q, s.todos)
	return todoSvc.DismissTodoAlarm(ctx, stateID)
}

// SnoozeTodoAlarm reschedules a fired todo alarm to fire again at the given time.
func (s *Service) SnoozeTodoAlarm(ctx context.Context, stateID int64, until time.Time) error {
	st, err := s.q.GetTodoAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("todo alarm state %d not found", stateID)
	}
	if err != nil {
		return fmt.Errorf("get todo alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return fmt.Errorf("todo alarm state %d is already dismissed", stateID)
	}
	todoSvc := NewTodoService(s.db, s.q, s.todos)
	return todoSvc.SnoozeTodoAlarm(ctx, stateID, until)
}

// computeTriggerTime calculates the absolute trigger time for an alarm on an event.
// It's a convenience wrapper used in tests; production code uses computeTriggerTimeForInstance.
func computeTriggerTime(evt event.Event, a model.Alarm) (time.Time, error) {
	return computeTriggerTimeForInstance(recurrence.ExpandedEvent{
		Event:        evt,
		InstanceTime: evt.StartTime,
	}, a)
}

// buildRepeatTriggers returns a list of trigger times for a repeating alarm.
// The result includes the initial trigger time plus additional firings
// at the specified duration interval, up to the repeat count.
func buildRepeatTriggers(triggerAt time.Time, repeat int, durStr string) []time.Time {
	triggers := []time.Time{triggerAt}
	if repeat <= 0 || durStr == "" {
		return triggers
	}
	for i := 1; i <= repeat; i++ {
		triggerAt = duration.Add(triggerAt, durStr)
		if triggerAt.IsZero() || triggerAt.Equal(triggers[len(triggers)-1]) {
			break
		}
		triggers = append(triggers, triggerAt)
	}
	return triggers
}
