package sync

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
	"github.com/douglasdemoura/chroncal/internal/todo"
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
	// uidConflict403 makes the account-wide UID rule answer 403 with the
	// CALDAV:no-uid-conflict precondition (RFC 4791 section 5.3.2.1), not 412.
	uidConflict403 bool
	// getStatus maps a collection prefix to a forced GET status.
	getStatus map[string]int
	// dropGets makes every GET end the connection with no answer, as a
	// server that cannot be reached does.
	dropGets bool
	// noGetEtag makes every GET answer without an ETag header, as a
	// server that gives no etag does.
	noGetEtag bool
	deletes   int
	puts      int
	// lastIfMatch is the If-Match header of the last PUT.
	lastIfMatch string
	// gets counts the GET requests.
	gets int
	// refusePuts makes the next refusePuts PUTs answer 412, as a server
	// that frees the UID of a deleted object late does.
	refusePuts int
	// removed holds the hrefs that a remote delete removed. The next
	// REPORT of their collection lists each one with 404, as an RFC 6578
	// sync-collection lists a removed member.
	removed []string
}

var (
	icloudUIDPattern     = regexp.MustCompile(`(?m)^UID:(.+?)\r?$`)
	icloudSummaryPattern = regexp.MustCompile(`(?m)^SUMMARY:[^\r\n]*`)
)

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
		body, _ := io.ReadAll(r.Body)
		if f.serveFullReport(w, r, string(body)) {
			return
		}
		// Incremental sync-collection with no changed member: the pull
		// imports nothing. A removed member of the collection gets 404.
		var gone strings.Builder
		kept := f.removed[:0]
		for _, p := range f.removed {
			if strings.HasPrefix(p, r.URL.Path) {
				gone.WriteString("\n  <d:response><d:href>" + p + "</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>")
				continue
			}
			kept = append(kept, p)
		}
		f.removed = kept
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">` + gone.String() + `
  <d:sync-token>tok-next</d:sync-token>
</d:multistatus>`))
	case "PUT":
		f.puts++
		f.lastIfMatch = r.Header.Get("If-Match")
		body, _ := io.ReadAll(r.Body)
		m := icloudUIDPattern.FindStringSubmatch(string(body))
		if m == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		uid := m[1]
		if f.refusePuts > 0 {
			f.refusePuts--
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
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
				if f.uidConflict403 {
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <C:no-uid-conflict><D:href>` + p + `</D:href></C:no-uid-conflict>
</D:error>`))
					return
				}
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
		if _, ok := f.objects[r.URL.Path]; ok && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		f.nextTag++
		etag := fmt.Sprintf(`"e%d"`, f.nextTag)
		f.objects[r.URL.Path] = string(body)
		f.etags[r.URL.Path] = etag
		// The collection holds the href again, so no REPORT lists it as
		// removed.
		f.removed = slices.DeleteFunc(f.removed, func(p string) bool { return p == r.URL.Path })
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusCreated)
	case "GET":
		f.gets++
		if f.dropGets {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		for prefix, code := range f.getStatus {
			if strings.HasPrefix(r.URL.Path, prefix) {
				w.WriteHeader(code)
				return
			}
		}
		body, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/calendar")
		if !f.noGetEtag {
			w.Header().Set("ETag", f.etags[r.URL.Path])
		}
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

// fakeHrefPattern finds the hrefs of a calendar-multiget body.
var fakeHrefPattern = regexp.MustCompile(`<d:href>([^<]*)</d:href>`)

// serveFullReport answers the two REPORTs of a full inventory: a
// sync-collection with an empty token lists each object of the collection
// with its etag, and a calendar-multiget returns the listed bodies. It
// reports false for an incremental sync-collection. The caller holds f.mu.
func (f *icloudFakeServer) serveFullReport(w http.ResponseWriter, r *http.Request, body string) bool {
	var out strings.Builder
	switch {
	case strings.Contains(body, "calendar-multiget"):
		for _, m := range fakeHrefPattern.FindAllStringSubmatch(body, -1) {
			p := m[1]
			data, ok := f.objects[p]
			if !ok {
				out.WriteString("\n  <d:response><d:href>" + p + "</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>")
				continue
			}
			var esc strings.Builder
			_ = xml.EscapeText(&esc, []byte(data))
			out.WriteString("\n  <d:response><d:href>" + p + "</d:href><d:propstat><d:prop><d:getetag>" + f.etags[p] +
				"</d:getetag><c:calendar-data>" + esc.String() + "</c:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>")
		}
	case strings.Contains(body, "<d:sync-token></d:sync-token>"):
		for p := range f.objects {
			if strings.HasPrefix(p, r.URL.Path) {
				out.WriteString("\n  <d:response><d:href>" + p + "</d:href><d:propstat><d:prop><d:getetag>" + f.etags[p] +
					"</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>")
			}
		}
		out.WriteString("\n  <d:sync-token>tok-full</d:sync-token>")
	default:
		return false
	}
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">` + out.String() + `
</d:multistatus>`))
	return true
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

func (f *icloudFakeServer) setRefusePuts(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusePuts = n
}

func (f *icloudFakeServer) setGetStatus(prefix string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getStatus == nil {
		f.getStatus = map[string]int{}
	}
	f.getStatus[prefix] = code
}

// editRemote gives the object at path a new ETag, as a remote edit does.
func (f *icloudFakeServer) editRemote(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTag++
	f.etags[path] = fmt.Sprintf(`"e%d"`, f.nextTag)
}

// editRemoteSummary replaces the SUMMARY of the object at path and gives
// it a new ETag, as a remote edit of the title does.
func (f *icloudFakeServer) editRemoteSummary(path, summary string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[path] = icloudSummaryPattern.ReplaceAllString(f.objects[path], "SUMMARY:"+summary)
	f.nextTag++
	f.etags[path] = fmt.Sprintf(`"e%d"`, f.nextTag)
}

// deleteRemote removes the object at path, as a delete by another client
// does. The next REPORT of the collection lists the removal.
func (f *icloudFakeServer) deleteRemote(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, path)
	delete(f.etags, path)
	f.removed = append(f.removed, path)
}

func (f *icloudFakeServer) setNoGetEtag(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noGetEtag = v
}

// setRemoteBody replaces the body of the object at path and gives it a
// new ETag.
func (f *icloudFakeServer) setRemoteBody(path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[path] = body
	f.nextTag++
	f.etags[path] = fmt.Sprintf(`"e%d"`, f.nextTag)
}

func (f *icloudFakeServer) body(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[path]
}

func (f *icloudFakeServer) etag(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.etags[path]
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

// linkThirdCalendar adds one more linked calendar to the account of calA.
func linkThirdCalendar(t *testing.T, engine *Engine, q *storage.Queries, calA int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	a, err := q.GetCalendar(ctx, calA)
	if err != nil {
		t.Fatalf("GetCalendar A: %v", err)
	}
	cal, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
	if err != nil {
		t.Fatalf("CreateCalendar %s: %v", name, err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        cal.ID,
		AccountID: a.AccountID,
		RemoteUrl: storage.StringToNullable("/cal/" + name + "/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount %s: %v", name, err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET sync_token = 'tok0' WHERE id = ?`, cal.ID); err != nil {
		t.Fatalf("seed sync token %s: %v", name, err)
	}
	return cal.ID
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

// newICloudPairService wraps engine in a Service, so a test can resolve a
// conflict through the same path as the CLI.
func newICloudPairService(engine *Engine, q *storage.Queries) *Service {
	return &Service{engine: engine, db: engine.db, q: q}
}

// wantMoveConflict asserts one open move conflict on calB whose source is
// calA, and returns it.
func wantMoveConflict(t *testing.T, q *storage.Queries, calA, calB int64, uid string) storage.SyncConflict {
	t.Helper()
	ctx := context.Background()
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts on B = %v, %v; want one", conflicts, err)
	}
	c := conflicts[0]
	if c.Uid != uid || c.MoveSourceCalendarID == nil || *c.MoveSourceCalendarID != calA {
		t.Fatalf("conflict = %+v, want a move conflict of %s from %d", c, uid, calA)
	}
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts on A = %v, %v; want none", conflicts, err)
	}
	return c
}

