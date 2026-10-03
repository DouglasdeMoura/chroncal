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
	candidates, err := s.defaultAlarmCandidates(ctx, expandedEvents, alarmMap, overrides)
	if err != nil {
		return nil, err
	}

	var due []DueAlarm
	for _, cand := range candidates {
		instanceEvent := cand.InstanceEvent()
		for _, spec := range cand.Specs {
			// Default alarms anchor on the event start and do not repeat.
			synthetic := model.Alarm{
				Action:       spec.Action,
				TriggerValue: spec.TriggerValue,
				Related:      "START",
			}
			triggerAt, err := computeTriggerTimeForInstance(cand.Expanded, synthetic)
			if err != nil {
				continue
			}
			if triggerAt.After(now) {
				continue
			}
			if now.Sub(triggerAt) > StaleThreshold {
				continue // same stale rule as stored alarms
			}

			fired, err := s.defaultAlarmFired(ctx, cand.Expanded.ID, spec, triggerAt)
			if err != nil {
				// Transient DB error: abort rather than risk re-firing,
				// like the stored-alarm path does.
				return nil, fmt.Errorf("get default alarm state: %w", err)
			}
			if fired {
				continue // already fired/acknowledged
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

// defaultCandidate is one expanded event that receives default alarms,
// together with the specs that apply to it.
type defaultCandidate struct {
	Expanded recurrence.ExpandedEvent
	Specs    []model.DefaultAlarm
}

// InstanceEvent returns the expanded event with its start and end set to the
// occurrence, which is what a notification and a state row expect.
func (c defaultCandidate) InstanceEvent() event.Event {
	inst := c.Expanded.Event
	inst.StartTime = c.Expanded.InstanceTime
	inst.EndTime = c.Expanded.InstanceTime.Add(c.Expanded.Span())
	return inst
}

// The suppression reasons DescribeDefaultAlarms reports. An empty reason
// means the check loop applies the specs.
const (
	// DefaultSuppressedNone means the specs apply to the event.
	DefaultSuppressedNone = ""
	// DefaultSuppressedNoSpecs means no default alarm is configured at all:
	// the calendar inherits the global list and the global list is empty.
	DefaultSuppressedNoSpecs = "no_specs"
	// DefaultSuppressedCalendarOptOut means the calendar set its own list
	// to the empty value.
	DefaultSuppressedCalendarOptOut = "calendar_opt_out"
	// DefaultSuppressedEventHasAlarms means the event carries at least one
	// alarm row. A sync-only sentinel such as ACTION:NONE counts, because
	// the organiser turned the reminder off.
	DefaultSuppressedEventHasAlarms = "event_has_alarms"
	// DefaultSuppressedAllDayExcluded means the alarms.skip_all_day setting
	// excludes this all-day event.
	DefaultSuppressedAllDayExcluded = "all_day_excluded"
)

// DefaultAlarmStatus reports the default alarms that apply to one event and
// whether the check loop uses them.
type DefaultAlarmStatus struct {
	// Specs is the effective spec list: the calendar setting when the
	// calendar has one, otherwise the global [alarms] default.
	Specs []model.DefaultAlarm
	// Source is "calendar" when the calendar sets its own list, and
	// "global" when the calendar inherits the configuration.
	Source string
	// Applied is true when the check loop fires Specs for this event.
	Applied bool
	// Suppressed is the reason the check loop skips the event. It is empty
	// when Applied is true.
	Suppressed string
}

// DescribeDefaultAlarms explains the default alarms for one event. It answers
// the question a user cannot answer otherwise: why did my default alarm not
// fire for this event? (issue #815). The rules match
// defaultAlarmCandidates, so Applied here means the check loop uses the
// specs.
func (s *Service) DescribeDefaultAlarms(ctx context.Context, evt event.Event) (DefaultAlarmStatus, error) {
	status := DefaultAlarmStatus{Source: "global", Specs: s.defaults.Triggers}

	raw, explicit, err := s.calendarDefaultSetting(ctx, evt.CalendarID)
	if err != nil {
		return DefaultAlarmStatus{}, err
	}
	if explicit {
		status.Source = "calendar"
		status.Specs, _ = model.ParseDefaultAlarmList(raw)
	}

	switch {
	case len(status.Specs) == 0:
		status.Suppressed = DefaultSuppressedNoSpecs
		if status.Source == "calendar" {
			status.Suppressed = DefaultSuppressedCalendarOptOut
		}
	case s.defaults.SkipAllDay && evt.AllDay:
		status.Suppressed = DefaultSuppressedAllDayExcluded
	default:
		rows, err := s.q.ListEventIDsWithAlarms(ctx, []int64{evt.ID})
		if err != nil {
			return DefaultAlarmStatus{}, fmt.Errorf("list events with alarms: %w", err)
		}
		if len(rows) > 0 {
			status.Suppressed = DefaultSuppressedEventHasAlarms
		} else {
			status.Applied = true
		}
	}
	return status, nil
}

// calendarDefaultSetting returns the raw per-calendar spec list and whether
// the calendar has an explicit setting. An explicit empty value means the
// calendar turned default alarms off.
func (s *Service) calendarDefaultSetting(ctx context.Context, calendarID int64) (string, bool, error) {
	cals, err := s.q.ListCalendars(ctx)
	if err != nil {
		return "", false, fmt.Errorf("list calendars: %w", err)
	}
	for _, c := range cals {
		if c.ID != calendarID {
			continue
		}
		if c.DefaultAlarms == nil {
			return "", false, nil
		}
		return *c.DefaultAlarms, true, nil
	}
	return "", false, nil // an unknown calendar inherits the global default
}

// defaultAlarmCandidates returns the expanded events that receive default
// alarms, with the specs that apply to each. It skips an event that carries
// any alarm row, fireable or not, and an event whose calendar has no specs.
// The check loop and the missed scan share it, so both apply the same rules.
func (s *Service) defaultAlarmCandidates(
	ctx context.Context,
	expandedEvents []recurrence.ExpandedEvent,
	alarmMap map[int64][]model.Alarm,
	overrides map[int64][]model.DefaultAlarm,
) ([]defaultCandidate, error) {
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

	var out []defaultCandidate
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
		out = append(out, defaultCandidate{Expanded: expEvt, Specs: specs})
	}
	return out, nil
}

// defaultAlarmFired reports whether a default_alarm_state row exists for the
// event, the spec, and the trigger time. A real DB error is returned so the
// caller does not treat the alarm as unfired.
func (s *Service) defaultAlarmFired(ctx context.Context, eventID int64, spec model.DefaultAlarm, triggerAt time.Time) (bool, error) {
	_, err := s.q.GetDefaultAlarmState(ctx, storage.GetDefaultAlarmStateParams{
		EventID:      eventID,
		Action:       spec.Action,
		TriggerValue: spec.TriggerValue,
		TriggerAt:    triggerAt.UTC().Format(time.RFC3339),
	})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

// MissedDefaultAlarm is a default alarm that never fired because it became
// stale. A default alarm has no event_alarms row, so the report carries the
// configured trigger instead of an alarm ID.
type MissedDefaultAlarm struct {
	EventTitle   string
	Action       string
	TriggerValue string
	TriggerAt    time.Time
	Age          time.Duration
}

// checkMissedDefaultAlarms returns the default alarms that never fired
// because their trigger time passed the stale threshold. It shares
// defaultAlarmCandidates with the check loop, so a reported miss is one the
// check loop would otherwise have fired. A default alarm does not repeat, so
// there is one trigger per spec.
func (s *Service) checkMissedDefaultAlarms(
	ctx context.Context,
	expandedEvents []recurrence.ExpandedEvent,
	alarmMap map[int64][]model.Alarm,
	now time.Time,
	overrides map[int64][]model.DefaultAlarm,
) ([]MissedDefaultAlarm, error) {
	candidates, err := s.defaultAlarmCandidates(ctx, expandedEvents, alarmMap, overrides)
	if err != nil {
		return nil, err
	}

	var missed []MissedDefaultAlarm
	for _, cand := range candidates {
		title := cand.Expanded.Title
		for _, spec := range cand.Specs {
			triggerAt, err := computeTriggerTimeForInstance(cand.Expanded, model.Alarm{
				Action:       spec.Action,
				TriggerValue: spec.TriggerValue,
				Related:      "START",
			})
			if err != nil {
				continue
			}
			if triggerAt.After(now) || now.Sub(triggerAt) <= StaleThreshold {
				continue // not stale yet
			}
			fired, err := s.defaultAlarmFired(ctx, cand.Expanded.ID, spec, triggerAt)
			if err != nil || fired {
				continue // already fired, or a DB error: skip
			}
			missed = append(missed, MissedDefaultAlarm{
				EventTitle:   title,
				Action:       spec.Action,
				TriggerValue: spec.TriggerValue,
				TriggerAt:    triggerAt,
				Age:          now.Sub(triggerAt),
			})
		}
	}
	return missed, nil
}

// listExpiredSnoozedDefaults returns snoozed default alarms whose
// snooze-until time is at or before now. A snoozed default re-fires only
// while it is still eligible (defaultStateEligible): an opt-out between
// fire and snooze expiry must not produce a notification.
func (s *Service) listExpiredSnoozedDefaults(ctx context.Context, now time.Time, overrides map[int64][]model.DefaultAlarm) ([]DueAlarm, error) {
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
		if !s.defaultStateEligible(ctx, st, evt, overrides) {
			continue
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

// defaultStateEligible reports whether an existing default-alarm state row
// may still notify or stay in the pending list. It applies the same rules
// as the synthesis path to the state row's event: the event carries no
// alarm rows (a later pull may have added one), the all-day rule passes,
// and the row's (action, trigger) spec is still configured for the event's
// calendar. A DB error reads as ineligible so a failure never turns into a
// wrongful notification.
func (s *Service) defaultStateEligible(ctx context.Context, st storage.DefaultAlarmState, evt event.Event, overrides map[int64][]model.DefaultAlarm) bool {
	if s.defaults.SkipAllDay && evt.AllDay {
		return false
	}
	found := false
	for _, spec := range s.defaultAlarmsFor(evt.CalendarID, overrides) {
		if spec.Action == st.Action && spec.TriggerValue == st.TriggerValue {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	rows, err := s.q.ListEventIDsWithAlarms(ctx, []int64{st.EventID})
	if err != nil || len(rows) > 0 {
		if err != nil {
			slog.Debug("default-alarm eligibility check failed",
				"event_id", st.EventID, "error", err)
		}
		return false
	}
	return true
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

// PendingDefaultAlarm is one fired default alarm that is not acknowledged.
// Eligible reports whether the row can still notify. The pending list shows
// every row, so Eligible is the only way the caller learns that a reminder
// stopped: a stored alarm stays listed even after a sync pull removes its
// event_alarms row, and the user must still be able to dismiss it.
type PendingDefaultAlarm struct {
	State storage.DefaultAlarmState
	// Event is the event the row fired for. It is the zero value when the
	// event is gone, for example after a soft delete.
	Event event.Event
	// Eligible is false when the event is gone, when the event gained an
	// alarm row, or when the row's spec left the configuration. Such a row
	// never notifies again.
	Eligible bool
}

// ListPendingDefaultAlarms returns every fired default alarm that is not
// acknowledged, oldest trigger first. It does not drop the rows that can no
// longer notify (defaultStateEligible): the user still has to dismiss them,
// and a hidden row gives no way to learn its ID. The notification path reads
// Eligible through listExpiredSnoozedDefaults instead.
func (s *Service) ListPendingDefaultAlarms(ctx context.Context) ([]PendingDefaultAlarm, error) {
	states, err := s.q.ListPendingDefaultAlarmStates(ctx)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, nil
	}
	overrides, err := s.loadCalendarDefaultOverrides(ctx)
	if err != nil {
		return nil, err
	}
	pending := make([]PendingDefaultAlarm, 0, len(states))
	for _, st := range states {
		p := PendingDefaultAlarm{State: st, Eligible: true}
		evt, err := s.events.Get(ctx, st.EventID)
		if err != nil {
			// The event is gone, so the row cannot notify. It stays in the
			// list until the user dismisses it or the purge removes it.
			p.Eligible = false
			pending = append(pending, p)
			continue
		}
		p.Event = evt
		p.Eligible = s.defaultStateEligible(ctx, st, evt, overrides)
		pending = append(pending, p)
	}
	return pending, nil
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
