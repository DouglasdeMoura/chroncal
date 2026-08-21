package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// This file holds the database-backed TUI harness. Hand-built models cannot
// see a write that goes through a domain service, so the harness mounts the
// root model on a real app.App over a throwaway database. A test then drives
// real messages through Model.Update, executes each returned tea.Cmd, and
// asserts on the stored rows afterwards.
//
// Regression context (issue #601): EventEditMsg once fetched the event and
// attendees but not the alarms. The save then wrote an empty alarm list over
// the stored rows, and deleteUnmatchedAlarms removed every VALARM, including
// a preserved sync-only one. No hand-built-model test could catch that.

const (
	harnessWidth  = 120
	harnessHeight = 40
)

// newDBBackedModel builds the root TUI model on a fresh database through
// app.New. That is the same constructor cmd/chroncal uses, so the services
// behind the model match production wiring. XDG paths point at temp dirs, so
// the UI-state store stays out of the developer machine.
func newDBBackedModel(t *testing.T) (Model, *app.App) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	a, err := app.New(filepath.Join(t.TempDir(), "chroncal.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })

	m := NewModel(a, "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: harnessWidth, Height: harnessHeight})
	m = updated.(Model)

	// Init loads calendars on startup. The edit form needs them, otherwise
	// submit fails with "No calendars available".
	cmd := m.loadCalendars()
	require.NotNil(t, cmd)
	m, _ = step(t, m, cmd())
	require.NotEmpty(t, m.calendars)
	return m, a
}

// step runs one message through Model.Update and executes the returned
// command. A plain result goes back to the caller, so a test can assert
// each hop of a message chain. A tea.BatchMsg result holds no single
// message, so step drains the batch through drainBatch. It returns the
// last message that is not a batch, or nil when no message results.
func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Msg) {
	t.Helper()
	updated, cmd := m.Update(msg)
	m = updated.(Model)
	if cmd == nil {
		return m, nil
	}
	result := cmd()
	if batch, ok := result.(tea.BatchMsg); ok {
		return drainBatch(t, m, batch)
	}
	return m, result
}

// drainBatch executes every command that a tea.BatchMsg holds. Each
// produced message goes back through Model.Update, and the commands that
// Update returns join the queue until no command remains. A command that
// returns another batch unwraps into the same queue. It returns the last
// message that is not a batch, or nil when the batch gave none.
func drainBatch(t *testing.T, m Model, batch tea.BatchMsg) (Model, tea.Msg) {
	t.Helper()
	var last tea.Msg
	queue := append([]tea.Cmd(nil), batch...)
	for len(queue) > 0 {
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		result := cmd()
		if child, ok := result.(tea.BatchMsg); ok {
			queue = append(queue, child...)
			continue
		}
		if result == nil {
			continue
		}
		last = result
		updated, next := m.Update(result)
		m = updated.(Model)
		if next != nil {
			queue = append(queue, next)
		}
	}
	return m, last
}

// submitOpenForm saves the mounted form the way the user does. Ctrl+S fires
// the form submit, the form answers with eventFormSubmitNowMsg, and that
// message makes the form build EventFormSaveMsg against live state. Both
// hops are strict. A save that skips the deferred submit fails here, so the
// test rejects a stale-receiver save.
func submitOpenForm(t *testing.T, m Model) (Model, EventFormSaveMsg) {
	t.Helper()
	m, next := step(t, m, keyPressMsg("ctrl+s"))
	require.NotNil(t, next)
	require.IsType(t, eventFormSubmitNowMsg{}, next,
		"ctrl+s must emit eventFormSubmitNowMsg, got %T", next)
	m, next = step(t, m, next)
	require.NotNil(t, next)
	require.IsType(t, EventFormSaveMsg{}, next,
		"the deferred submit must emit EventFormSaveMsg, got %T", next)
	return m, next.(EventFormSaveMsg)
}

// findAlarm returns the alarm with the given action and trigger.
func findAlarm(t *testing.T, alarms []model.Alarm, action, trigger string) model.Alarm {
	t.Helper()
	for _, a := range alarms {
		if a.Action == action && a.TriggerValue == trigger {
			return a
		}
	}
	t.Fatalf("alarm %s %s is missing; alarms = %+v", action, trigger, alarms)
	return model.Alarm{}
}

// TestHarness_EditSave_PreservesStoredAlarms guards issue #601. The user
// edits only the title of an event that carries two alarms. One alarm is a
// normal DISPLAY alarm. The other is shaped like a preserved sync-only
// VALARM (action NONE, the sentinel migration 044 admits). The edit loader
// must carry both rows into the form, and the save must write both rows
// back unchanged instead of an empty list.
func TestHarness_EditSave_PreservesStoredAlarms(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()

	seeded, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Synced meeting",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	want := []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "15 minute warning"},
		{Action: "NONE", TriggerValue: "-PT5M"},
	}
	require.NoError(t, a.Events.ReplaceAlarms(ctx, seeded.ID, want))

	// Open the edit form the way the event dialog's Edit action does.
	m, next := step(t, m, EventEditMsg{Event: seeded})
	require.IsType(t, eventEditLoadedMsg{}, next)
	m, _ = step(t, m, next)
	require.True(t, m.formOpen, "the edit form did not open")
	require.Len(t, m.form.alarms, 2,
		"the edit loader must carry the stored alarms into the form")

	// Edit only the title, then save.
	m.form.titleField.SetValue("Renamed meeting")
	m, save := submitOpenForm(t, m)
	require.Equal(t, "Renamed meeting", save.Title)
	require.Len(t, save.Alarms, 2, "the save message dropped a stored alarm")

	m, next = step(t, m, save)
	updated, ok := next.(eventUpdatedMsg)
	require.True(t, ok, "expected eventUpdatedMsg, got %T", next)
	require.NoError(t, updated.err)
	m, _ = step(t, m, next)

	fresh, err := a.Events.Get(ctx, seeded.ID)
	require.NoError(t, err)
	require.Equal(t, "Renamed meeting", fresh.Title)

	stored, err := a.Events.ListAlarms(ctx, seeded.ID)
	require.NoError(t, err)
	require.Len(t, stored, 2, "a save that touches only the title must keep both alarm rows")
	display := findAlarm(t, stored, "DISPLAY", "-PT15M")
	require.Equal(t, "15 minute warning", display.Description)
	findAlarm(t, stored, "NONE", "-PT5M")
}