// TestMoveEventRecordsConflictWhenSourceChangedOnServer moves an event on an
// iCloud-like server after a remote edit of the source object. The source
// DELETE gets 412. The push must not retry the PUT, and it must keep B
// dirty. It must keep the source tombstone and the source object. It must
// record one move conflict on B that holds both bodies (issue #841).
func TestMoveEventRecordsConflictWhenSourceChangedOnServer(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")

	putsBefore, _ := srv.counts()
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil {
		t.Fatalf("PushLocalEdits B: %v", err)
	}
	if res.Pushed != 0 || res.Conflicts != 1 || len(res.Errors) != 0 {
		t.Fatalf("push = %+v, want one conflict, no push, no error", res)
	}
	if puts, _ := srv.counts(); puts != putsBefore+1 {
		t.Fatalf("PUTs = %d, want %d: no PUT retry after a refused source DELETE", puts, putsBefore+1)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || srB.Dirty != 1 || srB.RemoteUrl != "" {
		t.Fatalf("sync resource B = %+v, %v; want dirty without a remote href", srB, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if !strings.Contains(c.LocalIcal, "SUMMARY:Dentist") || !strings.Contains(c.ServerIcal, "SUMMARY:Dentist (remote)") {
		t.Fatalf("conflict bodies = local %q, server %q; want the moved body and the remote edit", c.LocalIcal, c.ServerIcal)
	}
	if c.ServerEtag == "" {
		t.Fatal("conflict has no server etag")
	}

	// A later prompt pass of B skips the row: no PUT, no second record. A
	// full pass of A keeps the tombstone and sends no DELETE, so the
	// remote edit does not come back beside the moved copy.
	putsBefore, deletesBefore := srv.counts()
	for _, cal := range []int64{calB, calA} {
		res, err := engine.SyncCalendar(ctx, cal, ConflictPrompt)
		if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Pushed != 0 {
			t.Fatalf("later pass of %d = %+v, %v; want nothing pushed and no new conflict", cal, res, err)
		}
	}
	if puts, deletes := srv.counts(); puts != putsBefore || deletes != deletesBefore {
		t.Fatalf("PUTs, DELETEs = %d, %d; want %d, %d: the open move conflict holds both sides", puts, deletes, putsBefore, deletesBefore)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A after the later passes = %v, %v; want one", ts, err)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("event after the later passes = %+v, %v; want it in B only", got, err)
	}

	// A server-wins pass skips the row too: no PUT, no DELETE, no new
	// record. A move is a choice of the user.
	putsBefore, deletesBefore = srv.counts()
	res, err = engine.SyncCalendar(ctx, calB, ConflictServerWins)
	if err != nil || len(res.Errors) != 0 || res.AutoResolved != 0 || res.Pushed != 0 || res.SkippedConflicts != 1 {
		t.Fatalf("server-wins pass of B = %+v, %v; want one skipped conflict and nothing else", res, err)
	}
	if puts, deletes := srv.counts(); puts != putsBefore || deletes != deletesBefore {
		t.Fatalf("PUTs, DELETEs = %d, %d; want %d, %d: a server-wins pass sends nothing for a move conflict", puts, deletes, putsBefore, deletesBefore)
	}
	if again := wantMoveConflict(t, q, calA, calB, evt.UID); again.DetectedAt != c.DetectedAt {
		t.Fatalf("detected_at = %s, want the first record %s", again.DetectedAt, c.DetectedAt)
	}
}

// TestEditConflictRecordKeepsAnOpenMoveConflict records an edit conflict
// on a resource that holds an open move conflict. The move row must stay
// as it is, with its bodies and its source calendar.
func TestEditConflictRecordKeepsAnOpenMoveConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", ServerIcal: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	again := wantMoveConflict(t, q, calA, calB, evt.UID)
	if again.ServerEtag != c.ServerEtag || again.ServerIcal != c.ServerIcal || again.LocalIcal != c.LocalIcal || again.DetectedAt != c.DetectedAt {
		t.Fatalf("conflict after the edit record = %+v, want the move row unchanged %+v", again, c)
	}

	// A resolved row becomes the new edit conflict.
	if err := engine.markConflictResolved(ctx, calB, evt.UID, ResolutionLocal); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", ServerIcal: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict after the resolution: %v", err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(conflicts) != 1 || conflicts[0].MoveSourceCalendarID != nil || conflicts[0].ServerEtag != `"edit"` {
		t.Fatalf("conflicts on B = %+v, %v; want one open edit conflict", conflicts, err)
	}
}

// TestResolveMoveConflictPickLocalCompletesTheMove records a move conflict
// and resolves it for the local side. The next push must delete the
// source object and create the target object. A second remote edit before
// that push must record a fresh conflict, so no edit is lost.
func TestResolveMoveConflictPickLocalCompletesTheMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	// A second remote edit lands before the resolution is pushed.
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote 2)")
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	resolved, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || resolved.ResolvedAt == nil || resolved.Resolution == nil || *resolved.Resolution != ResolutionLocal {
		t.Fatalf("conflict after the local pick = %+v, %v; want resolved as local", resolved, err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || res.Conflicts != 1 {
		t.Fatalf("push after a second remote edit = %+v, %v; want a fresh conflict and no push", res, err)
	}
	c = wantMoveConflict(t, q, calA, calB, evt.UID)
	if !strings.Contains(c.ServerIcal, "SUMMARY:Dentist (remote 2)") {
		t.Fatalf("fresh conflict server body = %q, want the second remote edit", c.ServerIcal)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone after a refused conditional DELETE")
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local (again): %v", err)
	}
	res, err = engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push after the local pick = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatal("sync resource A still exists after the move")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(conflicts) != 0 {
		t.Fatalf("open conflicts on B = %v, %v; want none", conflicts, err)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestResolveMoveConflictPickServerCancelsTheMove records a move conflict
// and resolves it for the server side. The rows must go back to the source
// calendar with the server body. The source tombstone and the unpushed
// target row must go, and the next passes must change nothing on the server.
func TestResolveMoveConflictPickServerCancelsTheMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the server pick = %+v, %v; want it in A with the remote title", got, err)
	}
	if n := countRows(t, engine, "events", calB, evt.UID); n != 0 {
		t.Fatalf("event rows in B = %d, want none", n)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err == nil {
		t.Fatal("sync resource B still exists after the cancelled move")
	}
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || srA.Dirty != 0 || srA.Etag != c.ServerEtag {
		t.Fatalf("sync resource A = %+v, %v; want clean with the server etag %s", srA, err, c.ServerEtag)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
	resolved, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || resolved.ResolvedAt == nil || resolved.Resolution == nil || *resolved.Resolution != ResolutionServer {
		t.Fatalf("conflict after the server pick = %+v, %v; want resolved as server", resolved, err)
	}

	putsBefore, deletesBefore := srv.counts()
	syncPairClean(t, engine, calA, calB)
	if puts, deletes := srv.counts(); puts != putsBefore || deletes != deletesBefore {
		t.Fatalf("PUTs, DELETEs = %d, %d; want %d, %d: a cancelled move changes nothing on the server", puts, deletes, putsBefore, deletesBefore)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
}

// TestResolveMoveConflictPickServerCancelsALandedMove records a move
// conflict in the tombstone pass of a server that keeps a UID unique per
// collection. The create in B landed, and the source DELETE got 412. The
// cancel must move the rows back with the recorded server body and tombstone
// the copy in B. The next passes must leave only the source object on the
// server.
func TestResolveMoveConflictPickServerCancelsALandedMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server on a landed move: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the cancel = %+v, %v; want it in A with the remote title", got, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calB); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on B = %v, %v; want one for the created copy", ts, err)
	}
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || srA.Dirty != 0 || srA.Etag != c.ServerEtag {
		t.Fatalf("sync resource A = %+v, %v; want clean with the server etag %s", srA, err, c.ServerEtag)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
	srv.mu.Lock()
	body := srv.objects["/cal/a/"+evt.UID+".ics"]
	srv.mu.Unlock()
	if !strings.Contains(body, "SUMMARY:Dentist (remote)") {
		t.Fatalf("server body after the cancel = %q, want the remote title", body)
	}
}

// TestResolveMoveConflictRefusesServerAfterAFinishedMove resolves a move
// conflict for the local side, pushes, and then asks for the server side
// on the resolved row. The target holds the server object and the source
// row is gone, so the recorded server body is stale. The pick must be
// refused and must change nothing.
func TestResolveMoveConflictRefusesServerAfterAFinishedMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push after the local pick = %+v, %v; want one push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")

	_, err := svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "finished") {
		t.Fatalf("server pick after a finished move = %v, want a refusal", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("event after the refused pick = %+v, %v; want it in B", got, err)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none", n)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestResolveMoveConflictLocalAfterCancelRestoresTheEdit cancels a move
// whose moved copy holds an unpushed edit, and then asks for the local
// side. The cancel must record the live body first. The local pick must
// then restore that body in the source calendar, mark it dirty, and must
// not move the item again. The push then sends the edit to the source
// object.
func TestResolveMoveConflictLocalAfterCancelRestoresTheEdit(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := engine.events.Update(ctx, moved.ID, event.UpdateParams{
		Title: "Dentist (edited)", StartTime: moved.StartTime, EndTime: moved.EndTime, Status: moved.Status, Transp: moved.Transp, Class: moved.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the cancel = %+v, %v; want it in A with the remote title", got, err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || !strings.Contains(row.LocalIcal, "SUMMARY:Dentist (edited)") {
		t.Fatalf("recorded local body after the cancel = %q, %v; want the live edit", row.LocalIcal, err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local after the cancel: %v", err)
	}
	got, err = engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (edited)" {
		t.Fatalf("event after the restore = %+v, %v; want the edit in A", got, err)
	}
	if n := countRows(t, engine, "events", calB, evt.UID); n != 0 {
		t.Fatalf("event rows in B = %d, want none: the restore does not move the item", n)
	}
	resolved, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || resolved.Resolution == nil || *resolved.Resolution != ResolutionLocal {
		t.Fatalf("conflict after the restore = %+v, %v; want resolved as local", resolved, err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push of A after the restore = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
	if body := srv.body("/cal/a/" + evt.UID + ".ics"); !strings.Contains(body, "SUMMARY:Dentist (edited)") {
		t.Fatalf("server body = %q, want the restored edit", body)
	}
}

// TestResolveMoveConflictPickServerRefusesAnUnusableBody asks for the server
// side of a move conflict while the current source object cannot be encoded.
// The pick must refuse before any write and name the server body. The rows
// stay in the target calendar, the source tombstone stays, and the conflict
// stays open.
func TestResolveMoveConflictPickServerRefusesAnUnusableBody(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET server_ical = '' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("blank the server body: %v", err)
	}
	path := "/cal/a/" + evt.UID + ".ics"
	srv.setRemoteBody(path, regexp.MustCompile(`(?m)^PRODID:[^\r\n]*\r?\n`).ReplaceAllString(srv.body(path), ""))

	_, err := svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("server pick with an unreadable server body = %v, want a refusal", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("event after the refused pick = %+v, %v; want it still in B", got, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil {
		t.Fatalf("sync resource B after the refused pick: %v, want it kept", err)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)
}

// TestTombstonePassRecordsMoveConflictOnGenericServer moves an event on a
// server that keeps a UID unique per collection. The create in B lands at
// once, so the source DELETE goes out from the tombstone pass of A. A
// remote edit of the source object before that pass makes the DELETE get
// 412. The pass must record a move conflict on B and keep the tombstone
// and the source object. It must not bring the remote edit back into A.
// The local pick then completes the move.
func TestTombstonePassRecordsMoveConflictOnGenericServer(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 0 || res.Conflicts != 1 || res.AutoResolved != 0 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict and no delete", res, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if !strings.Contains(c.LocalIcal, "SUMMARY:Dentist\r") || !strings.Contains(c.ServerIcal, "SUMMARY:Dentist (remote)") {
		t.Fatalf("conflict bodies = local %q, server %q; want the moved body and the remote edit", c.LocalIcal, c.ServerIcal)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none: the remote edit must not come back", n)
	}

	// A later pass of A waits: no DELETE, no second record.
	_, deletesBefore := srv.counts()
	res, err = engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Deleted != 0 {
		t.Fatalf("later pass of A = %+v, %v; want nothing", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: the open move conflict holds the tombstone", deletes, deletesBefore)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A after the later pass = %d, want none", n)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	res, err = engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A after the local pick = %+v, %v; want one delete", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestTombstonePassWaitsForATargetWithoutServerObject runs the tombstone
// pass of the source while the target row has no server object and no
// dirty flag. A push clears the flag of a row whose local rows are gone
// without a PUT, so the flag proves nothing. The DELETE must wait only on
// the absent server object.
func TestTombstonePassWaitsForATargetWithoutServerObject(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = ?`, calB, evt.UID); err != nil {
		t.Fatalf("clear the target dirty flag: %v", err)
	}

	_, _, client, remoteURL, err := engine.loadCalendarClient(ctx, calA)
	if err != nil {
		t.Fatalf("loadCalendarClient: %v", err)
	}
	_, deletesBefore := srv.counts()
	res, err := engine.processTombstones(ctx, client, calA, remoteURL)
	if err != nil || len(res.errors) != 0 || res.deleted != 0 {
		t.Fatalf("tombstone pass of A = %+v, %v; want no delete", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: the target has no server object", deletes, deletesBefore)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
	// PushMove follows the same rule.
	var pushed []int64
	PushMove(ctx, q, calA, calB, evt.UID, func(calendarID int64) { pushed = append(pushed, calendarID) })
	if len(pushed) != 1 || pushed[0] != calB {
		t.Fatalf("pushes = %v, want only the target %d", pushed, calB)
	}
}

// TestTombstonePassWaitsForTheMoveTarget runs a full pass of the source
// calendar before the target calendar pushes the moved copy. The server
// accepts the create at once (a generic server). The source DELETE of a move
// is then left to the tombstone pass of the source. That pass must wait
// until the target holds the item, or the item is in no calendar on the
// server in between.
func TestTombstonePassWaitsForTheMoveTarget(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	_, deletesBefore := srv.counts()
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A before the target push = %+v, %v; want no delete", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: the source DELETE waits for the target", deletes, deletesBefore)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone before the target holds the item")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}

	res, err = engine.SyncCalendar(ctx, calB, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("pass of B = %+v, %v; want one push", res, err)
	}
	res, err = engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A after the target push = %+v, %v; want one delete", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
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

// TestMoveEventWithOpenConflictIsRefused moves an event whose UID has an
// open sync conflict in the source calendar. The move would clear the
// source dirty flag, and the next pass would resolve the conflict for the
// server in silence. The recorded local body would then be lost. The move
// must be refused and must change nothing.
func TestMoveEventWithOpenConflictIsRefused(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA,
		OwnerType:  "event",
		OwnerID:    evt.ID,
		Uid:        evt.UID,
		LocalIcal:  "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
		ServerIcal: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
		ServerEtag: `"server"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calA, evt.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}

	_, err := engine.events.Update(ctx, evt.ID, event.UpdateParams{
		Title:      evt.Title,
		StartTime:  evt.StartTime,
		EndTime:    evt.EndTime,
		CalendarID: calB,
		Status:     evt.Status,
		Transp:     evt.Transp,
		Class:      evt.Class,
	})
	if !errors.Is(err, storage.ErrMoveOpenConflict) {
		t.Fatalf("move with an open conflict = %v, want ErrMoveOpenConflict", err)
	}
	if !strings.Contains(err.Error(), "chroncal sync resolve") {
		t.Fatalf("error = %q, want the resolve command", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA {
		t.Fatalf("event after the refused move = %+v, %v; want it in A", got, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 {
		t.Fatalf("sync resource A = %+v, %v; want dirty", sr, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestMoveEventKeepsTargetDirtyWhenTargetCheckFails moves an event on an
// iCloud-like server. The create PUT in B gets 412, and the GET of the new
// href then fails with 503. The failed GET proves nothing about the target
// object. The push must record no conflict, must keep B dirty, and must
// keep the source object and the tombstone in A. The next pass then
// completes the move.
func TestMoveEventKeepsTargetDirtyWhenTargetCheckFails(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	srv.setGetStatus("/cal/b/", http.StatusServiceUnavailable)
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil {
		t.Fatalf("PushLocalEdits B: %v", err)
	}
	if len(res.Errors) == 0 || res.Pushed != 0 || res.Conflicts != 0 {
		t.Fatalf("push with a failed target GET = %+v, want an error, no push, no conflict", res)
	}
	// The error must name the failed check of the move target, not a
	// conflict path.
	if msg := res.Errors[0].Error(); !strings.Contains(msg, "check the move target") || strings.Contains(msg, "conflict") {
		t.Fatalf("error = %q, want the failed check of the move target", msg)
	}
	for _, cal := range []int64{calA, calB} {
		if conflicts, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts on %d = %v, %v; want none", cal, conflicts, err)
		}
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || srB.Dirty != 1 {
		t.Fatalf("sync resource B = %+v, %v; want dirty", srB, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone after a failed target GET")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}

	srv.mu.Lock()
	delete(srv.getStatus, "/cal/b/")
	srv.mu.Unlock()
	res, err = engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("retry push = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestPlainCreateDoesNotNeedSiblingTombstones pushes a new event while the
// lookup of the sibling tombstones fails. Only a create that gets 412 needs
// that lookup. A plain create must push, and the failed lookup must not
// keep it dirty.
func TestPlainCreateDoesNotNeedSiblingTombstones(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{
		CalendarID: calA, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Make every read of the tombstones table fail.
	if _, err := engine.db.ExecContext(ctx, `ALTER TABLE tombstones RENAME TO tombstones_gone`); err != nil {
		t.Fatalf("rename tombstones: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	if res.Pushed != 1 {
		t.Fatalf("push with a failed sibling lookup = %+v, want one push", res)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("the server has no object for the new event")
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Dirty != 0 {
		t.Fatalf("sync resource = %+v, %v; want clean", sr, err)
	}
}

// TestMoveEventOnServerWithNoUIDConflict403 moves an event on a server that
// refuses the duplicate UID with 403 and the CALDAV:no-uid-conflict
// precondition, as RFC 4791 section 5.3.2.1 says. That answer must start
// the source DELETE, as a 412 does. The move then completes.
func TestMoveEventOnServerWithNoUIDConflict403(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.mu.Lock()
	srv.uidConflict403 = true
	srv.mu.Unlock()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of the moved event = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestMoveEventIntoCalendarWithOpenConflictIsRefused moves an event into a
// calendar that holds an open sync conflict for the same UID. The move
// clears the tombstone of the target and marks the target dirty, so a
// later pass would push over the recorded conflict. The move must be
// refused with the same guidance as a conflict in the source.
func TestMoveEventIntoCalendarWithOpenConflictIsRefused(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB,
		OwnerType:  "event",
		OwnerID:    evt.ID,
		Uid:        evt.UID,
		LocalIcal:  "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
		ServerIcal: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
		ServerEtag: `"server"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	_, err := engine.events.Update(ctx, evt.ID, event.UpdateParams{
		Title:      evt.Title,
		StartTime:  evt.StartTime,
		EndTime:    evt.EndTime,
		CalendarID: calB,
		Status:     evt.Status,
		Transp:     evt.Transp,
		Class:      evt.Class,
	})
	if !errors.Is(err, storage.ErrMoveOpenConflict) {
		t.Fatalf("move into a calendar with an open conflict = %v, want ErrMoveOpenConflict", err)
	}
	if !strings.Contains(err.Error(), "chroncal sync resolve") {
		t.Fatalf("error = %q, want the resolve command", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA {
		t.Fatalf("event after the refused move = %+v, %v; want it in A", got, err)
	}
}

// TestMoveEventTreatsGoneTargetAsAbsent moves an event on an iCloud-like
// server whose new href answers 410 Gone, not 404. 410 also says that the
// target object is absent, so the push must send the source DELETE and
// retry the create, as it does after a 404.
func TestMoveEventTreatsGoneTargetAsAbsent(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.setGetStatus("/cal/b/", http.StatusGone)

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of the moved event = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts on B = %v, %v; want none", conflicts, err)
	}
}

// TestDoctorPushSettlesAMove runs the doctor push on the target half of a
// move on an iCloud-like server. The create gets 412 while the source
// object exists. The doctor push must send the source DELETE and retry the
// create, as the normal push does.
func TestDoctorPushSettlesAMove(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	if _, err := engine.DoctorPush(ctx, calB, evt.UID, nil); err != nil {
		t.Fatalf("DoctorPush of the moved event = %v, want nil", err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestPushMoveOrder checks the shared order of the write-path pushes after
// a move: the target first, and the source only when the target holds the
// item. The recorder push sends nothing, so the target stays dirty at
// first and the source push must not run.
func TestPushMoveOrder(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)

	var pushed []int64
	record := func(calendarID int64) { pushed = append(pushed, calendarID) }
	PushMove(ctx, q, calA, calB, evt.UID, record)
	if len(pushed) != 1 || pushed[0] != calB {
		t.Fatalf("pushes with a dirty target = %v, want only the target %d", pushed, calB)
	}

	// The target now holds the item.
	if _, err := engine.db.ExecContext(ctx,
		`UPDATE sync_resources SET dirty = 0, remote_url = '/cal/b/x.ics' WHERE calendar_id = ? AND uid = ?`, calB, evt.UID); err != nil {
		t.Fatalf("settle the target row: %v", err)
	}
	pushed = nil
	PushMove(ctx, q, calA, calB, evt.UID, record)
	if len(pushed) != 2 || pushed[0] != calB || pushed[1] != calA {
		t.Fatalf("pushes with a settled target = %v, want [%d %d]", pushed, calB, calA)
	}

	pushed = nil
	PushMove(ctx, q, calB, calB, evt.UID, record)
	if len(pushed) != 1 {
		t.Fatalf("pushes with no move = %v, want one", pushed)
	}
}

// TestPullOfMoveSourceKeepsMovedRows moves an event from A to B, and then
// the server deletes the A object before the source push. A full pull of A
// finds the UID gone. The deletion must not touch the rows of the UID:
// they live in B now, so they must stay live. The tombstone pass of A then
// clears the sync row and the tombstone of A.
func TestPullOfMoveSourceKeepsMovedRows(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()
	// A full snapshot of A sees no object of the UID.
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET sync_token = NULL WHERE id = ?`, calA); err != nil {
		t.Fatalf("clear sync token: %v", err)
	}
	_, _, client, remoteURL, err := engine.loadCalendarClient(ctx, calA)
	if err != nil {
		t.Fatalf("loadCalendarClient: %v", err)
	}
	if _, err := engine.pull(ctx, client, calA, remoteURL, ConflictPrompt); err != nil {
		t.Fatalf("pull A: %v", err)
	}

	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("moved event after the pull of A = %+v, %v; want it live in B", got, err)
	}
	// The absence of a tombstoned href proves nothing, so the tombstone
	// pass settles A: its DELETE gets 404 and clears both rows. The pass
	// waits for the target push, so B pushes first.
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	if _, err := engine.processTombstones(ctx, client, calA, remoteURL); err != nil {
		t.Fatalf("processTombstones A: %v", err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatalf("sync resource of A = %+v, want none", sr)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of A = %v, %v; want none", ts, err)
	}
}

// TestExplicitDeleteOfMoveSourceKeepsMovedRows moves an event from A to B,
// and the server then reports the A object as deleted (a 404). The delete
// must not touch the rows in B. The server delete did what the tombstone
// of A wanted, so the sync row and the tombstone of A go at once.
func TestExplicitDeleteOfMoveSourceKeepsMovedRows(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource A: %v", err)
	}
	deletions := newPendingDeletions(engine.logger)
	deletions.markExplicit(srA)
	if _, failed, _ := deletions.apply(ctx, engine, calA, nil); failed != 0 {
		t.Fatalf("apply failed = %d, want 0", failed)
	}

	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("moved event after the delete of A = %+v, %v; want it live in B", got, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatalf("sync resource of A = %+v, want none", sr)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of A = %v, %v; want none", ts, err)
	}
}

// TestDeleteOfMovedCopyFreesTheSourceDelete moves an event from A to B and
// deletes the copy in B before any sync. The unpushed row of B goes with
// the delete, so the source DELETE of A must not wait for it. The passes
// of A must remove the object from the server.
func TestDeleteOfMovedCopyFreesTheSourceDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err == nil {
		t.Fatal("sync resource B survives the delete of an unpushed copy")
	}
	if res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of B = %+v, %v; want a clean pass", res, err)
	}
	for pass := range 3 {
		res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
		if err != nil || len(res.Errors) != 0 {
			t.Fatalf("pass %d of A = %+v, %v; want a clean pass", pass, res, err)
		}
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("server objects = %v, want none", got)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestPushDeletesUnpushedRowWithoutLocalRows pushes a dirty sync row that
// has no server object and no local rows. The push must delete the row, not
// clear its dirty flag. No row with an empty remote href then stays without
// a live copy.
func TestPushDeletesUnpushedRowWithoutLocalRows(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calA, Uid: "ghost-uid", OwnerType: "event", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 0 {
		t.Fatalf("push = %+v, %v; want nothing pushed and no error", res, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: "ghost-uid"}); err == nil {
		t.Fatalf("sync resource = %+v, want none", sr)
	}
}

// TestStaleTombstoneCleanupKeepsAMoveTombstone backdates tombstones past
// the 30-day limit. A tombstone whose move target has no server object
// stays, a tombstone that an open move conflict holds stays, and a plain
// stale tombstone goes.
func TestStaleTombstoneCleanupKeepsAMoveTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	const stale = "2020-01-01T00:00:00Z"

	// An unpushed move target holds the tombstone.
	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET deleted_at = ? WHERE calendar_id = ? AND uid = ?`, stale, calA, evt.UID); err != nil {
		t.Fatalf("backdate the tombstone: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `INSERT INTO tombstones (calendar_id, uid, remote_url, deleted_at) VALUES (?, 'old-uid', '/cal/a/old-uid.ics', ?)`, calA, stale); err != nil {
		t.Fatalf("insert a plain stale tombstone: %v", err)
	}
	if err := q.DeleteStaleTombstones(ctx); err != nil {
		t.Fatalf("DeleteStaleTombstones: %v", err)
	}
	ts, err := q.ListTombstonesByCalendar(ctx, calA)
	if err != nil || len(ts) != 1 || ts[0].Uid != evt.UID {
		t.Fatalf("tombstones on A = %v, %v; want only the move tombstone", ts, err)
	}

	// An open move conflict holds the tombstone after the target landed.
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET deleted_at = ? WHERE calendar_id = ? AND uid = ?`, stale, calA, evt.UID); err != nil {
		t.Fatalf("backdate the tombstone: %v", err)
	}
	// The pass runs the stale cleanup at its end.
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Conflicts != 0 {
		t.Fatalf("later pass of A = %+v, %v; want a quiet pass", res, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A after the cleanup = %v, %v; want the held tombstone", ts, err)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)
}

// TestDeleteOfSharedUIDKeepsTheRemoteEdit stores one UID in A and B without
// a move, deletes the copy in A, and edits the A object on the server. The
// DELETE gets 412. That is a plain delete-vs-edit conflict. The pass must
// keep the remote edit, drop the tombstone, and record no move conflict, so
// no resolve can move the rows of B.
func TestDeleteOfSharedUIDKeepsTheRemoteEdit(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evtA := createPushedEvent(t, engine, calA)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evtB, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evtA.UID, CalendarID: calB, Title: "Dentist (B)", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evtA.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	if err := engine.events.Delete(ctx, evtA.ID); err != nil {
		t.Fatalf("Delete in A: %v", err)
	}
	srv.editRemote("/cal/a/" + evtA.UID + ".ics")

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.AutoResolved != 1 {
		t.Fatalf("pass of A = %+v, %v; want the remote edit kept and no conflict", res, err)
	}
	if !srv.has("/cal/a/" + evtA.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
	for _, cal := range []int64{calA, calB} {
		if conflicts, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts on %d = %v, %v; want none", cal, conflicts, err)
		}
	}
	got, err := engine.events.Get(ctx, evtB.ID)
	if err != nil || got.CalendarID != calB || got.Title != "Dentist (B)" {
		t.Fatalf("event in B = %+v, %v; want it untouched", got, err)
	}
}

// TestResolveMoveConflictRefusesRepeatServerPick cancels a move and asks
// for the server side again. The recorded body and etag are stale, so the
// repeat must be refused and must change nothing.
func TestResolveMoveConflictRefusesRepeatServerPick(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	// A newer local edit after the cancel must survive the repeat.
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Title = "Dentist (edited after the cancel)"
	if _, err := engine.events.Update(ctx, got.ID, event.UpdateParams{
		Title: got.Title, StartTime: got.StartTime, EndTime: got.EndTime, Status: got.Status, Transp: got.Transp, Class: got.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	_, err = svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Fatalf("repeat server pick = %v, want a refusal", err)
	}
	after, err := engine.events.Get(ctx, evt.ID)
	if err != nil || after.CalendarID != calA || after.Title != got.Title {
		t.Fatalf("event after the refused repeat = %+v, %v; want the local edit in A", after, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
		t.Fatalf("sync resource A = %+v, %v; want the local edit still dirty", sr, err)
	}
}

// TestResolveMoveConflictPickLocalNeedsAnEtag records a move conflict on a
// server that gives no etag, so the record holds none. The local pick
// must fetch the current etag from the server. While the server gives
// none, the pick must refuse and change nothing. When the server gives
// one, the pick takes it and the move completes with a conditional
// source DELETE.
func TestResolveMoveConflictPickLocalNeedsAnEtag(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	srv.setNoGetEtag(true)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if c.ServerEtag != "" {
		t.Fatalf("conflict etag = %q, want none from a server without etags", c.ServerEtag)
	}
	before, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource A: %v", err)
	}

	_, err = svc.ResolveConflict(ctx, c.ID, "local")
	if err == nil || !strings.Contains(err.Error(), "no etag") {
		t.Fatalf("local pick without an etag = %v, want a refusal", err)
	}
	if after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || after != before {
		t.Fatalf("sync resource A after the refusal = %+v, %v; want unchanged %+v", after, err, before)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)

	srv.setNoGetEtag(false)
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("local pick with an etag: %v", err)
	}
	after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if want := strings.Trim(srv.etag("/cal/a/"+evt.UID+".ics"), `"`); err != nil || after.Etag != want {
		t.Fatalf("sync resource A etag = %q, %v; want the current server etag %q", after.Etag, err, want)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push after the local pick = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestChainMoveBeforeSyncOnICloud moves an event from A to B and then to
// C before any sync. The tombstone of A must point at C, so the refused
// create in C sends the DELETE of A and the move completes.
func TestChainMoveBeforeSyncOnICloud(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, moved, calC)
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calC {
		t.Fatalf("tombstone of A = %+v, %v; want the target C", ts, err)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err == nil {
		t.Fatal("sync resource B survives the second move")
	}

	for pass := range 2 {
		for _, cal := range []int64{calC, calB, calA} {
			res, err := engine.SyncCalendar(ctx, cal, ConflictPrompt)
			if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 {
				t.Fatalf("pass %d of %d = %+v, %v; want a clean pass", pass, cal, res, err)
			}
		}
	}
	wantOnlyServerObject(t, srv, "/cal/c/"+evt.UID+".ics")
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestChainMoveBeforeSyncOnGenericServer moves an event from A to B and then
// to C before any sync, on a server that keeps a UID unique per collection.
// The source DELETE of A must wait for the create in C. A remote edit of the
// A object must record a move conflict on C, not a duplicate in A.
func TestChainMoveBeforeSyncOnGenericServer(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, moved, calC)

	// The DELETE of A waits for C.
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A before the push of C = %+v, %v; want no delete", res, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone before C holds the item")
	}
	if res, err := engine.SyncCalendar(ctx, calC, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("pass of C = %+v, %v; want one push", res, err)
	}
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 || res.Deleted != 0 {
		t.Fatalf("pass of A after the remote edit = %+v, %v; want one move conflict", res, err)
	}
	wantMoveConflict(t, q, calA, calC, evt.UID)
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none", n)
	}
}

// TestTombstonePassKeepsRemoteEditWhenMovedCopyIsInTrash records no move
// conflict when the moved copy went to the trash before the source DELETE
// got 412. The move has no local side any more, so the plain rule applies.
// The remote edit stays, the tombstone goes, and no error repeats on every
// pass.
func TestTombstonePassKeepsRemoteEditWhenMovedCopyIsInTrash(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.AutoResolved != 1 {
		t.Fatalf("pass of A = %+v, %v; want the remote edit kept and no error", res, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
	for _, cal := range []int64{calA, calB} {
		if conflicts, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts on %d = %v, %v; want none", cal, conflicts, err)
		}
	}
}

// TestPlainDeleteBesideAnImportSettlesInline deletes a pushed event in A
// and imports the same UID into B. The tombstone of A names no move
// target. The refused create in B sends that delete, as the code did
// before the upgrade, and the create then lands, with no move conflict.
// A 412 on the delete keeps the remote edit and records no move conflict.
func TestPlainDeleteBesideAnImportSettlesInline(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evtA := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evtA.ID); err != nil {
		t.Fatalf("Delete in A: %v", err)
	}
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evtA.UID, CalendarID: calB, Title: "Dentist (B)", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evtA.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push of the import = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evtA.UID+".ics")
	for _, cal := range []int64{calA, calB} {
		if conflicts, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts on %d = %v, %v; want none", cal, conflicts, err)
		}
	}

	// A remote edit of the deleted object: the plain rule keeps it.
	evt2 := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt2.ID); err != nil {
		t.Fatalf("Delete the second event in A: %v", err)
	}
	srv.editRemote("/cal/a/" + evt2.UID + ".ics")
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt2.UID, CalendarID: calB, Title: "Dentist (B2)", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID of the second UID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evt2.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B (second UID): %v", err)
	}
	res, err = engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || res.Conflicts != 0 || len(res.Errors) != 1 {
		t.Fatalf("push with a remote edit of the deleted object = %+v, %v; want one error and no conflict", res, err)
	}
	if !srv.has("/cal/a/" + evt2.UID + ".ics") {
		t.Fatal("the remote edit in A is gone")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none after the plain rule", ts, err)
	}
	for _, cal := range []int64{calA, calB} {
		if conflicts, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts on %d = %v, %v; want none", cal, conflicts, err)
		}
	}
}

// TestResolveMoveConflictPickLocalRestoresTheSourceRow resolves a move
// conflict for the local side after the source calendar lost its sync
// row. The etag lives on that row, so the pick must create it again with
// the etag of the record. The next source DELETE then stays conditional:
// a second remote edit records a fresh conflict instead of a silent
// delete.
func TestResolveMoveConflictPickLocalRestoresTheSourceRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("DeleteSyncResource A: %v", err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || srA.Etag != c.ServerEtag || srA.RemoteUrl == "" || srA.Dirty != 0 {
		t.Fatalf("sync resource A = %+v, %v; want a clean row with the href and the etag %s", srA, err, c.ServerEtag)
	}

	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || res.Conflicts != 1 {
		t.Fatalf("push after a second remote edit = %+v, %v; want a fresh conflict and no push", res, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone after a DELETE that had to be conditional")
	}
}

// TestServerWinsPassLeavesAMoveConflictOpen records a move conflict on a
// landed move and runs a server-wins pass of the target. The target row
// is clean, but the conflict is not moot: the pass must leave it open, and
// the source DELETE must keep its wait.
func TestServerWinsPassLeavesAMoveConflictOpen(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	res, err := engine.SyncCalendar(ctx, calB, ConflictServerWins)
	if err != nil || len(res.Errors) != 0 || res.AutoResolved != 0 {
		t.Fatalf("server-wins pass of B = %+v, %v; want no auto resolution", res, err)
	}
	again := wantMoveConflict(t, q, calA, calB, evt.UID)
	if again.DetectedAt != c.DetectedAt {
		t.Fatalf("detected_at = %s, want the first record %s", again.DetectedAt, c.DetectedAt)
	}
	_, deletesBefore := srv.counts()
	if res, err := engine.SyncCalendar(ctx, calA, ConflictServerWins); err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Deleted != 0 {
		t.Fatalf("server-wins pass of A = %+v, %v; want a quiet pass", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: the open move conflict holds the tombstone", deletes, deletesBefore)
	}
}

// TestPullRefreshLeavesAMoveConflictBody runs the server-body refresh of
// the pull on a move conflict and on an edit conflict. The move conflict
// must keep the recorded body of the source object. The edit conflict
// takes the fresh body.
func TestPullRefreshLeavesAMoveConflictBody(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA, OwnerType: "event", OwnerID: 1, Uid: "edit-uid",
		LocalIcal: "local", ServerIcal: "old", ServerEtag: `"old"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	engine.refreshConflictServerBody(ctx, calB, evt.UID, "TARGET BODY", `"target"`)
	engine.refreshConflictServerBody(ctx, calA, "edit-uid", "fresh", `"fresh"`)
	again, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || again.ServerIcal != c.ServerIcal || again.ServerEtag != c.ServerEtag || again.ResolvedAt != nil {
		t.Fatalf("move conflict after the refresh = %q %q, %v; want the recorded source body %q %q", again.ServerIcal, again.ServerEtag, err, c.ServerIcal, c.ServerEtag)
	}
	edits, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(edits) != 1 || edits[0].ServerIcal != "fresh" || edits[0].ServerEtag != `"fresh"` {
		t.Fatalf("edit conflict after the refresh = %+v, %v; want the fresh body", edits, err)
	}
}

// TestMoveConflictRecordNeedsAnEncodableBody meets a source object that
// the importer accepts but the encoder rejects (no PRODID). The record
// must fail: no row, the tombstone and the dirty target stay, and the pass
// reports an error. A body that the encoder accepts records the conflict
// on the next pass.
func TestMoveConflictRecordNeedsAnEncodableBody(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	path := "/cal/a/" + evt.UID + ".ics"
	good := srv.body(path)
	bad := regexp.MustCompile(`(?m)^PRODID:[^\r\n]*\r?\n`).ReplaceAllString(good, "")
	srv.setRemoteBody(path, bad)

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Conflicts != 0 || res.Pushed != 0 || len(res.Errors) != 1 {
		t.Fatalf("push with an unencodable source body = %+v, %v; want one error and no record", res, err)
	}
	if !strings.Contains(res.Errors[0].Error(), "encode") {
		t.Fatalf("error = %q, want the encode failure", res.Errors[0])
	}
	for _, cal := range []int64{calA, calB} {
		if conflicts, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(conflicts) != 0 {
			t.Fatalf("conflicts on %d = %v, %v; want none", cal, conflicts, err)
		}
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
	if srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || srB.Dirty != 1 || srB.PushFailCount == 0 {
		t.Fatalf("sync resource B = %+v, %v; want dirty with a push failure", srB, err)
	}

	srv.setRemoteBody(path, good)
	res, err = engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Conflicts != 1 || len(res.Errors) != 0 {
		t.Fatalf("push with an encodable source body = %+v, %v; want one conflict", res, err)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)
}

// TestTombstonePassWaitsForAReadOnlyTarget moves an event to a calendar
// that becomes read-only after the move. The push of that calendar never
// sends the create, so the source DELETE must wait: the object stays on
// the server, and the tombstone stays.
func TestTombstonePassWaitsForAReadOnlyTarget(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}

	_, deletesBefore := srv.counts()
	for _, cal := range []int64{calB, calA, calA} {
		if res, err := engine.SyncCalendar(ctx, cal, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 0 {
			t.Fatalf("pass of %d = %+v, %v; want no delete", cal, res, err)
		}
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: the source DELETE waits for a read-only target", deletes, deletesBefore)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
}

// TestStaleTombstoneCleanupMatchesTheWaitSet builds the three kinds of
// tombstone in one calendar, backdates them, and runs the cleanup. The
// tombstones that stay must be exactly the wait set of the tombstone pass.
func TestStaleTombstoneCleanupMatchesTheWaitSet(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	// A move tombstone that an open move conflict holds.
	held := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, held, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + held.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	// A move tombstone whose target row has no server object.
	waiting := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, waiting, calB)
	// A plain delete.
	plain := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, plain.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET deleted_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatalf("backdate the tombstones: %v", err)
	}

	waits, err := storage.MoveDeleteWaitUIDs(ctx, q, calA)
	if err != nil {
		t.Fatalf("MoveDeleteWaitUIDs: %v", err)
	}
	if err := q.DeleteStaleTombstones(ctx); err != nil {
		t.Fatalf("DeleteStaleTombstones: %v", err)
	}
	kept := map[string]bool{}
	ts, err := q.ListTombstonesByCalendar(ctx, calA)
	if err != nil {
		t.Fatalf("ListTombstonesByCalendar: %v", err)
	}
	for _, t := range ts {
		kept[t.Uid] = true
	}
	want := map[string]bool{held.UID: true, waiting.UID: true}
	for uid := range want {
		if !waits[uid] || !kept[uid] {
			t.Fatalf("uid %s: wait %v, kept %v; want both", uid, waits[uid], kept[uid])
		}
	}
	if waits[plain.UID] || kept[plain.UID] {
		t.Fatalf("plain delete: wait %v, kept %v; want neither", waits[plain.UID], kept[plain.UID])
	}
	if len(waits) != len(kept) {
		t.Fatalf("wait set %v and kept set %v differ", waits, kept)
	}
}

// TestPullImportsALandedMovedCopyUnderAMoveConflict fetches a remote edit
// of the landed moved copy while its move conflict is open. The target
// row is clean, so the pull must import the body, and the move conflict
// must keep the recorded body of the source object. An open edit
// conflict still blocks the import.
func TestPullImportsALandedMovedCopyUnderAMoveConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	pathB := "/cal/b/" + evt.UID + ".ics"
	srv.editRemoteSummary(pathB, "Dentist (edited on the server)")
	data, err := parseICalData([]byte(srv.body(pathB)))
	if err != nil {
		t.Fatalf("parse the server body: %v", err)
	}
	uid, imported, _, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: pathB, href: pathB, etag: srv.etag(pathB), data: data})
	if err != nil || !imported || uid != evt.UID {
		t.Fatalf("import of the landed copy = %q, %v, %v; want an import", uid, imported, err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB || got.Title != "Dentist (edited on the server)" {
		t.Fatalf("event after the pull = %+v, %v; want the server edit in B", got, err)
	}
	again, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || again.ServerIcal != c.ServerIcal || again.ServerEtag != c.ServerEtag || again.ResolvedAt != nil {
		t.Fatalf("move conflict after the pull = %+v, %v; want the recorded source body kept", again, err)
	}

	// An edit conflict blocks the import.
	other := createPushedEvent(t, engine, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: other.ID, Uid: other.UID,
		LocalIcal: "local", ServerIcal: "old", ServerEtag: `"old"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	pathOther := "/cal/b/" + other.UID + ".ics"
	srv.editRemoteSummary(pathOther, "Other (edited on the server)")
	data, err = parseICalData([]byte(srv.body(pathOther)))
	if err != nil {
		t.Fatalf("parse the other body: %v", err)
	}
	if _, imported, _, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: pathOther, href: pathOther, etag: srv.etag(pathOther), data: data}); err != nil || imported {
		t.Fatalf("import under an edit conflict = %v, %v; want no import", imported, err)
	}
}

// TestEditConflictToleratesAnUnencodableServerBody pushes a local edit
// against a server body that the encoder rejects. The 412 must still record
// the edit conflict, with an empty server body. The local edit then has a
// row, and the next pull can refresh the body.
func TestEditConflictToleratesAnUnencodableServerBody(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	bad := regexp.MustCompile(`(?m)^PRODID:[^\r\n]*\r?\n`).ReplaceAllString(srv.body(path), "")
	srv.setRemoteBody(path, bad)
	if _, err := engine.events.Update(ctx, evt.ID, event.UpdateParams{
		Title: "Dentist (local)", StartTime: evt.StartTime, EndTime: evt.EndTime, Status: evt.Status, Transp: evt.Transp, Class: evt.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || res.Conflicts != 1 || len(res.Errors) != 0 {
		t.Fatalf("push against an unencodable server body = %+v, %v; want one recorded conflict", res, err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(conflicts) != 1 || conflicts[0].ServerIcal != "" || conflicts[0].MoveSourceCalendarID != nil {
		t.Fatalf("conflicts on A = %+v, %v; want one edit conflict with an empty server body", conflicts, err)
	}
	if !strings.Contains(conflicts[0].LocalIcal, "SUMMARY:Dentist (local)") {
		t.Fatalf("recorded local body = %q, want the local edit", conflicts[0].LocalIcal)
	}
}

// TestResolveMoveConflictRefusesLocalAfterAMoveBack completes a move with
// the local pick and the push, then moves the item back. The source holds
// a fresh unpushed row. A repeat local pick must refuse, so the stale
// etag never lands on that row and the create PUT stays unconditional.
func TestResolveMoveConflictRefusesLocalAfterAMoveBack(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push after the local pick = %+v, %v; want one push", res, err)
	}
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, moved, calA)
	before, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || before.RemoteUrl != "" || before.Dirty != 1 {
		t.Fatalf("sync resource A after the move back = %+v, %v; want a dirty unpushed row", before, err)
	}

	// The move back starts a new history, so it deletes the resolved row.
	if _, err := q.GetSyncConflict(ctx, c.ID); err == nil {
		t.Fatal("the resolved move row survives the move back")
	}
	if _, err = svc.ResolveConflict(ctx, c.ID, "local"); err == nil {
		t.Fatal("repeat local pick after a move back succeeded, want a refusal")
	}
	after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || after.Etag != "" {
		t.Fatalf("sync resource A etag = %q, %v; want no etag on the unpushed row", after.Etag, err)
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
}

// TestMoveConflictRecordWaitsForAnOpenEditConflict meets an open edit
// conflict on the landed moved copy when the source DELETE would record
// the move. The edit row must stay as it is, and the source DELETE must
// wait. After the edit conflict is resolved, the pass records the move.
func TestMoveConflictRecordWaitsForAnOpenEditConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: "target edit", ServerEtag: `"target"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")

	_, deletesBefore := srv.counts()
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A under an edit conflict of the copy = %+v, %v; want a quiet pass", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: the source DELETE waits for the edit conflict", deletes, deletesBefore)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(conflicts) != 1 || conflicts[0].MoveSourceCalendarID != nil || conflicts[0].ServerIcal != "target edit" {
		t.Fatalf("conflicts on B = %+v, %v; want the edit conflict unchanged", conflicts, err)
	}
	// The record query itself leaves an open edit row alone too.
	source := calA
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "moved", ServerIcal: "source edit", ServerEtag: `"source"`, MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict (move): %v", err)
	}
	conflicts, err = q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(conflicts) != 1 || conflicts[0].MoveSourceCalendarID != nil || conflicts[0].ServerIcal != "target edit" {
		t.Fatalf("conflicts on B after a move record = %+v, %v; want the edit conflict unchanged", conflicts, err)
	}

	if err := engine.markConflictResolved(ctx, calB, evt.UID, ResolutionServer); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}
	res, err = engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 {
		t.Fatalf("pass of A after the edit resolution = %+v, %v; want one move conflict", res, err)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)
}

// TestMootMoveConflictClosesWhenTheMovedCopyIsGone records a move conflict
// and then deletes the moved copy before any pass. The conflict has no local
// side any more. The next pass must close it as server-auto and send the
// source DELETE. It must keep the remote edit on the 412 and bring the item
// back into the source calendar. A later server pick must not run on the
// closed row.
func TestMootMoveConflictClosesWhenTheMovedCopyIsGone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}

	// The delete of the unpushed copy closes its move conflict as
	// abandoned at once, so the pass of A keeps the remote edit only.
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.AutoResolved != 1 {
		t.Fatalf("pass of A = %+v, %v; want the remote edit kept", res, err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.ResolvedAt == nil || row.Resolution == nil || *row.Resolution != ResolutionServerAuto || row.MoveState != MoveStateAbandoned {
		t.Fatalf("conflict after the pass = %+v, %v; want abandoned as server-auto", row, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
	for _, pick := range []string{"server", "local"} {
		if _, err := svc.ResolveConflict(ctx, c.ID, pick); err == nil || !strings.Contains(err.Error(), "abandoned") {
			t.Fatalf("%s pick on an abandoned move = %v, want a refusal", pick, err)
		}
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none: no restore beside the trashed copy", n)
	}
}

// TestServerWinsReportsAnUnencodableServerBody runs a server-wins push
// against a server body that the encoder rejects. The record tolerates
// the body, but the pass cannot adopt it. The pass must leave the row
// open and report an error.
func TestServerWinsReportsAnUnencodableServerBody(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	bad := regexp.MustCompile(`(?m)^PRODID:[^\r\n]*\r?\n`).ReplaceAllString(srv.body(path), "")
	srv.setRemoteBody(path, bad)
	if _, err := engine.events.Update(ctx, evt.ID, event.UpdateParams{
		Title: "Dentist (local)", StartTime: evt.StartTime, EndTime: evt.EndTime, Status: evt.Status, Transp: evt.Transp, Class: evt.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	res, err := engine.SyncCalendar(ctx, calA, ConflictServerWins)
	if err != nil || res.Conflicts != 1 || res.AutoResolved != 0 || len(res.Errors) == 0 {
		t.Fatalf("server-wins pass = %+v, %v; want the row left open with an error", res, err)
	}
	if !strings.Contains(res.Errors[0].Error(), "encode") {
		t.Fatalf("error = %q, want the encode failure", res.Errors[0])
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts on A = %+v, %v; want one open", conflicts, err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.Title != "Dentist (local)" {
		t.Fatalf("event = %+v, %v; want the local edit kept", got, err)
	}
}

// TestDirtyLandedCopyKeepsItsRemoteEditForThePush holds a local edit on a
// landed moved copy under a move conflict while the target object changes on
// the server. The pull must skip the import and leave the stale etag. The
// push after the local pick then gets 412 and records the remote edit of the
// target object as an edit conflict. No edit is lost.
func TestDirtyLandedCopyKeepsItsRemoteEditForThePush(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := engine.events.Update(ctx, moved.ID, event.UpdateParams{
		Title: "Dentist (local)", StartTime: moved.StartTime, EndTime: moved.EndTime, Status: moved.Status, Transp: moved.Transp, Class: moved.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pathB := "/cal/b/" + evt.UID + ".ics"
	srv.editRemoteSummary(pathB, "Dentist (edited on the server)")
	data, err := parseICalData([]byte(srv.body(pathB)))
	if err != nil {
		t.Fatalf("parse the server body: %v", err)
	}
	_, imported, pullWarnings, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: pathB, href: pathB, etag: srv.etag(pathB), data: data})
	if err != nil || imported {
		t.Fatalf("import over a dirty moved copy = %v, %v; want no import", imported, err)
	}
	if len(pullWarnings) != 1 || !strings.Contains(pullWarnings[0].Message, "moved copy changed on the server") {
		t.Fatalf("pull warnings = %v, want one warning about the diverged copy", pullWarnings)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.Title != "Dentist (local)" {
		t.Fatalf("event after the pull = %+v, %v; want the local edit kept", got, err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || res.Conflicts != 1 {
		t.Fatalf("push of B after the local pick = %+v, %v; want an edit conflict", res, err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(conflicts) != 1 || conflicts[0].MoveSourceCalendarID != nil {
		t.Fatalf("conflicts on B = %+v, %v; want one edit conflict", conflicts, err)
	}
	if !strings.Contains(conflicts[0].ServerIcal, "SUMMARY:Dentist (edited on the server)") || !strings.Contains(conflicts[0].LocalIcal, "SUMMARY:Dentist (local)") {
		t.Fatalf("edit conflict bodies = server %q, local %q; want both edits recorded", conflicts[0].ServerIcal, conflicts[0].LocalIcal)
	}
}

// TestDeleteOfMovedTodoFreesTheSourceDelete moves a todo from A to B and
// deletes the copy in B before any sync. The todo delete must not leave
// an unpushed sync row in B behind, so the source DELETE of A goes out.
func TestDeleteOfMovedTodoFreesTheSourceDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	td, err := engine.todos.Create(ctx, todo.CreateParams{CalendarID: calA, Summary: "Floss"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("initial push = %+v, %v; want one clean push", res, err)
	}
	if _, err := engine.todos.Update(ctx, td.ID, todo.UpdateParams{Summary: td.Summary, Status: td.Status, CalendarID: calB}); err != nil {
		t.Fatalf("Update (move): %v", err)
	}
	if err := engine.todos.Delete(ctx, td.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: td.UID}); err == nil {
		t.Fatalf("sync resource B = %+v, want none after the delete of an unpushed copy", sr)
	}

	if res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of B = %+v, %v; want a clean pass", res, err)
	}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A = %+v, %v; want the source delete", res, err)
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("server objects = %v, want none", got)
	}
}

// TestImportAfterDeleteKeepsTheServerObject deletes a pushed event and
// imports the same UID again before any sync. The mark of the new copy must
// clear the pending delete. The push then sends the copy, and the tombstone
// pass does not delete the server object under live rows.
func TestImportAfterDeleteKeepsTheServerObject(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want one", ts, err)
	}
	// The ical import runs UpsertByUID, clears the pending delete of the
	// UID, and marks the UID dirty (icaltransfer.Import). The sync package
	// cannot import that package, so the test runs the same three steps.
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Dentist (imported)", StartTime: evt.StartTime, EndTime: evt.EndTime,
	}); err != nil {
		t.Fatalf("UpsertByUID: %v", err)
	}
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("clear the pending delete: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calA, evt.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 {
		t.Fatalf("sync resource A = %+v, %v; want dirty", sr, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A after the import = %v, %v; want none", ts, err)
	}

	_, deletesBefore := srv.counts()
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Deleted != 0 {
		t.Fatalf("pass of A = %+v, %v; want one push and no delete", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: no delete under live rows", deletes, deletesBefore)
	}
	path := "/cal/a/" + evt.UID + ".ics"
	if !srv.has(path) || !strings.Contains(srv.body(path), "SUMMARY:Dentist (imported)") {
		t.Fatalf("server object = %q, want the imported body", srv.body(path))
	}
}

// TestResolveMoveConflictPickServerReopensOnAFailedImport cancels a move
// while the import of the server body fails. The row must open again, so
// the user can run the pick again, and the second run must complete the
// cancel.
func TestResolveMoveConflictPickServerReopensOnAFailedImport(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil || !strings.Contains(err.Error(), "persist failed") {
		t.Fatalf("server pick with a failed import = %v, want the failure", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.ResolvedAt != nil || row.MoveState != MoveStateCancelFailed {
		t.Fatalf("conflict after the failed import = %+v, %v; want it open as cancel-failed", row, err)
	}
	engine.testHooks = nil

	// Passes of both calendars leave the row alone: the moot step must
	// not close a cancel that waits for its replay.
	for _, cal := range []int64{calA, calB} {
		if res, err := engine.SyncCalendar(ctx, cal, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.AutoResolved != 0 {
			t.Fatalf("pass of %d between the failed cancel and the replay = %+v, %v; want a quiet pass", cal, res, err)
		}
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.AutoResolved != 0 {
		t.Fatalf("push of B between the failed cancel and the replay = %+v, %v; want no auto resolution", res, err)
	}
	row, err = q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.ResolvedAt != nil || row.MoveState != MoveStateCancelFailed {
		t.Fatalf("conflict after the passes = %+v, %v; want still open as cancel-failed", row, err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("server pick again: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the second pick = %+v, %v; want it in A with the remote title", got, err)
	}
	row, err = q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.Resolution == nil || *row.Resolution != ResolutionServer {
		t.Fatalf("conflict after the second pick = %+v, %v; want resolved as server", row, err)
	}
}

// TestResolveMoveConflictPickServerFetchesTheCurrentSource edits the
// source object on the server again after the record. The server pick
// must read the current object, so the source calendar shows the newer
// body and the newer etag.
func TestResolveMoveConflictPickServerFetchesTheCurrentSource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.editRemoteSummary(path, "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	srv.editRemoteSummary(path, "Dentist (remote 2)")

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (remote 2)" {
		t.Fatalf("event after the cancel = %+v, %v; want the newer remote body in A", got, err)
	}
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if want := strings.Trim(srv.etag(path), `"`); err != nil || srA.Etag != want || srA.Dirty != 0 {
		t.Fatalf("sync resource A = %+v, %v; want clean with the current etag %q", srA, err, want)
	}
}

// TestResolveMoveConflictRefusesCancelAfterARestore cancels a move,
// restores the local body with the local pick, and asks for the server
// side again. The cancel state comes from the rows, not from the last
// pick, so the repeat must refuse and keep the restored edit.
func TestResolveMoveConflictRefusesCancelAfterARestore(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local after the cancel: %v", err)
	}
	restored, err := engine.events.Get(ctx, evt.ID)
	if err != nil || restored.CalendarID != calA || restored.Title != "Dentist" {
		t.Fatalf("event after the restore = %+v, %v; want the recorded local body in A", restored, err)
	}

	_, err = svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Fatalf("server pick after a restore = %v, want a refusal", err)
	}
	after, err := engine.events.Get(ctx, evt.ID)
	if err != nil || after.Title != "Dentist" {
		t.Fatalf("event after the refused cancel = %+v, %v; want the restored body kept", after, err)
	}
}

// TestCancelRecordsTheLiveBodyOfACleanLandedCopy cancels a move whose
// landed copy is clean but holds a body that a pull refreshed from the
// target object. The cancel must record that live body, so the local
// pick restores it in the source calendar.
func TestCancelRecordsTheLiveBodyOfACleanLandedCopy(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	pathB := "/cal/b/" + evt.UID + ".ics"
	srv.editRemoteSummary(pathB, "Dentist (edited on the server)")
	data, err := parseICalData([]byte(srv.body(pathB)))
	if err != nil {
		t.Fatalf("parse the server body: %v", err)
	}
	if _, imported, _, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: pathB, href: pathB, etag: srv.etag(pathB), data: data}); err != nil || !imported {
		t.Fatalf("import of the landed copy = %v, %v; want an import", imported, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || sr.Dirty != 0 {
		t.Fatalf("sync resource B = %+v, %v; want clean", sr, err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || !strings.Contains(row.LocalIcal, "SUMMARY:Dentist (edited on the server)") {
		t.Fatalf("recorded local body after the cancel = %q, %v; want the live body of the copy", row.LocalIcal, err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local after the cancel: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (edited on the server)" {
		t.Fatalf("event after the restore = %+v, %v; want the live body in A", got, err)
	}
}

// TestMoveToLocalCalendarRecordsAMoveConflict moves an event into a local
// calendar (no account) and edits the source object on the server. The
// source DELETE gets 412. The pass must record a move conflict on the
// local calendar, and the local pick must then delete the source object.
func TestMoveToLocalCalendarRecordsAMoveConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	local, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, local.ID)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 || res.Deleted != 0 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, local.ID, evt.UID)
	if !strings.Contains(c.LocalIcal, "SUMMARY:Dentist\r") || !strings.Contains(c.ServerIcal, "SUMMARY:Dentist (remote)") {
		t.Fatalf("conflict bodies = local %q, server %q; want the moved body and the remote edit", c.LocalIcal, c.ServerIcal)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none: the remote edit must not come back", n)
	}
	// A second pass of A leaves the conflict alone: the moved copy exists
	// as live rows in the local calendar.
	res, err = engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.AutoResolved != 0 || res.Deleted != 0 {
		t.Fatalf("second pass of A = %+v, %v; want a quiet pass", res, err)
	}
	if again := wantMoveConflict(t, q, calA, local.ID, evt.UID); again.DetectedAt != c.DetectedAt {
		t.Fatalf("detected_at = %s, want the first record %s", again.DetectedAt, c.DetectedAt)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	res, err = engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A after the local pick = %+v, %v; want the source delete", res, err)
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("server objects = %v, want none", got)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != local.ID {
		t.Fatalf("event = %+v, %v; want it in the local calendar", got, err)
	}
	// The move is finished. A second local pick must refuse, so the
	// moved body does not come back into A beside the local copy.
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "finished") {
		t.Fatalf("second local pick on a finished move = %v, want a refusal", err)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A after the refused pick = %d, want none", n)
	}
	if n := countRows(t, engine, "events", local.ID, evt.UID); n != 1 {
		t.Fatalf("event rows in the local calendar = %d, want one", n)
	}
}

// TestMoveToLocalCalendarCancelMovesBack records a move conflict on a
// local calendar and cancels the move. The rows must go back to the
// source calendar with the server body.
func TestMoveToLocalCalendarCancelMovesBack(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	local, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, local.ID)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, local.ID, evt.UID)

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the cancel = %+v, %v; want it in A with the remote title", got, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Pushed != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A after the cancel = %+v, %v; want a quiet pass", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
}

// TestCancelRecordsTheFreshEtagForALaterRestore edits the source object on
// the server again after the record, cancels the move, and then restores
// the local body. The cancel must record the etag it fetched, so the push
// of the restored body matches the source object.
func TestCancelRecordsTheFreshEtagForALaterRestore(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.editRemoteSummary(path, "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	srv.editRemoteSummary(path, "Dentist (remote 2)")

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if want := strings.Trim(srv.etag(path), `"`); err != nil || row.ServerEtag != want || !strings.Contains(row.ServerIcal, "SUMMARY:Dentist (remote 2)") {
		t.Fatalf("conflict after the cancel = etag %q, %v; want the fetched etag %q and body", row.ServerEtag, err, want)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local after the cancel: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push of A after the restore = %+v, %v; want one clean push", res, err)
	}
	if body := srv.body(path); !strings.Contains(body, "SUMMARY:Dentist\r") {
		t.Fatalf("server body = %q, want the restored local body", body)
	}
}

// TestPushSkipsAnUnpushedCopyUnderAnEditConflict holds an open edit conflict
// on the unpushed moved copy. The push must skip the row under every
// strategy and send nothing. A create cannot adopt a server body, and a move
// record must not replace the edit conflict.
func TestPushSkipsAnUnpushedCopyUnderAnEditConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")

	putsBefore, deletesBefore := srv.counts()
	// A prompt pass and the opportunistic push skip the row. A server-wins
	// pass settles it (TestServerWinsSettlesAMovedCopyUnderAnEditConflict).
	for _, strategy := range []ConflictStrategy{ConflictPrompt} {
		res, err := engine.SyncCalendar(ctx, calB, strategy)
		if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Pushed != 0 || res.SkippedConflicts != 1 {
			t.Fatalf("pass of B under %s = %+v, %v; want one skipped row", strategy, res, err)
		}
	}
	if puts, deletes := srv.counts(); puts != putsBefore || deletes != deletesBefore {
		t.Fatalf("PUTs, DELETEs = %d, %d; want %d, %d: the prompt pass waits for the edit conflict", puts, deletes, putsBefore, deletesBefore)
	}
	// The opportunistic push sends the row again, as for every edit
	// conflict. The refused source DELETE must not replace the edit
	// conflict with a move record: the push skips the row.
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.SkippedConflicts != 1 || res.Conflicts != 0 {
		t.Fatalf("opportunistic push of B = %+v, %v; want one skipped row", res, err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(conflicts) != 1 || conflicts[0].MoveSourceCalendarID != nil || conflicts[0].ServerIcal != "edit" {
		t.Fatalf("conflicts on B = %+v, %v; want the edit conflict unchanged", conflicts, err)
	}
	// The record itself reports the held row.
	source := calA
	n, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "moved", ServerIcal: "source edit", ServerEtag: `"source"`, MoveSourceCalendarID: &source,
	})
	if err != nil || n != 0 {
		t.Fatalf("move record over an open edit conflict = %d rows, %v; want none", n, err)
	}
}

// seedSecondAccount creates a second account on srv with two linked
// calendars, for a move between accounts.
func seedSecondAccount(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer) (calC, calD int64) {
	t.Helper()
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "other", ServerUrl: srv.srv.URL, AuthType: "basic", Username: "user2",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	engine.credStore.(*mockCredStore).creds[account.ID] = auth.Credential{AccountID: account.ID, Username: "user2", Password: "pw"}
	link := func(name string) int64 {
		cal, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: name})
		if err != nil {
			t.Fatalf("CreateCalendar %s: %v", name, err)
		}
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID: cal.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/" + name + "/"),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount %s: %v", name, err)
		}
		if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET sync_token = 'tok0' WHERE id = ?`, cal.ID); err != nil {
			t.Fatalf("seed sync token %s: %v", name, err)
		}
		return cal.ID
	}
	return link("c"), link("d")
}

// seedMoveConflictRows writes the rows of a move conflict by hand. It writes
// a pushed source row and a move tombstone in sourceID, and the conflict row
// on targetID with the etag of the source object.
func seedMoveConflictRows(t *testing.T, engine *Engine, q *storage.Queries, sourceID, targetID int64, uid string) int64 {
	t.Helper()
	ctx := context.Background()
	href := "/cal/x/" + uid + ".ics"
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: sourceID, Uid: uid, OwnerType: "event", RemoteUrl: href, Etag: "e1", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (?, ?, ?, ?)`, sourceID, uid, href, targetID); err != nil {
		t.Fatalf("insert tombstone: %v", err)
	}
	// The moved copy waits as live rows and a dirty row without a server
	// object.
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: uid, CalendarID: targetID, Title: "Moved", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID target: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: targetID, Uid: uid, OwnerType: "event", RemoteUrl: "", Etag: "", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource target: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: targetID, OwnerType: "event", OwnerID: 1, Uid: uid,
		LocalIcal: "local", ServerIcal: "server", ServerEtag: "e2", MoveSourceCalendarID: &sourceID,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, targetID)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, %v; want one", conflicts, err)
	}
	return conflicts[0].ID
}

// TestResolveMoveConflictRefusesLocalAfterFinishAndTrash completes a move
// with the local pick and the push, then deletes the moved copy. The
// state is finished, so a second local pick must refuse. Nothing comes
// back into the source calendar, and no etag lands on a row without a
// server object.
func TestResolveMoveConflictRefusesLocalAfterFinishAndTrash(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateLocalPicked {
		t.Fatalf("state after the local pick = %q, %v; want local-picked", row.MoveState, err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push after the local pick = %+v, %v; want one push", res, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateFinished {
		t.Fatalf("state after the push = %q, %v; want finished", row.MoveState, err)
	}
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}

	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	if err == nil || !strings.Contains(err.Error(), "finished") {
		t.Fatalf("local pick on a finished move = %v, want a refusal", err)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none", n)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatalf("sync resource A = %+v, want none", sr)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil || !strings.Contains(err.Error(), "finished") {
		t.Fatalf("server pick on a finished move = %v, want a refusal", err)
	}
}

// TestResolveMoveConflictLocalAfterFailedCancelRestores cancels a move
// while the import fails. The row waits as cancel-failed with the rows in
// the source calendar. The local pick must restore the recorded local
// body there, not stamp a local pick on a move that is over.
func TestResolveMoveConflictLocalAfterFailedCancelRestores(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil {
		t.Fatal("server pick with a failed import succeeded")
	}
	engine.testHooks = nil
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("state after the failed cancel = %q, %v; want cancel-failed and open", row.MoveState, err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("local pick after the failed cancel: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist" {
		t.Fatalf("event after the restore = %+v, %v; want the recorded local body in A", got, err)
	}
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || srA.Dirty != 1 {
		t.Fatalf("sync resource A = %+v, %v; want dirty", srA, err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of A after the restore = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
}

// TestResolveMoveConflictPickServerRestoresTheSourceRow cancels a move
// after the source calendar lost its sync row. The source object exists
// on the server, so the cancel must create the row again from the
// tombstone with the fetched etag. The next push of a local edit is then
// an update of that object, and the server holds one object.
func TestResolveMoveConflictPickServerRestoresTheSourceRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.editRemoteSummary(path, "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("DeleteSyncResource A: %v", err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if want := strings.Trim(srv.etag(path), `"`); err != nil || srA.RemoteUrl == "" || srA.Etag != want || srA.Dirty != 0 {
		t.Fatalf("sync resource A = %+v, %v; want a clean row with the href and the etag %q", srA, err, want)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := engine.events.Update(ctx, got.ID, event.UpdateParams{
		Title: "Dentist (local edit)", StartTime: got.StartTime, EndTime: got.EndTime, Status: got.Status, Transp: got.Transp, Class: got.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push of A = %+v, %v; want one clean update", res, err)
	}
	wantOnlyServerObject(t, srv, path)
	if body := srv.body(path); !strings.Contains(body, "SUMMARY:Dentist (local edit)") {
		t.Fatalf("server body = %q, want the local edit", body)
	}
}

// TestResolveMoveConflictsAcrossAccountsDoNotDeadlock resolves two move
// conflicts at once whose source and target accounts are swapped. Both
// resolves must complete: the locks of the two accounts are taken in one
// order.
func TestResolveMoveConflictsAcrossAccountsDoNotDeadlock(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC, calD := seedSecondAccount(t, engine, q, srv)

	x := seedMoveConflictRows(t, engine, q, calA, calD, "uid-x")
	y := seedMoveConflictRows(t, engine, q, calC, calB, "uid-y")

	done := make(chan error, 2)
	for _, id := range []int64{x, y} {
		go func(id int64) {
			_, err := svc.ResolveConflict(ctx, id, "local")
			done <- err
		}(id)
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("ResolveConflict: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the two resolves did not complete: a lock order deadlock")
		}
	}
	for _, id := range []int64{x, y} {
		if row, err := q.GetSyncConflict(ctx, id); err != nil || row.MoveState != MoveStateLocalPicked {
			t.Fatalf("conflict %d state = %q, %v; want local-picked", id, row.MoveState, err)
		}
	}
}

// TestReplayCancelKeepsTheRecordedLocalBody cancels a move while the
// import fails, lets a pull write the server body over the rows, and
// runs the cancel again. The second run must keep the recorded local
// body of the first cancel, not the live rows that hold the server body.
func TestReplayCancelKeepsTheRecordedLocalBody(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.editRemoteSummary(path, "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil {
		t.Fatal("server pick with a failed import succeeded")
	}
	engine.testHooks = nil
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || !strings.Contains(row.LocalIcal, "SUMMARY:Dentist\r") {
		t.Fatalf("recorded local body after the first cancel = %q, %v; want the moved body", row.LocalIcal, err)
	}

	// A pull of A writes the server body over the rows.
	data, err := parseICalData([]byte(srv.body(path)))
	if err != nil {
		t.Fatalf("parse the server body: %v", err)
	}
	if _, imported, _, err := engine.importFetchedResource(ctx, calA, nil, fetchedResource{path: path, href: path, etag: srv.etag(path), data: data}); err != nil || !imported {
		t.Fatalf("import into A = %v, %v; want an import", imported, err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the pull = %+v, %v; want the server body", got, err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("server pick again: %v", err)
	}
	row, err = q.GetSyncConflict(ctx, c.ID)
	if err != nil || !strings.Contains(row.LocalIcal, "SUMMARY:Dentist\r") || row.MoveState != MoveStateCancelled {
		t.Fatalf("recorded local body after the replay = %q (state %q), %v; want the moved body kept", row.LocalIcal, row.MoveState, err)
	}
}

// TestServerDeleteOfTheSourceFinishesAnOpenMove meets a server-reported
// delete of the source object while the move conflict is open. The pull must
// mark the move finished with a resolution. The next push of the target then
// creates the moved copy, and no wait without end occurs.
func TestServerDeleteOfTheSourceFinishesAnOpenMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	// The server deletes the source object, and the pull of A reports it.
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource A: %v", err)
	}
	deletions := newPendingDeletions(engine.logger)
	deletions.markExplicit(srA)
	if _, failed, _ := deletions.apply(ctx, engine, calA, nil); failed != 0 {
		t.Fatalf("apply failed = %d, want 0", failed)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateFinished || row.ResolvedAt == nil || row.Resolution == nil || *row.Resolution != ResolutionLocal {
		t.Fatalf("conflict after the server delete = %+v, %v; want finished and resolved as local", row, err)
	}

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.SkippedConflicts != 0 {
		t.Fatalf("push of B after the finish = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	for _, pick := range []string{"local", "server"} {
		if _, err := svc.ResolveConflict(ctx, c.ID, pick); err == nil || !strings.Contains(err.Error(), "finished") {
			t.Fatalf("%s pick on a finished move = %v, want a refusal", pick, err)
		}
	}
}

// TestCancelRefusesWhenTheSourceObjectIsGone asks for the server side
// after the source object left the server. A cancel would restore a dead
// href. The pick must refuse, mark the move finished with a resolution,
// and the next push must create the moved copy.
func TestCancelRefusesWhenTheSourceObjectIsGone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()

	out, err := svc.ResolveConflictOutcome(ctx, c.ID, "server")
	if err != nil || !out.MoveFinished || out.Note != "the source is gone, so the move finished; the next sync creates the moved copy" {
		t.Fatalf("server pick with the source gone = %+v, %v; want the finished outcome", out, err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateFinished || row.ResolvedAt == nil {
		t.Fatalf("conflict after the pick = %+v, %v; want finished and resolved", row, err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("event after the refusal = %+v, %v; want it still in B", got, err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestLocalPickRefusesWhenTheMovedCopyIsGone asks for the local side
// after the moved copy went to the trash. Nothing is left to create, so
// the pick must refuse and close the row as cancelled, as the moot step
// does. The remote edit then stays on the server.
func TestLocalPickRefusesWhenTheMovedCopyIsGone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}

	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	if err == nil || !strings.Contains(err.Error(), "moved copy") {
		t.Fatalf("local pick with the copy gone = %v, want a refusal", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateAbandoned || row.Resolution == nil || *row.Resolution != ResolutionServerAuto {
		t.Fatalf("conflict after the refusal = %+v, %v; want abandoned as server-auto", row, err)
	}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.AutoResolved != 1 || res.Deleted != 0 {
		t.Fatalf("pass of A = %+v, %v; want the remote edit kept", res, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}
}

// TestRestoreLocalVersionNamesThePlannedHrefOnACreatedRow resolves an edit
// conflict for the local side after the sync row went away. The record
// names the server object, so the row must take the planned href and the
// recorded etag. The push must update the object with If-Match: a create
// with If-None-Match gets 412 from the object (issue #866).
func TestRestoreLocalVersionNamesThePlannedHrefOnACreatedRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	local, err := engine.exportResource(ctx, calA, "event", evt.UID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: string(local), ServerIcal: "server", ServerEtag: srv.etag(path),
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, %v; want one", conflicts, err)
	}
	if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("DeleteSyncResource: %v", err)
	}

	if _, err := svc.ResolveConflict(ctx, conflicts[0].ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Etag != srv.etag(path) || sr.Dirty != 1 || sr.RemoteUrl != path {
		t.Fatalf("sync resource after the restore = %+v, %v; want a dirty row with the planned href and the recorded etag", sr, err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push after the restore = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, path)
}

// TestDeleteDuringTheFirstPutQueuesTheDeleteOfTheNewObject deletes an
// unpushed event while its first PUT is in flight. The soft delete removes
// the unpushed row, so the finalize finds no row. The push must queue a
// tombstone for the new href. The next pass must delete the object, so no
// pull brings the item back.
func TestDeleteDuringTheFirstPutQueuesTheDeleteOfTheNewObject(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{
		CalendarID: calA, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete during the PUT: %v", err)
		}
	}}
	// The write path pushes and then runs the tombstone pass, so the
	// queued delete of the new href goes out in the same call.
	res, err := engine.PushLocalEdits(ctx, calA)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Deleted != 1 {
		t.Fatalf("push with a delete in flight = %+v, %v; want the push and the delete of the new object", res, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatalf("sync resource = %+v, want none after the delete", sr)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none after the delete", ts, err)
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("server objects = %v, want none", got)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 1 {
		t.Fatalf("event rows in A = %d, want the row in the trash", n)
	}
	// A later pass brings nothing back.
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Pulled != 0 || res.Pushed != 0 {
		t.Fatalf("later pass of A = %+v, %v; want a quiet pass", res, err)
	}
}

// TestUnpushedRowDeleteKeepsAFreshMark deletes the unpushed sync row of a
// gone resource with the rev that the push read. A mark that landed
// after the read bumps rev, so the delete must leave that row alone.
func TestUnpushedRowDeleteKeepsAFreshMark(t *testing.T) {
	t.Parallel()
	_, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calA, Uid: "ghost-uid", OwnerType: "event", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	before, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: "ghost-uid"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	// A fresh mark lands after the read.
	if err := q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: calA, Uid: "ghost-uid"}); err != nil {
		t.Fatalf("MarkSyncResourceDirty: %v", err)
	}
	if _, err := q.DeleteUnpushedSyncResource(ctx, storage.DeleteUnpushedSyncResourceParams{CalendarID: calA, Uid: "ghost-uid", Rev: before.Rev}); err != nil {
		t.Fatalf("DeleteUnpushedSyncResource: %v", err)
	}
	after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: "ghost-uid"})
	if err != nil || after.Dirty != 1 {
		t.Fatalf("sync resource after the stale delete = %+v, %v; want the fresh mark kept", after, err)
	}
	if _, err := q.DeleteUnpushedSyncResource(ctx, storage.DeleteUnpushedSyncResourceParams{CalendarID: calA, Uid: "ghost-uid", Rev: after.Rev}); err != nil {
		t.Fatalf("DeleteUnpushedSyncResource (current rev): %v", err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: "ghost-uid"}); err == nil {
		t.Fatalf("sync resource after the delete at the current rev = %+v, want none", sr)
	}
}

// TestLookupErrorBlocksImportWithoutADivergedWarning makes the sync row
// lookup of a move conflict fail. The import must stay blocked, and the
// pull must not report the copy as diverged.
func TestLookupErrorBlocksImportWithoutADivergedWarning(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)

	if _, err := engine.db.ExecContext(ctx, `ALTER TABLE sync_resources RENAME TO sync_resources_gone`); err != nil {
		t.Fatalf("rename sync_resources: %v", err)
	}
	blocked, moveRow := engine.openConflictBlocksImport(ctx, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `ALTER TABLE sync_resources_gone RENAME TO sync_resources`); err != nil {
		t.Fatalf("rename sync_resources back: %v", err)
	}
	if !blocked || moveRow {
		t.Fatalf("lookup error = blocked %v, moveRow %v; want blocked without the diverged warning", blocked, moveRow)
	}
}

// TestCancelRefusesWithoutAnEtag asks for the server side of a move
// conflict on a server that gives no etag. The cancel must refuse, as the
// local pick does, and change nothing.
func TestCancelRefusesWithoutAnEtag(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	srv.setNoGetEtag(true)

	_, err := svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "no etag") {
		t.Fatalf("server pick without an etag = %v, want a refusal", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calB {
		t.Fatalf("event after the refusal = %+v, %v; want it still in B", got, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateOpen || row.ResolvedAt != nil {
		t.Fatalf("conflict after the refusal = %+v, %v; want still open", row, err)
	}
}

// TestNewMoveDropsTheStaleCancelledRow cancels a move and then moves the
// item to the same calendar again. The new move must delete the
// cancelled row, so no later local pick restores the old body beside the
// moved item. A cancelled row that outlives a live copy still refuses.
func TestNewMoveDropsTheStaleCancelledRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	back, err := engine.events.Get(ctx, evt.ID)
	if err != nil || back.CalendarID != calA {
		t.Fatalf("event after the cancel = %+v, %v; want it in A", back, err)
	}

	// The item moves to B again.
	moveEvent(t, engine, back, calB)
	if _, err := q.GetSyncConflict(ctx, c.ID); err == nil {
		t.Fatal("the cancelled row survives a new move of the UID into the same calendar")
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil {
		t.Fatal("local pick on a deleted row succeeded")
	}
	syncPairClean(t, engine, calA, calB)
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none", n)
	}

	// Defensive: a cancelled row beside a live copy in the target refuses
	// the restore.
	source := calA
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "old", ServerIcal: "old", ServerEtag: "e9", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if _, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: MoveStateCancelled, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &calA}); err != nil {
		t.Fatalf("SetMoveConflictState: %v", err)
	}
	if err := engine.markConflictResolved(ctx, calB, evt.UID, ResolutionServer); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}
	stale := wantMoveConflictAny(t, q, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, stale.ID, "local"); err == nil || !strings.Contains(err.Error(), "live copy") {
		t.Fatalf("local pick on a cancelled row beside a live copy = %v, want a refusal", err)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A after the refusal = %d, want none", n)
	}
}

// wantMoveConflictAny returns the one move conflict row of uid on calID,
// open or resolved.
func wantMoveConflictAny(t *testing.T, q *storage.Queries, calID int64, uid string) storage.SyncConflict {
	t.Helper()
	rows, err := q.ListResolvedSyncConflicts(context.Background())
	if err != nil {
		t.Fatalf("ListResolvedSyncConflicts: %v", err)
	}
	for _, r := range rows {
		if r.CalendarID == calID && r.Uid == uid && r.MoveSourceCalendarID != nil {
			return r
		}
	}
	t.Fatalf("no resolved move conflict of %s on %d", uid, calID)
	return storage.SyncConflict{}
}

// TestDeleteDuringAPullKeepsItsTombstone deletes a pushed event after the
// pull read the tombstones and before the persist of its body. The
// persist runs through UpsertByUID, which must not clear the tombstone,
// so the pending server DELETE survives.
func TestDeleteDuringAPullKeepsItsTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.editRemoteSummary(path, "Dentist (remote)")
	data, err := parseICalData([]byte(srv.body(path)))
	if err != nil {
		t.Fatalf("parse the server body: %v", err)
	}
	// The delete lands after the pull read the tombstones (nil here).
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, _, err := engine.importFetchedResource(ctx, calA, nil, fetchedResource{path: path, href: path, etag: srv.etag(path), data: data}); err != nil {
		t.Fatalf("importFetchedResource: %v", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A after the persist = %v, %v; want the pending delete kept", ts, err)
	}
}

// TestCancelOfALandedMoveLeavesAPlainTombstone cancels a landed move and
// then meets a remote edit of the target copy. The cleanup delete of that
// copy follows the delete-vs-edit rule: no reversed move conflict.
func TestCancelOfALandedMoveLeavesAPlainTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID != nil {
		t.Fatalf("tombstone of the landed copy = %+v, %v; want a plain delete", ts, err)
	}

	srv.editRemote("/cal/b/" + evt.UID + ".ics")
	res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.AutoResolved != 1 {
		t.Fatalf("pass of B = %+v, %v; want the remote edit kept and no reversed move conflict", res, err)
	}
	for _, cal := range []int64{calA, calB} {
		if open, err := q.ListSyncConflictsByCalendar(ctx, cal); err != nil || len(open) != 0 {
			t.Fatalf("open conflicts on %d = %v, %v; want none", cal, open, err)
		}
	}
}

// TestLocalPickRecreatesAMissingSourceTombstone resolves an open move for
// the local side after the source lost its tombstone. The pick must write
// the move tombstone again, so the refused create sends the source DELETE
// and the move completes.
func TestLocalPickRecreatesAMissingSourceTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("drop the source tombstone: %v", err)
	}

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
		t.Fatalf("source tombstone after the pick = %+v, %v; want a move tombstone with the target B", ts, err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push after the pick = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestMoveIntoAReadOnlyCalendarIsRefused moves an event into a read-only
// calendar. The move must be refused up front, so the wait on a read-only
// target cannot start from a move.
func TestMoveIntoAReadOnlyCalendarIsRefused(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}
	evt := createPushedEvent(t, engine, calA)
	_, err := engine.events.Update(ctx, evt.ID, event.UpdateParams{
		Title: evt.Title, StartTime: evt.StartTime, EndTime: evt.EndTime, CalendarID: calB, Status: evt.Status, Transp: evt.Transp, Class: evt.Class,
	})
	if err == nil {
		t.Fatal("move into a read-only calendar succeeded, want a refusal")
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA {
		t.Fatalf("event after the refused move = %+v, %v; want it in A", got, err)
	}
}

// TestReadOnlyTargetRecordsAVisibleMoveConflict moves an event to a
// calendar that becomes read-only after the move. The pass of the source
// must record a move conflict instead of a silent wait, and the cancel
// must bring the item back.
func TestReadOnlyTargetRecordsAVisibleMoveConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 || res.Deleted != 0 {
		t.Fatalf("pass of A with a read-only target = %+v, %v; want one visible move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 0 || len(res.Errors) != 0 {
		t.Fatalf("second pass of A = %+v, %v; want no second record", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")

	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA {
		t.Fatalf("event after the cancel = %+v, %v; want it in A", got, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestFailedCancelClearsTheSourceDirtyFlag cancels a move while the
// import fails. The source row must lose its dirty flag, so no push
// sends a part of the body before the replay. The replay then completes
// the cancel.
func TestFailedCancelClearsTheSourceDirtyFlag(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	// A dirty source row before the cancel stands in for a part import.
	if err := q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("MarkSyncResourceDirty: %v", err)
	}
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil {
		t.Fatal("server pick with a failed import succeeded")
	}
	engine.testHooks = nil

	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || srA.Dirty != 0 {
		t.Fatalf("source row after the failed cancel = %+v, %v; want the dirty flag cleared", srA, err)
	}
	putsBefore, _ := srv.counts()
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Pushed != 0 {
		t.Fatalf("push of A after the failed cancel = %+v, %v; want nothing sent", res, err)
	}
	if puts, _ := srv.counts(); puts != putsBefore {
		t.Fatalf("PUTs = %d, want %d: no push of a part body", puts, putsBefore)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("server pick again: %v", err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the replay = %+v, %v; want the server body in A", got, err)
	}
}

// TestLegacyMoveTombstoneSettlesInline pushes the target half of a move
// whose source tombstone names no target (a move from before the upgrade).
// The source DELETE waits for the target (the fourth clause of
// move_wait_tombstones). The refused create of the target sends that delete
// itself, so the move completes on an iCloud-like server.
func TestLegacyMoveTombstoneSettlesInline(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET moved_to_calendar_id = NULL, legacy_move = 1 WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
		t.Fatalf("make the tombstone a legacy one: %v", err)
	}
	// The pass of A waits: the target has no server object yet.
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A before the target push = %+v, %v; want no delete", res, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone before the target holds the item")
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push of the target = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestMoveDuringTheFirstPutQueuesAMoveTombstone moves an unpushed event
// to another calendar while its first PUT is in flight. The row of the
// source goes with the move, so the finalize finds no row. The tombstone
// for the new href must name the calendar that holds the moved copy, so
// the refused create there settles the delete.
func TestMoveDuringTheFirstPutQueuesAMoveTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{
		CalendarID: calA, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		moveEvent(t, engine, evt, calB)
	}}
	// The push of A runs its own tombstone pass after the push. The
	// delete waits: the moved copy in B has no server object yet.
	res, err := engine.PushLocalEdits(ctx, calA)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Deleted != 0 {
		t.Fatalf("push with a move in flight = %+v, %v; want the push and a wait on the delete", res, err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
		t.Fatalf("tombstone of A = %+v, %v; want a move tombstone with the target B", ts, err)
	}

	res, err = engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("push of B = %+v, %v; want the settled move", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestGatedTargetRecordsAVisibleMoveConflict moves an event that another
// person organizes. The organizer gate never sends the create of the moved
// copy. The push must record a visible move conflict, not a silent wait, and
// the cancel must bring the item back.
func TestGatedTargetRecordsAVisibleMoveConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 0 || res.Conflicts != 1 {
		t.Fatalf("push of a gated target = %+v, %v; want one visible move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 0 || res.SkippedConflicts != 1 {
		t.Fatalf("second push of the gated target = %+v, %v; want the row skipped", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calA {
		t.Fatalf("event after the cancel = %+v, %v; want it in A", got, err)
	}
}

// TestDeletedLandedCopyAbandonsTheMove deletes the landed moved copy
// while its move conflict is open. The copy keeps its sync row with a
// plain tombstone, so the row alone proves nothing. The local pick must
// refuse and abandon the move, and the moot step must do the same.
func TestDeletedLandedCopyAbandonsTheMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the landed copy: %v", err)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil {
		t.Fatalf("sync resource B after the delete: %v, want it kept with a tombstone", err)
	}

	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	if err == nil || !strings.Contains(err.Error(), "abandoned") {
		t.Fatalf("local pick with the landed copy deleted = %v, want a refusal", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateAbandoned {
		t.Fatalf("conflict after the refusal = %+v, %v; want abandoned", row, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone")
	}

	// The moot step reaches the same state on its own.
	evt2 := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt2, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B (second event) = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt2.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A (second event) = %+v, %v; want one move conflict", res, err)
	}
	c2 := wantMoveConflict(t, q, calA, calB, evt2.UID)
	if err := engine.events.Delete(ctx, evt2.ID); err != nil {
		t.Fatalf("Delete the second landed copy: %v", err)
	}
	if res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil || res.AutoResolved < 1 {
		t.Fatalf("pass of B after the delete = %+v, %v; want the move abandoned", res, err)
	}
	if row, err := q.GetSyncConflict(ctx, c2.ID); err != nil || row.MoveState != MoveStateAbandoned {
		t.Fatalf("second conflict after the pass = %+v, %v; want abandoned", row, err)
	}
}

// TestCancelRefusesWhenTheMovedCopyIsInTheTrash asks for the server side
// after the moved copy went to the trash. A cancel would move the trashed
// rows back and un-delete them. The pick must refuse and abandon the
// move, and the rows must stay in the trash.
func TestCancelRefusesWhenTheMovedCopyIsInTheTrash(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}

	_, err := svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "abandoned") {
		t.Fatalf("server pick with the copy in the trash = %v, want a refusal", err)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none: no move back of trashed rows", n)
	}
	var deleted int
	if err := engine.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE calendar_id = ? AND uid = ? AND deleted_at IS NOT NULL`, calB, evt.UID).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatalf("trashed rows in B = %d, %v; want the copy still in the trash", deleted, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateAbandoned {
		t.Fatalf("conflict after the refusal = %+v, %v; want abandoned", row, err)
	}
}

// TestReadOnlyTargetDoesNotRecordOverALocalPick records a move conflict for
// a read-only target and resolves it for the local side. The next pass of
// the source must not record a fresh open conflict over the resolved row.
// The user chose, and the tombstone waits for the target.
func TestReadOnlyTargetDoesNotRecordOverALocalPick(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	// The local pick refuses a read-only target: that calendar never
	// creates the copy.
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("local pick on a read-only target = %v, want a refusal", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.ResolvedAt != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict after the refusal = %+v, %v; want it open", row, err)
	}
	// A local-picked row (the target became read-only after the pick)
	// keeps its state: the pass records no fresh conflict over it.
	if _, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: MoveStateLocalPicked, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &calA}); err != nil {
		t.Fatalf("SetMoveConflictState: %v", err)
	}
	if err := engine.markConflictResolved(ctx, calB, evt.UID, ResolutionLocal); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A after the local pick = %+v, %v; want no new record and no delete", res, err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 0 {
		t.Fatalf("open conflicts on B = %v, %v; want none", open, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateLocalPicked {
		t.Fatalf("conflict after the pass = %+v, %v; want the local pick kept", row, err)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
}

// TestRefusedCreateWithoutAPendingDeleteIsAPushFailure pushes a create
// that the server refuses while no pending delete of the UID waits in a
// writable sibling calendar. No pass frees the UID, so the push must
// report an error and a push failure for the sync doctor.
func TestRefusedCreateWithoutAPendingDeleteIsAPushFailure(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evtA := createPushedEvent(t, engine, calA)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evtA.UID, CalendarID: calB, Title: "Dentist (B)", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evtA.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || res.Conflicts != 0 || len(res.Warnings) != 0 || len(res.Errors) != 1 {
		t.Fatalf("push of a copy with no pending delete = %+v, %v; want one push error", res, err)
	}
	if !strings.Contains(res.Errors[0].Error(), "the server already holds this UID") {
		t.Fatalf("error = %q, want the held UID", res.Errors[0])
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evtA.UID})
	if err != nil || srB.Dirty != 1 || srB.PushFailCount == 0 {
		t.Fatalf("sync resource B = %+v, %v; want dirty with a push failure", srB, err)
	}
	wantOnlyServerObject(t, srv, "/cal/a/"+evtA.UID+".ics")
}

// TestRestoreAfterFailedCancelClosesTheCancel cancels a move while the
// import fails, restores the local body, and asks for the server side
// again. The restore completes the cancel, so the replay must refuse and
// keep the restored body.
func TestRestoreAfterFailedCancelClosesTheCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil {
		t.Fatal("server pick with a failed import succeeded")
	}
	engine.testHooks = nil
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("local pick after the failed cancel: %v", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateCancelled {
		t.Fatalf("state after the restore = %q, %v; want cancelled", row.MoveState, err)
	}

	_, err := svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Fatalf("server pick after the restore = %v, want a refusal", err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calA || got.Title != "Dentist" {
		t.Fatalf("event after the refused replay = %+v, %v; want the restored body in A", got, err)
	}
}

// TestRefusedCreateWith403SettlesThePendingDelete pushes an import of a UID
// that a pending delete of a sibling calendar still holds. The server
// refuses the create with 403 and the CALDAV:no-uid-conflict precondition.
// The push must treat it as the 412 case: it sends the pending delete and
// the create lands, with no push failure.
func TestRefusedCreateWith403SettlesThePendingDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.mu.Lock()
	srv.uidConflict403 = true
	srv.mu.Unlock()

	evtA := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evtA.ID); err != nil {
		t.Fatalf("Delete in A: %v", err)
	}
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evtA.UID, CalendarID: calB, Title: "Dentist (B)", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evtA.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push with a 403 refusal = %+v, %v; want one clean push", res, err)
	}
	if srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evtA.UID}); err != nil || srB.PushFailCount != 0 || srB.Dirty != 0 {
		t.Fatalf("sync resource B = %+v, %v; want a clean row", srB, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evtA.UID+".ics")
}

// TestRefusedCreateNamesTheHolderOfTheUID pushes creates that the server
// refuses while no pending delete frees the UID. A UID in the same
// collection under another href gets the plain message. A UID that a
// read-only sibling calendar holds gets the name of that calendar.
func TestRefusedCreateNamesTheHolderOfTheUID(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)

	// Same collection, another href.
	same, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calB, Title: "Same", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	srv.setRemoteBody("/cal/b/other.ics", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//x//EN\r\nBEGIN:VEVENT\r\nUID:"+same.UID+"\r\nDTSTAMP:20260403T100000Z\r\nDTSTART:20260403T100000Z\r\nSUMMARY:Other\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 1 || len(res.Warnings) != 0 {
		t.Fatalf("push with the UID under another href = %+v, %v; want one error", res, err)
	}
	if msg := res.Errors[0].Error(); !strings.Contains(msg, "the server already holds this UID") || strings.Contains(msg, "read-only calendar") {
		t.Fatalf("error = %q, want the plain message", msg)
	}

	// A read-only sibling calendar holds a pending delete of the UID.
	evtA := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evtA.ID); err != nil {
		t.Fatalf("Delete in A: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evtA.UID, CalendarID: calB, Title: "Dentist (B)", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evtA.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}
	res, err = engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 2 {
		t.Fatalf("push with a read-only holder = %+v, %v; want two errors", res, err)
	}
	var named bool
	for _, e := range res.Errors {
		if strings.Contains(e.Error(), evtA.UID) && strings.Contains(e.Error(), `read-only calendar "a"`) {
			named = true
		}
	}
	if !named {
		t.Fatalf("errors = %v, want the read-only calendar named for %s", res.Errors, evtA.UID)
	}
}

// TestLocalPickRefusesAGatedTarget moves an event that another person
// organizes. The local pick must refuse, because the target calendar can
// never create the copy. A resolved row of a gated copy keeps the row
// dirty and visible: the push does not clear the flag.
func TestLocalPickRefusesAGatedTarget(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)

	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	if err == nil || !strings.Contains(err.Error(), "cannot create") {
		t.Fatalf("local pick on a gated target = %v, want a refusal", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.ResolvedAt != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict after the refusal = %+v, %v; want it open", row, err)
	}

	// A resolved row of the gated copy (the gate began after the pick).
	if _, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: MoveStateLocalPicked, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &calA}); err != nil {
		t.Fatalf("SetMoveConflictState: %v", err)
	}
	if err := engine.markConflictResolved(ctx, calB, evt.UID, ResolutionLocal); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || res.SkippedConflicts != 1 {
		t.Fatalf("push of the gated copy after the pick = %+v, %v; want the row skipped", res, err)
	}
	srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || srB.Dirty != 1 || srB.PushFailCount == 0 {
		t.Fatalf("sync resource B = %+v, %v; want dirty with a push failure", srB, err)
	}
}

// TestRestoreRefusesALiveCopyInAThirdCalendar cancels a move from A to B,
// then moves the item to D. The new move deletes the cancelled row on B. A
// stale cancelled row on B must still refuse the restore while D holds the
// item. The old body then does not come back in A.
func TestRestoreRefusesALiveCopyInAThirdCalendar(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calD := linkThirdCalendar(t, engine, q, calA, "d")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	back, err := engine.events.Get(ctx, evt.ID)
	if err != nil || back.CalendarID != calA {
		t.Fatalf("event after the cancel = %+v, %v; want it in A", back, err)
	}
	moveEvent(t, engine, back, calD)
	if _, err := q.GetSyncConflict(ctx, c.ID); err == nil {
		t.Fatal("the cancelled row on B survives a new move of the UID to D")
	}

	// Defensive: a stale cancelled row on B refuses the restore.
	source := calA
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "old", ServerIcal: "old", ServerEtag: "e9", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if _, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: MoveStateCancelled, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &calA}); err != nil {
		t.Fatalf("SetMoveConflictState: %v", err)
	}
	if err := engine.markConflictResolved(ctx, calB, evt.UID, ResolutionServer); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}
	stale := wantMoveConflictAny(t, q, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, stale.ID, "local"); err == nil || !strings.Contains(err.Error(), "another calendar holds a live copy") {
		t.Fatalf("restore with a live copy in D = %v, want a refusal", err)
	}
	if n := countRows(t, engine, "events", calA, evt.UID); n != 0 {
		t.Fatalf("event rows in A = %d, want none", n)
	}
	if n := countRows(t, engine, "events", calD, evt.UID); n != 1 {
		t.Fatalf("event rows in D = %d, want one", n)
	}
}

// TestServerPickFinishesWithoutTheSourceHalf asks for the server side of an
// open move whose source row and tombstone are both gone. The cancel has
// nothing but stale data, so the pick finishes the move. The result says
// so, with no error.
func TestServerPickFinishesWithoutTheSourceHalf(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("DeleteSyncResource A: %v", err)
	}
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("drop the source tombstone: %v", err)
	}

	out, err := svc.ResolveConflictOutcome(ctx, c.ID, "server")
	if err != nil || !out.MoveFinished || out.UID != evt.UID || out.Note != "the source is gone, so the move finished; the next sync creates the moved copy" {
		t.Fatalf("server pick without the source half = %+v, %v; want the finished outcome", out, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateFinished || row.ResolvedAt == nil {
		t.Fatalf("conflict after the pick = %+v, %v; want finished and resolved", row, err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calB {
		t.Fatalf("event after the refusal = %+v, %v; want it in B", got, err)
	}
}

// TestFailedFinishMarkKeepsTheTombstone makes the finished mark of a move
// fail while the source DELETE succeeds. The pass must report the error
// and keep the tombstone, so the next pass retries the mark.
func TestFailedFinishMarkKeepsTheTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	// A local pick lets the mark run an UPDATE. An open row would hold the
	// tombstone in the wait view, so the clear would skip it.
	source := calA
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "x", ServerIcal: "x", ServerEtag: "e1", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET move_state = 'local-picked', resolved_at = '2026-04-01T00:00:00Z', resolution = 'local' WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("local pick: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `CREATE TRIGGER fail_finish BEFORE UPDATE ON sync_conflicts BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	_, _, client, remoteURL, err := engine.loadCalendarClient(ctx, calA)
	if err != nil {
		t.Fatalf("loadCalendarClient: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetTombstone: %v", err)
	}
	outcome, err := engine.deleteTombstone(ctx, client, calA, remoteURL, ts, "")
	if outcome != tombstoneFailed || err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("deleteTombstone with a failed mark = %v, %v; want a failure", outcome, err)
	}
	if _, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("tombstone after the failed mark: %v, want it kept", err)
	}
	if _, err := engine.db.ExecContext(ctx, `DROP TRIGGER fail_finish`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if outcome, err := engine.deleteTombstone(ctx, client, calA, remoteURL, ts, ""); outcome != tombstoneDeleted || err != nil {
		t.Fatalf("retry = %v, %v; want the delete settled", outcome, err)
	}
	if row := wantMoveConflictAny(t, q, calB, evt.UID); row.MoveState != MoveStateFinished {
		t.Fatalf("state after the retry = %q, want finished", row.MoveState)
	}
}

// TestRestoreOnCancelFailedIsAtomic restores the local body of a failed
// cancel while the state write fails. The resolution must roll back with
// it, so the row stays open as cancel-failed.
func TestRestoreOnCancelFailedIsAtomic(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil {
		t.Fatal("server pick with a failed import succeeded")
	}
	engine.testHooks = nil
	if _, err := engine.db.ExecContext(ctx, `CREATE TRIGGER fail_state BEFORE UPDATE OF move_state ON sync_conflicts WHEN NEW.move_state = 'cancelled' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("restore with a failed state write = %v, want the failure", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.ResolvedAt != nil || row.MoveState != MoveStateCancelFailed {
		t.Fatalf("conflict after the failed restore = %+v, %v; want open as cancel-failed", row, err)
	}
}

// TestResolveRefusesAConflictThatChangedUnderTheLock changes the source
// calendar of a move conflict while the resolve waits for the locks. The
// resolve must refuse: its locks cover other calendars.
func TestResolveRefusesAConflictThatChangedUnderTheLock(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{afterConflictLock: func() {
		if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET move_source_calendar_id = ? WHERE id = ?`, calC, c.ID); err != nil {
			t.Errorf("change the conflict: %v", err)
		}
	}}
	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), "changed while it waited") {
		t.Fatalf("resolve of a changed conflict = %v, want a refusal", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.ResolvedAt != nil {
		t.Fatalf("conflict after the refusal = %+v, %v; want it open", row, err)
	}
}

// TestDoctorPushQueuesTheDeleteOfAVanishedRow deletes an unpushed event
// while the doctor push sends its first PUT. The doctor must write a
// tombstone for the new href and no new sync row, as the push does.
func TestDoctorPushQueuesTheDeleteOfAVanishedRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete during the PUT: %v", err)
		}
	}}
	_, err = engine.DoctorPush(ctx, calA, evt.UID, nil)
	engine.testHooks = nil
	if err != nil {
		t.Fatalf("DoctorPush: %v", err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatalf("sync resource after the doctor push = %+v, want none", sr)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || !strings.HasSuffix(ts.RemoteUrl, evt.UID+".ics") || ts.MovedToCalendarID != nil {
		t.Fatalf("tombstone after the doctor push = %+v, %v; want a plain tombstone for the new href", ts, err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Deleted != 1 {
		t.Fatalf("pass of A = %+v, %v; want the delete of the new object", res, err)
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("server objects = %v, want none", got)
	}
}

// TestCancelRefusesACollisionInTheSource imports the UID into the source
// calendar after the move, and then asks for the server side. The move
// back would break the UNIQUE key, so the cancel must refuse with the
// collision error of uidmove and change nothing.
func TestCancelRefusesACollisionInTheSource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in A: %v", err)
	}

	before, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetSyncConflict: %v", err)
	}
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote 2)")

	_, err = svc.ResolveConflict(ctx, c.ID, "server")
	if !errors.Is(err, event.ErrMoveTargetHasUID) {
		t.Fatalf("cancel with the UID in the source = %v, want ErrMoveTargetHasUID", err)
	}
	// The cancel record commits with the move back, so the refusal leaves
	// the record as it was.
	after, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || after.LocalIcal != before.LocalIcal || after.ServerIcal != before.ServerIcal || after.ServerEtag != before.ServerEtag {
		t.Fatalf("record after the refused cancel = %+v, %v; want it unchanged", after, err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calB {
		t.Fatalf("event after the refusal = %+v, %v; want it in B", got, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.ResolvedAt != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict after the refusal = %+v, %v; want it open", row, err)
	}
}

// TestDeleteDuringThePutBesideAnImportWritesAPlainTombstone deletes an
// unpushed event while its first PUT is in flight, and an unrelated
// import of the same UID waits unpushed in another calendar. The deleted
// rows stay in the trash of the source, so the tombstone must be plain,
// and the DELETE must go out.
func TestDeleteDuringThePutBesideAnImportWritesAPlainTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete during the PUT: %v", err)
		}
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: evt.UID, CalendarID: calB, Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour),
		}); err != nil {
			t.Errorf("UpsertByUID in B: %v", err)
		}
		if err := q.MarkResourceDirty(ctx, calB, evt.UID, "event"); err != nil {
			t.Errorf("MarkResourceDirty B: %v", err)
		}
	}}
	// The tombstone is plain, and a new plain delete never waits, so the
	// tombstone pass of the same push sends it.
	res, err := engine.PushLocalEdits(ctx, calA)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Deleted != 1 {
		t.Fatalf("push of A = %+v, %v; want the push and the delete of the new object", res, err)
	}
	if srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("the new object in A is still on the server")
	}
}

