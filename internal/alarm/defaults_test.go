package alarm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// newDefaultAlarmService returns an alarm service plus its event service,
// with cfg installed. It uses a no-op todo lister like the other tests.
func newDefaultAlarmService(t *testing.T, cfg DefaultAlarmConfig) (*Service, *event.Service) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc, &mockAlarmLister{todoAlarms: map[int64][]model.Alarm{}})
	svc.SetDefaultConfig(cfg)
	return svc, evtSvc
}

// dueTriggers summarizes a Check result as "action:trigger" strings for one
// event, so tests assert the trigger set without times.
func dueTriggers(due []DueAlarm) []string {
	out := make([]string, 0, len(due))
	for _, da := range due {
		out = append(out, da.Alarm.Action+":"+da.Alarm.TriggerValue)
	}
	return out
}

func assertTriggers(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("due triggers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("due triggers = %v, want %v", got, want)
		}
	}
}

// createEventAt creates one non-recurring event without alarms.
func createEventAt(t *testing.T, evtSvc *event.Service, calendarID int64, title string, start time.Time, allDay bool) event.Event {
	t.Helper()
	evt, err := evtSvc.Create(context.Background(), event.CreateParams{
		CalendarID: calendarID,
		Title:      title,
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
		AllDay:     allDay,
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return evt
}

// createTestCalendar adds one calendar and returns its ID.
func createTestCalendar(t *testing.T, q *storage.Queries, name string) int64 {
	t.Helper()
	cal, err := q.CreateCalendar(context.Background(), storage.CreateCalendarParams{Name: name, Color: "#111111"})
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	return cal.ID
}

func TestDefaultAlarm_FiresForEventWithoutAlarms(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{
			{Action: "DISPLAY", TriggerValue: "-PT30M"},
			{Action: "AUDIO", TriggerValue: "-PT15M"},
		},
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "Imported meeting", now.Add(-10*time.Minute), false)

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M", "AUDIO:-PT15M")
	for _, da := range due {
		if !da.IsDefault {
			t.Fatalf("alarm %+v should be marked IsDefault", da.Alarm)
		}
		if da.Alarm.ID != 0 {
			t.Fatalf("default alarm should carry no stored ID, got %d", da.Alarm.ID)
		}
		if da.Event.Title != "Imported meeting" {
			t.Fatalf("unexpected event %q", da.Event.Title)
		}
	}
}

func TestDefaultAlarm_SkipsEventWithAlarms(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Has own alarm", now.Add(-10*time.Minute), false)
	if err := evtSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT5M"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	// Only the event's own alarm is due; the default does not apply.
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT5M")
	if due[0].IsDefault {
		t.Fatal("stored alarm must not be marked IsDefault")
	}
}

func TestDefaultAlarm_SkipsActionNoneSentinel(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Reminder off", now.Add(-10*time.Minute), false)
	// The server wrote the Google ACTION:NONE sentinel: one alarm row with
	// a non-fireable action.
	if err := evtSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{
		{Action: "NONE", TriggerValue: "-PT10M"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due))
}

func TestDefaultAlarm_CalendarOverrideReplacesGlobal(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc, &mockAlarmLister{todoAlarms: map[int64][]model.Alarm{}})
	svc.SetDefaultConfig(DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}},
	})

	other := createTestCalendar(t, q, "Imported")
	empty := ""
	if err := q.UpdateCalendarDefaultAlarms(context.Background(), storage.UpdateCalendarDefaultAlarmsParams{
		DefaultAlarms: &empty,
		ID:            other,
	}); err != nil {
		t.Fatalf("update calendar: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "On default calendar", now.Add(-50*time.Minute), false)
	createEventAt(t, evtSvc, other, "On off calendar", now.Add(-50*time.Minute), false)

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	// Calendar 1 inherits the global -PT15M. The "off" calendar gets none.
	byEvent := map[string]string{}
	for _, da := range due {
		byEvent[da.Event.Title] = da.Alarm.TriggerValue
	}
	if got := byEvent["On default calendar"]; got != "-PT15M" {
		t.Fatalf("default-calendar trigger = %q, want -PT15M", got)
	}
	if _, ok := byEvent["On off calendar"]; ok {
		t.Fatalf("calendar with empty setting must get no default alarms, got %+v", due)
	}
}

func TestDefaultAlarm_CalendarOverrideList(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc, &mockAlarmLister{todoAlarms: map[int64][]model.Alarm{}})
	// No global defaults at all: the per-calendar list works alone.
	other := createTestCalendar(t, q, "Work")
	list := "-PT45M,AUDIO:-PT5M"
	if err := q.UpdateCalendarDefaultAlarms(context.Background(), storage.UpdateCalendarDefaultAlarmsParams{
		DefaultAlarms: &list,
		ID:            other,
	}); err != nil {
		t.Fatalf("update calendar: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, other, "Meeting", now.Add(-50*time.Minute), false)

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT45M", "AUDIO:-PT5M")
}

func TestDefaultAlarm_SkipAllDay(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers:   []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
		SkipAllDay: true,
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "All-day offsite", now.Add(-10*time.Minute), true)

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due))
}

