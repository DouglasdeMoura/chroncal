package event

import (
	"context"

	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
)

func TestEventService_CreateUppercasesEnums(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	e, err := svc.Create(ctx, CreateParams{
		CalendarID: 1, Title: "Lowercase Enums",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		Status:    "tentative",
		Transp:    "transparent",
		Class:     "private",
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if e.Status != "TENTATIVE" {
		t.Errorf("Status = %q, want TENTATIVE", e.Status)
	}
	if e.Transp != "TRANSPARENT" {
		t.Errorf("Transp = %q, want TRANSPARENT", e.Transp)
	}
	if e.Class != "PRIVATE" {
		t.Errorf("Class = %q, want PRIVATE", e.Class)
	}
}

func TestEventService_UpdateUppercasesEnums(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createEvent(t, svc)

	updated, err := svc.Update(ctx, created.ID, UpdateParams{
		Title:      created.Title,
		StartTime:  created.StartTime,
		EndTime:    created.EndTime,
		CalendarID: 1,
		Status:     "cancelled",
		Transp:     "transparent",
		Class:      "confidential",
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.Status != "CANCELLED" {
		t.Errorf("Status = %q, want CANCELLED", updated.Status)
	}
	if updated.Transp != "TRANSPARENT" {
		t.Errorf("Transp = %q, want TRANSPARENT", updated.Transp)
	}
	if updated.Class != "CONFIDENTIAL" {
		t.Errorf("Class = %q, want CONFIDENTIAL", updated.Class)
	}
}

func TestEventService_ListAlarmsByEventIDs_Correctness(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create 3 events with varying numbers of alarms
	events := make([]Event, 3)
	alarmsPerEvent := [][]model.Alarm{
		// Event 1: 3 alarms
		{
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 min"},
			{Action: "DISPLAY", TriggerValue: "-PT5M", Description: "5 min"},
			{Action: "DISPLAY", TriggerValue: "PT0H", Description: "at start"},
		},
		// Event 2: 1 alarm
		{
			{Action: "DISPLAY", TriggerValue: "-PT30M", Description: "30 min"},
		},
		// Event 3: no alarms (empty slice)
		{},
	}

	for i, alarms := range alarmsPerEvent {
		e, err := svc.Create(ctx, CreateParams{
			CalendarID: 1,
			Title:      "Event " + string(rune('1'+i)),
			StartTime:  time.Date(2026, 4, 1+i, 14, 0, 0, 0, time.UTC),
			EndTime:    time.Date(2026, 4, 1+i, 15, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("create event %d: %v", i, err)
		}
		events[i] = e
		if len(alarms) > 0 {
			if err := svc.ReplaceAlarms(ctx, e.ID, alarms); err != nil {
				t.Fatalf("replace alarms event %d: %v", i, err)
			}
		}
	}

	// Fetch alarms individually (old N+1 pattern)
	individualMap := make(map[int64][]model.Alarm)
	for _, e := range events {
		alarms, err := svc.ListAlarms(ctx, e.ID)
		if err != nil {
			t.Fatalf("ListAlarms(%d): %v", e.ID, err)
		}
		individualMap[e.ID] = alarms
	}

	// Fetch alarms in batch (new optimized pattern)
	eventIDs := []int64{events[0].ID, events[1].ID, events[2].ID}
	batchMap, err := svc.ListFireableAlarmsByEventIDs(ctx, eventIDs)
	if err != nil {
		t.Fatalf("ListFireableAlarmsByEventIDs: %v", err)
	}

	// Verify results are identical
	for _, e := range events {
		individual := individualMap[e.ID]
		batch := batchMap[e.ID]

		if len(individual) != len(batch) {
			t.Errorf("event %d: individual got %d alarms, batch got %d",
				e.ID, len(individual), len(batch))
			continue
		}

		for i := range individual {
			if individual[i].ID != batch[i].ID {
				t.Errorf("event %d alarm %d: ID = %d (batch), want %d (individual)",
					e.ID, i, batch[i].ID, individual[i].ID)
			}
			if individual[i].Action != batch[i].Action {
				t.Errorf("event %d alarm %d: Action = %q (batch), want %q",
					e.ID, i, batch[i].Action, individual[i].Action)
			}
			if individual[i].TriggerValue != batch[i].TriggerValue {
				t.Errorf("event %d alarm %d: TriggerValue = %q (batch), want %q",
					e.ID, i, batch[i].TriggerValue, individual[i].TriggerValue)
			}
			if individual[i].Description != batch[i].Description {
				t.Errorf("event %d alarm %d: Description = %q (batch), want %q",
					e.ID, i, batch[i].Description, individual[i].Description)
			}
		}
	}

	// Test empty input
	emptyMap, err := svc.ListFireableAlarmsByEventIDs(ctx, []int64{})
	if err != nil {
		t.Errorf("empty eventIDs: got error %v", err)
	}
	if emptyMap != nil {
		t.Errorf("empty eventIDs: got non-nil map %v", emptyMap)
	}

	// Test non-existent event IDs
	nonExistent := []int64{9999, 9998}
	nonExistentMap, err := svc.ListFireableAlarmsByEventIDs(ctx, nonExistent)
	if err != nil {
		t.Errorf("non-existent eventIDs: got error %v", err)
	}
	if len(nonExistentMap) != 0 {
		t.Errorf("non-existent eventIDs: got map with %d entries, want 0", len(nonExistentMap))
	}
}

func TestEventService_ReplaceAlarms_XPropertiesRoundTrip(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{{
		UID: "evt-alarm-xp", Action: "DISPLAY", TriggerValue: "-PT15M",
		XProperties: []model.XProperty{{Name: "X-APPLE-DEFAULT-ALARM", Value: "TRUE"}},
	}})
	if err != nil {
		t.Fatalf("ReplaceAlarms error: %v", err)
	}
	alarms, err := svc.ListAlarms(ctx, e.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("ListAlarms: %v (n=%d)", err, len(alarms))
	}
	if len(alarms[0].XProperties) != 1 || alarms[0].XProperties[0].Name != "X-APPLE-DEFAULT-ALARM" {
		t.Fatalf("XProperties = %+v, want X-APPLE-DEFAULT-ALARM round-tripped", alarms[0].XProperties)
	}

	// nil XProperties (CLI/TUI paths with no X-prop knowledge) keep stored rows.
	err = svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{{
		UID: "evt-alarm-xp", Action: "DISPLAY", TriggerValue: "-PT15M",
	}})
	if err != nil {
		t.Fatalf("ReplaceAlarms (nil xprops) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 1 || len(alarms[0].XProperties) != 1 {
		t.Fatalf("nil XProperties must keep stored rows; got %+v", alarms)
	}

	// Empty non-nil slice (import path after remote cleared them) wipes.
	err = svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{{
		UID: "evt-alarm-xp", Action: "DISPLAY", TriggerValue: "-PT15M",
		XProperties: []model.XProperty{},
	}})
	if err != nil {
		t.Fatalf("ReplaceAlarms (empty xprops) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 1 || len(alarms[0].XProperties) != 0 {
		t.Fatalf("empty XProperties must clear stored rows; got %+v", alarms[0].XProperties)
	}
}

func TestEventService_ReplaceAlarms_ContentMatchRespectsUID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "uid-a", Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms error: %v", err)
	}
	alarms, _ := svc.ListAlarms(ctx, e.ID)
	origID := alarms[0].ID

	// Identical content with a different non-empty UID and no other pair:
	// the third pass of model.PairAlarms gives the row the new UID, so the
	// row and its alarm_state stay (issue #922).
	err = svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "uid-b", Action: "DISPLAY", TriggerValue: "-PT15M"},
	})
	if err != nil {
		t.Fatalf("ReplaceAlarms (uid swap) error: %v", err)
	}
	alarms, _ = svc.ListAlarms(ctx, e.ID)
	if len(alarms) != 1 {
		t.Fatalf("alarms = %d, want 1", len(alarms))
	}
	if alarms[0].ID != origID {
		t.Errorf("row ID = %d, want %d: the row must take the new UID", alarms[0].ID, origID)
	}
	if alarms[0].UID != "uid-b" {
		t.Errorf("UID = %q, want uid-b", alarms[0].UID)
	}
}

