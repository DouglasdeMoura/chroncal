package sync

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// breakEventAlarms makes the alarms relation fail on every load. The rename
// is deterministic: no retry can fix it, which is exactly the wedge shape
// issue #568 describes.
func breakEventAlarms(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE event_alarms RENAME TO event_alarms_broken`); err != nil {
		t.Fatalf("rename event_alarms: %v", err)
	}
}

// seedWedgedEvent inserts one dirty event resource whose alarms relation
// cannot load.
func seedWedgedEvent(t *testing.T, engine *Engine, db *sql.DB, q *storage.Queries, calendarID int64, uid string) {
	t.Helper()
	ctx := context.Background()
	insertTestEvent(t, db, calendarID, uid)
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource %s: %v", uid, err)
	}
	breakEventAlarms(t, db)
}

func TestExportResourceNamesUnreadableRelations(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	seedWedgedEvent(t, engine, db, q, cals[0].ID, "wedged-export")

	_, err = engine.exportResource(ctx, "event", "wedged-export")
	if err == nil {
		t.Fatal("exportResource succeeded on an unreadable relation")
	}
	for _, want := range []string{"unreadable relation(s) alarms", "stuck", "chroncal sync doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q misses %q", err.Error(), want)
		}
	}
}

// TestDiagnoseAndDoctorPushRecoverWedgedResource covers the full escape
// hatch: the diagnosis lists the wedged resource with its broken relation,
// and the confirmed push converges the calendar while announcing the loss.
func TestDiagnoseAndDoctorPushRecoverWedgedResource(t *testing.T) {
	t.Parallel()

	var putBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			putBody.Store(string(body))
			w.Header().Set("ETag", `"etag-doctor"`)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	credStore := &mockCredStore{creds: make(map[int64]auth.Credential)}
	engine := NewEngine(db, q, credStore,
		calendar.NewService(db, q),
		event.NewService(db, q),
		todo.NewService(db, q),
		journal.NewService(db, q),
		nil)

	account, err := q.CreateAccount(context.Background(), storage.CreateAccountParams{
		Name:      "doc",
		ServerUrl: srv.URL,
		AuthType:  "basic",
		Username:  "user-doc",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	credStore.creds[account.ID] = auth.Credential{AccountID: account.ID, Username: "user-doc", Password: "pw"}

	cal, err := q.CreateCalendar(context.Background(), storage.CreateCalendarParams{Name: "doctor"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := q.LinkCalendarToAccount(context.Background(), storage.LinkCalendarToAccountParams{
		ID:        cal.ID,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable(srv.URL + "/cal/doctor/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	seedWedgedEvent(t, engine, db, q, cal.ID, "wedged-doctor")

	wedged, err := engine.DiagnoseCalendar(context.Background(), cal.ID)
	if err != nil {
		t.Fatalf("DiagnoseCalendar: %v", err)
	}
	if len(wedged) != 1 {
		t.Fatalf("DiagnoseCalendar returned %d entries, want 1", len(wedged))
	}
	if wedged[0].UID != "wedged-doctor" || len(wedged[0].Relations) != 1 || wedged[0].Relations[0] != "alarms" {
		t.Errorf("unexpected diagnosis: %+v", wedged[0])
	}

	dropped, err := engine.DoctorPush(context.Background(), cal.ID, "wedged-doctor")
	if err != nil {
		t.Fatalf("DoctorPush: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "alarms" {
		t.Errorf("dropped = %v, want [alarms]", dropped)
	}

	stillDirty, err := q.ListDirtySyncResources(context.Background(), cal.ID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(stillDirty) != 0 {
		t.Errorf("resource still dirty after DoctorPush: %+v", stillDirty)
	}
	body, _ := putBody.Load().(string)
	if body == "" {
		t.Fatal("the fake server saw no PUT")
	}
	if strings.Contains(body, "BEGIN:VALARM") {
		t.Errorf("the incomplete PUT carried a VALARM anyway:\n%s", body)
	}
}
