package alarm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/douglasdemoura/tcal/internal/duration"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/storage"
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
	// Check event alarms
	eventAlarms, err := s.checkEventAlarms(ctx, now)
	if err != nil {
		return nil, nil, fmt.Errorf("check event alarms: %w", err)
	}

	// Check todo alarms
	todoAlarms, err := s.checkTodoAlarms(ctx, now)
	if err != nil {
		return nil, nil, fmt.Errorf("check todo alarms: %w", err)
	}

	return eventAlarms, todoAlarms, nil
}

// checkEventAlarms finds due event alarms
func (s *Service) checkEventAlarms(ctx context.Context, now time.Time) ([]DueAlarm, error) {
	// Query events with alarms in a generous window around now.
	windowStart := now.Add(-StaleThreshold - 24*time.Hour)
	windowEnd := now.Add(StaleThreshold + 24*time.Hour)

	events, err := s.events.ListByDateRange(ctx, windowStart, windowEnd)
	if err != nil {
		return nil, err
	}

	var due []DueAlarm

	// 1. Fresh alarms that haven't fired yet.
	for _, evt := range events {
		alarms, err := s.events.ListAlarms(ctx, evt.ID)
		if err != nil {
			return nil, fmt.Errorf("list alarms for event %d: %w", evt.ID, err)
		}
		for _, a := range alarms {
			triggerAt, err := computeTriggerTime(evt, a)
			if err != nil {
				continue // skip alarms with unparseable triggers
			}

			// Must be in the past (due) but not stale
			if triggerAt.After(now) {
				continue
			}
			if now.Sub(triggerAt) > StaleThreshold {
				continue
			}

			// Check if already fired
			triggerKey := triggerAt.UTC().Format(time.RFC3339)
			_, err = s.q.GetAlarmState(ctx, storage.GetAlarmStateParams{
				AlarmID:   a.ID,
				TriggerAt: triggerKey,
			})
			if err == nil {
				// Already has a state row -- skip
				continue
			}

			due = append(due, DueAlarm{
				Event:     evt,
				Alarm:     a,
				TriggerAt: triggerAt,
			})
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

// ListExpiredSnoozed returns snoozed alarms whose snooze-until time is at or
// before now. The caller should re-fire and mark them via MarkRefired.
func (s *Service) ListExpiredSnoozed(ctx context.Context, now time.Time) ([]DueAlarm, error) {
	states, err := s.q.ListExpiredSnoozedAlarmStates(ctx, sql.NullString{
		String: now.UTC().Format(time.RFC3339),
		Valid:  true,
	})
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

		triggerAt, _ := time.Parse(time.RFC3339, st.SnoozedTo.String)

		due = append(due, DueAlarm{
			Event:     evt,
			Alarm:     matched,
			TriggerAt: triggerAt,
			StateID:   st.ID,
		})
	}
	return due, nil
}

// MarkFired records that an alarm has been fired.
func (s *Service) MarkFired(ctx context.Context, da DueAlarm) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.q.CreateAlarmState(ctx, storage.CreateAlarmStateParams{
		AlarmID:   da.Alarm.ID,
		EventID:   da.Event.ID,
		TriggerAt: da.TriggerAt.UTC().Format(time.RFC3339),
		FiredAt:   sql.NullString{String: now, Valid: true},
	})
	return err
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
	if st.AckedAt.Valid {
		return fmt.Errorf("alarm state %d already dismissed", stateID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.AcknowledgeAlarmState(ctx, storage.AcknowledgeAlarmStateParams{
		AckedAt: sql.NullString{String: now, Valid: true},
		ID:      stateID,
	})
}

// MarkRefired updates a snoozed alarm's fired_at and clears snoozed_to.
func (s *Service) MarkRefired(ctx context.Context, stateID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.q.RefireAlarmState(ctx, storage.RefireAlarmStateParams{
		FiredAt: sql.NullString{String: now, Valid: true},
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
func (s *Service) ComputeSnooze(ctx context.Context, stateID int64, dur time.Duration) (SnoozeResult, error) {
	if dur <= 0 {
		return SnoozeResult{}, fmt.Errorf("snooze duration must be positive")
	}

	st, err := s.q.GetAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return SnoozeResult{}, fmt.Errorf("alarm state %d not found (use 'tcal alarm list' to see pending alarms)", stateID)
	}
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt.Valid {
		return SnoozeResult{}, fmt.Errorf("alarm state %d is already dismissed", stateID)
	}

	evt, err := s.events.Get(ctx, st.EventID)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get event %d: %w", st.EventID, err)
	}

	now := time.Now()

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
	if until.After(evt.EndTime) {
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
func (s *Service) SnoozeUntilStart(ctx context.Context, stateID int64) (SnoozeResult, error) {
	st, err := s.q.GetAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return SnoozeResult{}, fmt.Errorf("alarm state %d not found (use 'tcal alarm list' to see pending alarms)", stateID)
	}
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt.Valid {
		return SnoozeResult{}, fmt.Errorf("alarm state %d is already dismissed", stateID)
	}

	evt, err := s.events.Get(ctx, st.EventID)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get event %d: %w", st.EventID, err)
	}

	now := time.Now()
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
	return s.q.SnoozeAlarmState(ctx, storage.SnoozeAlarmStateParams{
		SnoozedTo: sql.NullString{String: until.UTC().Format(time.RFC3339), Valid: true},
		ID:        stateID,
	})
}

// ListPending returns all fired-but-not-acknowledged alarms.
func (s *Service) ListPending(ctx context.Context) ([]storage.AlarmState, error) {
	return s.q.ListPendingAlarmStates(ctx)
}

func computeTriggerTime(evt event.Event, a model.Alarm) (time.Time, error) {
	trigger := a.TriggerValue
	if trigger == "" {
		return time.Time{}, fmt.Errorf("empty trigger value")
	}

	// Duration triggers: anchor-relative (RELATED=START or END).
	if duration.Validate(trigger) == nil {
		anchor := evt.StartTime
		if a.Related == "END" {
			anchor = evt.EndTime
		}
		// Convert to event's named timezone so that day-level arithmetic
		// (P1D, P1W) handles DST transitions correctly. Without this,
		// AddDate operates on a fixed UTC offset from RFC 3339 storage
		// and produces wrong wall-clock times across DST boundaries.
		if evt.Timezone != "" {
			if loc, err := time.LoadLocation(evt.Timezone); err == nil {
				anchor = anchor.In(loc)
			}
		}
		return duration.Add(anchor, trigger), nil
	}

	// Absolute triggers: the trigger IS the fire time; Related is ignored.
	// Try iCal UTC format: 20060102T150405Z
	if t, err := time.Parse("20060102T150405Z", trigger); err == nil {
		return t, nil
	}
	// Try iCal floating format: 20060102T150405 (interpret in event timezone)
	if t, err := time.Parse("20060102T150405", trigger); err == nil {
		if evt.Timezone != "" {
			if loc, lerr := time.LoadLocation(evt.Timezone); lerr == nil {
				return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
			}
		}
		return t, nil // treat as UTC if no timezone info
	}
	// Try RFC 3339 (legacy CLI-created triggers before normalization)
	if t, err := time.Parse(time.RFC3339, trigger); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("invalid trigger format: %q", trigger)
}
