package alarm

import (
	"context"
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
	if len(pending) != 1 || pending[0].ID != stateID {
		t.Fatalf("pending = %+v, want the refired state %d", pending, stateID)
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
