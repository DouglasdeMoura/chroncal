package alarm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// DefaultAlarmConfig holds the default-alarm preferences (issue #815). The
// check loop synthesizes a default alarm at check time for every event that
// carries no alarm row. A default alarm lives only in the check result and
// in default_alarm_state: it is never written to the event, so it never
// syncs back to the server.
//
// Triggers comes from the global [alarms] default config key. A per-calendar
// setting (calendars.default_alarms) replaces it for one calendar; the
// check loop reads that setting from the calendars table on every check.
type DefaultAlarmConfig struct {
	Triggers []model.DefaultAlarm
	// SkipAllDay excludes all-day events from default alarms. An all-day
	// event starts at midnight, so a "-PT15M" trigger fires the evening
	// before.
	SkipAllDay bool
}

// SetDefaultConfig installs the default-alarm preferences. The CLI calls it
// once after config load. A zero value disables default alarms.
func (s *Service) SetDefaultConfig(cfg DefaultAlarmConfig) {
	s.defaults = cfg
}

// triggerStrings returns the trigger values for the forward-window sizing.
func (c DefaultAlarmConfig) triggerStrings() []string {
	out := make([]string, 0, len(c.Triggers))
	for _, t := range c.Triggers {
		out = append(out, t.TriggerValue)
	}
	return out
}

// defaultAlarmsFor resolves the default triggers for one calendar. A
// per-calendar setting replaces the global list. The overrides map holds an
// entry only for calendars with an explicit setting, and that entry may be
// empty: an empty list means default alarms are off for that calendar.
func (s *Service) defaultAlarmsFor(calendarID int64, overrides map[int64][]model.DefaultAlarm) []model.DefaultAlarm {
	if specs, ok := overrides[calendarID]; ok {
		return specs
	}
	return s.defaults.Triggers
}

// loadCalendarDefaultOverrides reads the per-calendar default-alarm setting
// of every calendar. It returns a map that holds an entry only for a
// calendar with an explicit setting. The value may be empty: an empty list
// means the calendar turned default alarms off.
func (s *Service) loadCalendarDefaultOverrides(ctx context.Context) (map[int64][]model.DefaultAlarm, error) {
	cals, err := s.q.ListCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	overrides := make(map[int64][]model.DefaultAlarm, len(cals))
	for _, c := range cals {
		if c.DefaultAlarms == nil {
			continue // no explicit setting: the calendar inherits the global default
		}
		specs, invalid := model.ParseDefaultAlarmList(*c.DefaultAlarms)
		for _, bad := range invalid {
			slog.Debug("skipping invalid per-calendar default alarm",
				"calendar_id", c.ID, "spec", bad)
		}
		overrides[c.ID] = specs
	}
	return overrides, nil
}