func TestEventService_ReplaceAlarms_CrossEventUIDCollision(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e1 := createEvent(t, svc)
	e2 := createEvent(t, svc)

	if err := svc.ReplaceAlarms(ctx, e1.ID, []model.Alarm{
		{UID: "dup-uid", Action: "DISPLAY", TriggerValue: "-PT15M"},
	}); err != nil {
		t.Fatalf("ReplaceAlarms e1: %v", err)
	}

	// Same VALARM UID on a second event (servers duplicate events). An
	// alarm UID is unique in its event only (migration 063, issue #922),
	// so the second event keeps the UID.
	if err := svc.ReplaceAlarms(ctx, e2.ID, []model.Alarm{
		{UID: "dup-uid", Action: "DISPLAY", TriggerValue: "-PT15M"},
	}); err != nil {
		t.Fatalf("ReplaceAlarms e2: %v", err)
	}
	alarms, err := svc.ListAlarms(ctx, e2.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("ListAlarms e2: %v (n=%d)", err, len(alarms))
	}
	if alarms[0].UID != "dup-uid" {
		t.Errorf("e2 alarm UID = %q, want dup-uid", alarms[0].UID)
	}
}

// One event with two alarms of one UID: the second alarm cannot keep the
// UID. A fireable alarm gets a minted UID, and a preserved foreign alarm
// gets no UID (issue #586).
func TestEventService_ReplaceAlarms_DuplicateUIDInOneEvent(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	if err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "dup-uid", Action: "DISPLAY", TriggerValue: "-PT15M"},
		{UID: "dup-uid", Action: "DISPLAY", TriggerValue: "-PT5M"},
		{UID: "dup-uid", Action: "X-APPLE-SOUND", TriggerValue: "-PT1M"},
	}); err != nil {
		t.Fatalf("ReplaceAlarms: %v", err)
	}
	alarms, err := svc.ListAlarms(ctx, e.ID)
	if err != nil || len(alarms) != 3 {
		t.Fatalf("ListAlarms: %v (n=%d)", err, len(alarms))
	}
	uids := map[string]string{}
	for _, a := range alarms {
		uids[a.TriggerValue] = a.UID
	}
	if uids["-PT15M"] != "dup-uid" {
		t.Errorf("first alarm UID = %q, want dup-uid", uids["-PT15M"])
	}
	if got := uids["-PT5M"]; got == "" || got == "dup-uid" {
		t.Errorf("second alarm UID = %q, want a minted UID", got)
	}
	if got := uids["-PT1M"]; got != "" {
		t.Errorf("foreign alarm UID = %q, want no UID", got)
	}
}

