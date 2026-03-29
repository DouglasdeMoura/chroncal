package alarm

import (
	"context"
	"database/sql"
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
}

func NewService(db *sql.DB, q *storage.Queries, events *event.Service) *Service {
	return &Service{db: db, q: q, events: events}
}

// Check finds all alarms that are due at the given time.
// An alarm is due when:
//   - trigger_at <= now (the alarm time has passed)
//   - trigger_at > now - StaleThreshold (not too old)
//   - no alarm_state row exists with fired_at set for this alarm+trigger
//
// It also returns snoozed alarms whose snooze-until time has expired.
func (s *Service) Check(ctx context.Context, now time.Time) ([]DueAlarm, error) {
	// Query events with alarms in a generous window around now.
	// We look from (now - StaleThreshold - 24h) to (now + StaleThreshold + 24h) for start times,
	// then filter precisely by computed trigger time.
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
			triggerAt := computeTriggerTime(evt, a)

			// Must be in the past (due) but not stale
			if triggerAt.After(now) {
				continue
			}
			if now.Sub(triggerAt) > StaleThreshold {
				continue
			}

			// Check if already fired
			triggerKey := triggerAt.UTC().Format(time.RFC3339)
			_, err := s.q.GetAlarmState(ctx, storage.GetAlarmStateParams{
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
func (s *Service) Dismiss(ctx context.Context, stateID int64) error {
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
	Capped     bool   // true if the snooze was capped at event end
	PastStart  bool   // true if the snooze fires after event start
	EventStart time.Time
	EventEnd   time.Time
}

// ComputeSnooze calculates the snooze-until time, capped at event end.
// It returns metadata about the computation so the CLI can display warnings.
func (s *Service) ComputeSnooze(ctx context.Context, stateID int64, dur time.Duration) (SnoozeResult, error) {
	st, err := s.q.GetAlarmStateByID(ctx, stateID)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	evt, err := s.events.Get(ctx, st.EventID)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get event %d: %w", st.EventID, err)
	}

	now := time.Now()
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
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
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

func computeTriggerTime(evt event.Event, a model.Alarm) time.Time {
	anchor := evt.StartTime
	if a.Related == "END" {
		anchor = evt.EndTime
	}
	return duration.Add(anchor, a.TriggerValue)
}