func TestDefaultAlarm_AllDayFiresWhenNotSkipped(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "All-day offsite", now.Add(-10*time.Minute), true)

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M")
}

func TestDefaultAlarm_NoRefireAfterMark(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M")

	stateID, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}
	if stateID == 0 {
		t.Fatal("MarkFired should return a state ID")
	}

	// The claim holds: the next check does not re-fire the same trigger.
	due, _, err = svc.Check(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	assertTriggers(t, dueTriggers(due))
}

func TestDefaultAlarm_SnoozeRefireDismiss(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	stateID, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	// Snooze past the event start and verify the snooze hides the alarm.
	snooze := SnoozeResult{Until: now.Add(30 * time.Minute)}
	if err := svc.SnoozeDefault(ctx, stateID, snooze.Until); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	due, _, err = svc.Check(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("check while snoozed: %v", err)
	}
	assertTriggers(t, dueTriggers(due))

	// Once the snooze expires the alarm re-fires and carries the state ID.
	later := snooze.Until.Add(time.Minute)
	due, _, err = svc.Check(ctx, later)
	if err != nil {
		t.Fatalf("check after snooze: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M")
	if due[0].StateID != stateID {
		t.Fatalf("refired state ID = %d, want %d", due[0].StateID, stateID)
	}
	if !due[0].IsDefault {
		t.Fatal("refired default alarm must keep IsDefault")
	}

	claimed, err := svc.MarkDefaultRefired(ctx, stateID)
	if err != nil || !claimed {
		t.Fatalf("mark refired: claimed=%v err=%v", claimed, err)
	}

	pending, err := svc.ListPendingDefaultAlarms(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].State.ID != stateID || !pending[0].Eligible {
		t.Fatalf("pending = %+v, want the eligible refired state %d", pending, stateID)
	}

	if err := svc.DismissDefault(ctx, stateID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	pending, err = svc.ListPendingDefaultAlarms(ctx)
	if err != nil {
		t.Fatalf("list pending after dismiss: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after dismiss = %+v, want empty", pending)
	}
}

func TestDefaultAlarm_RecurringSeriesSnoozeResolvesInstance(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	start := now.Add(-24*time.Hour - 10*time.Minute)
	if _, err := evtSvc.Create(context.Background(), event.CreateParams{
		CalendarID:     1,
		Title:          "Daily standup",
		StartTime:      start,
		EndTime:        start.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	}); err != nil {
		t.Fatalf("create recurring event: %v", err)
	}

	ctx := context.Background()
	// The second occurrence started 10 minutes ago; its trigger is due.
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M")
	if !due[0].Event.StartTime.Equal(now.Add(-10 * time.Minute)) {
		t.Fatalf("instance start = %v, want %v", due[0].Event.StartTime, now.Add(-10*time.Minute))
	}

	stateID, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	// Snooze to 5 minutes after the occurrence started. The re-fire check
	// must resolve the same occurrence, not the first series entry.
	until := now.Add(-5 * time.Minute)
	if err := svc.SnoozeDefault(ctx, stateID, until); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	due, _, err = svc.Check(ctx, until.Add(time.Minute))
	if err != nil {
		t.Fatalf("check after snooze: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M")
	if !due[0].Event.StartTime.Equal(now.Add(-10 * time.Minute)) {
		t.Fatalf("refired instance start = %v, want %v", due[0].Event.StartTime, now.Add(-10*time.Minute))
	}
}

func TestDefaultAlarm_InvalidCalendarSpecIsSkipped(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc, &mockAlarmLister{todoAlarms: map[int64][]model.Alarm{}})
	svc.SetDefaultConfig(DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}},
	})
	other := createTestCalendar(t, q, "HandEdited")
	broken := "garbage-spec,-PT45M"
	if err := q.UpdateCalendarDefaultAlarms(context.Background(), storage.UpdateCalendarDefaultAlarmsParams{
		DefaultAlarms: &broken,
		ID:            other,
	}); err != nil {
		t.Fatalf("update calendar: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, other, "Meeting", now.Add(-50*time.Minute), false)

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	// The invalid spec is dropped, the valid one still fires.
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT45M")
}

func TestDefaultAlarm_LongLeadExpandsWindow(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-P3D"}},
	})
	now := time.Now().Truncate(time.Second)
	// The event sits beyond the base forward window (48h), so only the
	// configured lead time (-P3D) pulls it into the expansion window. Its
	// trigger fired an hour ago, so the alarm is due.
	createEventAt(t, evtSvc, 1, "Conference", now.Add(71*time.Hour), false)

	due, _, err := svc.Check(context.Background(), now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-P3D")
}