// A server can duplicate a resource, so the same foreign VALARM UID
// arrives on two events. Each event keeps the UID that the other client
// authored (issues #586, #922).
func TestEventService_ReplaceAlarms_ForeignUIDOnTwoEventsStays(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e1 := createEvent(t, svc)
	e2 := createEvent(t, svc)

	foreign := model.Alarm{UID: "foreign-dup", Action: "X-APPLE-SOUND", TriggerValue: "-PT15M"}
	if err := svc.ReplaceAlarms(ctx, e1.ID, []model.Alarm{foreign}); err != nil {
		t.Fatalf("ReplaceAlarms e1: %v", err)
	}
	if err := svc.ReplaceAlarms(ctx, e2.ID, []model.Alarm{foreign}); err != nil {
		t.Fatalf("ReplaceAlarms e2: %v", err)
	}

	alarms, err := svc.ListAlarms(ctx, e2.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("ListAlarms e2: %v (n=%d)", err, len(alarms))
	}
	if alarms[0].UID != "foreign-dup" {
		t.Errorf("e2 foreign alarm UID = %q, want foreign-dup", alarms[0].UID)
	}
}

// validateDurationValue must reject a span that carries the end past
// the storable range, and it must accept the same span on a normal
// start (issue #582 round 5).
func TestValidateDurationValue_StorableEnd(t *testing.T) {
	unstorable := time.Date(9999, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := validateDurationValue(unstorable, "P365D"); err == nil {
		t.Error("a span whose end passes the storable range must fail")
	}
	normal := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := validateDurationValue(normal, "P365D"); err != nil {
		t.Errorf("a storable span must pass: %v", err)
	}
	// An empty span and a zero start stay valid: there is nothing to
	// anchor the check on.
	if err := validateDurationValue(normal, ""); err != nil {
		t.Errorf("an empty span must pass: %v", err)
	}
	if err := validateDurationValue(time.Time{}, "P365D"); err != nil {
		t.Errorf("a zero start must skip the storability check: %v", err)
	}
}

// A preserved foreign alarm keeps an empty UID. A minted UID would reach
// the server on the next push, and the client that wrote the VALARM never
// authored that value (issue #586). The rule must hold on the first write
// and on a later write that content-matches the stored row.
func TestEventService_ReplaceAlarms_KeepsForeignAlarmUIDEmpty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	foreign := model.Alarm{Action: "X-APPLE-SOUND", TriggerValue: "-PT5M", Related: "START"}
	fireable := model.Alarm{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START"}

	if err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{foreign, fireable}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	assertAlarmUIDs(t, svc, e.ID)

	// A re-import writes the same content again. syncMatchedAlarm must not
	// backfill a UID onto the preserved row.
	if err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{foreign, fireable}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	assertAlarmUIDs(t, svc, e.ID)
}

// A malformed stored action must not lock the owning event. An older
// release run against a repaired database writes such a row again,
// because migration 045 does not run a second time. The service maps the
// value as it reads the row, so every later write accepts it (issue #607).
func TestListAlarms_NormalizesMalformedStoredAction(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	seedMalformedEventAlarm(t, svc, e.ID)

	alarms, err := svc.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatalf("ListAlarms: %v", err)
	}
	if len(alarms) != 1 {
		t.Fatalf("alarms = %d, want 1", len(alarms))
	}
	if alarms[0].Action != model.UnsupportedAlarmAction {
		t.Errorf("action = %q, want %q", alarms[0].Action, model.UnsupportedAlarmAction)
	}
}

