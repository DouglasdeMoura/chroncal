package sync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// icloudFakeServer emulates the iCloud rule that a UID is unique in the
// whole account. A PUT of a UID that another collection holds gets 412,
// and a GET of the new href then gets 404 (issue #828).
type icloudFakeServer struct {
	srv *httptest.Server

	mu      gosync.Mutex
	objects map[string]string // path -> body
	etags   map[string]string // path -> ETag
	nextTag int
	// failDelete makes every DELETE answer 403 while it is true. A 403 is
	// not transient, so the test does not wait for a retry backoff.
	failDelete bool
	// generic turns off the account-wide UID rule. The server then acts
	// like a server that keeps a UID unique per collection only.
	generic bool
	// putStatus maps a collection prefix to a forced PUT status.
	putStatus map[string]int
	deletes   int
	puts      int
}

var icloudUIDPattern = regexp.MustCompile(`(?m)^UID:(.+?)\r?$`)

func newICloudFakeServer(t *testing.T) *icloudFakeServer {
	t.Helper()
	f := &icloudFakeServer{objects: map[string]string{}, etags: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *icloudFakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case "PROPFIND":
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:ic="http://apple.com/ns/ical/">
  <d:response>
    <d:href>` + r.URL.Path + `</d:href>
    <d:propstat>
      <d:prop><ic:calendar-color></ic:calendar-color></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
	case "REPORT":
		// Empty incremental sync-collection: the pull imports nothing.
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:sync-token>tok-next</d:sync-token>
</d:multistatus>`))
	case "PUT":
		f.puts++
		body, _ := io.ReadAll(r.Body)
		m := icloudUIDPattern.FindStringSubmatch(string(body))
		if m == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		uid := m[1]
		for prefix, code := range f.putStatus {
			if strings.HasPrefix(r.URL.Path, prefix) {
				w.WriteHeader(code)
				return
			}
		}
		for p, b := range f.objects {
			if f.generic {
				break
			}
			if p == r.URL.Path {
				continue
			}
			if om := icloudUIDPattern.FindStringSubmatch(b); om != nil && om[1] == uid {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}
		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
			if cur, ok := f.etags[r.URL.Path]; !ok || cur != ifMatch {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}
		f.nextTag++
		etag := fmt.Sprintf(`"e%d"`, f.nextTag)
		f.objects[r.URL.Path] = string(body)
		f.etags[r.URL.Path] = etag
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusCreated)
	case "GET":
		body, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/calendar")
		w.Header().Set("ETag", f.etags[r.URL.Path])
		_, _ = w.Write([]byte(body))
	case "DELETE":
		f.deletes++
		if f.failDelete {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if _, ok := f.objects[r.URL.Path]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != f.etags[r.URL.Path] {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		delete(f.objects, r.URL.Path)
		delete(f.etags, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *icloudFakeServer) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for p := range f.objects {
		out = append(out, p)
	}
	return out
}

func (f *icloudFakeServer) setFailDelete(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failDelete = v
}

func (f *icloudFakeServer) setGeneric(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generic = v
}

func (f *icloudFakeServer) setPutStatus(prefix string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putStatus == nil {
		f.putStatus = map[string]int{}
	}
	f.putStatus[prefix] = code
}

// editRemote gives the object at path a new ETag, as a remote edit does.
func (f *icloudFakeServer) editRemote(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTag++
	f.etags[path] = fmt.Sprintf(`"e%d"`, f.nextTag)
}

func (f *icloudFakeServer) has(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[path]
	return ok
}

func (f *icloudFakeServer) counts() (puts, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts, f.deletes
}

// seedICloudPair creates one account on srv with two linked calendars.
func seedICloudPair(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer) (calA, calB int64) {
	t.Helper()
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "icloud",
		ServerUrl: srv.srv.URL,
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	engine.credStore.(*mockCredStore).creds[account.ID] = auth.Credential{AccountID: account.ID, Username: "user", Password: "pw"}
	link := func(name string) int64 {
		cal, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
		if err != nil {
			t.Fatalf("CreateCalendar %s: %v", name, err)
		}
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID:        cal.ID,
			AccountID: &account.ID,
			RemoteUrl: storage.StringToNullable("/cal/" + name + "/"),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount %s: %v", name, err)
		}
		if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET sync_token = 'tok0' WHERE id = ?`, cal.ID); err != nil {
			t.Fatalf("seed sync token %s: %v", name, err)
		}
		return cal.ID
	}
	return link("a"), link("b")
}

// newICloudPairEngine returns a test engine with one iCloud-like account
// and its two linked calendars.
func newICloudPairEngine(t *testing.T) (engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA, calB int64) {
	t.Helper()
	engine, _, q = newTestEngine(t)
	srv = newICloudFakeServer(t)
	calA, calB = seedICloudPair(t, engine, q, srv)
	return engine, q, srv, calA, calB
}

// createPushedEvent creates one event on calID and pushes it to the server.
func createPushedEvent(t *testing.T, engine *Engine, calID int64) event.Event {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{
		CalendarID: calID,
		Title:      "Dentist",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calID)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("initial push = %+v, %v; want one clean push", res, err)
	}
	return evt
}