// "alarm missed" reports a default alarm whose trigger went stale without a
// default_alarm_state row. The long lead time must also pull the event into
// the missed window, which the stored triggers alone cannot do.
func TestDefaultAlarm_MissedReportsStaleDefault(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-P4D"}},
	})
	now := time.Now().Truncate(time.Second)
	// The event starts 71 hours out. No stored alarm exists, so only the
	// -P4D default puts it into the missed window. Its trigger passed 25
	// hours ago, which is past the stale threshold.
	createEventAt(t, evtSvc, 1, "Conference", now.Add(71*time.Hour), false)

	ctx := context.Background()
	missedEvents, missedTodos, missedDefaults, err := svc.CheckMissed(ctx, now, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("check missed: %v", err)
	}
	if len(missedEvents) != 0 || len(missedTodos) != 0 {
		t.Fatalf("stored missed = %+v / %+v, want none", missedEvents, missedTodos)
	}
	if len(missedDefaults) != 1 {
		t.Fatalf("missed defaults = %+v, want 1", missedDefaults)
	}
	m := missedDefaults[0]
	if m.EventTitle != "Conference" || m.TriggerValue != "-P4D" || m.Action != "DISPLAY" {
		t.Fatalf("missed default = %+v", m)
	}
	if m.Age <= StaleThreshold {
		t.Fatalf("missed default age = %v, want more than the stale threshold", m.Age)
	}
}

// A default alarm with a default_alarm_state row already fired, so
// "alarm missed" must not report it.
func TestDefaultAlarm_MissedSkipsFiredDefault(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}},
	})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-30*time.Hour), false)

	ctx := context.Background()
	_, _, missedDefaults, err := svc.CheckMissed(ctx, now, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("check missed: %v", err)
	}
	if len(missedDefaults) != 1 {
		t.Fatalf("missed defaults = %+v, want 1", missedDefaults)
	}

	firedAt := now.UTC().Format(time.RFC3339)
	if _, err := svc.q.CreateDefaultAlarmState(ctx, storage.CreateDefaultAlarmStateParams{
		AlarmEventID:      evt.ID,
		AlarmAction:       "DISPLAY",
		AlarmTriggerValue: "-PT15M",
		AlarmTriggerAt:    missedDefaults[0].TriggerAt.UTC().Format(time.RFC3339),
		AlarmFiredAt:      &firedAt,
	}); err != nil {
		t.Fatalf("create default alarm state: %v", err)
	}

	_, _, missedDefaults, err = svc.CheckMissed(ctx, now, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("check missed after firing: %v", err)
	}
	if len(missedDefaults) != 0 {
		t.Fatalf("missed defaults after firing = %+v, want none", missedDefaults)
	}
}

// A default alarm does not apply to an event that carries its own alarm, so
// "alarm missed" must not report one for it.
func TestDefaultAlarm_MissedSkipsEventWithAlarms(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}},
	})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-30*time.Hour), false)
	if err := evtSvc.ReplaceAlarms(context.Background(), evt.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT10M", Related: "START"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}

	_, _, missedDefaults, err := svc.CheckMissed(context.Background(), now, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("check missed: %v", err)
	}
	if len(missedDefaults) != 0 {
		t.Fatalf("missed defaults = %+v, want none for an event with alarms", missedDefaults)
	}
}