// seedCreateConflict records an edit conflict on a create. The planned href
// of a new event in calA already holds an object with the same UID, and the
// server refuses the create with 412.
func seedCreateConflict(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA int64) (uid, path string) {
	t.Helper()
	ctx := context.Background()
	uid = "create-conflict"
	path = "/cal/a/" + uid + ".ics"
	srv.setRemoteBody(path, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//x//EN\r\nBEGIN:VEVENT\r\nUID:"+uid+"\r\nDTSTAMP:20260403T100000Z\r\nDTSTART:20260403T100000Z\r\nDTEND:20260403T110000Z\r\nSUMMARY:Server\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: uid, CalendarID: calA, Title: "Local", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calA, uid, "event"); err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}
	srv.setPutStatus("/cal/a/", http.StatusPreconditionFailed)
	t.Cleanup(func() {
		srv.mu.Lock()
		delete(srv.putStatus, "/cal/a/")
		srv.mu.Unlock()
	})
	return uid, path
}

// TestRestoreOnACreateConflictIsAConditionalUpdate records an edit conflict
// on a create, edits the object on the server again, and then asks for the
// local side. The row must name the object with the recorded etag. The push
// then sends If-Match, gets 412, and records a fresh conflict, so it does
// not overwrite the newer remote edit.
func TestRestoreOnACreateConflictIsAConditionalUpdate(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	uid, path := seedCreateConflict(t, engine, q, srv, calA)
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Conflicts != 1 {
		t.Fatalf("push of the create = %+v, %v; want one edit conflict", res, err)
	}
	srv.mu.Lock()
	delete(srv.putStatus, "/cal/a/")
	srv.mu.Unlock()
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(conflicts) != 1 || conflicts[0].ServerEtag == "" {
		t.Fatalf("conflicts = %+v, %v; want one edit conflict with an etag", conflicts, err)
	}
	srv.editRemoteSummary(path, "Server (newer)")

	if _, err := svc.ResolveConflict(ctx, conflicts[0].ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: uid})
	if err != nil || sr.RemoteUrl == "" || sr.Etag != conflicts[0].ServerEtag || sr.Dirty != 1 {
		t.Fatalf("sync resource after the pick = %+v, %v; want the href and the recorded etag", sr, err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || res.Pushed != 0 || res.Conflicts != 1 {
		t.Fatalf("push after the pick = %+v, %v; want a fresh conflict", res, err)
	}
	if body := srv.body(path); !strings.Contains(body, "SUMMARY:Server (newer)") {
		t.Fatalf("server body = %q, want the newer remote edit kept", body)
	}
}

// TestServerWinsSettlesACreateConflict records an edit conflict on a
// create in a server-wins pass. The pass must adopt the server body and
// name the object on the row, so the next push is an update.
func TestServerWinsSettlesACreateConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	uid, _ := seedCreateConflict(t, engine, q, srv, calA)
	res, err := engine.SyncCalendar(ctx, calA, ConflictServerWins)
	if err != nil || len(res.Errors) != 0 || res.AutoResolved != 1 {
		t.Fatalf("server-wins pass = %+v, %v; want the create conflict settled", res, err)
	}
	got, err := engine.events.GetByCalendarUID(ctx, calA, uid)
	if err != nil || got.Title != "Server" {
		t.Fatalf("event after the pass = %+v, %v; want the server body", got, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: uid})
	if err != nil || sr.Dirty != 0 || !strings.HasSuffix(sr.RemoteUrl, uid+".ics") || sr.Etag == "" {
		t.Fatalf("sync resource after the pass = %+v, %v; want a clean row that names the object", sr, err)
	}
}

// TestRefusedLocalPickLeavesNoTombstone resolves an open move for the local
// side after the source lost its tombstone. The record has no etag, and the
// server gives none. The pick refuses, and it must not write the tombstone
// again.
func TestRefusedLocalPickLeavesNoTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("drop the source tombstone: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET server_etag = '' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("blank the etag: %v", err)
	}
	srv.setNoGetEtag(true)

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "no etag") {
		t.Fatalf("local pick without an etag = %v, want a refusal", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A after the refusal = %v, %v; want none", ts, err)
	}
}