// TestHarness_CreateSave_WritesAlarmRows checks the create path end to end.
// app.go calls ReplaceAlarms after Create only when the save message carries
// alarms, so the created row must hold the alarm the user added in the form.
func TestHarness_CreateSave_WritesAlarmRows(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	day := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	m, _ = step(t, m, EventCreateMsg{Day: day})
	require.True(t, m.formOpen, "the create form did not open")
	require.Zero(t, m.form.editID)

	// Pin the form timezone to UTC. The form fills the start time from the
	// machine wall clock, and save reads that time in the form timezone.
	// A machine outside UTC shifts the start instant out of the fixed
	// window below. UTC keeps the start inside the window on every machine.
	m.form.timezoneField.SetValue("UTC")

	m.form.titleField.SetValue("Planned outage")
	m.form.alarms = []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT10M"}}

	m, save := submitOpenForm(t, m)
	require.Equal(t, "Planned outage", save.Title)
	require.Len(t, save.Alarms, 1)

	m, next := step(t, m, save)
	created, ok := next.(eventCreatedMsg)
	require.True(t, ok, "expected eventCreatedMsg, got %T", next)
	require.NoError(t, created.err)
	m, _ = step(t, m, next)

	events, err := a.Events.ListByDateRange(ctx, day, day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Len(t, events, 1)

	stored, err := a.Events.ListAlarms(ctx, events[0].ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, "DISPLAY", stored[0].Action)
	require.Equal(t, "-PT10M", stored[0].TriggerValue)
}
