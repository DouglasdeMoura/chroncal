package sync

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestPushServerWinsRepullPreservesDTEND drives the ConflictServerWins
// accept-server path with a server body whose DTEND fails to parse locally.
// The re-import must store the raw string in the preservation slot again
// (issue #567). The local-edit clear in the event service must not break
// this path. A sync upsert never goes through UpdateParams, so it never
// clears the slot (issue #649).
func TestPushServerWinsRepullPreservesDTEND(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	insertTestEvent(t, db, calendarID, "srv-wins-dtend")

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodPut:
			return &http.Response{
				StatusCode: http.StatusPreconditionFailed,
				Status:     "412 Precondition Failed",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("precondition failed")),
				Request:    r,
			}, nil
		case http.MethodGet:
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Type": []string{"text/calendar; charset=utf-8"},
					"Etag":         []string{`"etag-server"`},
				},
				Body: io.NopCloser(strings.NewReader("BEGIN:VCALENDAR\r\n" +
					"VERSION:2.0\r\n" +
					"PRODID:-//chroncal//tests//EN\r\n" +
					"BEGIN:VEVENT\r\n" +
					"UID:srv-wins-dtend\r\n" +
					"DTSTAMP:20260101T000000Z\r\n" +
					"DTSTART:20260101T150000Z\r\n" +
					"DTEND;TZID=Customized Time Zone:20260101T163000\r\n" +
					"SUMMARY:Server version\r\n" +
					"END:VEVENT\r\n" +
					"END:VCALENDAR\r\n")),
				Request: r,
			}, nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calendarID,
		Uid:          "srv-wins-dtend",
		OwnerType:    "event",
		RemoteUrl:    "/calendar/srv-wins-dtend.ics",
		Etag:         "etag-before",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	result, err := engine.push(ctx, client, calendarID, "", "", ConflictServerWins, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	// Issue #610: a server-wins 412 records the row, adopts the server
	// body, and settles it in the same pass. AutoResolved counts it; an
	// open Conflicts count would mean the row stayed unresolved.
	if result.autoResolved != 1 {
		t.Fatalf("autoResolved = %d, want 1", result.autoResolved)
	}
	if result.conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", result.conflicts)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %d, want 0: %v", len(result.errors), result.errors)
	}

	evt, err := q.GetEventByUID(ctx, "srv-wins-dtend")
	if err != nil {
		t.Fatalf("GetEventByUID: %v", err)
	}
	xps, err := q.ListXPropertiesByOwner(ctx, storage.ListXPropertiesByOwnerParams{
		OwnerType: "event", OwnerID: evt.ID,
	})
	if err != nil {
		t.Fatalf("ListXPropertiesByOwner: %v", err)
	}
	found := false
	for _, xp := range xps {
		if xp.Name != model.XPropOriginalDTEND {
			continue
		}
		found = true
		if xp.Value != "20260101T163000" {
			t.Errorf("preserved value = %q, want 20260101T163000", xp.Value)
		}
	}
	if !found {
		t.Fatalf("the ServerWins re-pull did not store %s again in %+v",
			model.XPropOriginalDTEND, xps)
	}
}