// checkDefaultEventAlarms finds due default alarms for the expanded events.
// alarmMap holds the stored fireable alarms per parent event ID. A default
// alarm applies only when the event carries no alarm row at all: the event's
// own reminders win, and a non-fireable sentinel such as ACTION:NONE means
// the organiser turned the reminder off (issue #815).
func (s *Service) checkDefaultEventAlarms(
	ctx context.Context,
	expandedEvents []recurrence.ExpandedEvent,
	alarmMap map[int64][]model.Alarm,
	now time.Time,
	overrides map[int64][]model.DefaultAlarm,
) ([]DueAlarm, error) {
	if len(s.defaults.Triggers) == 0 && len(overrides) == 0 {
		return nil, nil
	}

	candidates := make([]int64, 0, len(expandedEvents))
	seen := make(map[int64]struct{}, len(expandedEvents))
	for _, expEvt := range expandedEvents {
		if _, ok := seen[expEvt.ID]; ok {
			continue
		}
		seen[expEvt.ID] = struct{}{}
		if len(alarmMap[expEvt.ID]) == 0 {
			candidates = append(candidates, expEvt.ID)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	rows, err := s.q.ListEventIDsWithAlarms(ctx, candidates)
	if err != nil {
		return nil, fmt.Errorf("list events with alarms: %w", err)
	}
	hasAnyAlarm := make(map[int64]struct{}, len(rows))
	for _, r := range rows {
		hasAnyAlarm[r] = struct{}{}
	}

	var due []DueAlarm
	for _, expEvt := range expandedEvents {
		if len(alarmMap[expEvt.ID]) > 0 {
			continue // the event's own reminders win
		}
		if _, ok := hasAnyAlarm[expEvt.ID]; ok {
			continue // carries a non-fireable alarm, e.g. the ACTION:NONE sentinel
		}
		specs := s.defaultAlarmsFor(expEvt.CalendarID, overrides)
		if len(specs) == 0 {
			continue
		}
		if s.defaults.SkipAllDay && expEvt.AllDay {
			continue
		}

		instanceEvent := expEvt.Event
		instanceEvent.StartTime = expEvt.InstanceTime
		instanceEvent.EndTime = expEvt.InstanceTime.Add(expEvt.Span())

		for _, spec := range specs {
			// Default alarms anchor on the event start and do not repeat.
			synthetic := model.Alarm{
				Action:       spec.Action,
				TriggerValue: spec.TriggerValue,
				Related:      "START",
			}
			triggerAt, err := computeTriggerTimeForInstance(expEvt, synthetic)
			if err != nil {
				continue
			}
			if triggerAt.After(now) {
				continue
			}
			if now.Sub(triggerAt) > StaleThreshold {
				continue // same stale rule as stored alarms
			}

			triggerKey := triggerAt.UTC().Format(time.RFC3339)
			_, err = s.q.GetDefaultAlarmState(ctx, storage.GetDefaultAlarmStateParams{
				EventID:      expEvt.ID,
				TriggerValue: spec.TriggerValue,
				TriggerAt:    triggerKey,
			})
			if err == nil {
				continue // already fired/acknowledged
			}
			if !errors.Is(err, sql.ErrNoRows) {
				// Transient DB error: abort rather than risk re-firing,
				// like the stored-alarm path does.
				return nil, fmt.Errorf("get default alarm state: %w", err)
			}

			due = append(due, DueAlarm{
				Event:     instanceEvent,
				Alarm:     synthetic,
				TriggerAt: triggerAt,
				IsDefault: true,
			})
		}
	}
	return due, nil
}

// listExpiredSnoozedDefaults returns snoozed default alarms whose
// snooze-until time is at or before now.
func (s *Service) listExpiredSnoozedDefaults(ctx context.Context, now time.Time) ([]DueAlarm, error) {
	nowStr := now.UTC().Format(time.RFC3339)
	states, err := s.q.ListExpiredSnoozedDefaultAlarmStates(ctx, &nowStr)
	if err != nil {
		return nil, err
	}

	var due []DueAlarm
	for _, st := range states {
		evt, err := s.resolveDefaultStateEvent(ctx, st)
		if err != nil {
			continue // event may have been deleted
		}
		triggerAt, _ := time.Parse(time.RFC3339, storage.NullableToString(st.SnoozedTo))
		due = append(due, DueAlarm{
			Event: evt,
			Alarm: model.Alarm{
				Action:       st.Action,
				TriggerValue: st.TriggerValue,
				Related:      "START",
			},
			TriggerAt: triggerAt,
			StateID:   st.ID,
			IsDefault: true,
		})
	}
	return due, nil
}

// resolveDefaultStateEvent returns the event whose start time corresponds to
// the occurrence a default_alarm_state row fired for. It mirrors
// resolveStateEvent, which reads the trigger definition from the stored
// trigger value instead of an event_alarms row.
func (s *Service) resolveDefaultStateEvent(ctx context.Context, st storage.DefaultAlarmState) (event.Event, error) {
	master, err := s.events.Get(ctx, st.EventID)
	if err != nil {
		return event.Event{}, err
	}
	if master.RecurrenceRule == "" {
		return master, nil
	}
	if triggerAt, parseErr := time.Parse(time.RFC3339, st.TriggerAt); parseErr == nil {
		synthetic := model.Alarm{
			Action:       st.Action,
			TriggerValue: st.TriggerValue,
			Related:      "START",
		}
		return s.resolveInstanceForTrigger(ctx, master, synthetic, triggerAt), nil
	}
	return master, nil // an unreadable trigger_at falls back to the master
}

// ListPendingDefaultAlarms returns all fired default alarms that are not
// acknowledged.
func (s *Service) ListPendingDefaultAlarms(ctx context.Context) ([]storage.DefaultAlarmState, error) {
	return s.q.ListPendingDefaultAlarmStates(ctx)
}

// DismissDefault acknowledges a fired default alarm so it will not show as
// pending. Returns an error if the state ID does not exist or is already
// dismissed.
func (s *Service) DismissDefault(ctx context.Context, stateID int64) error {
	st, err := s.q.GetDefaultAlarmStateByID(ctx, stateID)
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
	return s.q.AcknowledgeDefaultAlarmState(ctx, storage.AcknowledgeDefaultAlarmStateParams{
		AckedAt: &now,
		ID:      stateID,
	})
}

// ComputeDefaultSnooze calculates the snooze-until time for a fired default
// alarm, capped at event end. It mirrors ComputeSnooze.
func (s *Service) ComputeDefaultSnooze(ctx context.Context, stateID int64, dur time.Duration, now time.Time) (SnoozeResult, error) {
	st, err := s.q.GetDefaultAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return SnoozeResult{}, fmt.Errorf("alarm state %d not found (use 'chroncal alarm list' to see pending alarms)", stateID)
	}
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return SnoozeResult{}, fmt.Errorf("alarm state %d is already dismissed", stateID)
	}

	evt, err := s.resolveDefaultStateEvent(ctx, st)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get event %d: %w", st.EventID, err)
	}
	return computeSnoozeResult(evt, dur, now)
}