// DescribeDefaultAlarms must agree with the check loop, and it must name the
// reason when it does not apply the specs. That reason is the only way a user
// can tell why a configured default alarm stays silent.
func TestDefaultAlarm_DescribeDefaultAlarms(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers:   []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
		SkipAllDay: true,
	})
	now := time.Now().Truncate(time.Second)
	ctx := context.Background()

	// An event without alarms gets the global specs.
	plain := createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(time.Hour), false)
	status, err := svc.DescribeDefaultAlarms(ctx, plain)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !status.Applied || status.Suppressed != DefaultSuppressedNone || status.Source != "global" {
		t.Fatalf("status = %+v, want applied from the global list", status)
	}
	if len(status.Specs) != 1 || status.Specs[0].TriggerValue != "-PT30M" {
		t.Fatalf("specs = %+v", status.Specs)
	}

	// An event with its own alarm does not, and the reason says so.
	withAlarm := createEventAt(t, evtSvc, 1, "Invited meeting", now.Add(time.Hour), false)
	if err := evtSvc.ReplaceAlarms(ctx, withAlarm.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT10M", Related: "START"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}
	status, err = svc.DescribeDefaultAlarms(ctx, withAlarm)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if status.Applied || status.Suppressed != DefaultSuppressedEventHasAlarms {
		t.Fatalf("status = %+v, want event_has_alarms", status)
	}

	// The ACTION:NONE sentinel counts as an alarm row.
	sentinel := createEventAt(t, evtSvc, 1, "No reminder", now.Add(time.Hour), false)
	if err := evtSvc.ReplaceAlarms(ctx, sentinel.ID, []model.Alarm{
		{Action: "NONE", TriggerValue: "-PT10M", Related: "START"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}
	status, err = svc.DescribeDefaultAlarms(ctx, sentinel)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if status.Applied || status.Suppressed != DefaultSuppressedEventHasAlarms {
		t.Fatalf("status = %+v, want event_has_alarms for the sentinel", status)
	}

	// skip_all_day excludes an all-day event.
	allDay := createEventAt(t, evtSvc, 1, "Holiday", now.Add(48*time.Hour), true)
	status, err = svc.DescribeDefaultAlarms(ctx, allDay)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if status.Applied || status.Suppressed != DefaultSuppressedAllDayExcluded {
		t.Fatalf("status = %+v, want all_day_excluded", status)
	}

	// A per-calendar list replaces the global one and reports its source.
	work := createTestCalendar(t, svc.q, "Work")
	workEvt := createEventAt(t, evtSvc, work, "Work meeting", now.Add(time.Hour), false)
	list := "AUDIO:-PT5M"
	if err := svc.q.UpdateCalendarDefaultAlarms(ctx, storage.UpdateCalendarDefaultAlarmsParams{
		DefaultAlarms: &list, ID: work,
	}); err != nil {
		t.Fatalf("set calendar defaults: %v", err)
	}
	status, err = svc.DescribeDefaultAlarms(ctx, workEvt)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !status.Applied || status.Source != "calendar" || len(status.Specs) != 1 ||
		status.Specs[0].Action != "AUDIO" || status.Specs[0].TriggerValue != "-PT5M" {
		t.Fatalf("status = %+v, want the calendar list", status)
	}

	// A calendar that opts out reports calendar_opt_out.
	off := ""
	if err := svc.q.UpdateCalendarDefaultAlarms(ctx, storage.UpdateCalendarDefaultAlarmsParams{
		DefaultAlarms: &off, ID: work,
	}); err != nil {
		t.Fatalf("turn off calendar defaults: %v", err)
	}
	status, err = svc.DescribeDefaultAlarms(ctx, workEvt)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if status.Applied || status.Suppressed != DefaultSuppressedCalendarOptOut || status.Source != "calendar" {
		t.Fatalf("status = %+v, want calendar_opt_out", status)
	}
}

// With no configuration at all, the reason is that nothing is configured.
func TestDefaultAlarm_DescribeReportsNoSpecs(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(time.Hour), false)

	status, err := svc.DescribeDefaultAlarms(context.Background(), evt)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if status.Applied || status.Suppressed != DefaultSuppressedNoSpecs || status.Source != "global" {
		t.Fatalf("status = %+v, want no_specs", status)
	}
}

func TestDefaultAlarm_StateRowsAreLocalOnly(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, err := svc.MarkFired(ctx, due[0]); err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	// The event's stored alarm list must stay empty: the default alarm is
	// never written to event_alarms, so export and sync never see it.
	alarms, err := evtSvc.ListAlarms(ctx, evt.ID)
	if err != nil {
		t.Fatalf("list alarms: %v", err)
	}
	if len(alarms) != 0 {
		t.Fatalf("default alarm leaked into event_alarms: %+v", alarms)
	}
}

func TestDefaultAlarm_SameTriggerDifferentActionsBothFire(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{
			{Action: "DISPLAY", TriggerValue: "-PT15M"},
			{Action: "AUDIO", TriggerValue: "-PT15M"},
		},
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT15M", "AUDIO:-PT15M")

	// Both actions claim their own state row: the shared trigger time must
	// not make the second claim look like a lost race.
	first, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired first: %v", err)
	}
	second, err := svc.MarkFired(ctx, due[1])
	if err != nil {
		t.Fatalf("mark fired second: %v", err)
	}
	if first == second {
		t.Fatalf("both actions claimed state row %d", first)
	}

	due, _, err = svc.Check(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	assertTriggers(t, dueTriggers(due))
}

func TestDefaultAlarm_ClaimRefusesEventThatGainedAnAlarm(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M")

	// A sync pull delivers the organiser's alarm between the check and the
	// claim. The claim's guard must refuse the fire (issue #579 protocol).
	if err := evtSvc.ReplaceAlarms(ctx, evt.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT5M"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}
	if _, err := svc.MarkFired(ctx, due[0]); !errors.Is(err, ErrNotFireable) {
		t.Fatalf("mark fired = %v, want ErrNotFireable", err)
	}
}

func TestDefaultAlarm_OptOutStopsSnoozedRefire(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	evtSvc := event.NewService(db, q)
	svc := NewService(db, q, evtSvc, &mockAlarmLister{todoAlarms: map[int64][]model.Alarm{}})
	cfg := DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	}
	svc.SetDefaultConfig(cfg)

	now := time.Now().Truncate(time.Second)
	other := createTestCalendar(t, q, "Work")
	createEventAt(t, evtSvc, other, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	assertTriggers(t, dueTriggers(due), "DISPLAY:-PT30M")
	stateID, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}
	until := now.Add(30 * time.Minute)
	if err := svc.SnoozeDefault(ctx, stateID, until); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	// The user turns default alarms off for the calendar while the alarm
	// sleeps. The expired snooze must not notify. The row stays in the
	// pending list, marked ineligible, so the user can still dismiss it.
	off := ""
	if err := q.UpdateCalendarDefaultAlarms(ctx, storage.UpdateCalendarDefaultAlarmsParams{
		DefaultAlarms: &off,
		ID:            other,
	}); err != nil {
		t.Fatalf("turn off: %v", err)
	}
	due, _, err = svc.Check(ctx, until.Add(time.Minute))
	if err != nil {
		t.Fatalf("check after opt-out: %v", err)
	}
	assertTriggers(t, dueTriggers(due))
	pending, err := svc.ListPendingDefaultAlarms(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Eligible {
		t.Fatalf("pending after opt-out = %+v, want one ineligible row", pending)
	}

	// The same suppression applies when a pull adds an alarm to the event
	// while the alarm sleeps.
	svc.SetDefaultConfig(cfg) // defaults back on for the calendar path
	list := "-PT30M"
	if err := q.UpdateCalendarDefaultAlarms(ctx, storage.UpdateCalendarDefaultAlarmsParams{
		DefaultAlarms: &list,
		ID:            other,
	}); err != nil {
		t.Fatalf("turn on: %v", err)
	}
	// Eligibility passes again only until an alarm row shows up; add one.
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	var workCalendarID int64
	for _, c := range cals {
		if c.Name == "Work" {
			workCalendarID = c.ID
		}
	}
	evts, err := evtSvc.ListByDateRange(ctx, now.Add(-24*time.Hour), now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range evts {
		if e.CalendarID == workCalendarID {
			if err := evtSvc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
				{Action: "DISPLAY", TriggerValue: "-PT5M"},
			}); err != nil {
				t.Fatalf("replace alarms: %v", err)
			}
		}
	}
	due, _, err = svc.Check(ctx, until.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("check after alarm row: %v", err)
	}
	// The stored alarm the pull added fires, but the snoozed default
	// must not re-fire.
	for _, da := range due {
		if da.IsDefault {
			t.Fatalf("snoozed default re-fired after the event gained an alarm: %+v", da)
		}
	}
}

func TestDefaultAlarm_OptOutStopsRefireWhenSpecRemovedFromConfig(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	stateID, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}
	until := now.Add(30 * time.Minute)
	if err := svc.SnoozeDefault(ctx, stateID, until); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	// The user drops the spec from the global config while the alarm sleeps.
	svc.SetDefaultConfig(DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "AUDIO", TriggerValue: "-PT30M"}},
	})
	due, _, err = svc.Check(ctx, until.Add(time.Minute))
	if err != nil {
		t.Fatalf("check after config change: %v", err)
	}
	// The new AUDIO spec may fire, but the snoozed DISPLAY state must not
	// re-fire: its spec is no longer configured.
	for _, da := range due {
		if da.IsDefault && da.Alarm.Action == "DISPLAY" {
			t.Fatalf("snoozed default re-fired after its spec left the config: %+v", da)
		}
	}
}

