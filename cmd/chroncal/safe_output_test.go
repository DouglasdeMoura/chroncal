package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

func TestWriteAlarmCheckLine_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeAlarmCheckLine(&buf, time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC), "DISPLAY", "Bad\x1b]52;c;clip\a\r\nTitle", false)

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("alarm line contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("alarm line contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("alarm line contains OSC payload: %q", out)
	}
	if !strings.Contains(out, "Bad Title") {
		t.Fatalf("alarm line did not contain sanitized title: %q", out)
	}
}

func TestPrintDiscoveredCalendars_SanitizesRemoteMetadata(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDiscoveredCalendars(&buf, "work", []caldav.RemoteCalendar{{
		Name:                  "Cal\x1b]52;c;clip\a",
		Path:                  "/dav/\x1b[31mwork",
		Description:           "Line\r\nbreak",
		SupportedComponentSet: []string{"VEVENT", "VTODO"},
	}})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("discover output contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("discover output contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("discover output contains OSC payload: %q", out)
	}
	if !strings.Contains(out, "Cal") || !strings.Contains(out, "/dav/work") || !strings.Contains(out, "Line break") {
		t.Fatalf("discover output did not contain sanitized fields: %q", out)
	}
}

func TestWritePendingAlarmLine_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writePendingAlarmLine(&buf, "state-1", "2026-04-04 09:30", "DISPLAY", "Bad\x1b]52;c;clip\a\r\nTitle", false, " (snoozed)")

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("pending alarm line contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("pending alarm line contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("pending alarm line contains OSC payload: %q", out)
	}
	if !strings.Contains(out, "Bad Title") {
		t.Fatalf("pending alarm line did not contain sanitized title: %q", out)
	}
}

func TestWriteMissedAlarmLine_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeMissedAlarmLine(&buf, time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC), "Bad\x1b]52;c;clip\a\r\nTitle", "", 2*time.Hour)

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("missed alarm line contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("missed alarm line contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("missed alarm line contains OSC payload: %q", out)
	}
	if !strings.Contains(out, "Bad Title") {
		t.Fatalf("missed alarm line did not contain sanitized title: %q", out)
	}
}

