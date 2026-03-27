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

// Snooze reschedules a fired alarm to fire again after the given duration.
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
