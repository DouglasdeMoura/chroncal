package sync

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/hydrate"

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

	dropped, err := engine.DoctorPush(context.Background(), cal.ID, "wedged-doctor", []string{"alarms"})
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

// newDoctorTestEnv builds an engine whose calendar points at srvURL. The
// basic credential is already stored.
func newDoctorTestEnv(t *testing.T, srvURL, name string) (*Engine, *sql.DB, *storage.Queries, storage.Calendar) {
	t.Helper()
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
		Name:      name,
		ServerUrl: srvURL,
		AuthType:  "basic",
		Username:  "user-" + name,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	credStore.creds[account.ID] = auth.Credential{AccountID: account.ID, Username: "user-" + name, Password: "pw"}

	cal, err := q.CreateCalendar(context.Background(), storage.CreateCalendarParams{Name: name})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := q.LinkCalendarToAccount(context.Background(), storage.LinkCalendarToAccountParams{
		ID:        cal.ID,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable(srvURL + "/cal/" + name + "/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	return engine, db, q, cal
}

// TestPushRecordsFailureAndDoctorResetsCounter pins the push-failure
// bookkeeping (issue #646). A push attempt that fails on the unreadable
// relation increments push_fail_count and stores the error. The diagnosis
// reports the count. A successful doctor push resets both columns.
func TestPushRecordsFailureAndDoctorResetsCounter(t *testing.T) {
	t.Parallel()

	var failWrites atomic.Bool
	failWrites.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failWrites.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("ETag", `"etag-doctor"`)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	engine, db, q, cal := newDoctorTestEnv(t, srv.URL, "counter")
	ctx := context.Background()
	seedWedgedEvent(t, engine, db, q, cal.ID, "wedge-count")

	// One push attempt fails on the unreadable relation. The export aborts
	// before any PUT, so the fake client must stay untouched.
	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return nil, nil
	})
	result, err := engine.push(ctx, client, cal.ID, srv.URL, "", ConflictPrompt, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(result.errors) != 1 {
		t.Fatalf("errors = %v, want one export failure", result.errors)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: cal.ID, Uid: "wedge-count"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.PushFailCount != 1 {
		t.Fatalf("PushFailCount = %d, want 1", res.PushFailCount)
	}
	if !strings.Contains(res.LastPushError, "unreadable relation(s)") {
		t.Fatalf("LastPushError = %q, want it to name the relation", res.LastPushError)
	}

	wedged, err := engine.DiagnoseCalendar(ctx, cal.ID)
	if err != nil {
		t.Fatalf("DiagnoseCalendar: %v", err)
	}
	if len(wedged) != 1 || wedged[0].PushFailCount != 1 {
		t.Fatalf("diagnosis = %+v, want one entry with PushFailCount 1", wedged)
	}

	// A successful doctor push resets the bookkeeping.
	failWrites.Store(false)
	if _, err := engine.DoctorPush(ctx, cal.ID, "wedge-count", []string{"alarms"}); err != nil {
		t.Fatalf("DoctorPush: %v", err)
	}
	res, err = q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: cal.ID, Uid: "wedge-count"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.PushFailCount != 0 || res.LastPushError != "" {
		t.Fatalf("bookkeeping after doctor push: count=%d error=%q, want 0 and empty",
			res.PushFailCount, res.LastPushError)
	}
	if res.Dirty != 0 {
		t.Fatalf("Dirty = %d, want 0", res.Dirty)
	}
}

// TestDoctorPushRecoversLostPutResponse mirrors the lost-response recovery
// of the regular push (issue #294) on the doctor path (issue #647). The
// first PUT lands and its response fails transiently. The retry sends the
// stale If-Match and the server answers 412. The doctor adopts the ETag of
// the landed write instead of failing the same way forever.
func TestDoctorPushRecoversLostPutResponse(t *testing.T) {
	t.Parallel()

	var putBody atomic.Value
	var putCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			putBody.Store(string(body))
			putCount.Add(1)
			if putCount.Load() == 1 {
				// The write landed. The response fails transiently, like a
				// reset connection would.
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			// The retry carries the stale pre-PUT If-Match.
			w.WriteHeader(http.StatusPreconditionFailed)
		case http.MethodGet:
			// The server holds exactly the body of the first PUT.
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
			w.Header().Set("Etag", `"etag-after"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(putBody.Load().(string)))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	engine, db, q, cal := newDoctorTestEnv(t, srv.URL, "lostput")
	ctx := context.Background()

	// A previously synced wedged resource: it carries a remote URL and an
	// ETag, so the doctor PUT is conditional.
	insertTestEvent(t, db, cal.ID, "doctor-lost-put")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   cal.ID,
		Uid:          "doctor-lost-put",
		OwnerType:    "event",
		RemoteUrl:    "/cal/lostput/doctor-lost-put.ics",
		Etag:         `"etag-before"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	breakEventAlarms(t, db)

	dropped, err := engine.DoctorPush(ctx, cal.ID, "doctor-lost-put", []string{"alarms"})
	if err != nil {
		t.Fatalf("DoctorPush: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "alarms" {
		t.Fatalf("dropped = %v, want [alarms]", dropped)
	}
	if got := putCount.Load(); got != 2 {
		t.Fatalf("PUT count = %d, want 2 (one lost response, one rejected retry)", got)
	}
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: cal.ID, Uid: "doctor-lost-put"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Etag != "etag-after" {
		t.Fatalf("Etag = %q, want the landed write's etag-after", res.Etag)
	}
	if res.Dirty != 0 {
		t.Fatalf("Dirty = %d, want 0", res.Dirty)
	}
}

// wedgeShaped must reject context-driven failures: a cancelled or timed-out
// context fails every relation load at once, and listing every dirty
// resource as wedged would invite a destructive confirmation on a healthy
// database.
func TestWedgeShapedRejectsContextCauses(t *testing.T) {
	t.Parallel()

	plain := &hydrate.HydrationError{
		Err: errors.New("event 1 alarms: db busy"),
		Failures: []hydrate.RelFailure{
			{Kind: "event", ID: 1, Relation: "alarms", Cause: errors.New("db busy")},
		},
	}
	if !wedgeShaped(plain) {
		t.Error("a non-context hydration failure must stay wedge-shaped")
	}
	if wedgeShaped(errors.New("not a hydration error")) {
		t.Error("a non-hydration error is not a wedge shape")
	}
	if wedgeShaped(nil) {
		t.Error("nil is not a wedge shape")
	}
	cancelled := &hydrate.HydrationError{
		Err: errors.New("event 1 alarms: context canceled"),
		Failures: []hydrate.RelFailure{
			{Kind: "event", ID: 1, Relation: "alarms", Cause: context.Canceled},
		},
	}
	if wedgeShaped(cancelled) {
		t.Error("a cancellation cause must not read as a deterministic wedge")
	}
	mixed := &hydrate.HydrationError{
		Err: errors.Join(errors.New("a"), errors.New("b")),
		Failures: []hydrate.RelFailure{
			{Kind: "event", ID: 1, Relation: "alarms", Cause: context.DeadlineExceeded},
			{Kind: "event", ID: 1, Relation: "alarms", Cause: errors.New("db busy")},
		},
	}
	if wedgeShaped(mixed) {
		t.Error("a cancelled load taints the whole report; the retry must decide, not the classifier")
	}
}

// The user confirms one loss set. A push that would drop a different set
// must fail closed instead of silently covering more than the user accepted.
func TestDoctorPushRefusesChangedRelationSet(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	engine, db, q, cal := newDoctorTestEnv(t, srv.URL, "changed-set")
	ctx := context.Background()
	seedWedgedEvent(t, engine, db, q, cal.ID, "wedged-set")

	dropped, err := engine.DoctorPush(ctx, cal.ID, "wedged-set", []string{"attendees"})
	if err == nil {
		t.Fatalf("DoctorPush succeeded with a mismatched diagnosis, dropped = %v", dropped)
	}
	if !strings.Contains(err.Error(), "changed since diagnosis") {
		t.Errorf("error = %v, want it to name the changed set", err)
	}
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: cal.ID, Uid: "wedged-set"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Errorf("resource left the dirty state after a refused push")
	}
}

// The regular push skips resources with an open conflict because a PUT under
// an open conflict desynchronizes the recorded snapshots (issue #104). The
// doctor must refuse them for the same reason.
func TestDoctorPushRefusesOpenConflict(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	engine, db, q, cal := newDoctorTestEnv(t, srv.URL, "conflicted")
	ctx := context.Background()
	seedWedgedEvent(t, engine, db, q, cal.ID, "wedged-conflict")

	evt, err := q.GetEventByUID(ctx, "wedged-conflict")
	if err != nil {
		t.Fatalf("GetEventByUID: %v", err)
	}
	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: cal.ID,
		OwnerType:  "event",
		OwnerID:    evt.ID,
		Uid:        "wedged-conflict",
		LocalIcal:  "BEGIN:VCALENDAR",
		ServerIcal: "BEGIN:VCALENDAR",
		ServerEtag: `"stale"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	if _, err := engine.DoctorPush(ctx, cal.ID, "wedged-conflict", []string{"alarms"}); err == nil {
		t.Fatal("DoctorPush succeeded under an open sync conflict")
	} else if !strings.Contains(err.Error(), "open sync conflict") {
		t.Errorf("error = %v, want it to name the open conflict", err)
	}
}
