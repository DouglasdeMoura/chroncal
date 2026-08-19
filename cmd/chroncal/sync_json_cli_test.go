package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/storage"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// seedSyncConflictForTest inserts one unresolved conflict for the given
// calendar and returns its ID so the resolve command can target it. The
// bodies are full VCALENDAR payloads. The local pick imports LocalIcal.
func seedSyncConflictForTest(t *testing.T, dbPath string, calendarID int64) int64 {
	t.Helper()

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	const icalBody = "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:conflict-uid-1\r\n" +
		"DTSTAMP:20260401T120000Z\r\n" +
		"DTSTART:20260403T120000Z\r\nDTEND:20260403T130000Z\r\n" +
		"SUMMARY:Conflicted\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	ctx := context.Background()
	if err := a.Queries.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calendarID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "conflict-uid-1",
		LocalIcal:  icalBody,
		ServerIcal: icalBody,
		ServerEtag: "\"etag-1\"",
	}); err != nil {
		t.Fatalf("create sync conflict: %v", err)
	}

	conflicts, err := a.Queries.ListSyncConflicts(ctx)
	if err != nil {
		t.Fatalf("list sync conflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("seeded conflicts = %d, want 1", len(conflicts))
	}
	return conflicts[0].ID
}

// TestSyncResolveOutputJSON guards issue #307: `sync resolve --output json`
// must emit machine-readable JSON, not the plain-text confirmation line.
func TestSyncResolveOutputJSON(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	calID, _ := createLinkedCalendarForTest(t, dbPath)
	id := seedSyncConflictForTest(t, dbPath, calID)

	stdout, stderr, err := runChroncalCommand(t,
		"sync", "resolve", strconv.FormatInt(id, 10), "--pick", "local", "--output", "json")
	if err != nil {
		t.Fatalf("sync resolve -o json: %v (stderr: %s)", err, stderr)
	}

	var out map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &out); jerr != nil {
		t.Fatalf("sync resolve -o json produced non-JSON stdout %q: %v", stdout, jerr)
	}
}

// TestSyncResetOutputJSON guards issue #307: `sync reset --output json` must
// emit machine-readable JSON, not the plain-text "Reset sync state" line.
func TestSyncResetOutputJSON(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	stdout, stderr, err := runChroncalCommand(t,
		"sync", "reset", "--calendar", "Work", "--output", "json")
	if err != nil {
		t.Fatalf("sync reset -o json: %v (stderr: %s)", err, stderr)
	}

	var out []map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &out); jerr != nil {
		t.Fatalf("sync reset -o json produced non-JSON stdout %q: %v", stdout, jerr)
	}
}

// TestRenderSyncConflictsResolvedFlag guards the `sync conflicts --resolved`
// view (issue #610). A resolved row keeps its recording. The listing must
// show the resolution and its timestamp in both output formats. JSON
// consumers get explicit nulls for an unresolved row instead of absent keys.
func TestRenderSyncConflictsResolvedFlag(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			orig := outputFmt
			outputFmt = format
			defer func() { outputFmt = orig }()

			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)

			resolved := []syncPkg.Conflict{{
				ID:         7,
				CalendarID: 1,
				OwnerType:  "event",
				UID:        "resolved-uid",
				DetectedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
				Resolution: "server-auto",
				ResolvedAt: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
			}}
			if err := renderSyncConflicts(cmd, resolved, true); err != nil {
				t.Fatalf("renderSyncConflicts(%s, resolved): %v", format, err)
			}

			if format == "text" {
				got := out.String()
				if !strings.Contains(got, "resolution=server-auto") || !strings.Contains(got, "resolved=2026-08-02 10:00") {
					t.Fatalf("text output = %q, want resolution and resolved timestamp", got)
				}
			} else {
				var items []map[string]any
				if err := json.Unmarshal(out.Bytes(), &items); err != nil {
					t.Fatalf("json output = %q: %v", out.String(), err)
				}
				if len(items) != 1 || items[0]["resolution"] != "server-auto" || items[0]["resolved_at"] == nil {
					t.Fatalf("json output = %q, want one resolved row with resolution and resolved_at", out.String())
				}
			}

			// An unresolved row renders explicit nulls, not absent keys.
			if format == "json" {
				out.Reset()
				open := []syncPkg.Conflict{{
					ID: 8, CalendarID: 1, OwnerType: "event", UID: "open-uid",
					DetectedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
				}}
				if err := renderSyncConflicts(cmd, open, false); err != nil {
					t.Fatalf("renderSyncConflicts(%s, unresolved): %v", format, err)
				}
				var openItems []map[string]any
				if err := json.Unmarshal(out.Bytes(), &openItems); err != nil {
					t.Fatalf("json output = %q: %v", out.String(), err)
				}
				if len(openItems) != 1 || openItems[0]["resolution"] != nil || openItems[0]["resolved_at"] != nil {
					t.Fatalf("json output = %q, want one unresolved row with null resolution fields", out.String())
				}
			}
		})
	}
}