// TestLocalPickFinishesWhenTheSourceIsGone resolves an open move for the
// local side while the record has no etag and the source object is gone
// from the server. The target can create the copy, so the pick finishes
// the move silently, and the next push creates the moved copy.
func TestLocalPickFinishesWhenTheSourceIsGone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET server_etag = '' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("blank the etag: %v", err)
	}
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()

	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("local pick with the source gone = %v, want a silent finish", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateFinished || row.ResolvedAt == nil {
		t.Fatalf("conflict after the pick = %+v, %v; want finished and resolved", row, err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want one push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
}

// TestDivergedCopyWarnsOncePerEtag pulls the same remote edit of a dirty
// landed copy twice while its move conflict is open. The first pull warns,
// the second with the same etag does not, and a newer etag warns again.
func TestDivergedCopyWarnsOncePerEtag(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := engine.events.Update(ctx, moved.ID, event.UpdateParams{
		Title: "Dentist (local)", StartTime: moved.StartTime, EndTime: moved.EndTime, Status: moved.Status, Transp: moved.Transp, Class: moved.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pathB := "/cal/b/" + evt.UID + ".ics"
	pull := func() []ImportWarning {
		t.Helper()
		data, err := parseICalData([]byte(srv.body(pathB)))
		if err != nil {
			t.Fatalf("parse the server body: %v", err)
		}
		_, _, warnings, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: pathB, href: pathB, etag: srv.etag(pathB), data: data})
		if err != nil {
			t.Fatalf("importFetchedResource: %v", err)
		}
		return warnings
	}
	srv.editRemoteSummary(pathB, "Dentist (server 1)")
	if w := pull(); len(w) != 1 {
		t.Fatalf("first pull warnings = %v, want one", w)
	}
	if w := pull(); len(w) != 0 {
		t.Fatalf("second pull with the same etag warnings = %v, want none", w)
	}
	srv.editRemoteSummary(pathB, "Dentist (server 2)")
	if w := pull(); len(w) != 1 {
		t.Fatalf("pull with a newer etag warnings = %v, want one", w)
	}
}

// TestAbandonedLocalMoveIsNotRecordedAgain moves an event into a local
// calendar, records the move conflict, and trashes the moved copy. The
// moot step abandons the move. The tombstone pass must not record the
// conflict again, and a move record must not replace the abandoned row.
func TestAbandonedLocalMoveIsNotRecordedAgain(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	local, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, local.ID)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, local.ID, evt.UID)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the copy in the local calendar: %v", err)
	}
	for pass := range 2 {
		if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Conflicts != 0 {
			t.Fatalf("pass %d of A after the trash = %+v, %v; want no new record", pass, res, err)
		}
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateAbandoned {
		t.Fatalf("conflict after the passes = %+v, %v; want abandoned", row, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object in A is gone: the remote edit must stay")
	}
}

// TestDeleteOfAnUnpushedCopyClosesItsConflict deletes an unpushed moved copy
// that has an open edit conflict. The delete must close the conflict. The
// move tombstone that names the calendar of the copy becomes a plain delete,
// so the source DELETE goes out.
func TestDeleteOfAnUnpushedCopyClosesItsConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 0 {
		t.Fatalf("open conflicts on B = %v, %v; want the conflict closed", open, err)
	}
	// The move tombstone keeps its target, but it does not wait: the
	// target row is gone, and its conflict is closed.
	if n, err := q.CountMoveWaitTombstones(ctx, storage.CountMoveWaitTombstonesParams{CalendarID: calA, Uid: evt.UID}); err != nil || n != 0 {
		t.Fatalf("waiting tombstones of A = %d, %v; want none", n, err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A = %+v, %v; want the source delete", res, err)
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("server objects = %v, want none", got)
	}
}

// TestNewMoveKeepsTheMoveRowsOfAnotherAccount moves an event inside one
// account while a resolved move row of the same UID lives in a calendar
// of another account. The new move must delete only the rows of its own
// account.
func TestNewMoveKeepsTheMoveRowsOfAnotherAccount(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC, calD := seedSecondAccount(t, engine, q, srv)

	evt := createPushedEvent(t, engine, calA)
	other := calC
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calD, OwnerType: "event", OwnerID: 1, Uid: evt.UID,
		LocalIcal: "x", ServerIcal: "x", ServerEtag: "e1", MoveSourceCalendarID: &other,
	}); err != nil {
		t.Fatalf("CreateSyncConflict in the other account: %v", err)
	}
	if err := engine.markConflictResolved(ctx, calD, evt.UID, ResolutionServer); err != nil {
		t.Fatalf("markConflictResolved: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if row := wantMoveConflictAny(t, q, calD, evt.UID); row.CalendarID != calD {
		t.Fatalf("resolved row of the other account = %+v, want it kept", row)
	}
}

// TestMoveStateWritesNameTheSource writes the state of a move conflict
// with the wrong source calendar and on an edit conflict. Both writes
// must change nothing, so the caller sees that the conflict changed.
func TestMoveStateWritesNameTheSource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	wrong := calB
	if n, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: MoveStateCancelled, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &wrong}); err != nil || n != 0 {
		t.Fatalf("state write with the wrong source = %d rows, %v; want none", n, err)
	}
	if n, err := q.ReopenCancelledMoveConflict(ctx, storage.ReopenCancelledMoveConflictParams{CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &wrong}); err != nil || n != 0 {
		t.Fatalf("reopen with the wrong source = %d rows, %v; want none", n, err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA, OwnerType: "event", OwnerID: 1, Uid: "edit-uid",
		LocalIcal: "x", ServerIcal: "x", ServerEtag: "e1",
	}); err != nil {
		t.Fatalf("CreateSyncConflict edit: %v", err)
	}
	if n, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: MoveStateCancelled, CalendarID: calA, Uid: "edit-uid", MoveSourceCalendarID: &calB}); err != nil || n != 0 {
		t.Fatalf("state write on an edit conflict = %d rows, %v; want none", n, err)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(open) != 1 || open[0].MoveState != MoveStateOpen {
		t.Fatalf("move conflicts on B = %+v, %v; want one open", open, err)
	}
}

// TestPurgeDuringThePutBesideAnImportWritesAPlainTombstone deletes and
// purges an unpushed event while its first PUT is in flight. An unrelated
// import of the same UID waits unpushed in another calendar. The calendar
// holds no row of the UID, but no row of the PUT moved, so the tombstone
// must be plain.
func TestPurgeDuringThePutBesideAnImportWritesAPlainTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	srv.setGeneric(true)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete during the PUT: %v", err)
		}
		if err := engine.events.PurgeByID(ctx, evt.ID); err != nil {
			t.Errorf("PurgeByID during the PUT: %v", err)
		}
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: evt.UID, CalendarID: calB, Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour),
		}); err != nil {
			t.Errorf("UpsertByUID in B: %v", err)
		}
		if err := q.MarkResourceDirty(ctx, calB, evt.UID, "event"); err != nil {
			t.Errorf("MarkResourceDirty B: %v", err)
		}
	}}
	// No row of the PUT moved, so the tombstone is plain, and the
	// tombstone pass of the same push sends it.
	res, err := engine.PushLocalEdits(ctx, calA)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Deleted != 1 {
		t.Fatalf("push of A = %+v, %v; want the push and the delete of the new object", res, err)
	}
	if srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("the new object in A is still on the server")
	}
}

// TestLocalPickRefusesWhenTheSourceHoldsTheItemAgain imports the UID into
// the source calendar after the move, which clears the source tombstone.
// The local pick must refuse: a new tombstone would delete the server
// object under the live rows.
func TestLocalPickRefusesWhenTheSourceHoldsTheItemAgain(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in A: %v", err)
	}
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("clear the pending delete: %v", err)
	}

	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	if err == nil || !strings.Contains(err.Error(), "the source calendar holds") {
		t.Fatalf("local pick with the item in the source again = %v, want a refusal", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestNewPlainDeleteBesideAGatedRowGoesOut deletes a pushed event in A
// while B holds an unrelated unpushed row of the same UID that the
// organizer gate holds back. A new plain delete never waits, so the pass
// of A must send the DELETE.
func TestNewPlainDeleteBesideAGatedRowGoesOut(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evtA := createPushedEvent(t, engine, calA)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	other, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evtA.UID, CalendarID: calB, Title: "Invite", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := engine.events.ReplaceAttendeesForSync(ctx, other.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evtA.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}
	if err := engine.events.Delete(ctx, evtA.ID); err != nil {
		t.Fatalf("Delete in A: %v", err)
	}

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A = %+v, %v; want the plain delete", res, err)
	}
	if srv.has("/cal/a/" + evtA.UID + ".ics") {
		t.Fatal("the deleted object in A is still on the server")
	}
}

// TestRestoreIgnoresALiveCopyInAnotherAccount cancels a move and restores
// the local body while another account holds a live copy of the UID. That
// copy is another item, so the restore must go on.
func TestRestoreIgnoresALiveCopyInAnotherAccount(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC, _ := seedSecondAccount(t, engine, q, srv)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calC, Title: "Other account", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in the other account: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("restore with a copy in another account: %v", err)
	}
	if got, err := engine.events.GetByCalendarUID(ctx, calA, evt.UID); err != nil || got.Title != "Dentist" {
		t.Fatalf("event in A after the restore = %+v, %v; want the recorded local body", got, err)
	}
}

// TestRestoreImportsIntoADirtySourceRow cancels a move whose source row
// has no server object, so the cancel leaves the row dirty with the server
// body. The local restore must still import the recorded body.
func TestRestoreImportsIntoADirtySourceRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = '' WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
		t.Fatalf("drop the server object of the source row: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
		t.Fatalf("source row after the cancel = %+v, %v; want dirty", sr, err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calA || got.Title != "Dentist" {
		t.Fatalf("event after the restore = %+v, %v; want the recorded local body in A", got, err)
	}
}

// TestRestoredCopyReplacesAnAbandonedMoveRow abandons a landed move by a
// delete of the copy, restores the copy from the trash, and lets the
// source DELETE get 412 again. The pass must record a fresh open move
// conflict over the abandoned row, not skip it in silence.
func TestRestoredCopyReplacesAnAbandonedMoveRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the landed copy: %v", err)
	}
	// The pass of B abandons the move and deletes the landed copy on the
	// server. The move tombstone of A stays.
	if res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of B after the delete = %+v, %v; want a clean pass", res, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateAbandoned {
		t.Fatalf("conflict after the delete = %+v, %v; want abandoned", row, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want the move tombstone kept", ts, err)
	}
	// The copy comes back from the trash, and its push lands again.
	if err := engine.events.RestoreByID(ctx, evt.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of the restored copy = %+v, %v; want one push", res, err)
	}

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 {
		t.Fatalf("pass of A after the restore = %+v, %v; want a fresh move conflict", res, err)
	}
	if row := wantMoveConflict(t, q, calA, calB, evt.UID); row.MoveState != MoveStateOpen {
		t.Fatalf("state = %q, want open", row.MoveState)
	}
}

// TestDeleteOfAnUnpushedCopyAbandonsALocalPick resolves a move for the
// local side and deletes the moved copy before its push. The local-picked
// row must close as abandoned, and the source DELETE goes out as a plain
// delete.
func TestDeleteOfAnUnpushedCopyAbandonsALocalPick(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateAbandoned {
		t.Fatalf("conflict after the delete = %+v, %v; want abandoned", row, err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A = %+v, %v; want the source delete", res, err)
	}
	if got := srv.paths(); len(got) != 0 {
		t.Fatalf("server objects = %v, want none", got)
	}
}

// TestServerWinsSettlesAMovedCopyUnderAnEditConflict meets an object at
// the href of an unpushed moved copy, so the refused create records an
// edit conflict. A server-wins pass must adopt the server body and name
// the object on the row, and the source delete then goes out. The test
// runs a move tombstone and a tombstone from before the upgrade.
func TestServerWinsSettlesAMovedCopyUnderAnEditConflict(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()

			evt := createPushedEvent(t, engine, calA)
			moveEvent(t, engine, evt, calB)
			if legacy {
				if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET moved_to_calendar_id = NULL, legacy_move = 1 WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
					t.Fatalf("make the tombstone a legacy one: %v", err)
				}
			}
			pathB := "/cal/b/" + evt.UID + ".ics"
			srv.setRemoteBody(pathB, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//x//EN\r\nBEGIN:VEVENT\r\nUID:"+evt.UID+"\r\nDTSTAMP:20260403T100000Z\r\nDTSTART:20260403T100000Z\r\nDTEND:20260403T110000Z\r\nSUMMARY:Server B\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
			if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
				t.Fatalf("push of B = %+v, %v; want one edit conflict", res, err)
			}
			if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 1 || open[0].MoveSourceCalendarID != nil {
				t.Fatalf("conflicts on B = %+v, %v; want one edit conflict", open, err)
			}

			res, err := engine.SyncCalendar(ctx, calB, ConflictServerWins)
			if err != nil || len(res.Errors) != 0 || res.AutoResolved < 1 {
				t.Fatalf("server-wins pass of B = %+v, %v; want the edit conflict settled", res, err)
			}
			srB, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
			if err != nil || srB.RemoteUrl == "" || srB.Dirty != 0 {
				t.Fatalf("sync resource B = %+v, %v; want a clean row that names the object", srB, err)
			}
			if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calB || got.Title != "Server B" {
				t.Fatalf("event after the pass = %+v, %v; want the server body in B", got, err)
			}
			if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
				t.Fatalf("pass of A = %+v, %v; want the source delete", res, err)
			}
			wantOnlyServerObject(t, srv, pathB)
		})
	}
}

// TestManualServerPickOnACreateConflictNamesTheObject records an edit
// conflict on a create and resolves it for the server side by hand. The
// row must name the object at the planned href, so it is no row without a
// server object any more.
func TestManualServerPickOnACreateConflictNamesTheObject(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	uid, _ := seedCreateConflict(t, engine, q, srv, calA)
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Conflicts != 1 {
		t.Fatalf("push of the create = %+v, %v; want one edit conflict", res, err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, %v; want one", conflicts, err)
	}
	if _, err := svc.ResolveConflict(ctx, conflicts[0].ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: uid})
	if err != nil || !strings.HasSuffix(sr.RemoteUrl, uid+".ics") || sr.Dirty != 0 || sr.Etag != conflicts[0].ServerEtag {
		t.Fatalf("sync resource after the pick = %+v, %v; want a clean row that names the object", sr, err)
	}
	if got, err := engine.events.GetByCalendarUID(ctx, calA, uid); err != nil || got.Title != "Server" {
		t.Fatalf("event after the pick = %+v, %v; want the server body", got, err)
	}
}

// TestLegacyTombstoneIgnoresAGatedSibling deletes an event in A with a
// tombstone from before the upgrade. B holds an unpushed row of the same UID
// that the organizer gate cleared. That row never pushes, so the pass of A
// must send the DELETE instead of a wait without end.
func TestLegacyTombstoneIgnoresAGatedSibling(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evtA := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evtA.ID); err != nil {
		t.Fatalf("Delete in A: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET legacy_move = 1 WHERE calendar_id = ? AND uid = ?`, calA, evtA.UID); err != nil {
		t.Fatalf("make the tombstone a legacy one: %v", err)
	}
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	other, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evtA.UID, CalendarID: calB, Title: "Invite", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := engine.events.ReplaceAttendeesForSync(ctx, other.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evtA.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 0 {
		t.Fatalf("push of the gated row = %+v, %v; want nothing sent", res, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evtA.UID}); err != nil || sr.Dirty != 0 || sr.RemoteUrl != "" {
		t.Fatalf("sync resource B = %+v, %v; want the gate to clear the dirty flag", sr, err)
	}

	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("pass of A = %+v, %v; want the delete", res, err)
	}
	if srv.has("/cal/a/" + evtA.UID + ".ics") {
		t.Fatal("the deleted object in A is still on the server")
	}
}

// failCancel records a move conflict on an iCloud-like pair and runs a
// cancel whose import fails, so the row waits as cancel-failed.
func failCancel(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, svc *Service, calA, calB int64) (event.Event, storage.SyncConflict) {
	t.Helper()
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil {
		t.Fatal("server pick with a failed import succeeded")
	}
	engine.testHooks = nil
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelFailed {
		t.Fatalf("conflict after the failed cancel = %+v, %v; want cancel-failed", row, err)
	}
	return evt, row
}

// TestReplayRefusesAfterADeleteOfTheItem deletes the item in the source
// after a failed cancel. The replay must refuse and keep the tombstone of
// that delete.
func TestReplayRefusesAfterADeleteOfTheItem(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := failCancel(t, engine, q, srv, svc, calA, calB)

	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete in A: %v", err)
	}
	_, err := svc.ResolveConflict(ctx, c.ID, "server")
	if err == nil || !strings.Contains(err.Error(), "changed after the failed cancel") {
		t.Fatalf("replay after a delete = %v, want a refusal", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones on A = %v, %v; want the tombstone of the delete", ts, err)
	}
}

// TestFailedCancelBlocksANewMove moves the item to a third calendar after
// a failed cancel. The open cancel-failed row holds the UID, so the move
// must be refused.
func TestFailedCancelBlocksANewMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC := linkThirdCalendar(t, engine, q, calA, "c")
	evt, _ := failCancel(t, engine, q, srv, svc, calA, calB)

	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA {
		t.Fatalf("event after the failed cancel = %+v, %v; want it in A", got, err)
	}
	_, err = engine.events.Update(ctx, got.ID, event.UpdateParams{
		Title: got.Title, StartTime: got.StartTime, EndTime: got.EndTime, CalendarID: calC, Status: got.Status, Transp: got.Transp, Class: got.Class,
	})
	if !errors.Is(err, storage.ErrMoveOpenConflict) {
		t.Fatalf("move after a failed cancel = %v, want ErrMoveOpenConflict", err)
	}
	if after, err := engine.events.Get(ctx, evt.ID); err != nil || after.CalendarID != calA {
		t.Fatalf("event after the refused move = %+v, %v; want it in A", after, err)
	}
}

// TestCloseMoveConflictLeavesAFailedCancel closes a move conflict that
// waits as cancel-failed. The close must change nothing: the row waits
// for the replay.
func TestCloseMoveConflictLeavesAFailedCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := failCancel(t, engine, q, srv, svc, calA, calB)

	if err := q.CloseMoveConflict(ctx, storage.CloseMoveConflictParams{CalendarID: calB, Uid: evt.UID}); err != nil {
		t.Fatalf("CloseMoveConflict: %v", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("conflict after the close = %+v, %v; want cancel-failed and open", row, err)
	}
}

// TestReadOnlyTargetWithTheSourceGoneFinishesTheMove moves an event to a
// calendar that becomes read-only, and the source object leaves the server.
// The record of the move conflict finds the source gone. The pass must
// finish the move and drop the tombstone, and no failure repeats on every
// pass.
func TestReadOnlyTargetWithTheSourceGoneFinishesTheMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()

	for pass := range 2 {
		if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Conflicts != 0 {
			t.Fatalf("pass %d of A = %+v, %v; want a clean pass", pass, res, err)
		}
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want none", ts, err)
	}
}

// TestReadOnlyTargetWithAnEditConflictRecordsNothing holds an open edit
// conflict on the moved copy in a read-only target. The pass of the
// source must record nothing and report no error, on every pass.
func TestReadOnlyTargetWithAnEditConflictRecordsNothing(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}
	for pass := range 2 {
		if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Conflicts != 0 {
			t.Fatalf("pass %d of A = %+v, %v; want a quiet pass", pass, res, err)
		}
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(open) != 1 || open[0].MoveSourceCalendarID != nil {
		t.Fatalf("conflicts on B = %+v, %v; want the edit conflict only", open, err)
	}
}

// TestRestoreRefusesAnEditAfterTheCancel cancels a move whose source row
// has no server object, so the row stays dirty after the cancel, and then
// edits the item. The local restore must refuse: the edit after the
// cancel is newer than the recorded body.
func TestRestoreRefusesAnEditAfterTheCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = '' WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
		t.Fatalf("drop the server object of the source row: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := engine.events.Update(ctx, got.ID, event.UpdateParams{
		Title: "Dentist (edited after the cancel)", StartTime: got.StartTime, EndTime: got.EndTime, Status: got.Status, Transp: got.Transp, Class: got.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	_, err = svc.ResolveConflict(ctx, c.ID, "local")
	if err == nil || !strings.Contains(err.Error(), "changed after the cancel") {
		t.Fatalf("restore over an edit after the cancel = %v, want a refusal", err)
	}
	if after, err := engine.events.Get(ctx, evt.ID); err != nil || after.Title != "Dentist (edited after the cancel)" {
		t.Fatalf("event after the refusal = %+v, %v; want the edit kept", after, err)
	}
}