func moveEvent(t *testing.T, engine *Engine, evt event.Event, toCal int64) {
	t.Helper()
	if _, err := engine.events.Update(context.Background(), evt.ID, event.UpdateParams{
		Title:      evt.Title,
		StartTime:  evt.StartTime,
		EndTime:    evt.EndTime,
		CalendarID: toCal,
		Status:     evt.Status,
		Transp:     evt.Transp,
		Class:      evt.Class,
	}); err != nil {
		t.Fatalf("Update (move): %v", err)
	}
}

// TestMoveEventBetweenCalendarsOfOneICloudAccount moves an event from
// calendar A to calendar B of the same account. The target calendar syncs
// first, which is the worst order: the create in B must not get 412 while
// the source object in A still exists (issue #828).
func TestMoveEventBetweenCalendarsOfOneICloudAccount(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts on B = %v, %v; want none", conflicts, err)
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource B: %v", err)
	}
	if srB.Dirty != 0 || srB.RemoteUrl == "" {
		t.Fatalf("sync resource B = %+v, want clean with a remote href", srB)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatal("sync resource A still exists after the move")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestMoveEventKeepsTargetDirtyWhenSourceDeleteFails proves that the local
// copy stays the truth. When the source DELETE fails, the push sends no
// second PUT to the target, records no conflict, and keeps the target
// dirty. The next sync then completes the move.
func TestMoveEventKeepsTargetDirtyWhenSourceDeleteFails(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	srv.setFailDelete(true)
	putsBefore, _ := srv.counts()
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil {
		t.Fatalf("PushLocalEdits B: %v", err)
	}
	if len(res.Errors) == 0 || res.Pushed != 0 || res.Conflicts != 0 {
		t.Fatalf("push with failed source DELETE = %+v, want one error and no push", res)
	}
	if puts, _ := srv.counts(); puts != putsBefore+1 {
		t.Fatalf("PUTs = %d, want %d: no PUT retry after a failed source DELETE", puts, putsBefore+1)
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || srB.Dirty != 1 {
		t.Fatalf("sync resource B = %+v, %v; want dirty", srB, err)
	}
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts on B = %v, %v; want none", conflicts, err)
	}

	srv.setFailDelete(false)
	res, err = engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("retry push = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestMoveEventBackBeforeSyncKeepsSource moves an event to B and back to A
// before any sync. The server object in A must survive, and B must get no
// copy.
func TestMoveEventBackBeforeSyncKeepsSource(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, moved, calA)

	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
}

// TestMoveEventKeepsSourceWhenTargetPutFailsOnGenericServer moves an event
// on a server that keeps a UID unique per collection only. The create PUT
// in B fails with 507. The source object in A must stay on the server, and
// the tombstone in A must stay for the normal tombstone pass.
func TestMoveEventKeepsSourceWhenTargetPutFailsOnGenericServer(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	srv.setPutStatus("/cal/b/", http.StatusInsufficientStorage)
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil {
		t.Fatalf("PushLocalEdits B: %v", err)
	}
	if len(res.Errors) == 0 || res.Pushed != 0 {
		t.Fatalf("push with failed PUT = %+v, want one error and no push", res)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone after a failed target PUT")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || srB.Dirty != 1 {
		t.Fatalf("sync resource B = %+v, %v; want dirty", srB, err)
	}
}

// TestMoveEventWaitsWhenSourceChangedOnServer moves an event on an
// iCloud-like server after a remote edit of the source object. The source
// DELETE gets 412. The push must not retry the PUT, must keep B dirty,
// must record a clear error, and must record no conflict.
func TestMoveEventWaitsWhenSourceChangedOnServer(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")

	putsBefore, _ := srv.counts()
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil {
		t.Fatalf("PushLocalEdits B: %v", err)
	}
	if res.Pushed != 0 || res.Conflicts != 0 || len(res.Errors) != 1 {
		t.Fatalf("push = %+v, want one error, no push, no conflict", res)
	}
	msg := res.Errors[0].Error()
	if !strings.Contains(msg, evt.UID) || !strings.Contains(msg, "changed on the server") {
		t.Fatalf("error = %q, want the UID and a source-changed reason", msg)
	}
	if puts, _ := srv.counts(); puts != putsBefore+1 {
		t.Fatalf("PUTs = %d, want %d: no PUT retry after a refused source DELETE", puts, putsBefore+1)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || srB.Dirty != 1 {
		t.Fatalf("sync resource B = %+v, %v; want dirty", srB, err)
	}
	if !strings.Contains(srB.LastPushError, "changed on the server") {
		t.Fatalf("last push error = %q, want the source-changed reason", srB.LastPushError)
	}
	for _, cal := range []int64{calA, calB} {
		if conflicts, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts on %d = %v, %v; want none", cal, conflicts, err)
		}
	}
}

// TestMoveEventOnGenericServerLeavesSourceToTombstonePass moves an event on
// a server that accepts the create PUT at once. The push of B sends no
// source DELETE. The tombstone pass of A then deletes the source object.
func TestMoveEventOnGenericServerLeavesSourceToTombstonePass(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	_, deletesBefore := srv.counts()
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one clean push", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: the push of B must not delete the source", deletes, deletesBefore)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}

	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}
