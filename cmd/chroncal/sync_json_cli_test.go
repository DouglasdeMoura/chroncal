package main

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// seedSyncConflictForTest inserts one unresolved conflict for the given
// calendar and returns its ID so the resolve command can target it.
func seedSyncConflictForTest(t *testing.T, dbPath string, calendarID int64) int64 {
	t.Helper()

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	ctx := context.Background()
	if err := a.Queries.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calendarID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "conflict-uid-1",
		LocalIcal:  "BEGIN:VEVENT\r\nUID:conflict-uid-1\r\nEND:VEVENT\r\n",
		ServerIcal: "BEGIN:VEVENT\r\nUID:conflict-uid-1\r\nEND:VEVENT\r\n",
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