// TestTombstoneUpsertResetsLegacyMove writes a tombstone again over a
// tombstone from before the upgrade. The new tombstone is a new delete, so
// it must not keep the legacy mark.
func TestTombstoneUpsertResetsLegacyMove(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	if _, err := engine.db.ExecContext(ctx, `INSERT INTO tombstones (calendar_id, uid, remote_url, legacy_move) VALUES (?, 'legacy-uid', '/cal/a/legacy-uid.ics', 1)`, calA); err != nil {
		t.Fatalf("insert a legacy tombstone: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calA, Uid: "legacy-uid", RemoteUrl: "/cal/a/legacy-uid.ics"}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: "legacy-uid"})
	if err != nil || ts.LegacyMove != 0 {
		t.Fatalf("tombstone after the upsert = %+v, %v; want legacy_move 0", ts, err)
	}
}

// TestDeleteAfterAFailedCancelAbandonsTheMove runs the seven steps that
// stranded a failed cancel. The steps are a move, a remote edit of the
// source, the push with its move conflict, and a cancel whose import fails.
// A delete (or a purge) of the item in the source and the pass of the source
// follow. The pass must close the failed cancel as abandoned, so the source
// DELETE goes out under the plain rule, and both picks refuse.
func TestDeleteAfterAFailedCancelAbandonsTheMove(t *testing.T) {
	t.Parallel()
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge=%v", purge), func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := failCancel(t, engine, q, srv, svc, calA, calB)

			if err := engine.events.Delete(ctx, evt.ID); err != nil {
				t.Fatalf("Delete in A: %v", err)
			}
			if purge {
				if err := engine.events.PurgeByID(ctx, evt.ID); err != nil {
					t.Fatalf("PurgeByID in A: %v", err)
				}
			}
			res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
			if err != nil || len(res.Errors) != 0 {
				t.Fatalf("pass of A = %+v, %v; want a clean pass", res, err)
			}
			row, err := q.GetSyncConflict(ctx, c.ID)
			if err != nil || row.MoveState != MoveStateAbandoned || row.ResolvedAt == nil {
				t.Fatalf("conflict after the pass = %+v, %v; want abandoned", row, err)
			}
			if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
				t.Fatalf("tombstones on A = %v, %v; want none after the plain rule", ts, err)
			}
			for _, pick := range []string{"local", "server"} {
				if _, err := svc.ResolveConflict(ctx, c.ID, pick); err == nil || !strings.Contains(err.Error(), "abandoned") {
					t.Fatalf("%s pick on the abandoned move = %v, want a refusal", pick, err)
				}
			}
		})
	}
}

// TestRefusedCreateBesideAHeldTombstoneIsAFailure moves an event from A to
// C, so the tombstone of A waits for the create in C. Then it imports the
// same UID into B. The refused create in B must not wait in silence on the
// tombstone of A: that tombstone waits for another move. The push reports an
// error and a push failure.
func TestRefusedCreateBesideAHeldTombstoneIsAFailure(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calC)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calB, Title: "Import", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evt.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || len(res.Warnings) != 0 || len(res.Errors) != 1 {
		t.Fatalf("push of B = %+v, %v; want one push error and no warning", res, err)
	}
	if !strings.Contains(res.Errors[0].Error(), "the server already holds this UID") {
		t.Fatalf("error = %q, want the held UID", res.Errors[0])
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || sr.PushFailCount == 0 {
		t.Fatalf("sync resource B = %+v, %v; want a push failure", sr, err)
	}
}

// TestLocalPickOnACreateConflictWorksOffline resolves an edit conflict of
// a create for the local side without a credential. The planned href
// comes from the server URL and the UID, so the pick needs no client.
func TestLocalPickOnACreateConflictWorksOffline(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	uid, _ := seedCreateConflict(t, engine, q, srv, calA)
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Conflicts != 1 {
		t.Fatalf("push of the create = %+v, %v; want one edit conflict", res, err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, %v; want one", conflicts, err)
	}
	cal, err := q.GetCalendar(ctx, calA)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	delete(engine.credStore.(*mockCredStore).creds, *cal.AccountID)

	if _, err := svc.ResolveConflict(ctx, conflicts[0].ID, "local"); err != nil {
		t.Fatalf("local pick without a credential: %v", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: uid})
	if err != nil || sr.RemoteUrl != "/cal/a/"+uid+".ics" || sr.Etag != conflicts[0].ServerEtag {
		t.Fatalf("sync resource after the pick = %+v, %v; want the planned href and the recorded etag", sr, err)
	}
}

// TestPushOfAMissingUnpushedRowClosesItsConflicts pushes an unpushed row
// whose local rows are gone and that holds an open edit conflict, while a
// move tombstone names its calendar. The push deletes the row, so it must
// close the conflict and turn the move tombstone into a plain delete.
func TestPushOfAMissingUnpushedRowClosesItsConflicts(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calB, Uid: "ghost-uid", OwnerType: "event", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: 1, Uid: "ghost-uid",
		LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calA, Uid: "ghost-uid", RemoteUrl: "/cal/a/ghost-uid.ics", MovedToCalendarID: &calB,
	}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	// A server-wins pass reaches the row although its edit conflict is
	// open, and the export finds no local rows.
	res, err := engine.SyncCalendar(ctx, calB, ConflictServerWins)
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of B = %+v, %v; want a clean pass", res, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: "ghost-uid"}); err == nil {
		t.Fatalf("sync resource = %+v, want none", sr)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 0 {
		t.Fatalf("open conflicts on B = %v, %v; want none", open, err)
	}
	if n, err := q.CountMoveWaitTombstones(ctx, storage.CountMoveWaitTombstonesParams{CalendarID: calA, Uid: "ghost-uid"}); err != nil || n != 0 {
		t.Fatalf("waiting tombstones of A = %d, %v; want none", n, err)
	}
}