// The pending list keeps a default-alarm row after the row stops notifying,
// so the user can still find its ID and dismiss it. A stored alarm behaves
// the same way when a sync pull removes its event_alarms row.
func TestDefaultAlarm_PendingKeepsRowThatStoppedNotifying(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil || len(due) != 1 {
		t.Fatalf("check: %v due=%d", err, len(due))
	}
	stateID, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	pending, err := svc.ListPendingDefaultAlarms(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || !pending[0].Eligible {
		t.Fatalf("pending = %+v, want one eligible row", pending)
	}

	// The user drops the spec from the configuration.
	svc.SetDefaultConfig(DefaultAlarmConfig{})
	pending, err = svc.ListPendingDefaultAlarms(ctx)
	if err != nil {
		t.Fatalf("list pending after config change: %v", err)
	}
	if len(pending) != 1 || pending[0].State.ID != stateID {
		t.Fatalf("pending = %+v, want the state %d to stay listed", pending, stateID)
	}
	if pending[0].Eligible {
		t.Fatal("a row whose spec left the configuration must not be eligible")
	}

	// The row is still dismissable, which is why the list must show it.
	if err := svc.DismissDefault(ctx, stateID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	pending, err = svc.ListPendingDefaultAlarms(ctx)
	if err != nil {
		t.Fatalf("list pending after dismiss: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after dismiss = %+v, want empty", pending)
	}
}

func TestDefaultAlarm_RefireClaimRefusesEventThatGainedAnAlarm(t *testing.T) {
	svc, evtSvc := newDefaultAlarmService(t, DefaultAlarmConfig{
		Triggers: []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT30M"}},
	})
	now := time.Now().Truncate(time.Second)
	evt := createEventAt(t, evtSvc, 1, "Synced meeting", now.Add(-10*time.Minute), false)

	ctx := context.Background()
	due, _, err := svc.Check(ctx, now)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	stateID, err := svc.MarkFired(ctx, due[0])
	if err != nil {
		t.Fatalf("mark fired: %v", err)
	}
	until := now.Add(30 * time.Minute)
	if err := svc.SnoozeDefault(ctx, stateID, until); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	// The eligibility read inside Check passed, then a sync import commits
	// an alarm row before the refire claim runs. The claim's own
	// event_alarms recheck must make the UPDATE a no-op, so the refire
	// reports a lost claim and nothing dispatches.
	if err := evtSvc.ReplaceAlarms(ctx, evt.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT5M"},
	}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}
	claimed, err := svc.MarkDefaultRefired(ctx, stateID)
	if err != nil {
		t.Fatalf("mark default refired: %v", err)
	}
	if claimed {
		t.Fatal("refire claim won after the event gained an alarm row")
	}
}
