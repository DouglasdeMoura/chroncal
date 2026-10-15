package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// genericCalDAV is a CalDAV stub that keeps a UID unique per collection
// only. A PUT stores the object, and a DELETE removes it.
type genericCalDAV struct {
	mu      gosync.Mutex
	objects map[string]string
	tag     int
}

func (g *genericCalDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		g.objects[r.URL.Path] = string(body)
		g.tag++
		w.Header().Set("ETag", fmt.Sprintf(`"e%d"`, g.tag))
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		if _, ok := g.objects[r.URL.Path]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(g.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (g *genericCalDAV) paths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.objects))
	for p := range g.objects {
		out = append(out, p)
	}
	return out
}

// TestMoveWritePathPushDeletesSourceObject moves a synced event from Work
// to Personal on a server that keeps a UID unique per collection. The
// write-path push must send the source delete at once. Before the fix the
// Work object stayed on the server until the next full sync, so the event
// showed in both calendars.
func TestMoveWritePathPushDeletesSourceObject(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	stub := &genericCalDAV{objects: map[string]string{}}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "__push_move_test", ServerUrl: srv.URL, AuthType: "bearer", Username: "alice",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	link := func(name, path string) int64 {
		t.Helper()
		cal, err := a.Calendars.Create(ctx, name, "#7C3AED", "")
		if err != nil {
			t.Fatalf("calendar create %s: %v", name, err)
		}
		if err := a.Calendars.LinkToAccount(ctx, cal.ID, account.ID, srv.URL+path); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
		return cal.ID
	}
	workID, personalID := link("MoveWork", "/calendars/work/"), link("MovePersonal", "/calendars/personal/")
	a.AllowPlaintext = true
	credStore, err := auth.NewCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, true)
	if err != nil {
		t.Fatalf("credential store: %v", err)
	}
	if err := credStore.Set(auth.Credential{
		AccountID:          account.ID,
		AccountFingerprint: auth.AccountFingerprint(srv.URL, "bearer", "alice"),
		AccessToken:        "test-token",
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := a.Events.Create(ctx, event.CreateParams{CalendarID: workID, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("event create: %v", err)
	}
	var out, warn bytes.Buffer
	pushCalendarAfterWrite(a, workID, &out, &warn)
	if got := stub.paths(); len(got) != 1 || !strings.HasPrefix(got[0], "/calendars/work/") {
		t.Fatalf("server objects after the first push = %v, want one in work", got)
	}

	moved, err := a.Events.Update(ctx, evt.ID, event.UpdateParams{
		Title: evt.Title, StartTime: evt.StartTime, EndTime: evt.EndTime, CalendarID: personalID,
		Status: evt.Status, Transp: evt.Transp, Class: evt.Class,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&warn)
	opportunisticPushMove(a, workID, moved.CalendarID, cmd)

	got := stub.paths()
	if len(got) != 1 || !strings.HasPrefix(got[0], "/calendars/personal/") {
		t.Fatalf("server objects after the move push = %v, want only one in personal (out %q, warn %q)", got, out.String(), warn.String())
	}
}
