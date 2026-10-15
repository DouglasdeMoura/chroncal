package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// genericCalDAVStub keeps a UID unique per collection only. A PUT stores
// the object, and a DELETE removes it.
type genericCalDAVStub struct {
	mu      gosync.Mutex
	objects map[string]bool
	tag     int
}

func (g *genericCalDAVStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		_, _ = io.ReadAll(r.Body)
		g.objects[r.URL.Path] = true
		g.tag++
		w.Header().Set("ETag", fmt.Sprintf(`"e%d"`, g.tag))
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		if !g.objects[r.URL.Path] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(g.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (g *genericCalDAVStub) paths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.objects))
	for p := range g.objects {
		out = append(out, p)
	}
	return out
}

// runPushCmd runs a push command and every command of a batch it returns.
func runPushCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	require.NotNil(t, cmd)
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
}

// TestEditMovePushesSourceCalendar moves a synced event from Work to
// Personal in the TUI on a server that keeps a UID unique per collection.
// The update message must carry the source calendar, and the push after
// the edit must delete the Work object at once. Before the fix only
// Personal was pushed, and the Work copy stayed until the next full sync.
func TestEditMovePushesSourceCalendar(t *testing.T) {
	t.Setenv("CHRONCAL_SECURITY_DISABLE_KEYRING", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stub := &genericCalDAVStub{objects: map[string]bool{}}
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
	runPushCmd(t, m.runOpportunisticPush(work))
	require.Len(t, stub.paths(), 1)

	// Move the event with the edit form, as the user does.
	m, next := step(t, m, EventEditMsg{Event: evt})
	m, _ = step(t, m, next)
	require.True(t, m.formOpen)
	for i, c := range m.form.calendars {
		if c.ID == personal {
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
	require.Equal(t, personal, msg.calendarID)
	require.Equal(t, work, msg.fromCalendarID, "the update message must carry the source calendar")

	runPushCmd(t, m.runMovePush(msg.fromCalendarID, msg.calendarID))
	got := stub.paths()
	require.Len(t, got, 1, "server objects = %v", got)
	require.True(t, strings.HasPrefix(got[0], "/calendars/personal/"), "server objects = %v", got)
}
