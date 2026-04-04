package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
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
	writeMissedAlarmLine(&buf, time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC), "Bad\x1b]52;c;clip\a\r\nTitle", false, 2*time.Hour)

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
		AccountName:         "Main\r\nAccount",
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
	if !strings.Contains(out, "Work") || !strings.Contains(out, "Main Account") || !strings.Contains(out, "HTTP 500: server error") {
		t.Fatalf("sync status line did not contain sanitized fields: %q", out)
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