// TestMoveDeleteRestoreKeepsTheMove moves an event to B, deletes the
// unpushed copy, and restores it from the trash. The source tombstone keeps
// its target through the delete. After a remote edit of the source, the push
// of B then records a move conflict, not a second copy of the UID.
func TestMoveDeleteRestoreKeepsTheMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete the moved copy: %v", err)
	}
	if err := engine.events.RestoreByID(ctx, evt.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
		t.Fatalf("tombstone of A = %+v, %v; want the move target kept", ts, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Conflicts != 1 || res.Pushed != 0 {
		t.Fatalf("push of the restored copy = %+v, %v; want one move conflict", res, err)
	}
	wantMoveConflict(t, q, calA, calB, evt.UID)
	wantOnlyServerObject(t, srv, "/cal/a/"+evt.UID+".ics")
}

// TestGatedCopyUnderAnEditConflictRecordsTheMove holds an open edit
// conflict on an unpushed moved copy that the organizer gate holds back.
// The opportunistic push skips the row and keeps the edit conflict. A
// server-wins pass closes the edit conflict, which no push can settle, and
// records the move conflict, so the move stays visible.
func TestGatedCopyUnderAnEditConflictRecordsTheMove(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.SkippedConflicts != 1 {
		t.Fatalf("opportunistic push of the gated copy = %+v, %v; want one skip", res, err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 1 || open[0].MoveSourceCalendarID != nil {
		t.Fatalf("open conflicts = %v, %v; want the edit conflict kept", open, err)
	}
	res, err = engine.SyncCalendar(ctx, calB, ConflictServerWins)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 {
		t.Fatalf("server-wins pass of the gated copy = %+v, %v; want one move conflict", res, err)
	}
	if row := wantMoveConflict(t, q, calA, calB, evt.UID); row.MoveState != MoveStateOpen {
		t.Fatalf("state = %q, want open", row.MoveState)
	}
}

// TestGatedCopyAfterAnEndedMoveFollowsThePlainGate holds a terminal move
// row on an unpushed moved copy that the organizer gate holds back. The
// move is over: the source tombstone becomes a plain delete, and the gate
// clears the row.
func TestGatedCopyAfterAnEndedMoveFollowsThePlainGate(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	source := calA
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "x", ServerIcal: "x", ServerEtag: "e1", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if err := q.CloseMoveConflict(ctx, storage.CloseMoveConflictParams{CalendarID: calB, Uid: evt.UID}); err != nil {
		t.Fatalf("CloseMoveConflict: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.SkippedConflicts != 0 {
		t.Fatalf("push of the gated copy = %+v, %v; want the plain gate", res, err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID != nil {
		t.Fatalf("tombstone of A = %+v, %v; want a plain delete", ts, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || sr.Dirty != 0 {
		t.Fatalf("sync resource B = %+v, %v; want the gate to clear the row", sr, err)
	}
}

// TestGatedRowClosesItsEditConflict holds an open edit conflict on a pushed
// row that the organizer gate holds back. No push settles that conflict. A
// server-wins pass imports the recorded server body, closes the conflict as
// server-auto, and clears the row with the server etag.
func TestGatedRowClosesItsEditConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calA, evt.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}
	serverBody := strings.Replace(srv.body("/cal/a/"+evt.UID+".ics"), "SUMMARY:Dentist", "SUMMARY:Dentist (server)", 1)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: serverBody, ServerEtag: "edit",
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictServerWins); err != nil || len(res.Errors) != 0 || res.SkippedConflicts != 0 {
		t.Fatalf("pass of A = %+v, %v; want the gate, not the skip", res, err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calA); err != nil || len(open) != 0 {
		t.Fatalf("open conflicts = %v, %v; want the edit conflict closed", open, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 0 {
		t.Fatalf("sync resource = %+v, %v; want a clean row", sr, err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.Title != "Dentist (server)" {
		t.Fatalf("event = %+v, %v; want the recorded server body", got, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Etag != "edit" {
		t.Fatalf("sync resource = %+v, %v; want the server etag", sr, err)
	}
}

// TestConflictReplaceResetsTheMoveColumns replaces a resolved move row with
// a new record. The new conflict must start with no warned etag and no
// cancel rev.
func TestConflictReplaceResetsTheMoveColumns(t *testing.T) {
	t.Parallel()
	_, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	source := calA
	params := storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: 1, Uid: "reset-uid",
		LocalIcal: "x", ServerIcal: "x", ServerEtag: "e1", MoveSourceCalendarID: &source,
	}
	if _, err := q.CreateSyncConflict(ctx, params); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if _, err := q.MarkMoveConflictWarned(ctx, storage.MarkMoveConflictWarnedParams{Etag: "e7", CalendarID: calB, Uid: "reset-uid"}); err != nil {
		t.Fatalf("MarkMoveConflictWarned: %v", err)
	}
	if err := q.SetMoveConflictCancelRev(ctx, storage.SetMoveConflictCancelRevParams{CancelSourceRev: 9, CalendarID: calB, Uid: "reset-uid", MoveSourceCalendarID: &source}); err != nil {
		t.Fatalf("SetMoveConflictCancelRev: %v", err)
	}
	if err := q.CloseMoveConflict(ctx, storage.CloseMoveConflictParams{CalendarID: calB, Uid: "reset-uid"}); err != nil {
		t.Fatalf("CloseMoveConflict: %v", err)
	}
	if n, err := q.CreateSyncConflict(ctx, params); err != nil || n != 1 {
		t.Fatalf("replace = %d rows, %v; want one", n, err)
	}
	row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: "reset-uid"})
	if err != nil || row.WarnedEtag != "" || row.CancelSourceRev != 0 || row.MoveState != MoveStateOpen {
		t.Fatalf("replaced row = %+v, %v; want fresh move columns", row, err)
	}
}

// TestFailedQueueOfAVanishedObjectIsAnError deletes an unpushed event
// while its first PUT is in flight, and the write of the tombstone fails.
// The push must report an error that names the object on the server and
// must not count the row as pushed.
func TestFailedQueueOfAVanishedObjectIsAnError(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "Dentist", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete during the PUT: %v", err)
		}
		if _, err := engine.db.ExecContext(ctx, `CREATE TRIGGER fail_tombstone BEFORE INSERT ON tombstones BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
			t.Errorf("create trigger: %v", err)
		}
	}}
	res, err := engine.PushLocalEdits(ctx, calA)
	engine.testHooks = nil
	if err != nil || res.Pushed != 0 || len(res.Errors) != 1 {
		t.Fatalf("push with a failed queue = %+v, %v; want one error and no push", res, err)
	}
	if !strings.Contains(res.Errors[0].Error(), "delete it there") {
		t.Fatalf("error = %q, want the object named on the server", res.Errors[0])
	}
}

// TestImportAfterADeleteSendsAConditionalUpdate deletes a pushed event and
// imports the UID again with a new body. The import clears the plain
// tombstone and marks the row dirty. The push then sends the new body as an
// update of the object with If-Match.
func TestImportAfterADeleteSendsAConditionalUpdate(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Dentist (imported)", StartTime: evt.StartTime, EndTime: evt.EndTime,
		Import: storage.NewImport(true),
	}); err != nil {
		t.Fatalf("UpsertByUID: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Deleted != 0 {
		t.Fatalf("push after the import = %+v, %v; want one update and no delete", res, err)
	}
	srv.mu.Lock()
	ifMatch := srv.lastIfMatch
	srv.mu.Unlock()
	if ifMatch == "" {
		t.Fatal("the push sent no If-Match: want a conditional update")
	}
	path := "/cal/a/" + evt.UID + ".ics"
	if body := srv.body(path); !strings.Contains(body, "SUMMARY:Dentist (imported)") {
		t.Fatalf("server body = %q, want the imported body", body)
	}
}

// TestGatedCopyRecordsTheMoveForTheLiveSource holds back a moved copy by
// the organizer gate while two source tombstones name its calendar. The
// object of the first source is gone. The push must finish that source and
// record the move conflict for the second one.
func TestGatedCopyRecordsTheMoveForTheLiveSource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calD := linkThirdCalendar(t, engine, q, calA, "d")

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	// A second source in D names B as well, with its object on the server.
	pathD := "/cal/d/" + evt.UID + ".ics"
	srv.setRemoteBody(pathD, srv.body("/cal/a/"+evt.UID+".ics"))
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calD, Uid: evt.UID, OwnerType: "event", RemoteUrl: pathD, Etag: strings.Trim(srv.etag(pathD), `"`), Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource D: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calD, Uid: evt.UID, RemoteUrl: pathD, MovedToCalendarID: &calB}); err != nil {
		t.Fatalf("CreateTombstone D: %v", err)
	}
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 {
		t.Fatalf("push of the gated copy = %+v, %v; want one move conflict", res, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones on A = %v, %v; want the gone source finished", ts, err)
	}
	wantMoveConflict(t, q, calD, calB, evt.UID)
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
		t.Fatalf("sync resource B = %+v, %v; want it dirty while a source remains", sr, err)
	}
}

// TestLocalPickRefusesAReadOnlySource records a move conflict and makes
// the source read-only. The local pick must refuse: the source never
// sends the delete that the move needs.
func TestLocalPickRefusesAReadOnlySource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "the source calendar is read-only") {
		t.Fatalf("local pick with a read-only source = %v, want a refusal", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.ResolvedAt != nil {
		t.Fatalf("conflict after the refusal = %+v, %v; want it open", row, err)
	}
}

// TestRefusedCreateNamesAHeldReadOnlyHolder moves an event from A to C,
// so the tombstone of A waits for C, makes A read-only, and imports the
// UID into B. The refused create in B must name A, held or not.
func TestRefusedCreateNamesAHeldReadOnlyHolder(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calC)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calB, Title: "Import", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evt.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), `read-only calendar "a"`) {
		t.Fatalf("push of B = %+v, %v; want an error that names the read-only holder", res, err)
	}
}

// TestRestoreRefusesAnEditPushedAfterTheCancel cancels a move, edits the
// item in the source, and pushes it. The source row is clean again, but
// its rev differs from the rev of the cancel. The local pick must refuse:
// the restore would overwrite the pushed edit.
func TestRestoreRefusesAnEditPushedAfterTheCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := engine.events.Update(ctx, got.ID, event.UpdateParams{
		Title: "Dentist (after the cancel)", StartTime: got.StartTime, EndTime: got.EndTime, Status: got.Status, Transp: got.Transp, Class: got.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Pushed != 1 {
		t.Fatalf("push of the edit = %+v, %v; want one push", res, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 0 {
		t.Fatalf("source row after the push = %+v, %v; want it clean", sr, err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "changed after the cancel") {
		t.Fatalf("ResolveConflict local = %v, want a refusal", err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.Title != "Dentist (after the cancel)" {
		t.Fatalf("event after the refusal = %+v, %v; want the pushed edit kept", got, err)
	}
}

// TestPlainTombstoneWriteKeepsTheMoveTarget writes a plain tombstone over
// a move tombstone. The move target must stay.
func TestPlainTombstoneWriteKeepsTheMoveTarget(t *testing.T) {
	t.Parallel()
	_, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calA, Uid: "keep-uid", RemoteUrl: "/cal/a/keep-uid.ics", MovedToCalendarID: &calB}); err != nil {
		t.Fatalf("CreateTombstone move: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calA, Uid: "keep-uid", RemoteUrl: "/cal/a/keep-uid.ics"}); err != nil {
		t.Fatalf("CreateTombstone plain: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: "keep-uid"})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
		t.Fatalf("tombstone = %+v, %v; want the move target kept", ts, err)
	}
}

// TestWaitForTheSourceDeleteRecordsAWait lets a create wait for a pending
// delete of a sibling calendar. The row must store the wait with no push
// failure, so the sync doctor lists it when the delete never comes.
func TestWaitForTheSourceDeleteRecordsAWait(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calC)
	if res, err := engine.PushLocalEdits(ctx, calC); err != nil || res.Pushed != 1 {
		t.Fatalf("push of C = %+v, %v; want one push", res, err)
	}
	srv.mu.Lock()
	delete(srv.objects, "/cal/c/"+evt.UID+".ics")
	srv.mu.Unlock()
	srv.setGeneric(false)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calB, Title: "Import", StartTime: start, EndTime: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertByUID in B: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calB, evt.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty B: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Warnings) != 1 || len(res.Errors) != 0 {
		t.Fatalf("push of B = %+v, %v; want one wait warning", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.PushFailCount != 0 || sr.WaitKind != waitKindSourceGone || sr.WaitHolderCalendarID != 0 {
		t.Fatalf("sync resource B = %+v, %v; want the wait with no push failure", sr, err)
	}
}

// TestGatedRowKeepsItsEditConflictUnderPrompt holds an open edit conflict
// on a pushed row that the organizer gate holds back. A prompt pass and
// the opportunistic push must skip the row: the conflict stays open and
// the row stays dirty, so the user settles it.
func TestGatedRowKeepsItsEditConflictUnderPrompt(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	if err := q.MarkResourceDirty(ctx, calA, evt.UID, "event"); err != nil {
		t.Fatalf("MarkResourceDirty: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	check := func(name string, res *SyncResult, err error) {
		t.Helper()
		if err != nil || len(res.Errors) != 0 || res.SkippedConflicts != 1 {
			t.Fatalf("%s = %+v, %v; want one skip", name, res, err)
		}
		if open, err := q.ListSyncConflictsByCalendar(ctx, calA); err != nil || len(open) != 1 {
			t.Fatalf("%s: open conflicts = %v, %v; want the edit conflict kept", name, open, err)
		}
		if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
			t.Fatalf("%s: sync resource = %+v, %v; want the row dirty", name, sr, err)
		}
	}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	check("prompt pass", res, err)
	res, err = engine.PushLocalEdits(ctx, calA)
	check("opportunistic push", res, err)
}

// TestImportOfAnOverrideIntoADeletedSeriesSendsNoPut deletes a pushed
// series and imports one override of it. The tombstone of the series
// stays, so the import marks nothing dirty and the push sends no PUT.
func TestImportOfAnOverrideIntoADeletedSeriesSendsNoPut(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	putsBefore, _ := srv.counts()
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Override", StartTime: evt.StartTime, EndTime: evt.EndTime,
		RecurrenceID: evt.StartTime.Format(time.RFC3339), Import: storage.NewImport(true),
	}); err != nil {
		t.Fatalf("UpsertByUID override: %v", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones = %v, %v; want the series delete kept", ts, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil && sr.Dirty != 0 {
		t.Fatalf("sync resource = %+v; want no dirty mark", sr)
	}
	if _, err := engine.PushLocalEdits(ctx, calA); err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	if puts, _ := srv.counts(); puts != putsBefore {
		t.Fatalf("puts = %d, want %d: the override must stay local", puts, putsBefore)
	}
}

// TestDoctorPushTreatsAHeldMoveCopyAsAnOpenConflict lets the doctor push a
// moved copy whose source changed on the server. A sync of the source
// records an edit conflict on the copy between the conflict check and the
// PUT. The doctor must refuse with its open-conflict message, count no
// push failure, and keep the row dirty.
func TestDoctorPushTreatsAHeldMoveCopyAsAnOpenConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	engine.testHooks = &engineTestHooks{beforeDoctorPut: func() {
		if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
			CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
			LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
		}); err != nil {
			t.Errorf("CreateSyncConflict: %v", err)
		}
	}}
	_, err := engine.DoctorPush(ctx, calB, evt.UID, nil)
	if err == nil || !strings.Contains(err.Error(), "has an open sync conflict") {
		t.Fatalf("DoctorPush = %v, want the open-conflict refusal", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 || sr.PushFailCount != 0 {
		t.Fatalf("sync resource B = %+v, %v; want it dirty with no push failure", sr, err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 1 || open[0].MoveSourceCalendarID != nil {
		t.Fatalf("open conflicts = %v, %v; want the edit conflict kept", open, err)
	}
}

// failCancelOf records a move conflict for a moved event and runs a
// server pick whose import fails. The conflict waits as a failed cancel.
func failCancelOf(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA, calB int64) (event.Event, storage.SyncConflict) {
	t.Helper()
	return failCancelWith(t, engine, q, srv, calA, calB, nil)
}

// failCancelWith is failCancelOf with beforePick, which runs after the
// record of the move conflict and before the server pick.
func failCancelWith(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA, calB int64, beforePick func(evt event.Event)) (event.Event, storage.SyncConflict) {
	t.Helper()
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if beforePick != nil {
		beforePick(evt)
	}
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed")}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil {
		t.Fatal("server pick with a failed import succeeded")
	}
	engine.testHooks = nil
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelFailed {
		t.Fatalf("conflict = %+v, %v; want a failed cancel", row, err)
	}
	return evt, row
}

// pullSource imports the server object of uid into calA, as a pull does.
func pullSource(t *testing.T, engine *Engine, srv *icloudFakeServer, calA int64, uid string) {
	t.Helper()
	path := "/cal/a/" + uid + ".ics"
	data, err := parseICalData([]byte(srv.body(path)))
	if err != nil {
		t.Fatalf("parse the server body: %v", err)
	}
	etag := strings.Trim(srv.etag(path), `"`)
	if _, imported, _, err := engine.importFetchedResource(context.Background(), calA, nil, fetchedResource{path: path, href: path, etag: etag, data: data}); err != nil || !imported {
		t.Fatalf("pull of the source = %v, %v; want an import", imported, err)
	}
}

// TestPullOfTheServerBodySettlesAFailedCancel fails a cancel and pulls the
// source, which imports the recorded server body. The moot step must
// close the failed cancel as cancelled, so a new move of the UID can go.
func TestPullOfTheServerBodySettlesAFailedCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt, c := failCancelOf(t, engine, q, srv, calA, calB)
	if n, err := engine.resolveMootMoveConflicts(ctx, calA); err != nil || n != 0 {
		t.Fatalf("moot step before the pull = %d, %v; want no close", n, err)
	}
	pullSource(t, engine, srv, calA, evt.UID)
	if n, err := engine.resolveMootMoveConflicts(ctx, calA); err != nil || n != 1 {
		t.Fatalf("moot step after the pull = %d, %v; want one close", n, err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelled || row.ResolvedAt == nil {
		t.Fatalf("conflict = %+v, %v; want it closed as cancelled", row, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || row.CancelSourceRev != sr.Rev {
		t.Fatalf("cancel rev = %d, source row = %+v, %v; want the rev of the source", row.CancelSourceRev, sr, err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event = %+v, %v; want the server body in A", got, err)
	}
}

// TestPullOfANewerBodySettlesAFailedCancel fails a cancel, edits the
// source object on the server again, and pulls the source. The source row
// has a newer etag than the record. The moot step must still close the
// failed cancel as cancelled.
func TestPullOfANewerBodySettlesAFailedCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt, c := failCancelOf(t, engine, q, srv, calA, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote 2)")
	pullSource(t, engine, srv, calA, evt.UID)
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Etag == c.ServerEtag {
		t.Fatalf("source row = %+v, %v; want a newer etag than %q", sr, err, c.ServerEtag)
	}
	if n, err := engine.resolveMootMoveConflicts(ctx, calA); err != nil || n != 1 {
		t.Fatalf("moot step after the pull = %d, %v; want one close", n, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateCancelled {
		t.Fatalf("conflict = %+v, %v; want it closed as cancelled", row, err)
	}
}

// TestSourceEtagOfTheRecordDoesNotSettleAFailedCancel fails a cancel,
// then gives the clean source row the recorded server etag with no change
// of the rev. An etag that matches proves no import, so the moot step must
// leave the failed cancel open.
func TestSourceEtagOfTheRecordDoesNotSettleAFailedCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt, c := failCancelOf(t, engine, q, srv, calA, calB)
	before, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if err := q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{CalendarID: calA, Uid: evt.UID, Etag: c.ServerEtag}); err != nil {
		t.Fatalf("ClearSyncResourceDirty: %v", err)
	}
	if after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || after.Rev != before.Rev {
		t.Fatalf("source row = %+v, %v; want the rev %d unchanged", after, err, before.Rev)
	}
	if n, err := engine.resolveMootMoveConflicts(ctx, calA); err != nil || n != 0 {
		t.Fatalf("moot step = %d, %v; want no close", n, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("conflict = %+v, %v; want the failed cancel open", row, err)
	}
}

// TestRebuiltSourceRowSettlesAFailedCancelOnlyAfterAPull covers path 1. The
// source has no sync row at the cancel. The cancel rebuilds it from the
// tombstone with the recorded server etag, and then the import fails. The
// moot step must not settle the row before a pull. Path 2 (the move back of
// a source row that exists) is TestPullOfTheServerBodySettlesAFailedCancel.
func TestRebuiltSourceRowSettlesAFailedCancelOnlyAfterAPull(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt, c := failCancelWith(t, engine, q, srv, calA, calB, func(evt event.Event) {
		if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil {
			t.Fatalf("DeleteSyncResource: %v", err)
		}
	})
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Etag != c.ServerEtag || sr.Dirty != 0 {
		t.Fatalf("source row = %+v, %v; want a clean row with the recorded etag %q", sr, err, c.ServerEtag)
	}
	if n, err := engine.resolveMootMoveConflicts(ctx, calA); err != nil || n != 0 {
		t.Fatalf("moot step before the pull = %d, %v; want no close", n, err)
	}
	pullSource(t, engine, srv, calA, evt.UID)
	if n, err := engine.resolveMootMoveConflicts(ctx, calA); err != nil || n != 1 {
		t.Fatalf("moot step after the pull = %d, %v; want one close", n, err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateCancelled {
		t.Fatalf("conflict = %+v, %v; want it closed as cancelled", row, err)
	}
}

// TestLocalPickAfterACancelRefusesAReadOnlySource cancels a move and makes
// the source read-only. The local pick on the cancelled move must refuse:
// the source never sends the restore.
func TestLocalPickAfterACancelRefusesAReadOnlySource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "the source calendar is read-only") {
		t.Fatalf("local pick after the cancel = %v, want a refusal", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateCancelled {
		t.Fatalf("conflict = %+v, %v; want it cancelled", row, err)
	}
}

// TestGoneItemKeepsTheResolutionOfALocalPick picks local on a move
// conflict and then deletes the item. The close of the gone item must set
// the state abandoned and keep the resolution and resolved_at.
func TestGoneItemKeepsTheResolutionOfALocalPick(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	picked, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || picked.MoveState != MoveStateLocalPicked || picked.ResolvedAt == nil || picked.Resolution == nil {
		t.Fatalf("conflict after the pick = %+v, %v; want a resolved local pick", picked, err)
	}
	if err := storage.CloseConflictsOfGoneItem(ctx, q, calB, evt.UID); err != nil {
		t.Fatalf("CloseConflictsOfGoneItem: %v", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateAbandoned {
		t.Fatalf("conflict = %+v, %v; want it abandoned", row, err)
	}
	if row.ResolvedAt == nil || *row.ResolvedAt != *picked.ResolvedAt || row.Resolution == nil || *row.Resolution != *picked.Resolution {
		t.Fatalf("resolution = %v at %v, want %v at %v", row.Resolution, row.ResolvedAt, *picked.Resolution, *picked.ResolvedAt)
	}
}

// TestGatedCopyWithoutARowEndsTheMove holds back a moved copy by the
// organizer gate, and the export of the copy finds no row while its sync
// row stays dirty. No exportable copy exists, so the push must treat the copy
// as gone: a clean row, no push failure, and a plain source tombstone.
func TestGatedCopyWithoutARowEndsTheMove(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	// The gate reads the event by UID in every calendar. A calendar-scoped
	// export can then find no copy in B. The hook gives that result.
	engine.testHooks = &engineTestHooks{exportFailure: fmt.Errorf("%w: event uid %s", errResourceMissing, evt.UID)}
	res, err := engine.PushLocalEdits(ctx, calB)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.SkippedConflicts != 0 {
		t.Fatalf("push of B = %+v, %v; want a quiet clear", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err == nil && (sr.Dirty != 0 || sr.PushFailCount != 0) {
		t.Fatalf("sync resource B = %+v; want it clean with no push failure", sr)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID != nil {
		t.Fatalf("tombstone A = %+v, %v; want a plain delete", ts, err)
	}
}

// TestResetRefusesTheSourceOfAnOpenMove records a move conflict and asks for
// a reset of the source calendar. The reset deletes the conflicts of its
// calendar, so it refuses while a move conflict is open. Nothing changes:
// the conflict stays open, and the source keeps its move tombstone and its
// sync row.
func TestResetRefusesTheSourceOfAnOpenMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := svc.ResetCalendar(ctx, calA); err == nil || !strings.Contains(err.Error(), `calendar "a" has open move conflicts; resolve them first (chroncal sync conflicts)`) {
		t.Fatalf("ResetCalendar A = %v, want the refusal", err)
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.MoveState != MoveStateOpen || row.ResolvedAt != nil {
		t.Fatalf("conflict = %+v, %v; want it open", row, err)
	}
	if ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil || ts.MovedToCalendarID == nil {
		t.Fatalf("tombstone A = %+v, %v; want the move tombstone kept", ts, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Etag == "" {
		t.Fatalf("sync resource A = %+v, %v; want it kept with its etag", sr, err)
	}
}

// TestResetOfTheTargetKeepsTheMove moves an event from A to B and resets
// B before the copy is pushed. The reset queues the copy again, so the
// move goes on: the server ends with one object of the UID, in B.
func TestResetOfTheTargetKeepsTheMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if err := svc.ResetCalendar(ctx, calB); err != nil {
		t.Fatalf("ResetCalendar B: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
		t.Fatalf("tombstone A = %+v, %v; want the move kept", ts, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
		t.Fatalf("sync resource B = %+v, %v; want the copy queued", sr, err)
	}
	if _, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil {
		t.Fatalf("pass of A: %v", err)
	}
	if _, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil {
		t.Fatalf("pass of B: %v", err)
	}
	if srv.has("/cal/a/"+evt.UID+".ics") || !srv.has("/cal/b/"+evt.UID+".ics") {
		t.Fatalf("server objects: a=%v b=%v; want one object in B", srv.has("/cal/a/"+evt.UID+".ics"), srv.has("/cal/b/"+evt.UID+".ics"))
	}
}

// TestUnlinkOfTheTargetFreesTheSourceDelete moves an event from A to B and
// unlinks B before the copy is pushed. B is local then, so the source
// tombstone follows the rule of a local target. The delete goes out, and it
// does not wait for a push that never comes.
func TestUnlinkOfTheTargetFreesTheSourceDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	calSvc := calendar.NewService(engine.db, q)
	b, err := calSvc.Get(ctx, calB)
	if err != nil {
		t.Fatalf("Get B: %v", err)
	}
	if err := calSvc.Disconnect(ctx, b, engine.credStore); err != nil {
		t.Fatalf("Disconnect B: %v", err)
	}
	if waits, err := storage.MoveDeleteWaitUIDs(ctx, q, calA); err != nil || waits[evt.UID] {
		t.Fatalf("wait set of A = %v, %v; want no wait for the local target", waits, err)
	}
	if _, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil {
		t.Fatalf("pass of A: %v", err)
	}
	if srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("the source object stays on the server: want the delete sent")
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calB {
		t.Fatalf("event = %+v, %v; want the copy in the local calendar", got, err)
	}
}

// TestTombstonePassIgnoresACopyOfTheUIDInAnotherAccount pushes a moved copy
// on a generic server and puts it in the trash with a raw write. The move
// target then stays on the source tombstone. A live row of the UID sits in a
// calendar of another account. The source DELETE gets 412. The copy in the
// target is gone, so the pass must follow the plain rule and record no move
// conflict from the other row.
func TestTombstonePassIgnoresACopyOfTheUIDInAnotherAccount(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want one push", res, err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-04-01T00:00:00Z' WHERE uid = ? AND calendar_id = ?`, evt.UID, calB); err != nil {
		t.Fatalf("trash the copy: %v", err)
	}
	other, err := q.CreateAccount(ctx, storage.CreateAccountParams{Name: "other", ServerUrl: "https://other.example.com", AuthType: "basic", Username: "u"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	calC, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "c"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{ID: calC.ID, AccountID: &other.ID, RemoteUrl: storage.StringToNullable("/other/c/")}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{UID: evt.UID, CalendarID: calC.ID, Title: "Other account", StartTime: evt.StartTime, EndTime: evt.EndTime}); err != nil {
		t.Fatalf("UpsertByUID in C: %v", err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || res.Conflicts != 0 {
		t.Fatalf("pass of A = %+v, %v; want no move conflict", res, err)
	}
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts of B = %v, %v; want none", conflicts, err)
	}
}

// TestResetRefusesTheTargetOfAFailedCancel fails a cancel and asks for a
// reset of the target calendar. A failed cancel is an open move conflict,
// so the reset refuses. The failed cancel stays, and its replay still
// brings the server body into the source.
func TestResetRefusesTheTargetOfAFailedCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt, c := failCancelOf(t, engine, q, srv, calA, calB)
	if err := svc.ResetCalendar(ctx, calB); err == nil || !strings.Contains(err.Error(), `calendar "b" has open move conflicts`) {
		t.Fatalf("ResetCalendar B = %v, want the refusal", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("conflict after the reset = %+v, %v; want the failed cancel kept", row, err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("replay after the reset: %v", err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calA || got.Title != "Dentist (remote)" {
		t.Fatalf("event after the replay = %+v, %v; want the server body in A", got, err)
	}
}

// TestResolveRefusesADetachedMove unlinks the source of an open move
// conflict. Both picks must refuse with the reason: the unlink ended the
// move. The refusal must not say that the copy is gone.
func TestResolveRefusesADetachedMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	calSvc := calendar.NewService(engine.db, q)
	a, err := calSvc.Get(ctx, calA)
	if err != nil {
		t.Fatalf("Get A: %v", err)
	}
	if err := calSvc.Disconnect(ctx, a, engine.credStore); err != nil {
		t.Fatalf("Disconnect A: %v", err)
	}
	for _, pick := range []string{"local", "server"} {
		_, err := svc.ResolveConflict(ctx, c.ID, pick)
		if err == nil || !strings.Contains(err.Error(), `ended because calendar "a" was unlinked`) || strings.Contains(err.Error(), "gone") {
			t.Fatalf("%s pick on a detached move = %v, want the unlink refusal", pick, err)
		}
	}
}

// TestResetRefusesTheSourceOfAFailedCancel fails a cancel and asks for a
// reset of the source calendar. The reset refuses, and the failed cancel
// stays open for its replay.
func TestResetRefusesTheSourceOfAFailedCancel(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	_, c := failCancelOf(t, engine, q, srv, calA, calB)
	if err := svc.ResetCalendar(ctx, calA); err == nil || !strings.Contains(err.Error(), `calendar "a" has open move conflicts`) {
		t.Fatalf("ResetCalendar A = %v, want the refusal", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("conflict = %+v, %v; want the failed cancel open", row, err)
	}
}

// TestMoveRelatedCalendarsNamesTheSourceOfAStoredConflict cancels a move,
// so no tombstone links the two calendars any more, and only the resolved
// move conflict on B names A. A reset of B deletes that row, so the lock
// set of B must hold A.
func TestMoveRelatedCalendarsNamesTheSourceOfAStoredConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of A = %v, %v; want none", ts, err)
	}
	related, err := storage.MoveRelatedCalendars(ctx, q, calB)
	if err != nil || !slices.Contains(related, calA) {
		t.Fatalf("calendars related to B = %v, %v; want A", related, err)
	}
}

// TestDeleteAfterTheFinalizeOfACreateQueuesItsTombstone pushes a new event
// and deletes it right after the finalize. The finalize records the href
// of the new object, so the delete must write a tombstone with that href.
func TestDeleteAfterTheFinalizeOfACreateQueuesItsTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "New", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterFinalize: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete: %v", err)
		}
	}}
	if _, err := engine.PushLocalEdits(ctx, calA); err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	engine.testHooks = nil
	if _, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil {
		t.Fatalf("pass of A: %v", err)
	}
	// The delete saw a pushed row, so its tombstone sent the DELETE of the
	// new object.
	if puts, deletes := srv.counts(); puts != 1 || deletes != 1 {
		t.Fatalf("puts = %d, deletes = %d; want one create and one delete", puts, deletes)
	}
	if srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("the server object stays after the delete")
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones = %v, %v; want the delete done", ts, err)
	}
}

// TestMoveRelatedCalendarsNamesTheTargetOfAnOwnTombstone moves an event
// from A to B before any push, so no conflict row exists. The move
// tombstone of A names B, so the lock set of A must hold B.
func TestMoveRelatedCalendarsNamesTheTargetOfAnOwnTombstone(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	related, err := storage.MoveRelatedCalendars(ctx, q, calA)
	if err != nil || !slices.Contains(related, calB) {
		t.Fatalf("calendars related to A = %v, %v; want B", related, err)
	}
}

// TestResetOfTheSourceKeepsTheMoveOfAnEvent moves an event from A to B on
// iCloud and resets A before any push. The reset keeps the move tombstone,
// so the refused create of B still sends the source DELETE. The server
// ends with one object of the UID, in B.
func TestResetOfTheSourceKeepsTheMoveOfAnEvent(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	resetSourceAndFinishTheMove(t, engine, q, srv, calA, calB, evt.UID)
}

// TestResetOfTheSourceKeepsTheMoveOfATodo is the todo case of
// TestResetOfTheSourceKeepsTheMoveOfAnEvent.
func TestResetOfTheSourceKeepsTheMoveOfATodo(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	td, err := engine.todos.Create(ctx, todo.CreateParams{CalendarID: calA, Summary: "Floss"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("initial push = %+v, %v; want one clean push", res, err)
	}
	if _, err := engine.todos.Update(ctx, td.ID, todo.UpdateParams{Summary: td.Summary, Status: td.Status, CalendarID: calB}); err != nil {
		t.Fatalf("Update (move): %v", err)
	}
	resetSourceAndFinishTheMove(t, engine, q, srv, calA, calB, td.UID)
}

// resetSourceAndFinishTheMove resets the source calendar A of a pending
// move of uid into B and syncs both calendars. It checks that the move
// tombstone survives the reset and that the server ends with one object
// of the UID, in B.
func resetSourceAndFinishTheMove(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA, calB int64, uid string) {
	t.Helper()
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	if err := svc.ResetCalendar(ctx, calA); err != nil {
		t.Fatalf("ResetCalendar A: %v", err)
	}
	if ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: uid}); err != nil || ts.MovedToCalendarID == nil {
		t.Fatalf("tombstone A = %+v, %v; want the move tombstone kept", ts, err)
	}
	if _, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil {
		t.Fatalf("pass of B: %v", err)
	}
	if _, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil {
		t.Fatalf("pass of A: %v", err)
	}
	if srv.has("/cal/a/"+uid+".ics") || !srv.has("/cal/b/"+uid+".ics") {
		t.Fatalf("server objects: a=%v b=%v; want one object in B", srv.has("/cal/a/"+uid+".ics"), srv.has("/cal/b/"+uid+".ics"))
	}
}

// TestResetOfTheTargetKeepsALandedCopy moves an event from A to B on a
// server that keeps a UID unique per collection only. The copy lands in
// B before the source delete goes out. A reset of B must keep the sync
// row of the copy, so the next push sends no second create and records
// no conflict. The source delete then finishes the move.
func TestResetOfTheTargetKeepsALandedCopy(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	if err := svc.ResetCalendar(ctx, calB); err != nil {
		t.Fatalf("ResetCalendar B: %v", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.RemoteUrl == "" || sr.Dirty != 0 {
		t.Fatalf("sync resource B = %+v, %v; want the landed row kept", sr, err)
	}
	putsBefore, _ := srv.counts()
	res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt)
	if err != nil || res.Conflicts != 0 || len(res.Errors) != 0 {
		t.Fatalf("pass of B = %+v, %v; want no conflict", res, err)
	}
	if puts, _ := srv.counts(); puts != putsBefore {
		t.Fatalf("puts = %d, want %d: no second create", puts, putsBefore)
	}
	if _, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil {
		t.Fatalf("pass of A: %v", err)
	}
	if srv.has("/cal/a/"+evt.UID+".ics") || !srv.has("/cal/b/"+evt.UID+".ics") {
		t.Fatalf("server objects: a=%v b=%v; want one object in B", srv.has("/cal/a/"+evt.UID+".ics"), srv.has("/cal/b/"+evt.UID+".ics"))
	}
}

// TestLocalPickRefusesBeforeTheNetwork records a move conflict with no
// server etag, so the local pick needs a GET of the source object. The
// target is read-only, so the pick must refuse before that GET.
func TestLocalPickRefusesBeforeTheNetwork(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET server_etag = '' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("clear the server etag: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}
	srv.mu.Lock()
	getsBefore := srv.gets
	srv.mu.Unlock()
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "the target calendar is read-only") {
		t.Fatalf("local pick = %v, want the read-only refusal", err)
	}
	srv.mu.Lock()
	gets := srv.gets
	srv.mu.Unlock()
	if gets != getsBefore {
		t.Fatalf("GETs = %d, want %d: no network read before a local refusal", gets, getsBefore)
	}
}

// TestPutOfAReCreatedRowKeepsItsBody pushes a new event, and in the PUT
// window deletes it and writes the UID again with a new body. The new sync
// row starts at the same rev, but it has a new id. The finalize must not
// clear it. The new row takes the href and the etag of the new object and
// stays dirty. The next push sends its body as an update.
func TestPutOfAReCreatedRowKeepsItsBody(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "First", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete: %v", err)
		}
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: evt.UID, CalendarID: calA, Title: "Second", StartTime: start, EndTime: start.Add(time.Hour),
		}); err != nil {
			t.Errorf("UpsertByUID: %v", err)
		}
		// A local write marks the new row dirty, which creates its sync
		// row with a new id and a rev that starts again.
		if err := q.MarkResourceDirty(ctx, calA, evt.UID, "event"); err != nil {
			t.Errorf("MarkResourceDirty: %v", err)
		}
	}}
	if _, err := engine.PushLocalEdits(ctx, calA); err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	engine.testHooks = nil
	path := "/cal/a/" + evt.UID + ".ics"
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 || sr.RemoteUrl != path || sr.Etag == "" {
		t.Fatalf("sync resource = %+v, %v; want the new row dirty with the href and etag", sr, err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Pushed != 1 || len(res.Errors) != 0 {
		t.Fatalf("second push = %+v, %v; want one update", res, err)
	}
	srv.mu.Lock()
	ifMatch := srv.lastIfMatch
	srv.mu.Unlock()
	if ifMatch == "" || !strings.Contains(srv.body(path), "SUMMARY:Second") {
		t.Fatalf("server body = %q, If-Match %q; want the new body as an update", srv.body(path), ifMatch)
	}
}

// TestGatedCopyKeepsItsEditConflictWhenTheRecordFails holds an open edit
// conflict on an unpushed moved copy that the organizer gate holds back.
// In a server-wins pass, the move record fails, because the GET of the
// source object fails. The edit conflict must stay open.
func TestGatedCopyKeepsItsEditConflictWhenTheRecordFails(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "local", ServerIcal: "edit", ServerEtag: `"edit"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	srv.setGetStatus("/cal/a/", http.StatusForbidden)
	res, err := engine.SyncCalendar(ctx, calB, ConflictServerWins)
	if err != nil || len(res.Errors) == 0 || res.Conflicts != 0 {
		t.Fatalf("server-wins pass = %+v, %v; want a failed record", res, err)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calB)
	if err != nil || len(open) != 1 || open[0].MoveSourceCalendarID != nil || open[0].ResolvedAt != nil {
		t.Fatalf("conflicts of B = %v, %v; want the edit conflict open", open, err)
	}
}

// TestCancelFinishFailureKeepsTheReplay cancels a move, and a trigger
// fails the statement that stamps the final state. The cancel must leave
// the row open as a failed cancel, not cancelled without its rev. The
// replay then completes the cancel with the rev of the source row.
func TestCancelFinishFailureKeepsTheReplay(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{beforeFinishCancel: func() {
		if _, err := engine.db.ExecContext(ctx, `CREATE TRIGGER fail_finish BEFORE UPDATE ON sync_conflicts
			WHEN NEW.move_state = 'cancelled' BEGIN SELECT RAISE(ABORT, 'injected finish failure'); END`); err != nil {
			t.Errorf("create trigger: %v", err)
		}
	}}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil || !strings.Contains(err.Error(), "injected finish failure") {
		t.Fatalf("server pick = %v, want the injected failure", err)
	}
	engine.testHooks = nil
	if _, err := engine.db.ExecContext(ctx, `DROP TRIGGER fail_finish`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("conflict after the failure = %+v, %v; want an open failed cancel", row, err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	row, err = q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelled || row.ResolvedAt == nil {
		t.Fatalf("conflict after the replay = %+v, %v; want it cancelled", row, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || row.CancelSourceRev != sr.Rev {
		t.Fatalf("cancel rev = %d, source row = %+v, %v; want the rev of the source", row.CancelSourceRev, sr, err)
	}
}

// TestCancelOfAChainMoveRetargetsTheMiddleTombstone moves an event from A
// to B on a server that keeps a UID unique per collection, and pushes B.
// Then it moves the event on to C and pushes C. The tombstone of B names
// C. A remote edit of the A object gets the source DELETE a 412, and the
// user cancels the move back to A. The tombstone of B must name A now,
// because A holds the live copy. A 412 on the delete of B then records a
// move conflict on A, and no second copy comes back.
func TestCancelOfAChainMoveRetargetsTheMiddleTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, got, calC)
	if res, err := engine.PushLocalEdits(ctx, calC); err != nil || res.Pushed != 1 {
		t.Fatalf("push of C = %+v, %v; want the copy landed", res, err)
	}
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calC, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calA {
		t.Fatalf("tombstone of B = %+v, %v; want the target A", ts, err)
	}
}

// TestCancelOfAChainMoveToTheMiddleRetargetsTheFirstTombstone moves an
// event from A to B and from B to C, with every copy pushed. The server
// keeps a UID unique per collection. The tombstone of A names C. A
// remote edit of the B object gets the DELETE of B a 412, so the move
// conflict on C names B as the source. The cancel moves the rows back to
// B. The tombstone of A must name B now, where the live copy is.
func TestCancelOfAChainMoveToTheMiddleRetargetsTheFirstTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, got, calC)
	if res, err := engine.PushLocalEdits(ctx, calC); err != nil || res.Pushed != 1 {
		t.Fatalf("push of C = %+v, %v; want the copy landed", res, err)
	}
	if ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calC {
		t.Fatalf("tombstone of A = %+v, %v; want the target C", ts, err)
	}
	srv.editRemoteSummary("/cal/b/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of B = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calB, calC, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calB {
		t.Fatalf("event = %+v, %v; want it back in B", got, err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
		t.Fatalf("tombstone of A = %+v, %v; want the target B", ts, err)
	}
}

// TestInterruptedCancelImportDoesNotSettle cancels a move whose row holds a
// stale cancel rev, and the context ends during the import of the server
// body. The cancel transaction recorded the rev of the source row, and the
// import changed nothing. The
// next moot step must not count the failed cancel as settled.
func TestInterruptedCancelImportDoesNotSettle(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	// A stale rev from an earlier state of the row. Without the write of
	// the cancel, the source rev differs from it, and the moot step counts
	// the cancel as settled.
	if err := q.SetMoveConflictCancelRev(ctx, storage.SetMoveConflictCancelRevParams{CancelSourceRev: 99, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &calA}); err != nil {
		t.Fatalf("SetMoveConflictCancelRev: %v", err)
	}
	pickCtx, cancel := context.WithCancel(ctx)
	engine.testHooks = &engineTestHooks{importFailure: context.Canceled, beforeImport: cancel}
	if _, err := svc.ResolveConflict(pickCtx, c.ID, "server"); err == nil {
		t.Fatal("server pick with an interrupted import succeeded")
	}
	engine.testHooks = nil
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("conflict = %+v, %v; want an open failed cancel", row, err)
	}
	if n, err := engine.resolveMootMoveConflicts(ctx, calA); err != nil || n != 0 {
		t.Fatalf("moot step = %d, %v; want no close", n, err)
	}
}

// TestCancelThatEndsInTheWindowSucceeds cancels a move, and the moot step
// settles the failed cancel after the import and before the final
// statement. The import succeeded, so the cancel must report success. The
// row must keep the rev that the settle recorded.
func TestCancelThatEndsInTheWindowSucceeds(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{beforeFinishCancel: func() {
		if err := q.CloseSettledFailedCancel(ctx, storage.CloseSettledFailedCancelParams{CancelSourceRev: 77, CancelSourceRowID: 5, CalendarID: calB, Uid: evt.UID}); err != nil {
			t.Errorf("CloseSettledFailedCancel: %v", err)
		}
	}}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("server pick: %v", err)
	}
	engine.testHooks = nil
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.MoveState != MoveStateCancelled {
		t.Fatalf("conflict = %+v, %v; want it cancelled", row, err)
	}
	if row.CancelSourceRev != 77 || row.CancelSourceRowID != 5 {
		t.Fatalf("cancel rev = %d, row id = %d; want the values of the settle", row.CancelSourceRev, row.CancelSourceRowID)
	}
}

// TestReopenOfACancelSkipsAnEndedMove writes a finished move conflict and
// runs the reopen of a cancel on it. The reopen touches only the states
// that a cancel starts from, so the finished row must stay as it is.
func TestReopenOfACancelSkipsAnEndedMove(t *testing.T) {
	t.Parallel()
	_, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: 1, Uid: "ended-uid",
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if err := q.FinishMoveConflict(ctx, storage.FinishMoveConflictParams{CalendarID: calB, Uid: "ended-uid", MoveSourceCalendarID: &calA}); err != nil {
		t.Fatalf("FinishMoveConflict: %v", err)
	}
	if n, err := q.ReopenCancelledMoveConflict(ctx, storage.ReopenCancelledMoveConflictParams{CalendarID: calB, Uid: "ended-uid", MoveSourceCalendarID: &calA}); err != nil || n != 0 {
		t.Fatalf("reopen of a finished move = %d, %v; want no row", n, err)
	}
	row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: "ended-uid"})
	if err != nil || row.MoveState != MoveStateFinished || row.ResolvedAt == nil {
		t.Fatalf("conflict = %+v, %v; want it finished", row, err)
	}
}

// TestFailedCancelBookkeepingIsAtomic fails the import of a cancel and then
// fails the clear of the source dirty flag with a trigger. The rev write
// and the clear run in one transaction. The failure must leave both as
// they were: the stale rev stays, and the source row stays dirty.
func TestFailedCancelBookkeepingIsAtomic(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("MarkSyncResourceDirty: %v", err)
	}
	engine.testHooks = &engineTestHooks{importFailure: errors.New("persist failed"), beforeImport: func() {
		if err := q.SetMoveConflictCancelRev(ctx, storage.SetMoveConflictCancelRevParams{CancelSourceRev: 99, CancelSourceRowID: 99, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &calA}); err != nil {
			t.Errorf("SetMoveConflictCancelRev: %v", err)
		}
		if _, err := engine.db.ExecContext(ctx, `CREATE TRIGGER fail_clear BEFORE UPDATE OF dirty ON sync_resources
			WHEN NEW.dirty = 0 AND OLD.dirty = 1 BEGIN SELECT RAISE(ABORT, 'injected clear failure'); END`); err != nil {
			t.Errorf("create trigger: %v", err)
		}
	}}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil || !strings.Contains(err.Error(), "injected clear failure") {
		t.Fatalf("server pick = %v, want the injected failure", err)
	}
	engine.testHooks = nil
	if _, err := engine.db.ExecContext(ctx, `DROP TRIGGER fail_clear`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	row, err := q.GetSyncConflict(ctx, c.ID)
	if err != nil || row.CancelSourceRev != 99 || row.CancelSourceRowID != 99 {
		t.Fatalf("conflict = %+v, %v; want the stale rev kept", row, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
		t.Fatalf("source row = %+v, %v; want it still dirty", sr, err)
	}
}

// TestRestoreRefusesARecreatedSourceRow cancels a move, and then the source
// sync row goes away and comes back with the same rev. The new row has a
// new id, so the local pick must refuse: the row changed after the cancel.
func TestRestoreRefusesARecreatedSourceRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	old, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `DELETE FROM sync_resources WHERE id = ?`, old.ID); err != nil {
		t.Fatalf("delete the source row: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy, rev)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)`, old.CalendarID, old.Uid, old.OwnerType, old.RemoteUrl, old.Etag, old.SyncStrategy, old.Rev); err != nil {
		t.Fatalf("create the source row again: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "changed after the cancel") {
		t.Fatalf("local pick on a re-created source row = %v, want a refusal", err)
	}
}

// TestVanishedRowBesideANewRowAtTheSameHref pushes a new event, and in the
// PUT window the event goes away and a new row of the UID names the same
// href. The PUT overwrote the object of the new row, so the new row must
// take the new etag and stay dirty, with no tombstone.
func TestVanishedRowBesideANewRowAtTheSameHref(t *testing.T) {
	t.Parallel()
	vanishedRowBesideANewRow(t, true)
}

// TestVanishedRowBesideANewRowAtAnotherHref is the case of another href.
// The object at the PUT path is an orphan. The push must report an error
// that names it, write no tombstone, and leave the new row as it is.
func TestVanishedRowBesideANewRowAtAnotherHref(t *testing.T) {
	t.Parallel()
	vanishedRowBesideANewRow(t, false)
}

func vanishedRowBesideANewRow(t *testing.T, sameHref bool) {
	t.Helper()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "First", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := "/cal/a/" + evt.UID + ".ics"
	newHref := path
	if !sameHref {
		newHref = "/cal/a/other-" + evt.UID + ".ics"
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete: %v", err)
		}
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{UID: evt.UID, CalendarID: calA, Title: "Second", StartTime: start, EndTime: start.Add(time.Hour)}); err != nil {
			t.Errorf("UpsertByUID: %v", err)
		}
		if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID: calA, Uid: evt.UID, OwnerType: "event", RemoteUrl: newHref, Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
		}); err != nil {
			t.Errorf("UpsertSyncResource: %v", err)
		}
	}}
	res, err := engine.PushLocalEdits(ctx, calA)
	engine.testHooks = nil
	if err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones = %v, %v; want none", ts, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if sameHref {
		if len(res.Errors) != 0 || sr.Dirty != 1 || sr.Etag == "old" || sr.RemoteUrl != path {
			t.Fatalf("push = %+v, row = %+v; want the new row dirty with the new etag", res, sr)
		}
		return
	}
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Error(), "delete the object at "+path) {
		t.Fatalf("push = %+v; want an error that names the orphan object", res)
	}
	if sr.RemoteUrl != newHref || sr.Etag != "old" {
		t.Fatalf("new row = %+v; want it unchanged", sr)
	}
	if !srv.has(path) {
		t.Fatal("the orphan object is gone: want nothing destructive")
	}
}

// TestDivergedCopyWarnsOncePerBodyWithoutAnEtag is the case of a server that
// gives no etag. The record knows no body of the copy, so the first pull
// only stores the hash as the baseline and does not warn. Each later body
// gets one warning.
func TestDivergedCopyWarnsOncePerBodyWithoutAnEtag(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	moved, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := engine.events.Update(ctx, moved.ID, event.UpdateParams{
		Title: "Dentist (local)", StartTime: moved.StartTime, EndTime: moved.EndTime, Status: moved.Status, Transp: moved.Transp, Class: moved.Class,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pathB := "/cal/b/" + evt.UID + ".ics"
	pull := func() []ImportWarning {
		t.Helper()
		data, err := parseICalData([]byte(srv.body(pathB)))
		if err != nil {
			t.Fatalf("parse the server body: %v", err)
		}
		_, _, warnings, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: pathB, href: pathB, etag: "", data: data})
		if err != nil {
			t.Fatalf("importFetchedResource: %v", err)
		}
		return warnings
	}
	if w := pull(); len(w) != 0 {
		t.Fatalf("first pull warnings = %v, want none: the baseline", w)
	}
	srv.editRemoteSummary(pathB, "Dentist (server 1)")
	if w := pull(); len(w) != 1 {
		t.Fatalf("pull of a new body warnings = %v, want one", w)
	}
	if w := pull(); len(w) != 0 {
		t.Fatalf("pull with the same body warnings = %v, want none", w)
	}
}

// TestImportedOverrideOfADeletedSeriesSendsNoPutAfterItsFields deletes a
// pushed series and imports one override of it, with the child fields
// that the ical import writes. The master is in the trash, so those writes
// make no dirty mark (issue #842). The push must not PUT the override alone.
func TestImportedOverrideOfADeletedSeriesSendsNoPutAfterItsFields(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	putsBefore, _ := srv.counts()
	override, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Override", StartTime: evt.StartTime, EndTime: evt.EndTime,
		RecurrenceID: evt.StartTime.Format(time.RFC3339), Import: storage.NewImport(true),
	})
	if err != nil {
		t.Fatalf("UpsertByUID override: %v", err)
	}
	// The ical import replaces every child collection, and each replace
	// marks the row dirty.
	if err := engine.events.ReplaceAlarms(ctx, override.ID, nil); err != nil {
		t.Fatalf("ReplaceAlarms: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	// The master is in the trash, so the override writes no sync state
	// (issue #842). The push then has no row to hold back.
	if len(res.Errors) != 0 {
		t.Fatalf("push = %+v; want no error", res)
	}
	if puts, _ := srv.counts(); puts != putsBefore {
		t.Fatalf("puts = %d, want %d: the override must stay local", puts, putsBefore)
	}
}

// TestDivergedCopyRecordSeedsTheEtagOfTheCopy records a move conflict for a
// landed copy. The record seeds the etag of the copy, so a pull of the
// unchanged copy gives no warning.
func TestDivergedCopyRecordSeedsTheEtagOfTheCopy(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	srv.setGeneric(true)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push B = %+v, %v; want one push", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource B: %v", err)
	}
	row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || row.WarnedEtag == "" || row.WarnedEtag != sr.Etag {
		t.Fatalf("conflict = %+v, %v; want the etag %q of the copy seeded", row, err, sr.Etag)
	}
}

// TestUnlinkedTargetConflictDoesNotHoldTheSource records a move conflict on
// B and then removes the account of B, which keeps the remote_url of B
// for a re-link. The conflict must stay open, but it must not hold the
// source tombstone of A, and both picks must refuse.
func TestUnlinkedTargetConflictDoesNotHoldTheSource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if waits, err := storage.MoveDeleteWaitUIDs(ctx, q, calA); err != nil || !waits[evt.UID] {
		t.Fatalf("wait set of A = %v, %v; want the open conflict to hold A", waits, err)
	}
	// The account delete of B: no account, the remote_url stays.
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET account_id = NULL WHERE id = ?`, calB); err != nil {
		t.Fatalf("unlink B: %v", err)
	}
	if waits, err := storage.MoveDeleteWaitUIDs(ctx, q, calA); err != nil || waits[evt.UID] {
		t.Fatalf("wait set of A = %v, %v; want no hold from the unlinked B", waits, err)
	}
	for _, pick := range []string{"local", "server"} {
		if _, err := svc.ResolveConflict(ctx, c.ID, pick); err == nil || !strings.Contains(err.Error(), `calendar "b" is not linked; add the account again or unlink the calendar`) {
			t.Fatalf("%s pick = %v, want the unlinked refusal", pick, err)
		}
	}
	if row, err := q.GetSyncConflict(ctx, c.ID); err != nil || row.ResolvedAt != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict = %+v, %v; want it kept open for a re-link", row, err)
	}
}

// TestGoneRowBesideAPendingDeleteStillClears deletes a pushed event, so its
// tombstone waits, and a late mark makes its sync row dirty. The DELETE
// fails, so the tombstone stays. In a server-wins pass, the gone-row branch
// must still clear the dirty flag. The skip of a pending delete holds back
// only a PUT.
func TestGoneRowBesideAPendingDeleteStillClears(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("MarkSyncResourceDirty: %v", err)
	}
	srv.setFailDelete(true)
	if _, err := engine.SyncCalendar(ctx, calA, ConflictServerWins); err != nil {
		t.Fatalf("pass of A: %v", err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones = %v, %v; want the delete still pending", ts, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.Dirty != 0 {
		t.Fatalf("sync resource = %+v, %v; want the dirty flag cleared by the gone-row handling", sr, err)
	}
}

// TestImportUpsertMarksAChangedRowDirty upserts a journal with the import
// flag into a synced calendar and clears the dirty flag, as a push does.
// Then it upserts the journal again with a new title. The upsert runs without the child
// replacements of the ical import, so only ApplyImport can mark the row.
// The mark must come, so the push sends the new title.
func TestImportUpsertMarksAChangedRowDirty(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	for i, title := range []string{"First", "Second"} {
		if _, err := engine.journals.UpsertByUID(ctx, journal.UpsertParams{UID: "plain-journal", CalendarID: calA, Summary: title, Import: storage.NewImport(true)}); err != nil {
			t.Fatalf("UpsertByUID %q: %v", title, err)
		}
		if i == 0 {
			if err := q.ClearSyncResourceDirtyFlag(ctx, storage.ClearSyncResourceDirtyFlagParams{CalendarID: calA, Uid: "plain-journal"}); err != nil {
				t.Fatalf("ClearSyncResourceDirtyFlag: %v", err)
			}
		}
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: "plain-journal"})
	if err != nil || sr.Dirty != 1 {
		t.Fatalf("sync resource = %+v, %v; want the changed journal dirty", sr, err)
	}
}

// TestEditConflictPickOnAnUnlinkedCalendarWorks records an edit conflict on
// a calendar and then removes its account, which keeps the remote_url. An
// edit-conflict pick is local only, so it must still work.
func TestEditConflictPickOnAnUnlinkedCalendarWorks(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	body := srv.body("/cal/a/" + evt.UID + ".ics")
	c, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: body, ServerIcal: body, ServerEtag: "e9",
	})
	if err != nil || c != 1 {
		t.Fatalf("CreateSyncConflict = %d, %v", c, err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET account_id = NULL WHERE id = ?`, calA); err != nil {
		t.Fatalf("unlink A: %v", err)
	}
	open, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(open) != 1 {
		t.Fatalf("open conflicts = %v, %v", open, err)
	}
	if _, err := svc.ResolveConflict(ctx, open[0].ID, "local"); err != nil {
		t.Fatalf("edit-conflict pick on an unlinked calendar = %v, want success", err)
	}
}

// TestMovePickRefusesAnUnlinkWhileItWaits records a move conflict, and the
// account of the target goes away while the pick waits for its locks. The
// check after the locks must refuse the pick.
func TestMovePickRefusesAnUnlinkWhileItWaits(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	engine.testHooks = &engineTestHooks{afterConflictLock: func() {
		if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET account_id = NULL WHERE id = ?`, calB); err != nil {
			t.Errorf("unlink B: %v", err)
		}
	}}
	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), `calendar "b" is not linked`) {
		t.Fatalf("move pick after an unlink in the wait = %v, want the unlinked refusal", err)
	}
}

// TestCancelLocksTheCalendarsOfTheTombstonesThatNameTheTarget sets up the
// chain A to B to C with source B, so the tombstone of A names C. The move
// back of the cancel rewrites that tombstone, so the resolve must hold the
// lock of A as well.
func TestCancelLocksTheCalendarsOfTheTombstonesThatNameTheTarget(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	moveEvent(t, engine, got, calC)
	if res, err := engine.PushLocalEdits(ctx, calC); err != nil || res.Pushed != 1 {
		t.Fatalf("push of C = %+v, %v; want the copy landed", res, err)
	}
	srv.editRemoteSummary("/cal/b/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.SyncCalendar(ctx, calB, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of B = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calB, calC, evt.UID)
	heldA := false
	engine.testHooks = &engineTestHooks{afterConflictLock: func() {
		lock := synclock.Calendar(engine.db, calA)
		if lock.TryLock() {
			lock.Unlock()
		} else {
			heldA = true
		}
	}}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	engine.testHooks = nil
	if !heldA {
		t.Fatal("the resolve did not hold the lock of A, whose tombstone the move back rewrites")
	}
}

// TestRefusedDeleteActsOnTheCurrentMoveTarget gets a 412 on the source
// DELETE of a move. In the window after the 412, a local write turns the
// move target of the tombstone plain, so its version changes. The pass must
// skip the tombstone with no error. The next pass then follows the plain
// rule: no move conflict.
func TestRefusedDeleteActsOnTheCurrentMoveTarget(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	engine.testHooks = &engineTestHooks{afterTombstoneDelete: func() {
		if err := q.ClearMoveTargetOfTombstones(ctx, storage.ClearMoveTargetOfTombstonesParams{Uid: evt.UID, CalendarID: &calB}); err != nil {
			t.Errorf("ClearMoveTargetOfTombstones: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || res.Conflicts != 0 || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want a quiet skip", res, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones = %v, %v; want the tombstone kept for the next pass", ts, err)
	}
	res, err = engine.SyncCalendar(ctx, calA, ConflictPrompt)
	if err != nil || res.Conflicts != 0 || len(res.Errors) != 0 {
		t.Fatalf("next pass of A = %+v, %v; want the plain rule", res, err)
	}
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts of B = %v, %v; want none", conflicts, err)
	}
}

// TestDoctorPushRefusesARowWithAPendingDelete deletes a pushed series and
// writes a live override with a dirty mark. The doctor push must refuse,
// as the push holds the row back: the pending delete owns the href.
func TestDoctorPushRefusesARowWithAPendingDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Override", StartTime: evt.StartTime, EndTime: evt.EndTime,
		RecurrenceID: evt.StartTime.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("UpsertByUID override: %v", err)
	}
	if err := q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("MarkSyncResourceDirty: %v", err)
	}
	putsBefore, _ := srv.counts()
	if _, err := engine.DoctorPush(ctx, calA, evt.UID, nil); err == nil || !strings.Contains(err.Error(), "pending delete") {
		t.Fatalf("DoctorPush = %v, want the pending-delete refusal", err)
	}
	if puts, _ := srv.counts(); puts != putsBefore {
		t.Fatalf("puts = %d, want %d", puts, putsBefore)
	}
}

// TestDoctorPushRecordsAFailedSettleOfAVanishedRow pushes a new event
// through the doctor. In the PUT window the event goes away and a new row
// of the UID names another href. The settle fails, so the doctor must
// record a push failure on the row, as the push does.
func TestDoctorPushRecordsAFailedSettleOfAVanishedRow(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calA, Title: "First", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterPut: func() {
		if err := engine.events.Delete(ctx, evt.ID); err != nil {
			t.Errorf("Delete: %v", err)
		}
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{UID: evt.UID, CalendarID: calA, Title: "Second", StartTime: start, EndTime: start.Add(time.Hour)}); err != nil {
			t.Errorf("UpsertByUID: %v", err)
		}
		if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID: calA, Uid: evt.UID, OwnerType: "event", RemoteUrl: "/cal/a/other-" + evt.UID + ".ics", Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
		}); err != nil {
			t.Errorf("UpsertSyncResource: %v", err)
		}
	}}
	_, err = engine.DoctorPush(ctx, calA, evt.UID, nil)
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), "delete the object at") {
		t.Fatalf("DoctorPush = %v, want the orphan error", err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.PushFailCount != 1 {
		t.Fatalf("sync resource = %+v, %v; want one push failure", sr, err)
	}
}

// TestRetargetBeforeTheDeleteSkipsTheTombstone rewrites the move target of a
// tombstone right before the tombstone pass sends its DELETE. The version
// changes, so the pass must skip the tombstone: no DELETE and no error.
func TestRetargetBeforeTheDeleteSkipsTheTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	_, deletesBefore := srv.counts()
	engine.testHooks = &engineTestHooks{beforeTombstoneDelete: func() {
		if err := q.RetargetMoveTombstones(ctx, storage.RetargetMoveTombstonesParams{ToCalendarID: &calC, Uid: evt.UID, FromCalendarID: &calB}); err != nil {
			t.Errorf("RetargetMoveTombstones: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Deleted != 0 {
		t.Fatalf("pass of A = %+v, %v; want a quiet skip", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("deletes = %d, want %d: no DELETE after the rewrite", deletes, deletesBefore)
	}
}

// TestRetargetAfterADeleteDoesNotFinishTheWrongMove sends the source DELETE
// of a move, and a local write points the tombstone at another calendar
// while the server handles the DELETE. The pass must not finish the move of the
// old target. It skips the tombstone, and the next pass finishes the
// current move.
func TestRetargetAfterADeleteDoesNotFinishTheWrongMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	// A move row of B that the local pick resolved: the finish of the move
	// marks it finished.
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if err := q.MarkSyncConflictResolved(ctx, storage.MarkSyncConflictResolvedParams{ResolvedAt: ptrString("2026-04-01T00:00:00Z"), Resolution: ptrString("local"), CalendarID: calB, Uid: evt.UID}); err != nil {
		t.Fatalf("MarkSyncConflictResolved: %v", err)
	}
	if _, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: MoveStateLocalPicked, CalendarID: calB, Uid: evt.UID, MoveSourceCalendarID: &calA}); err != nil {
		t.Fatalf("SetMoveConflictState: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterTombstoneDelete: func() {
		if err := q.RetargetMoveTombstones(ctx, storage.RetargetMoveTombstonesParams{ToCalendarID: &calC, Uid: evt.UID, FromCalendarID: &calB}); err != nil {
			t.Errorf("RetargetMoveTombstones: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want a quiet skip", res, err)
	}
	if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateLocalPicked {
		t.Fatalf("move row of B = %+v, %v; want it not finished", row, err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calC {
		t.Fatalf("tombstone = %+v, %v; want it kept with the target C", ts, err)
	}
	// The server object is gone all the same, so the source row records it.
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil && (sr.RemoteUrl != "" || sr.Etag != "") {
		t.Fatalf("source row = %+v; want no href and no etag", sr)
	}
}

// TestVanishedTombstoneBeforeTheDeleteIsNoError deletes a tombstone right
// before the pass sends its DELETE. That is a benign race: the pass must
// skip it with no error and no DELETE.
func TestVanishedTombstoneBeforeTheDeleteIsNoError(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, deletesBefore := srv.counts()
	engine.testHooks = &engineTestHooks{beforeTombstoneDelete: func() {
		if _, err := engine.db.ExecContext(ctx, `DELETE FROM tombstones WHERE uid = ?`, evt.UID); err != nil {
			t.Errorf("delete the tombstone: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want no error", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("deletes = %d, want %d", deletes, deletesBefore)
	}
}

// TestResolveLockSetIgnoresTombstonesOfOtherUIDs records a move conflict on
// B. A third calendar C holds a tombstone of another UID that names B. The
// move back of the resolve rewrites only tombstones of its own UID, so the
// resolve must not lock C.
func TestResolveLockSetIgnoresTombstonesOfOtherUIDs(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calC, Uid: "other-uid", RemoteUrl: "/cal/c/other-uid.ics", MovedToCalendarID: &calB}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	heldC := false
	engine.testHooks = &engineTestHooks{afterConflictLock: func() {
		lock := synclock.Calendar(engine.db, calC)
		if lock.TryLock() {
			lock.Unlock()
		} else {
			heldC = true
		}
	}}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	engine.testHooks = nil
	if heldC {
		t.Fatal("the resolve locked C for a tombstone of another UID")
	}
}

// TestServerWinsKeepsTheServerSeriesBesideAHeldOverride deletes a pushed
// series whose server object changed, and records the edit conflict of the
// series. It imports a live override, which the push holds back. A
// server-wins pass must not settle the edit conflict by an import of the
// recorded body. That import would bring the series back with the fresh
// etag, and the DELETE would then remove the server version. The DELETE
// must keep its stale etag, get a 412, and keep the server object.
func TestServerWinsKeepsTheServerSeriesBesideAHeldOverride(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.editRemoteSummary(path, "Dentist (server)")
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calA, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: srv.body(path), ServerIcal: srv.body(path), ServerEtag: strings.Trim(srv.etag(path), `"`),
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	override, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Override", StartTime: evt.StartTime, EndTime: evt.EndTime,
		RecurrenceID: evt.StartTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("UpsertByUID override: %v", err)
	}
	if err := engine.events.ReplaceAlarms(ctx, override.ID, nil); err != nil {
		t.Fatalf("ReplaceAlarms: %v", err)
	}
	if _, err := engine.SyncCalendar(ctx, calA, ConflictServerWins); err != nil {
		t.Fatalf("server-wins pass: %v", err)
	}
	if !srv.has(path) || !strings.Contains(srv.body(path), "SUMMARY:Dentist (server)") {
		t.Fatalf("server object = %q; want the server version kept", srv.body(path))
	}
}

func ptrString(v string) *string { return &v }

// TestRestoreDuringTheDeleteLeavesNoStaleHref deletes a pushed event, and
// the user restores it while the tombstone pass has the DELETE on the
// wire. The server object is gone, so the restored row must lose its href
// and its etag and stay dirty. The next push then creates the item again.
func TestRestoreDuringTheDeleteLeavesNoStaleHref(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterTombstoneDelete: func() {
		if err := engine.events.RestoreByID(ctx, evt.ID); err != nil {
			t.Errorf("RestoreByID: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want no error", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.RemoteUrl != "" || sr.Etag != "" || sr.Dirty != 1 {
		t.Fatalf("sync resource = %+v, %v; want a dirty row with no href", sr, err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Pushed != 1 {
		t.Fatalf("push after the restore = %+v, %v; want a create", res, err)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("the restored item is not on the server")
	}
}

// TestHeldBackEditSurvivesTheDeleteOfItsSeries deletes a pushed series and
// writes a live override beside the master in the trash. The tombstone pass
// deletes the series object. The override stays local: the export refuses
// a body with no master (issue #842). The next pass sends no PUT, and the
// override row stays live.
func TestHeldBackEditSurvivesTheDeleteOfItsSeries(t *testing.T) {
	t.Parallel()
	engine, _, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	override, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Override", StartTime: evt.StartTime, EndTime: evt.EndTime,
		RecurrenceID: evt.StartTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("UpsertByUID override: %v", err)
	}
	if err := engine.events.ReplaceAlarms(ctx, override.ID, nil); err != nil {
		t.Fatalf("ReplaceAlarms: %v", err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("first pass = %+v, %v; want the series deleted", res, err)
	}
	putsBefore, _ := srv.counts()
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Pushed != 0 {
		t.Fatalf("second pass = %+v, %v; want no push: the override stays local", res, err)
	}
	if puts, _ := srv.counts(); puts != putsBefore {
		t.Fatalf("puts = %d, want %d: the override must stay local", puts, putsBefore)
	}
	if body := srv.body("/cal/a/" + evt.UID + ".ics"); body != "" {
		t.Fatalf("server body = %q, want no object", body)
	}
	if got, err := engine.events.Get(ctx, override.ID); err != nil || got.DeletedAt != nil {
		t.Fatalf("override = %+v, %v; want it live", got, err)
	}
}

// TestConflictRecordedMidPassStopsTheDelete records a move conflict that
// names the source calendar right before the tombstone pass sends the
// source DELETE. The record bumps the version of the tombstone, so the pass
// must skip the DELETE.
func TestConflictRecordedMidPassStopsTheDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	_, deletesBefore := srv.counts()
	engine.testHooks = &engineTestHooks{beforeTombstoneDelete: func() {
		if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
			CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
			LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
		}); err != nil {
			t.Errorf("CreateSyncConflict: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want no error", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("deletes = %d, want %d: the new conflict must stop the DELETE", deletes, deletesBefore)
	}
}

// TestDoctorPushNamesADeleteThatWaitsForItsMove holds an open move conflict
// that names A as the source, so the delete of A waits for the move. A
// live dirty row of the UID in A waits behind that delete. The doctor must
// say that the move conflict comes first.
func TestDoctorPushNamesADeleteThatWaitsForItsMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	// A live row of the UID comes back into A, with a dirty mark.
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Back", StartTime: evt.StartTime, EndTime: evt.EndTime,
		RecurrenceID: evt.StartTime.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("UpsertByUID: %v", err)
	}
	if err := q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("MarkSyncResourceDirty: %v", err)
	}
	if _, err := engine.DoctorPush(ctx, calA, evt.UID, nil); err == nil || !strings.Contains(err.Error(), "the delete waits for its move; resolve the move conflict first") {
		t.Fatalf("DoctorPush = %v, want the wait advice", err)
	}
}

// TestConflictRecordedDuringTheDeleteKeepsTheMoveOpen sends the source
// DELETE of a move, and a move conflict is recorded while the DELETE is on
// the wire. The record changes the wait fingerprint of the tombstone, and
// the guarded clear compares that fingerprint. So the pass must not finish
// the new conflict: the tombstone stays, and the conflict stays open.
func TestConflictRecordedDuringTheDeleteKeepsTheMoveOpen(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	engine.testHooks = &engineTestHooks{afterTombstoneDelete: func() {
		if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
			CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
			LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
		}); err != nil {
			t.Errorf("CreateSyncConflict: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want no error", res, err)
	}
	row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || row.MoveState != MoveStateOpen || row.ResolvedAt != nil {
		t.Fatalf("move conflict = %+v, %v; want it open", row, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones = %v, %v; want the tombstone kept for the next pass", ts, err)
	}
}

// TestCleanRowWithLiveRowsIsCreatedAfterTheDelete deletes a pushed series,
// writes a live override, and clears the dirty flag of the sync row. The
// tombstone pass deletes the series object. The row has live rows, so it
// becomes dirty with no href. The master is in the trash, so the export
// refuses the body (issue #842). The next push sends no PUT and drops the
// row, and the override stays local.
func TestCleanRowWithLiveRowsIsCreatedAfterTheDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	override, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calA, Title: "Override", StartTime: evt.StartTime, EndTime: evt.EndTime,
		RecurrenceID: evt.StartTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("UpsertByUID override: %v", err)
	}
	if err := q.ClearSyncResourceDirtyFlag(ctx, storage.ClearSyncResourceDirtyFlagParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("ClearSyncResourceDirtyFlag: %v", err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Deleted != 1 {
		t.Fatalf("first pass = %+v, %v; want the series deleted", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.RemoteUrl != "" || sr.Dirty != 1 {
		t.Fatalf("sync resource = %+v, %v; want a dirty row with no href", sr, err)
	}
	putsBefore, _ := srv.counts()
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 || res.Pushed != 0 {
		t.Fatalf("second pass = %+v, %v; want no push: the master is in the trash", res, err)
	}
	if puts, _ := srv.counts(); puts != putsBefore {
		t.Fatalf("puts = %d, want %d: the override must stay local", puts, putsBefore)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err == nil {
		t.Fatalf("sync resource after the push = %v, want no row", err)
	}
	if got, err := engine.events.Get(ctx, override.ID); err != nil || got.DeletedAt != nil {
		t.Fatalf("override = %+v, %v; want it live", got, err)
	}
}

// TestSettleMissDoesNotRetryThePut moves an event on iCloud, so the create
// in B is refused and the push sends the source DELETE of A itself. A write
// changes the tombstone of A while the server handles that DELETE. The
// settle must not retry the PUT, and the tombstone stays for the next pass.
// The skip is quiet: the row of B stays dirty, with no conflict and no push
// failure.
func TestSettleMissDoesNotRetryThePut(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	putsBefore, _ := srv.counts()
	engine.testHooks = &engineTestHooks{afterTombstoneDelete: func() {
		if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET version = version + 1 WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
			t.Errorf("bump the tombstone: %v", err)
		}
	}}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Pushed != 0 {
		t.Fatalf("PushLocalEdits = %+v, %v; want a quiet skip", res, err)
	}
	engine.testHooks = nil
	if puts, _ := srv.counts(); puts != putsBefore+1 {
		t.Fatalf("puts = %d, want %d: no retry of the PUT", puts, putsBefore+1)
	}
	wantQuietSkip(t, q, calB, evt.UID)
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones of A = %v, %v; want the tombstone kept", ts, err)
	}
}

// TestSettleOfTwoSourcesHasNoSelfInflictedMiss moves an event from A to B
// on iCloud while D holds a tombstone of the UID from before the upgrade.
// The create in B is refused, and the push deletes both sources. The clear
// of A changes the wait fingerprint of D. The settle must read D again
// before its DELETE, so the change is no miss, and the PUT retries and
// lands.
func TestSettleOfTwoSourcesHasNoSelfInflictedMiss(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calD := linkThirdCalendar(t, engine, q, calA, "d")

	evt := createPushedEvent(t, engine, calA)
	pathD := "/cal/d/" + evt.UID + ".ics"
	srv.setRemoteBody(pathD, srv.body("/cal/a/"+evt.UID+".ics"))
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calD, Uid: evt.UID, OwnerType: "event", RemoteUrl: pathD, Etag: strings.Trim(srv.etag(pathD), `"`), Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource D: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calD, Uid: evt.UID, RemoteUrl: pathD}); err != nil {
		t.Fatalf("CreateTombstone D: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET legacy_move = 1 WHERE calendar_id = ?`, calD); err != nil {
		t.Fatalf("mark the legacy tombstone: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the create after both deletes", res, err)
	}
	if srv.has("/cal/a/"+evt.UID+".ics") || srv.has(pathD) || !srv.has("/cal/b/"+evt.UID+".ics") {
		t.Fatalf("server objects: a=%v d=%v b=%v; want one object in B", srv.has("/cal/a/"+evt.UID+".ics"), srv.has(pathD), srv.has("/cal/b/"+evt.UID+".ics"))
	}
}

// wantQuietSkip checks the state after a skipped settle. The row of the
// moved copy stays dirty, with no push failure. No conflict names the UID
// on that calendar.
func wantQuietSkip(t *testing.T, q *storage.Queries, calID int64, uid string) {
	t.Helper()
	ctx := context.Background()
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calID, Uid: uid})
	if err != nil || sr.Dirty != 1 || sr.PushFailCount != 0 {
		t.Fatalf("sync resource = %+v, %v; want it dirty with no push failure", sr, err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calID); err != nil || len(open) != 0 {
		t.Fatalf("conflicts = %v, %v; want none", open, err)
	}
}

// TestMoveRecordMissOnAGoneSourceIsNoGoneCopy records the move conflict of
// a refused source DELETE, and the source object is gone at the GET. The
// tombstone of the pass is stale, so the clear misses. The record must
// report errTombstoneChanged, never a gone copy: the tombstone pass skips
// the tombstone and does not apply the plain rule. With the current
// tombstone, the same record finishes the move.
func TestMoveRecordMissOnAGoneSourceIsNoGoneCopy(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	srv.setGetStatus("/cal/a/", http.StatusNotFound)
	_, _, client, remoteURL, err := engine.loadCalendarClient(ctx, calA)
	if err != nil {
		t.Fatalf("loadCalendarClient: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetTombstone: %v", err)
	}
	target, found, err := engine.moveTarget(ctx, ts)
	if err != nil || !found {
		t.Fatalf("moveTarget = %v, %v; want the copy in B", found, err)
	}
	stale := ts
	stale.Version--
	src := movedSource{calendarID: calA, remoteURL: remoteURL, tombstone: stale, clear: clearAfterOwnDelete()}
	if _, err := engine.recordMoveConflictFor(ctx, client, target, src); !errors.Is(err, errTombstoneChanged) {
		t.Fatalf("recordMoveConflictFor with a stale tombstone = %v, want errTombstoneChanged", err)
	}
	if outcome, err := engine.recordTombstoneMoveConflict(ctx, client, target, src); outcome != tombstoneSkipped || err != nil {
		t.Fatalf("recordTombstoneMoveConflict with a stale tombstone = %v, %v; want a skip", outcome, err)
	}
	if _, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("tombstone after the miss: %v, want it kept", err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 0 {
		t.Fatalf("conflicts on B = %v, %v; want none", open, err)
	}
	src.tombstone = ts
	if outcome, err := engine.recordTombstoneMoveConflict(ctx, client, target, src); outcome != tombstoneDeleted || err != nil {
		t.Fatalf("recordTombstoneMoveConflict with a fresh guard = %v, %v; want the move finished", outcome, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of A = %v, %v; want none", ts, err)
	}
}

// TestSettleWaitsForASourceHeldByAThirdCalendar moves an event from A to B
// on iCloud, so the create in B is refused and the push settles the source
// itself. After the refused create, an open move conflict on a third
// calendar C names A as its source. The settle must not send the DELETE of
// A. B stays dirty with no conflict, and a push failure flagged as waiting
// names C, so the sync doctor lists the row.
func TestSettleWaitsForASourceHeldByAThirdCalendar(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	putsBefore, deletesBefore := srv.counts()
	engine.testHooks = &engineTestHooks{beforeSettleSource: func() {
		if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
			CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
			LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
		}); err != nil {
			t.Errorf("CreateSyncConflict: %v", err)
		}
	}}
	res, err := engine.PushLocalEdits(ctx, calB)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Pushed != 0 || len(res.Warnings) != 1 {
		t.Fatalf("push of B = %+v, %v; want one wait warning", res, err)
	}
	if puts, deletes := srv.counts(); puts != putsBefore+1 || deletes != deletesBefore {
		t.Fatalf("PUTs, DELETEs = %d, %d; want %d, %d: no DELETE of a held source", puts, deletes, putsBefore+1, deletesBefore)
	}
	if !srv.has("/cal/a/" + evt.UID + ".ics") {
		t.Fatal("source object of A is gone, want it kept")
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 || sr.PushFailCount != 0 || sr.WaitKind != waitKindHeld || sr.WaitHolderCalendarID != calC {
		t.Fatalf("sync resource B = %+v, %v; want the wait on c with no push failure", sr, err)
	}
	if open, err := q.ListSyncConflictsByCalendar(ctx, calB); err != nil || len(open) != 0 {
		t.Fatalf("conflicts on B = %v, %v; want none", open, err)
	}
	// The doctor push gives the same wait and names C.
	if _, err := engine.DoctorPush(ctx, calB, evt.UID, nil); err == nil || !strings.Contains(err.Error(), `waits for the move conflict on calendar "c"; resolve it first`) {
		t.Fatalf("DoctorPush = %v, want the wait for the conflict on c", err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs after the doctor = %d, want %d", deletes, deletesBefore)
	}
	// More waits do not grow the failure count.
	if _, err := engine.PushLocalEdits(ctx, calB); err != nil {
		t.Fatalf("second push of B: %v", err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || sr.PushFailCount != 0 || sr.WaitKind != waitKindHeld {
		t.Fatalf("sync resource B after more waits = %+v, %v; want the wait with no push failure", sr, err)
	}
	svc := newICloudPairService(engine, q)
	waits, err := svc.WaitingResources(ctx)
	if err != nil || len(waits) != 1 || waits[0].CalendarID != calB || waits[0].Reason != `the move conflict on calendar "c"` {
		t.Fatalf("waits = %+v, %v; want the wait of B on c", waits, err)
	}
	// The moot step of a pass of C closes the holder: C holds no copy. No
	// path clears the wait columns, but the list derives the wait from its
	// cause, so the wait goes.
	if _, err := engine.SyncCalendar(ctx, calC, ConflictPrompt); err != nil {
		t.Fatalf("pass of C: %v", err)
	}
	if row := wantMoveConflictAny(t, q, calC, evt.UID); row.MoveState != MoveStateAbandoned {
		t.Fatalf("state of the conflict on C = %q, want abandoned", row.MoveState)
	}
	if waits, err := svc.WaitingResources(ctx); err != nil || len(waits) != 0 {
		t.Fatalf("waits after the moot step = %+v, %v; want none", waits, err)
	}
}

// TestTombstoneMissWithNoLiveRowsKeepsTheRowClean deletes a pushed event,
// and a write changes its tombstone while the server handles the DELETE.
// The clear misses, and the UID has no live rows. The sync row loses its
// href, and the clear must not mark it dirty: the row has nothing to push.
func TestTombstoneMissWithNoLiveRowsKeepsTheRowClean(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := q.ClearSyncResourceDirtyFlag(ctx, storage.ClearSyncResourceDirtyFlagParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("ClearSyncResourceDirtyFlag: %v", err)
	}
	engine.testHooks = &engineTestHooks{afterTombstoneDelete: func() {
		if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET version = version + 1 WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
			t.Errorf("bump the tombstone: %v", err)
		}
	}}
	res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want no error", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.RemoteUrl != "" || sr.Etag != "" || sr.Dirty != 0 {
		t.Fatalf("sync resource = %+v, %v; want a clean row with no href", sr, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 1 {
		t.Fatalf("tombstones of A = %v, %v; want the tombstone kept", ts, err)
	}
}

// TestThirdCalendarConflictDuringTheSettleDeleteRecordsTheWait moves an
// event from A to B on iCloud, so the push of B settles the source itself.
// An open move conflict on a third calendar C names A while the server
// handles the source DELETE. The clear reads the hold in its transaction,
// so the tombstone of A stays and is marked source_gone. The sync row of A
// keeps its href. The push records the wait on B and names C.
func TestThirdCalendarConflictDuringTheSettleDeleteRecordsTheWait(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	putsBefore, _ := srv.counts()
	engine.testHooks = &engineTestHooks{afterTombstoneDelete: func() {
		if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
			CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
			LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
		}); err != nil {
			t.Errorf("CreateSyncConflict: %v", err)
		}
	}}
	res, err := engine.PushLocalEdits(ctx, calB)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Pushed != 0 || len(res.Warnings) != 1 {
		t.Fatalf("push of B = %+v, %v; want one wait warning", res, err)
	}
	if puts, _ := srv.counts(); puts != putsBefore+1 {
		t.Fatalf("puts = %d, want %d: no retry of the PUT", puts, putsBefore+1)
	}
	wantSourceGoneTombstone(t, q, calA, evt.UID)
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 || sr.PushFailCount != 0 || sr.WaitKind != waitKindHeld || sr.WaitHolderCalendarID != calC {
		t.Fatalf("sync resource B = %+v, %v; want the wait on c", sr, err)
	}
}

// wantSourceGoneTombstone checks that the tombstone of uid in calID stays
// with the source_gone mark, and that the sync row there keeps its href.
func wantSourceGoneTombstone(t *testing.T, q *storage.Queries, calID int64, uid string) {
	t.Helper()
	ctx := context.Background()
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calID, Uid: uid})
	if err != nil || ts.SourceGone != 1 {
		t.Fatalf("tombstone = %+v, %v; want it kept with source_gone", ts, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calID, Uid: uid})
	if err != nil || sr.RemoteUrl == "" || sr.Etag == "" {
		t.Fatalf("sync resource = %+v, %v; want the href and the etag kept", sr, err)
	}
}

// TestPullOfAServerDeleteUnderAThirdCalendarHoldFinishesOnThePick moves an
// event from A to B. C holds a live copy of the UID, with an open move
// conflict that names A. The server deletes the source object, and the
// pull of A reports it. The clear must respect the hold. The tombstone of A
// stays with the source_gone mark, the row of A keeps its href, and the
// pull warns with the name of C. The local pick on C then finishes the
// move without a source fetch. The next passes create the copy in B and
// clear the tombstone of A.
func TestPullOfAServerDeleteUnderAThirdCalendarHoldFinishesOnThePick(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	copyC, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: evt.UID, CalendarID: calC, Title: "Copy in C", StartTime: evt.StartTime, EndTime: evt.EndTime,
	})
	if err != nil {
		t.Fatalf("UpsertByUID in C: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calC, Uid: evt.UID, OwnerType: "event", RemoteUrl: "", Etag: "", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource C: %v", err)
	}
	cid, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: copyC.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "", MoveSourceCalendarID: &calA,
	})
	if err != nil || cid == 0 {
		t.Fatalf("CreateSyncConflict = %d, %v", cid, err)
	}
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource A: %v", err)
	}
	deletions := newPendingDeletions(engine.logger)
	deletions.markExplicit(srA)
	_, failed, warns := deletions.apply(ctx, engine, calA, nil)
	if failed != 0 || len(warns) != 1 || !strings.Contains(warns[0].Message, `calendar "c"`) {
		t.Fatalf("apply = %d failed, warnings %+v; want one warning that names c", failed, warns)
	}
	wantSourceGoneTombstone(t, q, calA, evt.UID)
	waits, err := svc.WaitingResources(ctx)
	if err != nil || len(waits) != 1 || waits[0].CalendarID != calA || waits[0].Reason != `the move conflict on calendar "c"` {
		t.Fatalf("waiting resources of A = %+v, %v; want one wait on c", waits, err)
	}
	statuses, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, st := range statuses {
		if st.CalendarID == calA && len(st.Waiting) != 1 {
			t.Fatalf("status of A = %+v, want one wait", st)
		}
	}

	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calC)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("conflicts on C = %v, %v; want one", conflicts, err)
	}
	srv.mu.Lock()
	getsBefore := srv.gets
	srv.mu.Unlock()
	if _, err := svc.ResolveConflict(ctx, conflicts[0].ID, "local"); err != nil {
		t.Fatalf("local pick on C = %v, want a silent finish of the move", err)
	}
	srv.mu.Lock()
	gets := srv.gets
	srv.mu.Unlock()
	if gets != getsBefore {
		t.Fatalf("GETs during the pick = %d, want %d: the mark makes the fetch needless", gets, getsBefore)
	}
	row := wantMoveConflictAny(t, q, calC, evt.UID)
	if row.MoveState != MoveStateFinished {
		t.Fatalf("state of the conflict on C = %q, want finished", row.MoveState)
	}
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy created", res, err)
	}
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || len(res.Errors) != 0 {
		t.Fatalf("pass of A = %+v, %v; want no error", res, err)
	}
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of A = %v, %v; want none", ts, err)
	}
	if !srv.has("/cal/b/" + evt.UID + ".ics") {
		t.Fatal("the copy in B is missing on the server")
	}
}