// SnoozeDefaultUntilStart snoozes a fired default alarm to fire at the
// event's start time. It mirrors SnoozeUntilStart.
func (s *Service) SnoozeDefaultUntilStart(ctx context.Context, stateID int64, now time.Time) (SnoozeResult, error) {
	st, err := s.q.GetDefaultAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return SnoozeResult{}, fmt.Errorf("alarm state %d not found (use 'chroncal alarm list' to see pending alarms)", stateID)
	}
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return SnoozeResult{}, fmt.Errorf("alarm state %d is already dismissed", stateID)
	}

	evt, err := s.resolveDefaultStateEvent(ctx, st)
	if err != nil {
		return SnoozeResult{}, fmt.Errorf("get event %d: %w", st.EventID, err)
	}
	if now.After(evt.StartTime) {
		return SnoozeResult{}, fmt.Errorf("event %q has already started", evt.Title)
	}
	return SnoozeResult{
		Until:      evt.StartTime,
		EventStart: evt.StartTime,
		EventEnd:   evt.EndTime,
	}, nil
}

// SnoozeDefault reschedules a fired default alarm to fire again at the given
// time.
func (s *Service) SnoozeDefault(ctx context.Context, stateID int64, until time.Time) error {
	st, err := s.q.GetDefaultAlarmStateByID(ctx, stateID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("alarm state %d not found (use 'chroncal alarm list' to see pending alarms)", stateID)
	}
	if err != nil {
		return fmt.Errorf("get alarm state %d: %w", stateID, err)
	}
	if st.AckedAt != nil {
		return fmt.Errorf("alarm state %d is already dismissed", stateID)
	}
	snoozeStr := until.UTC().Format(time.RFC3339)
	return s.q.SnoozeDefaultAlarmState(ctx, storage.SnoozeDefaultAlarmStateParams{
		SnoozedTo: &snoozeStr,
		ID:        stateID,
	})
}

// MarkDefaultRefired re-fires a snoozed default alarm. It clears the snooze.
// The UPDATE is gated on snoozed_to IS NOT NULL so it acts as an atomic
// claim, like MarkRefired.
func (s *Service) MarkDefaultRefired(ctx context.Context, stateID int64) (claimed bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.q.RefireDefaultAlarmState(ctx, storage.RefireDefaultAlarmStateParams{
		FiredAt: &now,
		ID:      stateID,
	})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}
