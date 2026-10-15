package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// runPush runs one opportunistic push command.
func runPush(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	require.NotNil(t, cmd)
	cmd()
}

// tuiMoveFixture is a DB-backed model with two calendars of one account on
// a generic CalDAV stub, and one event in Work that the stub holds.
type tuiMoveFixture struct {
	m              Model
	a              *app.App
	stub           *testutil.GenericCalDAV
	work, personal int64
	evt            event.Event
}

func newTUIMoveFixture(t *testing.T) tuiMoveFixture {
	t.Helper()
	t.Setenv("CHRONCAL_SECURITY_DISABLE_KEYRING", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stub := testutil.NewGenericCalDAV()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	m, a := newDBBackedModel(t)
	ctx := context.Background()
	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "move-push", ServerUrl: srv.URL, AuthType: "bearer", Username: "alice",
	})
	require.NoError(t, err)
	link := func(name, path string) int64 {
		cal, err := a.Calendars.Create(ctx, name, "#7C3AED", "")
		require.NoError(t, err)
		require.NoError(t, a.Calendars.LinkToAccount(ctx, cal.ID, account.ID, srv.URL+path))
		return cal.ID
	}
	work, personal := link("MoveWork", "/calendars/work/"), link("MovePersonal", "/calendars/personal/")
	m.plaintextOptedIn = true
	store, err := m.openCredentialStore()
	require.NoError(t, err)
	require.NoError(t, store.Set(auth.Credential{
		AccountID:          account.ID,
		AccountFingerprint: auth.AccountFingerprint(srv.URL, "bearer", "alice"),
		AccessToken:        "test-token",
	}))
	m, _ = step(t, m, m.loadCalendars()())

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := a.Events.Create(ctx, event.CreateParams{CalendarID: work, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour)})
	require.NoError(t, err)
	runPush(t, m.runOpportunisticPush(work))
	require.Len(t, stub.Paths(), 1)
	return tuiMoveFixture{m: m, a: a, stub: stub, work: work, personal: personal, evt: evt}
}

// moveWithForm moves the event to Personal with the edit form, as the
// user does, and returns the update message.
func (f tuiMoveFixture) moveWithForm(t *testing.T) (Model, eventUpdatedMsg) {
	t.Helper()
	m, next := step(t, f.m, EventEditMsg{Event: f.evt})
	m, _ = step(t, m, next)
	require.True(t, m.formOpen)
	for i, c := range m.form.calendars {
		if c.ID == f.personal {
			m.form.calendarField.SetSelected(i)
		}
	}
	m, save := submitOpenForm(t, m)
	updated, cmd := m.Update(save)
	m = updated.(Model)
	require.NotNil(t, cmd)
	msg, ok := cmd().(eventUpdatedMsg)
	require.True(t, ok, "expected eventUpdatedMsg")
	require.NoError(t, msg.err)
	return m, msg
}

// TestEditMovePushesSourceCalendar moves a synced event from Work to
// Personal in the TUI on a server that keeps a UID unique per collection.
// The update message must carry the source calendar, and the push after
// the edit must delete the Work object at once. Before the fix only
// Personal was pushed, and the Work copy stayed until the next full sync.
func TestEditMovePushesSourceCalendar(t *testing.T) {
	f := newTUIMoveFixture(t)
	m, msg := f.moveWithForm(t)
	require.Equal(t, f.personal, msg.calendarID)
	require.Equal(t, f.work, msg.fromCalendarID, "the update message must carry the source calendar")
	require.Equal(t, f.evt.UID, msg.uid)

	runPush(t, m.runMovePush(msg.fromCalendarID, msg.calendarID, msg.uid))
	got := f.stub.Paths()
	require.Len(t, got, 1, "server objects = %v", got)
	require.True(t, strings.HasPrefix(got[0], "/calendars/personal/"), "server objects = %v", got)
}

// TestEditMovePushKeepsSourceWhenTargetFails moves a synced event, and the
// PUT to the target calendar fails. The source push must not run: the
// source object is then the only server copy. Its tombstone stays, so a
// later push completes the move.
func TestEditMovePushKeepsSourceWhenTargetFails(t *testing.T) {
	f := newTUIMoveFixture(t)
	// 507 is not transient, so the push does not wait for a retry backoff.
	f.stub.SetPutStatus("/calendars/personal/", http.StatusInsufficientStorage)
	m, msg := f.moveWithForm(t)

	runPush(t, m.runMovePush(msg.fromCalendarID, msg.calendarID, msg.uid))
	got := f.stub.Paths()
	require.Len(t, got, 1, "server objects = %v", got)
	require.True(t, strings.HasPrefix(got[0], "/calendars/work/"), "server objects = %v", got)
	ts, err := f.a.Queries.ListTombstonesByCalendar(context.Background(), f.work)
	require.NoError(t, err)
	require.Len(t, ts, 1, "the source tombstone must stay")
}