// TestSettleChecksEverySourceBeforeTheFirstDelete moves an event from A to
// B on iCloud while D holds a tombstone of the UID from before the upgrade.
// An open move conflict on a third calendar C names D. The settle must check
// both sources before the first DELETE. So it sends no DELETE at all, and
// the push records the wait on C.
func TestSettleChecksEverySourceBeforeTheFirstDelete(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calD := linkThirdCalendar(t, engine, q, calA, "d")
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	pathD := "/cal/d/" + evt.UID + ".ics"
	srv.setRemoteBody(pathD, srv.body("/cal/a/"+evt.UID+".ics"))
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calD, Uid: evt.UID, OwnerType: "event", RemoteUrl: pathD, Etag: strings.Trim(srv.etag(pathD), `"`), Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource D: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calD, Uid: evt.UID, RemoteUrl: pathD}); err != nil {
		t.Fatalf("CreateTombstone D: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET legacy_move = 1 WHERE calendar_id = ?`, calD); err != nil {
		t.Fatalf("mark the legacy tombstone: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	// The conflict comes after the move: a move refuses an item with an
	// open conflict. A is the first source and D the second, so only a
	// check of every source before the first DELETE keeps A.
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calD,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	_, deletesBefore := srv.counts()
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Pushed != 0 || len(res.Warnings) != 1 {
		t.Fatalf("push of B = %+v, %v; want one wait warning", res, err)
	}
	if _, deletes := srv.counts(); deletes != deletesBefore {
		t.Fatalf("DELETEs = %d, want %d: no source goes before every check", deletes, deletesBefore)
	}
	if !srv.has("/cal/a/"+evt.UID+".ics") || !srv.has(pathD) {
		t.Fatalf("server objects: a=%v d=%v; want both kept", srv.has("/cal/a/"+evt.UID+".ics"), srv.has(pathD))
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.WaitKind != waitKindHeld || sr.WaitHolderCalendarID != calC {
		t.Fatalf("sync resource B = %+v, %v; want the wait on c", sr, err)
	}
}

// TestClearRefusesAZeroRule calls the clear without a wait check. The zero
// value of tombstoneClear picks no predicate, so the clear must fail and
// keep the tombstone.
func TestClearRefusesAZeroRule(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetTombstone: %v", err)
	}
	if _, err := engine.clearDeletedTombstone(ctx, calA, ts, tombstoneClear{}); err == nil {
		t.Fatal("clear with a zero rule = nil, want an error")
	}
	if _, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("tombstone after the refused clear: %v, want it kept", err)
	}
}

// TestSettleIgnoresAConflictOnAnUnlinkedThirdCalendar moves an event from A
// to B on iCloud. An open move conflict on a third calendar C names A, and
// an account delete then leaves C unlinked. That conflict no longer holds
// the source, so the push of B settles A and creates the copy.
func TestSettleIgnoresAConflictOnAnUnlinkedThirdCalendar(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET account_id = NULL WHERE id = ?`, calC); err != nil {
		t.Fatalf("unlink C: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || len(res.Warnings) != 0 || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy created", res, err)
	}
	if srv.has("/cal/a/"+evt.UID+".ics") || !srv.has("/cal/b/"+evt.UID+".ics") {
		t.Fatalf("server objects: a=%v b=%v; want one object in B", srv.has("/cal/a/"+evt.UID+".ics"), srv.has("/cal/b/"+evt.UID+".ics"))
	}
}

// TestRestoreOfAGoneSourceCreatesTheItemAgain deletes a pushed event in A.
// An open move conflict on a third calendar C names A, and the server
// deletes the object. The pull marks the tombstone source_gone and keeps
// the href. A restore then clears the tombstone. It must drop the dead href
// first, so the next push creates the item with no If-Match.
func TestRestoreOfAGoneSourceCreatesTheItemAgain(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.Delete(ctx, evt.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	path := "/cal/a/" + evt.UID + ".ics"
	srv.mu.Lock()
	delete(srv.objects, path)
	srv.mu.Unlock()
	srA, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource A: %v", err)
	}
	deletions := newPendingDeletions(engine.logger)
	deletions.markExplicit(srA)
	if _, failed, _ := deletions.apply(ctx, engine, calA, nil); failed != 0 {
		t.Fatalf("apply failed = %d, want 0", failed)
	}
	wantSourceGoneTombstone(t, q, calA, evt.UID)
	svc := newICloudPairService(engine, q)
	if waits, err := svc.WaitingResources(ctx); err != nil || len(waits) != 1 || waits[0].CalendarID != calA {
		t.Fatalf("waits before the restore = %+v, %v; want the wait of A", waits, err)
	}
	if err := engine.events.RestoreByID(ctx, evt.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	// The restore clears the tombstone, so the cause of the wait ends.
	if waits, err := svc.WaitingResources(ctx); err != nil || len(waits) != 0 {
		t.Fatalf("waits after the restore = %+v, %v; want none", waits, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.RemoteUrl != "" || sr.Etag != "" || sr.Dirty != 1 {
		t.Fatalf("sync resource A after the restore = %+v, %v; want a dirty row with no href", sr, err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of A = %+v, %v; want the item created", res, err)
	}
	if !srv.has(path) {
		t.Fatal("the restored item is missing on the server")
	}
}

// TestTombstonePassRecordsTheWaitOfAHeldSource records the move conflict of
// a refused source DELETE in the tombstone pass. The source object is gone
// at the GET, and an open move conflict on a third calendar C holds the
// tombstone. The record must not skip in silence: the row of A stores the
// wait on C, and the tombstone keeps the source_gone mark.
func TestTombstonePassRecordsTheWaitOfAHeldSource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createPushedEvent(t, engine, calA)
	srv.setGeneric(true)
	moveEvent(t, engine, evt, calB)
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
		t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
	}
	srv.setGetStatus("/cal/a/", http.StatusNotFound)
	_, _, client, remoteURL, err := engine.loadCalendarClient(ctx, calA)
	if err != nil {
		t.Fatalf("loadCalendarClient: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetTombstone: %v", err)
	}
	target, found, err := engine.moveTarget(ctx, ts)
	if err != nil || !found {
		t.Fatalf("moveTarget = %v, %v; want the copy in B", found, err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	src := movedSource{calendarID: calA, remoteURL: remoteURL, tombstone: ts, clear: clearAfterOwnDelete()}
	if outcome, err := engine.recordTombstoneMoveConflict(ctx, client, target, src); outcome != tombstoneSkipped || err != nil {
		t.Fatalf("recordTombstoneMoveConflict = %v, %v; want a skip", outcome, err)
	}
	wantSourceGoneTombstone(t, q, calA, evt.UID)
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.WaitKind != waitKindHeld || sr.WaitHolderCalendarID != calC || sr.PushFailCount != 0 {
		t.Fatalf("sync resource A = %+v, %v; want the wait on c", sr, err)
	}
}

// goneSourceMoveConflict records a move conflict from A to B. It then marks
// the source of A as gone, as a pull of a server delete under a hold does.
func goneSourceMoveConflict(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA, calB int64) (event.Event, storage.SyncConflict) {
	t.Helper()
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET source_gone = 1 WHERE calendar_id = ?`, calA); err != nil {
		t.Fatalf("mark the source gone: %v", err)
	}
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()
	return evt, c
}

// TestGoneSourcePicksFinishForAWritableTarget runs both picks on a move
// whose source is marked gone, and the target can create the copy. Both
// picks finish the move with no request. The local pick is silent. The
// server pick says that the cancel cannot run. The next push creates the
// copy in B.
func TestGoneSourcePicksFinishForAWritableTarget(t *testing.T) {
	t.Parallel()
	for _, pick := range []string{"local", "server"} {
		t.Run(pick, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)

			srv.mu.Lock()
			getsBefore := srv.gets
			srv.mu.Unlock()
			putsBefore, deletesBefore := srv.counts()
			out, err := svc.ResolveConflictOutcome(ctx, c.ID, pick)
			if err != nil || !out.MoveFinished || out.Note != "the source is gone, so the move finished; the next sync creates the moved copy" || len(out.Warnings) != 0 {
				t.Fatalf("%s pick = %+v, %v; want the finished outcome", pick, out, err)
			}
			srv.mu.Lock()
			gets := srv.gets
			srv.mu.Unlock()
			if puts, deletes := srv.counts(); gets != getsBefore || puts != putsBefore || deletes != deletesBefore {
				t.Fatalf("requests during the pick: GET %d, PUT %d, DELETE %d; want none", gets-getsBefore, puts-putsBefore, deletes-deletesBefore)
			}
			if row := wantMoveConflictAny(t, q, calB, evt.UID); row.MoveState != MoveStateFinished {
				t.Fatalf("state = %q, want finished", row.MoveState)
			}
			if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calB {
				t.Fatalf("event after the pick = %+v, %v; want it in B", got, err)
			}
			if res, err := engine.PushLocalEdits(ctx, calB); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
				t.Fatalf("push of B = %+v, %v; want the copy created", res, err)
			}
		})
	}
}

