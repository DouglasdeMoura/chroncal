package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestOpportunisticPushConflictKeepsEditAndRecordsRow drives the real
// opportunistic-push seam against a CalDAV stub that answers the PUT with
// 412 (issue #610). The seam must (a) record a sync_conflicts row so
// `chroncal sync conflicts` lists the divergence, (b) keep the local edit
// dirty and unadopted, and (c) print the conflict note on stderr while
// stdout stays clean for JSON callers.
func TestOpportunisticPushConflictKeepsEditAndRecordsRow(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	var eventUID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/calendars/work/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusPreconditionFailed)
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
			w.Header().Set("ETag", `"etag-server"`)
			_, _ = w.Write([]byte("BEGIN:VCALENDAR\r\n" +
				"VERSION:2.0\r\n" +
				"PRODID:-//chroncal//test//EN\r\n" +
				"BEGIN:VEVENT\r\n" +
				"UID:" + eventUID + "\r\n" +
				"DTSTAMP:20260403T120000Z\r\n" +
				"DTSTART:20260403T120000Z\r\n" +
				"DTEND:20260403T130000Z\r\n" +
				"SUMMARY:Server version\r\n" +
				"END:VEVENT\r\n" +
				"END:VCALENDAR\r\n"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()

	cal, err := a.Calendars.Create(ctx, "Work", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__push_conflict_test",
		ServerUrl: srv.URL,
		AuthType:  "bearer",
		Username:  "alice",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := a.Calendars.LinkToAccount(ctx, cal.ID, account.ID, srv.URL+"/calendars/work/"); err != nil {
		t.Fatalf("link calendar: %v", err)
	}

	// Seed the credential through the same store constructor the seam uses,
	// so loadCalendarClient resolves it on every credential backend.
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

	// The write under test. A create through the service marks the resource
	// dirty exactly like `chroncal event add` does.
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: cal.ID,
		Title:      "Local edit",
		StartTime:  start,
		EndTime:    start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("event create: %v", err)
	}
	eventUID = evt.UID

	var out, warn bytes.Buffer
	pushCalendarAfterWrite(a, cal.ID, &out, &warn)

	if !strings.Contains(warn.String(), "1 local change(s) conflicted with the server") {
		t.Errorf("stderr = %q, want the conflict note", warn.String())
	}
	if !strings.Contains(warn.String(), `run "chroncal sync conflicts" to resolve`) {
		t.Errorf("stderr = %q, want the resolve hint", warn.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q; a conflicts-only push must keep stdout clean", out.String())
	}

	conflicts, err := a.Queries.ListSyncConflictsByCalendar(ctx, cal.ID)
	if err != nil {
		t.Fatalf("ListSyncConflictsByCalendar: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("sync conflicts = %d, want 1", len(conflicts))
	}
	if conflicts[0].Uid != evt.UID {
		t.Fatalf("conflict uid = %q, want %q", conflicts[0].Uid, evt.UID)
	}
	if conflicts[0].ServerEtag != "etag-server" {
		t.Fatalf("conflict ServerEtag = %q, want etag-server", conflicts[0].ServerEtag)
	}
	if !strings.Contains(conflicts[0].LocalIcal, "SUMMARY:Local edit") {
		t.Fatalf("conflict LocalIcal missing the kept local summary, got %q", conflicts[0].LocalIcal)
	}

	// The edit survives: the local row keeps the user's title and the
	// resource stays dirty for the next push.
	got, err := a.Events.GetByUID(ctx, evt.UID)
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if got.Title != "Local edit" {
		t.Fatalf("Title = %q, want Local edit (the opportunistic push never adopts the server body)", got.Title)
	}
	sr, err := a.Queries.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: cal.ID, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if sr.Dirty != 1 {
		t.Fatalf("Dirty = %d, want 1 (the edit stays pending)", sr.Dirty)
	}
}
