package sync

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// A missing master row means "no organizer known". The gate must answer
// true so an orphaned dirty row can still push.
func TestUserOrganizesEvent_MissingRowIsNoOrganizer(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	e := NewEngine(db, q, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	got, err := e.userOrganizesEvent(ctx, "no-such-uid", "owner@example.com")
	if err != nil {
		t.Fatalf("userOrganizesEvent: %v", err)
	}
	if !got {
		t.Errorf("userOrganizesEvent = false for a missing row, want true")
	}
}

// Any lookup failure other than ErrNoRows must surface as an error. The
// caller keeps the row dirty; the gate must not guess in either direction.
func TestUserOrganizesEvent_LookupFailurePropagates(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	e := NewEngine(db, q, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	_, err := e.userOrganizesEvent(ctx, "some-uid", "owner@example.com")
	if err == nil {
		t.Fatal("userOrganizesEvent returned no error on a failed lookup, want error")
	}
	if errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("unexpected sentinel: %v", err)
	}
}

// A failed organizer lookup in the push loop must also record a failed
// push attempt. push_fail_count and last_push_error then keep the same
// contract as every other failure path in push, and the sync doctor can
// report the wedge.
func TestEnginePushRecordsFailureWhenOrganizerLookupFails(t *testing.T) {
	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "gate-lookup-fail",
		OwnerType:    "event",
		RemoteUrl:    "",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// Rename the events table after the dirty list seed. The organizer
	// lookup then fails with a real query error, not with ErrNoRows.
	if _, err := db.ExecContext(ctx, "ALTER TABLE events RENAME TO events_gone"); err != nil {
		t.Fatalf("rename events table: %v", err)
	}

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return nil, nil
	})

	pushResult, err := engine.push(ctx, client, calendarID, "/calendar/", "me@example.com", ConflictServerWins, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(pushResult.errors) != 1 {
		t.Fatalf("errors = %v, want exactly the organizer lookup error", pushResult.errors)
	}
	if !strings.Contains(pushResult.errors[0].Error(), "organizer lookup for gate-lookup-fail") {
		t.Fatalf("error = %v, want the organizer lookup error", pushResult.errors[0])
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        "gate-lookup-fail",
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.PushFailCount != 1 {
		t.Errorf("PushFailCount = %d, want 1", res.PushFailCount)
	}
	if !strings.Contains(res.LastPushError, "organizer lookup") {
		t.Errorf("LastPushError = %q, want the organizer lookup error", res.LastPushError)
	}
	if res.Dirty != 1 {
		t.Errorf("Dirty = %d, want 1 so the next pass retries", res.Dirty)
	}
}
