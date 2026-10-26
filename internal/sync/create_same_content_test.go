package sync

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestCreateOfTheSameItemLinksToTheObject covers issue #918. After a new
// link to a collection that already holds the item, the create gets 412.
// The object holds the same item, so the push records no conflict: the row
// takes the href and the etag of the object and is clean.
func TestCreateOfTheSameItemLinksToTheObject(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	uid := "same-item"
	path := "/cal/a/" + uid + ".ics"
	srv.setRemoteBody(path, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//x//EN\r\nBEGIN:VEVENT\r\nUID:"+uid+
		"\r\nDTSTAMP:20260403T100000Z\r\nSEQUENCE:3\r\nDTSTART:20260403T100000Z\r\nDTEND:20260403T110000Z\r\nSUMMARY:Same\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: uid, CalendarID: calA, Title: "Same", StartTime: start, EndTime: start.Add(time.Hour),
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

	res, err := engine.PushLocalEdits(ctx, calA)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 {
		t.Fatalf("push = %+v, %v; want no conflict", res, err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calA)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, %v; want none", conflicts, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calA, Uid: uid})
	if err != nil || sr.Dirty != 0 || !strings.HasSuffix(sr.RemoteUrl, uid+".ics") || sr.Etag == "" {
		t.Fatalf("sync resource = %+v, %v; want a clean row that names the object", sr, err)
	}
	if body := srv.body(path); !strings.Contains(body, "SEQUENCE:3") {
		t.Errorf("server body = %q, want the object unchanged", body)
	}
}

// TestCreateOfAnotherItemStillRecordsAConflict keeps the conflict when the
// object at the href holds another version of the item.
func TestCreateOfAnotherItemStillRecordsAConflict(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()

	seedCreateConflict(t, engine, q, srv, calA)
	if res, err := engine.PushLocalEdits(ctx, calA); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v; want one conflict", res, err)
	}
}