// TestGoneSourcePicksRefuseATargetThatCannotCreate runs both picks on a move
// whose source is marked gone, while the target cannot create the copy. B
// is read-only in one case, and the organizer gate holds the copy in the
// other. Both picks refuse with the gone-source advice, and the conflict
// stays open.
func TestGoneSourcePicksRefuseATargetThatCannotCreate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, pick string }{
		{"read-only", "local"}, {"read-only", "server"}, {"gated", "local"}, {"gated", "server"},
	} {
		t.Run(tc.name+"/"+tc.pick, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
			switch tc.name {
			case "read-only":
				if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
					t.Fatalf("make B read-only: %v", err)
				}
			case "gated":
				if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
					{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
				}); err != nil {
					t.Fatalf("ReplaceAttendeesForSync: %v", err)
				}
			}
			if _, err := svc.ResolveConflict(ctx, c.ID, tc.pick); err == nil || !strings.Contains(err.Error(), `is gone, and calendar "b" cannot create the item; make it writable, or move the item to another calendar`) {
				t.Fatalf("%s pick = %v, want the gone-source refusal", tc.pick, err)
			}
			if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateOpen || row.ResolvedAt != nil {
				t.Fatalf("conflict = %+v, %v; want it open", row, err)
			}
			if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calB {
				t.Fatalf("event after the refusal = %+v, %v; want it in B", got, err)
			}
		})
	}
}

// TestCancelOfAGoneSourceRefusesAReadOnlyTarget runs the server pick without
// the source_gone mark. The fetch of the source object gets 404, and B is
// read-only. The cancel follows the same rule as the mark: it refuses, and
// the conflict stays open.
func TestCancelOfAGoneSourceRefusesAReadOnlyTarget(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}
	if _, err := svc.ResolveConflict(ctx, c.ID, "server"); err == nil || !strings.Contains(err.Error(), "cannot create the item") {
		t.Fatalf("server pick = %v, want the gone-source refusal", err)
	}
	if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict = %+v, %v; want it open", row, err)
	}
}

// TestGatedTargetTriesEverySourceOfAHeldMove moves a gated event from A to
// B while D holds a second source tombstone that names B. The source object
// of A is gone, and an open move conflict on C holds A. The gated record
// must not stop at A: D gets the record, and the push reports no wait.
func TestGatedTargetTriesEverySourceOfAHeldMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calD := linkThirdCalendar(t, engine, q, calA, "d")
	calC := linkThirdCalendar(t, engine, q, calA, "e")

	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	pathD := "/cal/d/" + evt.UID + ".ics"
	srv.setRemoteBody(pathD, srv.body("/cal/a/"+evt.UID+".ics"))
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calD, Uid: evt.UID, OwnerType: "event", RemoteUrl: pathD, Etag: strings.Trim(srv.etag(pathD), `"`), Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource D: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calD, Uid: evt.UID, RemoteUrl: pathD, MovedToCalendarID: &calB}); err != nil {
		t.Fatalf("CreateTombstone D: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	srv.setGetStatus("/cal/a/", http.StatusNotFound)
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 1 || len(res.Warnings) != 0 {
		t.Fatalf("push of the gated target = %+v, %v; want the record for D and no wait", res, err)
	}
	row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || row.MoveSourceCalendarID == nil || *row.MoveSourceCalendarID != calD {
		t.Fatalf("move conflict = %+v, %v; want D as the source", row, err)
	}
	// The hold of A is read before the loop, so A gets no record and keeps
	// its move target.
	tsA, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || tsA.MovedToCalendarID == nil || *tsA.MovedToCalendarID != calB || tsA.SourceGone != 0 {
		t.Fatalf("tombstone of A = %+v, %v; want its move target kept", tsA, err)
	}
}

// gatedTwoSourceMove moves a gated event from A to B while D holds a second
// source tombstone that names B. An open move conflict on a third calendar
// holds D. It returns the event and the IDs of D and the third calendar.
func gatedTwoSourceMove(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA, calB int64) (event.Event, int64, int64) {
	t.Helper()
	ctx := context.Background()
	calD := linkThirdCalendar(t, engine, q, calA, "d")
	calC := linkThirdCalendar(t, engine, q, calA, "e")
	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	pathD := "/cal/d/" + evt.UID + ".ics"
	srv.setRemoteBody(pathD, srv.body("/cal/a/"+evt.UID+".ics"))
	moveEvent(t, engine, evt, calB)
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calD, Uid: evt.UID, RemoteUrl: pathD, MovedToCalendarID: &calB}); err != nil {
		t.Fatalf("CreateTombstone D: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calD,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	return evt, calD, calC
}

// wantGatedMoveEnded checks the state after a gated push whose copy went.
// The tombstone of A is a plain delete. D keeps its move target, because a
// conflict holds it. The row of B gets no wait: it has nothing to create.
func wantGatedMoveEnded(t *testing.T, q *storage.Queries, res *SyncResult, err error, evt event.Event, calA, calB, calD int64) {
	t.Helper()
	ctx := context.Background()
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || len(res.Warnings) != 0 {
		t.Fatalf("push of the gated target = %+v, %v; want a plain gated row and no wait", res, err)
	}
	tsA, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || tsA.MovedToCalendarID != nil {
		t.Fatalf("tombstone of A = %+v, %v; want a plain delete", tsA, err)
	}
	tsD, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calD, Uid: evt.UID})
	if err != nil || tsD.MovedToCalendarID == nil || *tsD.MovedToCalendarID != calB {
		t.Fatalf("tombstone of D = %+v, %v; want its move target kept", tsD, err)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err == nil && sr.WaitKind != waitKindNone {
		t.Fatalf("sync resource B = %+v; want no wait", sr)
	}
}

// TestGatedTargetEndsOnlyTheUnheldMovesWhenTheCopyGoes runs the gated path
// with the sources A and D, where a conflict holds D. The copy goes at two
// points: before the check of the copy, and before the record of A. The
// holds are read before both, so the end of the move skips D. The row of B
// is a plain gated row with no wait: it has nothing to create.
func TestGatedTargetEndsOnlyTheUnheldMovesWhenTheCopyGoes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		at   int
	}{{"before the copy check", 1}, {"before the record of A", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			evt, calD, _ := gatedTwoSourceMove(t, engine, q, srv, calA, calB)
			calls := 0
			engine.testHooks = &engineTestHooks{beforeGatedSource: func() {
				calls++
				if calls != tc.at {
					return
				}
				if _, err := engine.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-04-01T00:00:00Z' WHERE uid = ? AND calendar_id = ?`, evt.UID, calB); err != nil {
					t.Errorf("remove the copy: %v", err)
				}
			}}
			res, err := engine.PushLocalEdits(ctx, calB)
			engine.testHooks = nil
			wantGatedMoveEnded(t, q, res, err, evt, calA, calB, calD)
		})
	}
}

// TestGoneSourcePicksFinishForALandedGatedCopy marks the source of a move as
// gone after the copy landed in B, and the organizer gate holds the copy.
// The copy needs no create, so both picks finish the move.
func TestGoneSourcePicksFinishForALandedGatedCopy(t *testing.T) {
	t.Parallel()
	for _, pick := range []string{"local", "server"} {
		t.Run(pick, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
			path := "/cal/b/" + evt.UID + ".ics"
			srv.setRemoteBody(path, "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
			if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = ?, etag = 'landed' WHERE calendar_id = ? AND uid = ?`, path, calB, evt.UID); err != nil {
				t.Fatalf("land the copy: %v", err)
			}
			if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
				{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
			}); err != nil {
				t.Fatalf("ReplaceAttendeesForSync: %v", err)
			}
			if _, err := svc.ResolveConflict(ctx, c.ID, pick); err != nil {
				t.Fatalf("%s pick = %v, want the finish", pick, err)
			}
			if row := wantMoveConflictAny(t, q, calB, evt.UID); row.MoveState != MoveStateFinished {
				t.Fatalf("state = %q, want finished", row.MoveState)
			}
		})
	}
}

// TestGoneSourcePicksFinishForALocalTarget moves an event into a local
// calendar and marks its source as gone. The item lives in the local
// calendar, and no server needs a create, so both picks finish the move.
func TestGoneSourcePicksFinishForALocalTarget(t *testing.T) {
	t.Parallel()
	for _, pick := range []string{"local", "server"} {
		t.Run(pick, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, _ := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			local, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
			if err != nil {
				t.Fatalf("CreateCalendar local: %v", err)
			}
			evt := createPushedEvent(t, engine, calA)
			moveEvent(t, engine, evt, local.ID)
			srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
			if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
				t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
			}
			c := wantMoveConflict(t, q, calA, local.ID, evt.UID)
			if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET source_gone = 1 WHERE calendar_id = ?`, calA); err != nil {
				t.Fatalf("mark the source gone: %v", err)
			}
			if _, err := svc.ResolveConflict(ctx, c.ID, pick); err != nil {
				t.Fatalf("%s pick = %v, want the finish", pick, err)
			}
			if row := wantMoveConflictAny(t, q, local.ID, evt.UID); row.MoveState != MoveStateFinished {
				t.Fatalf("state = %q, want finished", row.MoveState)
			}
			if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != local.ID {
				t.Fatalf("event = %+v, %v; want it in the local calendar", got, err)
			}
		})
	}
}

// TestLocalPickRechecksTheTargetInTheSettle runs the local pick with no
// recorded etag, so the pick fetches the source object and gets 404. B
// becomes read-only between the check of the target and the fetch. The
// settle reads the target again in its transaction, so the pick refuses,
// and the conflict stays open.
func TestLocalPickRechecksTheTargetInTheSettle(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET server_etag = '' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("blank the etag: %v", err)
	}
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()
	engine.testHooks = &engineTestHooks{beforeSourceFetch: func() {
		if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
			t.Errorf("make B read-only: %v", err)
		}
	}}
	_, err := svc.ResolveConflict(ctx, c.ID, "local")
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), "cannot create the item") {
		t.Fatalf("local pick = %v, want the refusal from the second read", err)
	}
	if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict = %+v, %v; want it open", row, err)
	}
}

// TestGatedWaitNamesThePlannedHref holds the only source of a gated move
// with a conflict of a third calendar. The push records the wait, and its
// warning names the same planned href as plannedHref.
func TestGatedWaitNamesThePlannedHref(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")
	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calC, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Warnings) != 1 {
		t.Fatalf("push of B = %+v, %v; want one wait", res, err)
	}
	want, err := engine.plannedHref(ctx, calB, evt.UID)
	if err != nil || res.Warnings[0].Path != want {
		t.Fatalf("warning path = %q, want the planned href %q (%v)", res.Warnings[0].Path, want, err)
	}
}

// TestGoneSourceFinishMarksACleanUnpushedTargetDirty finishes a move whose
// source is gone while the row of the copy in B is clean and unpushed. The
// finish must mark the row dirty in the same transaction, so the next push
// creates the copy.
func TestGoneSourceFinishMarksACleanUnpushedTargetDirty(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = ?`, calB, evt.UID); err != nil {
		t.Fatalf("clean the row of B: %v", err)
	}
	out, err := svc.ResolveConflictOutcome(ctx, c.ID, "local")
	if err != nil || !out.MoveFinished {
		t.Fatalf("local pick = %+v, %v; want the finish", out, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 || sr.RemoteUrl != "" {
		t.Fatalf("sync resource B = %+v, %v; want a dirty row with no href", sr, err)
	}
	if row := wantMoveConflictAny(t, q, calB, evt.UID); row.MoveState != MoveStateFinished {
		t.Fatalf("state = %q, want finished", row.MoveState)
	}
}

// TestGoneSourceNotesNameWhatRemains checks the note of a finished move.
// It names the pending create only when the copy has not landed. A landed
// copy and a local target need no create.
func TestGoneSourceNotesNameWhatRemains(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
	path := "/cal/b/" + evt.UID + ".ics"
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = ?, etag = 'landed', dirty = 0 WHERE calendar_id = ? AND uid = ?`, path, calB, evt.UID); err != nil {
		t.Fatalf("land the copy: %v", err)
	}
	out, err := svc.ResolveConflictOutcome(ctx, c.ID, "server")
	if err != nil || !out.MoveFinished || out.Note != "the source is gone, so the move finished; the moved copy is on the server already" {
		t.Fatalf("pick on a landed copy = %+v, %v; want the landed note", out, err)
	}
}

// TestGoneSourceRefusesAReadOnlyLandedCopyWithAnEdit lands the copy of a
// move in B, makes B read-only, and leaves an unsent edit on the row. No
// push of B sends that edit, so both picks refuse. A clean landed copy in a
// read-only B can stay, so both picks finish.
func TestGoneSourceRefusesAReadOnlyLandedCopyWithAnEdit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pick  string
		dirty int
	}{{"local", 1}, {"server", 1}, {"local", 0}, {"server", 0}} {
		t.Run(fmt.Sprintf("%s/dirty=%d", tc.pick, tc.dirty), func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
			path := "/cal/b/" + evt.UID + ".ics"
			if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = ?, etag = 'landed', dirty = ? WHERE calendar_id = ? AND uid = ?`, path, tc.dirty, calB, evt.UID); err != nil {
				t.Fatalf("land the copy: %v", err)
			}
			if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
				t.Fatalf("make B read-only: %v", err)
			}
			out, err := svc.ResolveConflictOutcome(ctx, c.ID, tc.pick)
			if tc.dirty == 1 {
				if err == nil || !strings.Contains(err.Error(), `calendar "b" is read-only and holds an unsent edit`) {
					t.Fatalf("pick = %+v, %v; want the unsent-edit refusal", out, err)
				}
				if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateOpen {
					t.Fatalf("conflict = %+v, %v; want it open", row, err)
				}
				return
			}
			if err != nil || !out.MoveFinished {
				t.Fatalf("pick = %+v, %v; want the finish", out, err)
			}
		})
	}
}

// TestGatedEndOfAMoveSkipsALateHold runs the gated path with the sources A
// and D, where a conflict holds D. After the read of the holds, a conflict
// on a fourth calendar starts to hold A, and the copy goes. The clear of
// each move target checks the hold itself, so A and D keep their targets.
func TestGatedEndOfAMoveSkipsALateHold(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	evt, calD, _ := gatedTwoSourceMove(t, engine, q, srv, calA, calB)
	// A conflict row is unique per calendar and UID, so the late hold of A
	// lives on a fourth calendar F.
	calF := linkThirdCalendar(t, engine, q, calA, "f")
	calls := 0
	engine.testHooks = &engineTestHooks{beforeGatedSource: func() {
		calls++
		if calls != 1 {
			return
		}
		if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
			CalendarID: calF, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
			LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
		}); err != nil {
			t.Errorf("CreateSyncConflict: %v", err)
		}
		if _, err := engine.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-04-01T00:00:00Z' WHERE uid = ? AND calendar_id = ?`, evt.UID, calB); err != nil {
			t.Errorf("remove the copy: %v", err)
		}
	}}
	if _, err := engine.PushLocalEdits(ctx, calB); err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	engine.testHooks = nil
	for _, cal := range []int64{calA, calD} {
		ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: cal, Uid: evt.UID})
		if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
			t.Fatalf("tombstone of calendar %d = %+v, %v; want its move target kept", cal, ts, err)
		}
	}
}

// TestGatedEndedMoveKeepsAHeldSource runs the gated path on a copy whose
// move row on B is finished. The sources are A and D, and a conflict holds
// D. The end of the move turns A into a plain delete only. D keeps its
// move target.
func TestGatedEndedMoveKeepsAHeldSource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	evt, calD, _ := gatedTwoSourceMove(t, engine, q, srv, calA, calB)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict B: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET move_state = 'finished', resolved_at = '2026-04-01T00:00:00Z', resolution = 'local' WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("finish the move row: %v", err)
	}
	if _, err := engine.PushLocalEdits(ctx, calB); err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	tsA, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || tsA.MovedToCalendarID != nil {
		t.Fatalf("tombstone of A = %+v, %v; want a plain delete", tsA, err)
	}
	tsD, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calD, Uid: evt.UID})
	if err != nil || tsD.MovedToCalendarID == nil || *tsD.MovedToCalendarID != calB {
		t.Fatalf("tombstone of D = %+v, %v; want its move target kept", tsD, err)
	}
}

// TestGoneSourceSettleRefusesARowThatChangedBeforeTheTransaction lands the
// copy in a read-only B with a clean row, so the first check accepts it.
// A hook then leaves an unsent edit on the row before the transaction. The
// transaction reads the row again, so both picks refuse, and the conflict
// stays open.
func TestGoneSourceSettleRefusesARowThatChangedBeforeTheTransaction(t *testing.T) {
	t.Parallel()
	for _, pick := range []string{"local", "server"} {
		t.Run(pick, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
			path := "/cal/b/" + evt.UID + ".ics"
			if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = ?, etag = 'landed', dirty = 0 WHERE calendar_id = ? AND uid = ?`, path, calB, evt.UID); err != nil {
				t.Fatalf("land the copy: %v", err)
			}
			if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB); err != nil {
				t.Fatalf("make B read-only: %v", err)
			}
			engine.testHooks = &engineTestHooks{beforeGoneSourceSettle: func() {
				if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 1 WHERE calendar_id = ? AND uid = ?`, calB, evt.UID); err != nil {
					t.Errorf("dirty the row: %v", err)
				}
			}}
			_, err := svc.ResolveConflictOutcome(ctx, c.ID, pick)
			engine.testHooks = nil
			if err == nil || !strings.Contains(err.Error(), "holds an unsent edit") {
				t.Fatalf("%s pick = %v, want the refusal from the transaction", pick, err)
			}
			if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateOpen {
				t.Fatalf("conflict = %+v, %v; want it open", row, err)
			}
		})
	}
}

// TestCancelReplayOfAGoneSourceExitsThroughTheLocalPick fails the import of
// a cancel, so the conflict waits as a failed cancel with its rows in A.
// The source object then goes. The server pick refuses, names the local
// pick, and writes nothing. The local pick then imports the recorded body
// over the rows of the failed import, drops the dead href, and closes the
// conflict as cancelled. The next push of A creates the item there.
func TestCancelReplayOfAGoneSourceExitsThroughTheLocalPick(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := failCancelOf(t, engine, q, srv, calA, calB)
	path := "/cal/a/" + evt.UID + ".ics"
	srv.mu.Lock()
	delete(srv.objects, path)
	srv.mu.Unlock()
	before, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil {
		t.Fatalf("GetSyncResource A: %v", err)
	}
	if _, err := svc.ResolveConflictOutcome(ctx, c.ID, "server"); err == nil || !strings.Contains(err.Error(), "is gone, so the cancel cannot complete; the item stays as it is; pick local to keep the moved version in the source calendar") {
		t.Fatalf("replay = %v, want the refusal that names the local pick", err)
	}
	row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
		t.Fatalf("conflict = %+v, %v; want an open failed cancel", row, err)
	}
	if after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil || after.RemoteUrl != before.RemoteUrl || after.Dirty != before.Dirty || after.Rev != before.Rev {
		t.Fatalf("sync resource A after the refusal = %+v, %v; want it unchanged from %+v", after, err, before)
	}

	out, err := svc.ResolveConflictOutcome(ctx, c.ID, "local")
	if err != nil || out.MoveFinished || out.UID != evt.UID || out.Note != "the source object is gone, so the next sync creates the item in the source calendar" {
		t.Fatalf("local pick = %+v, %v; want the restore with the gone-source note", out, err)
	}
	if row := wantMoveConflictAny(t, q, calB, evt.UID); row.MoveState != MoveStateCancelled {
		t.Fatalf("state = %q, want cancelled", row.MoveState)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || sr.RemoteUrl != "" || sr.Etag != "" || sr.Dirty != 1 {
		t.Fatalf("sync resource A = %+v, %v; want a dirty row with no href", sr, err)
	}
	if got, err := engine.events.Get(ctx, evt.ID); err != nil || got.CalendarID != calA || got.Title != evt.Title {
		t.Fatalf("event = %+v, %v; want the recorded body in A", got, err)
	}
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("push of A = %+v, %v; want the item created", res, err)
	}
	if !srv.has(path) {
		t.Fatal("the item is missing in A on the server")
	}
}

// TestGatedEndedMoveClearsAReadOnlySource runs the gated path on a copy
// whose move row on B is finished. The sources are a read-only A and a
// writable D. The list of sources of a push skips A, but the end of the
// move must turn both tombstones into plain deletes.
func TestGatedEndedMoveClearsAReadOnlySource(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calD := linkThirdCalendar(t, engine, q, calA, "d")
	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	pathD := "/cal/d/" + evt.UID + ".ics"
	srv.setRemoteBody(pathD, srv.body("/cal/a/"+evt.UID+".ics"))
	moveEvent(t, engine, evt, calB)
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calD, Uid: evt.UID, RemoteUrl: pathD, MovedToCalendarID: &calB}); err != nil {
		t.Fatalf("CreateTombstone D: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calD,
	}); err != nil {
		t.Fatalf("CreateSyncConflict B: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET move_state = 'finished', resolved_at = '2026-04-01T00:00:00Z', resolution = 'local' WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("finish the move row: %v", err)
	}
	if _, err := engine.PushLocalEdits(ctx, calB); err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	for _, cal := range []int64{calA, calD} {
		ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: cal, Uid: evt.UID})
		if err != nil || ts.MovedToCalendarID != nil {
			t.Fatalf("tombstone of calendar %d = %+v, %v; want a plain delete", cal, ts, err)
		}
	}
}

// TestResolveConflictWarnsWithTheUIDOfAFinishedMove runs a pick on a move
// whose source is gone through ResolveConflict. The finished outcome comes
// back as one warning, and the warning names the UID of the item.
func TestResolveConflictWarnsWithTheUIDOfAFinishedMove(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
	warns, err := svc.ResolveConflict(ctx, c.ID, "local")
	if err != nil || len(warns) != 1 || warns[0].UID != evt.UID || !strings.Contains(warns[0].Message, "the move finished") {
		t.Fatalf("ResolveConflict = %+v, %v; want one warning with the UID", warns, err)
	}
}

// sourceHalfGoneMoveConflict records a move conflict from A to B. It then
// drops the source row and the tombstone of A, so the move has no source
// half.
func sourceHalfGoneMoveConflict(t *testing.T, engine *Engine, q *storage.Queries, srv *icloudFakeServer, calA, calB int64) (event.Event, storage.SyncConflict) {
	t.Helper()
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	srv.editRemote("/cal/a/" + evt.UID + ".ics")
	if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, calB, evt.UID)
	if err := q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("DeleteSyncResource A: %v", err)
	}
	if err := q.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID}); err != nil {
		t.Fatalf("drop the source tombstone: %v", err)
	}
	return evt, c
}

// TestMissingSourceHalfFollowsTheGoneSourceRule runs both picks on a move
// with no source half. Nothing remains to wait for, so both targets finish
// the move. A read-only B gets a note that the item stays there locally. A
// writable B with a clean, unpushed row gets a dirty row.
func TestMissingSourceHalfFollowsTheGoneSourceRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ target, pick string }{
		{"read-only", "local"}, {"read-only", "server"}, {"clean", "local"}, {"clean", "server"},
	} {
		t.Run(tc.target+"/"+tc.pick, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := sourceHalfGoneMoveConflict(t, engine, q, srv, calA, calB)
			stmt := `UPDATE calendars SET remote_access = 'read' WHERE id = ?`
			args := []any{calB}
			if tc.target == "clean" {
				stmt = `UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = ?`
				args = []any{calB, evt.UID}
			}
			if _, err := engine.db.ExecContext(ctx, stmt, args...); err != nil {
				t.Fatalf("prepare B: %v", err)
			}
			out, err := svc.ResolveConflictOutcome(ctx, c.ID, tc.pick)
			if tc.target == "read-only" {
				// Nothing remains to wait for, so the move finishes. The
				// note says where the item stays.
				if err != nil || !out.MoveFinished || out.Note != `the source is gone, so the move finished; the item stays only in calendar "b" locally until the calendar is writable` {
					t.Fatalf("pick = %+v, %v; want the finish with the local note", out, err)
				}
				if row := wantMoveConflictAny(t, q, calB, evt.UID); row.MoveState != MoveStateFinished {
					t.Fatalf("state = %q, want finished", row.MoveState)
				}
				return
			}
			if err != nil || !out.MoveFinished {
				t.Fatalf("pick = %+v, %v; want the finish", out, err)
			}
			if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err != nil || sr.Dirty != 1 {
				t.Fatalf("sync resource B = %+v, %v; want it dirty", sr, err)
			}
		})
	}
}

// TestGoneSourceSettleRefusesAGatedCopyThatLostItsHref lands the copy of a
// gated event in B, so the first check accepts it. A hook then drops the
// href of the row before the transaction. The copy then needs a create
// that the gate holds back. The transaction applies the real answer of the
// gate, so the pick refuses.
func TestGoneSourceSettleRefusesAGatedCopyThatLostItsHref(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
	path := "/cal/b/" + evt.UID + ".ics"
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = ?, etag = 'landed' WHERE calendar_id = ? AND uid = ?`, path, calB, evt.UID); err != nil {
		t.Fatalf("land the copy: %v", err)
	}
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	engine.testHooks = &engineTestHooks{beforeGoneSourceSettle: func() {
		if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET remote_url = '', etag = '' WHERE calendar_id = ? AND uid = ?`, calB, evt.UID); err != nil {
			t.Errorf("drop the href: %v", err)
		}
	}}
	_, err := svc.ResolveConflictOutcome(ctx, c.ID, "local")
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), "cannot create the item") {
		t.Fatalf("local pick = %v, want the gated refusal", err)
	}
	if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict = %+v, %v; want it open", row, err)
	}
}

// TestGoneSourceSettleAbandonsACopyWithoutASyncRow drops the sync row of
// the copy in B before the transaction of the settle. A remote copy with no
// sync row never pushes, so it is not live (live_move_copies). The move
// ends as abandoned, and the settle writes no row for it.
func TestGoneSourceSettleAbandonsACopyWithoutASyncRow(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
	engine.testHooks = &engineTestHooks{beforeGoneSourceSettle: func() {
		if _, err := engine.db.ExecContext(ctx, `DELETE FROM sync_resources WHERE calendar_id = ? AND uid = ?`, calB, evt.UID); err != nil {
			t.Errorf("drop the row: %v", err)
		}
	}}
	_, err := svc.ResolveConflictOutcome(ctx, c.ID, "local")
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), "is gone, so the move is abandoned") {
		t.Fatalf("local pick = %v, want the abandoned move", err)
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err == nil {
		t.Fatal("the settle wrote a row for the copy, want none")
	}
}

// TestGatedEndedMoveClearsAnOnlyReadOnlySource runs the gated path on a
// copy whose move row on B is finished, and its one source A is
// read-only. No writable source exists, but the tombstone of A names B,
// so the end of the move must still turn it into a plain delete.
func TestGatedEndedMoveClearsAnOnlyReadOnlySource(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calB, OwnerType: "event", OwnerID: evt.ID, Uid: evt.UID,
		LocalIcal: "l", ServerIcal: "s", ServerEtag: "e", MoveSourceCalendarID: &calA,
	}); err != nil {
		t.Fatalf("CreateSyncConflict B: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_conflicts SET move_state = 'finished', resolved_at = '2026-04-01T00:00:00Z', resolution = 'local' WHERE calendar_id = ?`, calB); err != nil {
		t.Fatalf("finish the move row: %v", err)
	}
	if _, err := engine.PushLocalEdits(ctx, calB); err != nil {
		t.Fatalf("PushLocalEdits: %v", err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID != nil {
		t.Fatalf("tombstone of A = %+v, %v; want a plain delete", ts, err)
	}
}

// TestGoneSourceSettleAbandonsACopyPurgedBeforeTheTransaction removes the
// copy in B before the transaction of the settle. The transaction checks
// the copy again, so the move ends as abandoned, and no dirty row comes
// for the gone copy.
func TestGoneSourceSettleAbandonsACopyPurgedBeforeTheTransaction(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET dirty = 0 WHERE calendar_id = ? AND uid = ?`, calB, evt.UID); err != nil {
		t.Fatalf("clean the row of B: %v", err)
	}
	engine.testHooks = &engineTestHooks{beforeGoneSourceSettle: func() {
		if _, err := engine.db.ExecContext(ctx, `DELETE FROM events WHERE uid = ? AND calendar_id = ?`, evt.UID, calB); err != nil {
			t.Errorf("purge the copy: %v", err)
		}
	}}
	_, err := svc.ResolveConflictOutcome(ctx, c.ID, "local")
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), "is gone, so the move is abandoned") {
		t.Fatalf("local pick = %v, want the abandoned move", err)
	}
	if row := wantMoveConflictAny(t, q, calB, evt.UID); row.MoveState != MoveStateAbandoned {
		t.Fatalf("state = %q, want abandoned", row.MoveState)
	}
	if sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID}); err == nil && sr.Dirty != 0 {
		t.Fatalf("sync resource B = %+v; want no dirty mark for the gone copy", sr)
	}
}

// TestGoneSourceSettleReadsTheGateInTheTransaction leaves the copy in B
// with no href, so it waits for its create. Before the transaction, the
// event gets an organizer other than the user. The transaction reads the
// gate itself, so the pick refuses: the gate holds the create.
func TestGoneSourceSettleReadsTheGateInTheTransaction(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := goneSourceMoveConflict(t, engine, q, srv, calA, calB)
	engine.testHooks = &engineTestHooks{beforeGoneSourceSettle: func() {
		if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
			{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
		}); err != nil {
			t.Errorf("ReplaceAttendeesForSync: %v", err)
		}
	}}
	_, err := svc.ResolveConflictOutcome(ctx, c.ID, "local")
	engine.testHooks = nil
	if err == nil || !strings.Contains(err.Error(), "cannot create the item") {
		t.Fatalf("local pick = %v, want the gated refusal from the transaction", err)
	}
	if row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID}); err != nil || row.MoveState != MoveStateOpen {
		t.Fatalf("conflict = %+v, %v; want it open", row, err)
	}
}

// TestGatedPathSkipsTheRecordWithoutARecordableSource moves a gated event
// from A to B, and A is read-only. No source can get a record, and the copy
// is live. The push skips the record and its warning, and A keeps its move
// target.
func TestGatedPathSkipsTheRecordWithoutARecordableSource(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	var logs bytes.Buffer
	engine.logger = slog.New(slog.NewTextHandler(&logs, nil))
	evt := createPushedEvent(t, engine, calA)
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	moveEvent(t, engine, evt, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calA); err != nil {
		t.Fatalf("make A read-only: %v", err)
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 {
		t.Fatalf("push of B = %+v, %v; want no record", res, err)
	}
	if strings.Contains(logs.String(), "record the move conflict") {
		t.Fatalf("logs = %q, want no record warning", logs.String())
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calB {
		t.Fatalf("tombstone of A = %+v, %v; want its move target kept", ts, err)
	}
}

// TestLocalPickOfAFailedCancelRefusesBeforeTheProbe fails the import of a
// cancel and then edits the source row, so its rev changes. The local pick
// must refuse for the changed rev before it probes the source object.
func TestLocalPickOfAFailedCancelRefusesBeforeTheProbe(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	evt, c := failCancelOf(t, engine, q, srv, calA, calB)
	if _, err := engine.db.ExecContext(ctx, `UPDATE sync_resources SET rev = rev + 1, dirty = 1 WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
		t.Fatalf("edit the source row: %v", err)
	}
	srv.mu.Lock()
	getsBefore := srv.gets
	srv.mu.Unlock()
	if _, err := svc.ResolveConflictOutcome(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), "changed after the cancel") {
		t.Fatalf("local pick = %v, want the changed-rev refusal", err)
	}
	srv.mu.Lock()
	gets := srv.gets
	srv.mu.Unlock()
	if gets != getsBefore {
		t.Fatalf("GETs = %d, want none: the refusal runs before the probe", gets-getsBefore)
	}
}

// failingCredStore fails every read of a credential, as a locked keyring
// does.
type failingCredStore struct{ mockCredStore }

func (*failingCredStore) Get(int64, string) (auth.Credential, error) {
	return auth.Credential{}, errors.New("keyring locked")
}

// TestLocalPickOfAFailedCancelNamesTheProbeFailure fails the import of a
// cancel, so the conflict waits as a failed cancel. The local pick then
// probes the source object, and the probe fails in four ways. The refusal
// names the class of the failure and keeps its cause. The pick writes
// nothing, and the conflict stays a failed cancel.
func TestLocalPickOfAFailedCancelNamesTheProbeFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(engine *Engine, srv *icloudFakeServer)
		want string
	}{
		{"transport", func(_ *Engine, srv *icloudFakeServer) {
			srv.mu.Lock()
			srv.dropGets = true
			srv.mu.Unlock()
		}, `the server of calendar "a" cannot be reached; try again when it can`},
		{"401", func(_ *Engine, srv *icloudFakeServer) { srv.setGetStatus("/cal/a/", http.StatusUnauthorized) },
			`the credentials of calendar "a" do not work; fix them (chroncal account credentials or chroncal account reauth) and try again`},
		{"credential load", func(engine *Engine, _ *icloudFakeServer) { engine.credStore = &failingCredStore{} },
			`the credentials of calendar "a" do not work`},
		{"500", func(_ *Engine, srv *icloudFakeServer) { srv.setGetStatus("/cal/a/", http.StatusInternalServerError) },
			`the server of calendar "a" answered 500; try again later`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt, c := failCancelOf(t, engine, q, srv, calA, calB)
			before, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
			if err != nil {
				t.Fatalf("GetSyncResource A: %v", err)
			}
			tc.set(engine, srv)
			if _, err := svc.ResolveConflictOutcome(ctx, c.ID, "local"); err == nil || !strings.Contains(err.Error(), tc.want) || errors.Unwrap(err) == nil {
				t.Fatalf("local pick = %v, want %q with its cause", err, tc.want)
			}
			row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calB, Uid: evt.UID})
			if err != nil || row.MoveState != MoveStateCancelFailed || row.ResolvedAt != nil {
				t.Fatalf("conflict = %+v, %v; want an open failed cancel", row, err)
			}
			after, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: evt.UID})
			if err != nil || after.RemoteUrl != before.RemoteUrl || after.Dirty != before.Dirty || after.Rev != before.Rev {
				t.Fatalf("sync resource A = %+v, %v; want it unchanged from %+v", after, err, before)
			}
		})
	}
}
