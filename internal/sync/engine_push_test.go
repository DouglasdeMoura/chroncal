package sync

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestEnginePushSurfacesFinalizeFailure is the regression test for the
// silent finalize failure. FinalizePushedResource records the new server
// ETag and clears the dirty flag after a successful PUT. When that write
// fails, the row keeps the old ETag and stays dirty. The next push then
// replays a phantom 412 on a body the server already accepted. The push
// result must report the failure.
func TestEnginePushSurfacesFinalizeFailure(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "finalize-fail")

	// Fail only the finalize write. It is the sole statement that advances
	// the ETag of a dirty row. Every other bookkeeping write (for example
	// clearPushFailure) keeps the ETag, so the trigger leaves it alone.
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER block_finalize BEFORE UPDATE ON sync_resources
		FOR EACH ROW WHEN NEW.etag != OLD.etag
		BEGIN
			SELECT RAISE(ABORT, 'forced finalize failure');
		END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/calendar/finalize-fail.ics" {
			return newResponse(http.StatusCreated, map[string]string{"ETag": `"etag-new"`}), nil
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return newResponse(http.StatusInternalServerError, nil), nil
	})

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "finalize-fail",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/finalize-fail.ics",
		Etag:         `"etag-old"`,
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.pushed != 1 {
		t.Fatalf("pushed = %d, want 1: the PUT itself succeeded", result.pushed)
	}
	if len(result.errors) != 1 {
		t.Fatalf("errors = %v, want one finalize failure", result.errors)
	}
	if got := result.errors[0].Error(); !strings.Contains(got, "finalize-fail") || !strings.Contains(got, "forced finalize failure") {
		t.Fatalf("error = %q, want the finalize failure for finalize-fail", got)
	}

	// The aborted statement leaves the row dirty with the old ETag. That is
	// the wedged state the surfaced error must make visible.
	dirty, err := q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListDirtySyncResources: %v", err)
	}
	if len(dirty) != 1 || dirty[0].Uid != "finalize-fail" {
		t.Fatalf("dirty resources = %+v, want the finalize-fail row", dirty)
	}
}

// TestEnginePushSurfacesRemoteHrefFailure is the regression test for the
// silent remote-URL write. A first-time push leaves RemoteUrl empty until
// UpsertSyncResource records the href the server just accepted. When that
// write fails, the next push PUTs the same body to a new path. The server
// then holds a duplicate object. The push result must report the failure.
func TestEnginePushSurfacesRemoteHrefFailure(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "new-href")

	// Fail only the remote-URL write. It is the sole statement that changes
	// remote_url. FinalizePushedResource and clearPushFailure leave the
	// column alone, so the trigger lets them pass.
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER block_href_update BEFORE UPDATE ON sync_resources
		FOR EACH ROW WHEN NEW.remote_url != OLD.remote_url
		BEGIN
			SELECT RAISE(ABORT, 'forced href failure');
		END
	`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	overrideRemoteObjectNameGenerator(t, "new-href.ics")

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut && r.URL.Path == "/calendar/new-href.ics" {
			return newResponse(http.StatusCreated, map[string]string{"ETag": `"etag-new"`}), nil
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return newResponse(http.StatusInternalServerError, nil), nil
	})

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "new-href",
		OwnerType:    "event",
		RemoteUrl:    "",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "/calendar/", "", ConflictServerWins, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.pushed != 1 {
		t.Fatalf("pushed = %d, want 1: the PUT itself succeeded", result.pushed)
	}
	if len(result.errors) != 1 {
		t.Fatalf("errors = %v, want one remote-href failure", result.errors)
	}
	if got := result.errors[0].Error(); !strings.Contains(got, "new-href") || !strings.Contains(got, "forced href failure") {
		t.Fatalf("error = %q, want the remote-href failure for new-href", got)
	}

	// The aborted upsert leaves RemoteUrl empty. A later push would mint a
	// second path for the same UID. That is the duplicate-object state the
	// surfaced error must make visible.
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "new-href",
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if sr.RemoteUrl != "" {
		t.Fatalf("RemoteUrl = %q, want empty: the href write must not land", sr.RemoteUrl)
	}
}