// openOccurrenceEdit opens the edit form of the second occurrence of a
// daily series in Work and selects Personal in the calendar picker.
func (f tuiMoveFixture) openOccurrenceEdit(t *testing.T) Model {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	series, err := f.a.Events.Create(ctx, event.CreateParams{
		CalendarID: f.work, Title: "Standup", StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	require.NoError(t, err)
	occurrence := series
	occurrence.StartTime = start.Add(24 * time.Hour)
	occurrence.EndTime = occurrence.StartTime.Add(time.Hour)
	m, next := step(t, f.m, EventEditMsg{Event: occurrence})
	m, _ = step(t, m, next)
	require.True(t, m.formOpen)
	for i, c := range m.form.calendars {
		if c.ID == f.personal {
			m.form.calendarField.SetSelected(i)
		}
	}
	return m
}

// TestOccurrenceEditWithNewCalendarOffersNoThisEventOnly changes the
// calendar in the edit form of one occurrence. One occurrence cannot move
// alone, so the scope prompt must not offer "This event".
func TestOccurrenceEditWithNewCalendarOffersNoThisEventOnly(t *testing.T) {
	f := newTUIMoveFixture(t)
	m := f.openOccurrenceEdit(t)
	m, save := submitOpenForm(t, m)
	updated, _ := m.Update(save)
	m = updated.(Model)
	require.True(t, m.choiceOpen, "the scope prompt did not open")
	view := stripANSI(m.choiceDialog.View())
	require.NotContains(t, view, "This event")
	require.Contains(t, view, "This and following")
	require.Contains(t, view, "All events")
	require.Equal(t, 2, m.choiceDialog.choices)
}

// TestOccurrenceMoveRefusalKeepsTheForm feeds a refused occurrence move
// back to the model. The form must open again with the error, so the
// edits of the user are not lost.
func TestOccurrenceMoveRefusalKeepsTheForm(t *testing.T) {
	f := newTUIMoveFixture(t)
	m := f.openOccurrenceEdit(t)
	m, save := submitOpenForm(t, m)
	updated, _ := m.Update(save)
	m = updated.(Model)
	updated, _ = m.Update(eventUpdatedMsg{calendarID: f.personal, err: event.ErrInstanceCalendarMove, scoped: true})
	m = updated.(Model)
	require.True(t, m.formOpen, "the form closed after a refused move")
	require.Contains(t, stripANSI(m.form.View()), "one occurrence cannot move")
}

// TestMoveConflictRefusalKeepsTheForm feeds a move refusal for an open sync
// conflict back to the model, for an edit by row and for a scope edit.
// Each move refusal must open the form again with the error, so the edits
// of the user are not lost.
func TestMoveConflictRefusalKeepsTheForm(t *testing.T) {
	f := newTUIMoveFixture(t)
	for _, msg := range []tea.Msg{
		eventUpdatedMsg{calendarID: f.personal, err: fmt.Errorf("record calendar move: move u: %w", storage.ErrMoveOpenConflict)},
		eventUpdatedMsg{calendarID: f.personal, err: event.ErrOccurrenceOutsideSeries, scoped: true},
	} {
		m := f.openOccurrenceEdit(t)
		m, save := submitOpenForm(t, m)
		updated, _ := m.Update(save)
		m = updated.(Model)
		updated, _ = m.Update(msg)
		m = updated.(Model)
		require.True(t, m.formOpen, "the form closed after %T", msg)
	}
}

// TestOccurrenceMoveScopePromptFocusesCancel changes the calendar of one
// occurrence. The scope prompt then offers "This and following" first,
// which splits and moves the series. A quick Enter must cancel, not run
// that choice.
func TestOccurrenceMoveScopePromptFocusesCancel(t *testing.T) {
	f := newTUIMoveFixture(t)
	m := f.openOccurrenceEdit(t)
	m, save := submitOpenForm(t, m)
	updated, _ := m.Update(save)
	m = updated.(Model)
	require.True(t, m.choiceOpen)

	_, cmd := m.Update(keyPressMsg("enter"))
	require.NotNil(t, cmd)
	result, ok := cmd().(ChoiceDialogResultMsg)
	require.True(t, ok, "enter at the scope prompt gave no choice result")
	require.Equal(t, -1, result.Choice, "enter must cancel the scope prompt")
}