func TestWriteSyncStatusLine_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeSyncStatusLine(&buf, syncPkg.SyncStatus{
		CalendarName:        "Work\x1b]52;c;clip\a",
		LastSyncAt:          "2026-04-04T09:30:00Z",
		LastSyncAttemptedAt: "2026-04-04T09:45:00Z",
		LastSyncError:       "HTTP 500:\x1b[31m server\r\nerror",
		PendingPush:         1,
		Conflicts:           2,
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("sync status line contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("sync status line contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("sync status line contains OSC payload: %q", out)
	}
	if strings.Contains(out, "account=") {
		t.Fatalf("sync status line should not expose account details: %q", out)
	}
	if !strings.Contains(out, "Work") || !strings.Contains(out, "HTTP 500: server error") {
		t.Fatalf("sync status line did not contain sanitized fields: %q", out)
	}
}

// TestWriteSyncStatusLine_NamesStuckHrefs verifies that sync status names
// each href whose retries the pull stopped (issue #816). The href comes
// from the server, so the line must also remove control sequences.
func TestWriteSyncStatusLine_NamesStuckHrefs(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeSyncStatusLine(&buf, syncPkg.SyncStatus{
		CalendarName: "Work",
		StuckHrefs: []syncPkg.StuckHref{{
			Href:     "/calendar/stuck\x1b[31m.ics",
			UID:      "stuck-uid",
			Reason:   "unparseable",
			GaveUpAt: "2026-04-04T09:30:00Z",
		}},
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("stuck href line contains escape sequence: %q", out)
	}
	for _, want := range []string{"stuck href=/calendar/stuck", "uid=stuck-uid", "reason=unparseable", "since=2026-04-04T09:30:00Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("sync status output %q does not contain %q", out, want)
		}
	}
}

// TestWriteSyncStatusLine_NamesPushFailures: sync status names each sync row
// whose last push failed, with the count and the last error. The UID comes
// from the server, so the line also removes control sequences.
func TestWriteSyncStatusLine_NamesPushFailures(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeSyncStatusLine(&buf, syncPkg.SyncStatus{
		CalendarName: "Work",
		PushFailures: []syncPkg.PushFailure{{
			UID:       "failed\x1b[31m-uid",
			OwnerType: "event",
			Count:     2,
			Error:     "HTTP 500",
		}},
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("push failure line contains escape sequence: %q", out)
	}
	for _, want := range []string{"push failed uid=failed", "type=event", "attempts=2", "error=HTTP 500"} {
		if !strings.Contains(out, want) {
			t.Fatalf("sync status output %q does not contain %q", out, want)
		}
	}
}

// TestWriteSyncStatusLine_NamesUnownedEdits: sync status names each UID whose
// edit stays local because no series owns the edited row. The line gives the
// reason (storage.NoSeriesOwnerMsg).
func TestWriteSyncStatusLine_NamesUnownedEdits(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeSyncStatusLine(&buf, syncPkg.SyncStatus{
		CalendarName: "Work",
		UnownedEdits: []string{"legacy\x1b[31m-uid"},
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("unowned edit line contains escape sequence: %q", out)
	}
	for _, want := range []string{"edit not pushed uid=legacy", "reason=" + storage.NoSeriesOwnerMsg} {
		if !strings.Contains(out, want) {
			t.Fatalf("sync status output %q does not contain %q", out, want)
		}
	}
}

func TestWriteSyncConflictLine_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeSyncConflictLine(&buf, syncPkg.Conflict{
		ID:         12,
		OwnerType:  "event\x1b[31m",
		UID:        "uid\r\nvalue",
		DetectedAt: time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC),
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("sync conflict line contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("sync conflict line contains carriage return: %q", out)
	}
	if !strings.Contains(out, "event") || !strings.Contains(out, "uid value") {
		t.Fatalf("sync conflict line did not contain sanitized fields: %q", out)
	}
}

func TestWriteSyncResult_SanitizesErrors(t *testing.T) {
	t.Parallel()

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	writeSyncResult(&outBuf, &errBuf, &syncPkg.SyncResult{
		CalendarID: 1,
		Pushed:     1,
		Pulled:     2,
		Deleted:    3,
		Conflicts:  4,
		Errors: []error{
			errors.New("HTTP 500:\x1b]52;c;clip\a\r\nerror"),
		},
	})

	errOut := errBuf.String()
	if strings.Contains(errOut, "\x1b") {
		t.Fatalf("sync result error output contains escape sequence: %q", errOut)
	}
	if strings.Contains(errOut, "\r") {
		t.Fatalf("sync result error output contains carriage return: %q", errOut)
	}
	if strings.Contains(errOut, "]52;c;clip") {
		t.Fatalf("sync result error output contains OSC payload: %q", errOut)
	}
	if !strings.Contains(errOut, "HTTP 500: error") {
		t.Fatalf("sync result error output did not contain sanitized error: %q", errOut)
	}
}

// TestWriteSyncConflictLine_NamesTheMoveSource guards issue #841: a move
// conflict names the source calendar of the move, and an edit conflict
// does not.
func TestWriteSyncConflictLine_NamesTheMoveSource(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeSyncConflictLine(&buf, syncPkg.Conflict{
		ID:                   12,
		OwnerType:            "event",
		UID:                  "uid-1",
		DetectedAt:           time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC),
		MoveSourceCalendarID: 7,
	})
	if out := buf.String(); !strings.Contains(out, "moved-from-calendar=7") {
		t.Fatalf("move conflict line = %q, want the source calendar", out)
	}

	buf.Reset()
	writeSyncConflictLine(&buf, syncPkg.Conflict{
		ID:         13,
		OwnerType:  "event",
		UID:        "uid-2",
		DetectedAt: time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC),
	})
	if out := buf.String(); strings.Contains(out, "moved-from-calendar") {
		t.Fatalf("edit conflict line = %q, want no move source", out)
	}
}

// TestWriteSyncStatusLine_NamesWaitingRows checks the line of each row whose
// push waits for another step (issue #841). The reason comes from the push
// failure, so the line removes control sequences too.
func TestWriteSyncStatusLine_NamesWaitingRows(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeSyncStatusLine(&buf, syncPkg.SyncStatus{
		CalendarName: "Work",
		Waiting: []syncPkg.WaitingResource{{
			UID:    "wait-uid",
			Reason: "the move conflict on calendar \"c\x1b[31m\"",
		}},
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("waiting line contains escape sequence: %q", out)
	}
	if !strings.Contains(out, `waiting uid=wait-uid  on the move conflict on calendar "c`) {
		t.Fatalf("sync status output %q misses the waiting line", out)
	}
}

// TestWriteAlarmCheckLine_PrintsTheDate covers issue #920. A trigger can be
// on another day, so the line prints the date, as alarm list does.
func TestWriteAlarmCheckLine_PrintsTheDate(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	at := time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC)
	writeAlarmCheckLine(&buf, at, "DISPLAY", "Standup", false)

	want := at.Local().Format("2006-01-02 15:04") + "\tDISPLAY\tStandup\n"
	if got := buf.String(); got != want {
		t.Fatalf("alarm check line = %q, want %q", got, want)
	}
}