// The TUI edit path loads the stored alarms and writes the whole list
// back. A malformed stored action made that save fail, so no edit of the
// event could land (issue #607).
func TestUpdateWithRelations_SavesWithMalformedStoredAction(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)
	seedMalformedEventAlarm(t, svc, e.ID)

	alarms, err := svc.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatalf("ListAlarms: %v", err)
	}

	// The form carries the loaded alarms back into the save.
	if _, err := svc.UpdateWithRelations(ctx, e.ID, UpdateParams{
		Title:      "A new title",
		StartTime:  e.StartTime,
		EndTime:    e.EndTime,
		CalendarID: e.CalendarID,
	}, nil, alarms); err != nil {
		t.Fatalf("the save must succeed with a malformed stored alarm: %v", err)
	}

	after, err := svc.ListAlarms(ctx, e.ID)
	if err != nil {
		t.Fatalf("ListAlarms after the save: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("alarms after the save = %d, want 1; the row must survive", len(after))
	}
	if after[0].Action != model.UnsupportedAlarmAction {
		t.Errorf("action after the save = %q, want %q", after[0].Action, model.UnsupportedAlarmAction)
	}
}

// An incoming alarm whose UID a stored row holds must not pair by content
// with a stored row that has no UID. The UID backfill would put the UID on
// a second row of the event, and the unique index refuses that.
func TestEventService_ReplaceAlarms_UIDKeepsItsOwnRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	if err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "a", Action: "DISPLAY", TriggerValue: "-PT15M"},
		{Action: "X-APPLE-SOUND", TriggerValue: "-PT5M"},
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	stored, err := svc.ListAlarms(ctx, e.ID)
	if err != nil || len(stored) != 2 {
		t.Fatalf("ListAlarms: %v (n=%d)", err, len(stored))
	}
	if err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{
		{UID: "a", Action: "X-APPLE-SOUND", TriggerValue: "-PT5M"},
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	alarms, err := svc.ListAlarms(ctx, e.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("ListAlarms: %v (n=%d)", err, len(alarms))
	}
	if alarms[0].ID != stored[0].ID || alarms[0].UID != "a" {
		t.Errorf("alarm = row %d UID %q, want row %d UID a", alarms[0].ID, alarms[0].UID, stored[0].ID)
	}
}
