package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/storage"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// Import warnings surfaced on SyncResult are printed by the silent CLI entry
// points (initial sync after account add, opportunistic push after a write).
// One compact line per warning, prefixed so the user knows where it came
// from. Strictly nothing when there are none. The opportunistic
// push runs after every single write.
func TestFprintImportWarnings(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	fprintImportWarnings(&buf, []syncPkg.ImportWarning{
		{Path: "/cal/warned.ics", UID: "warned-uid", Message: "VEVENT warned-uid: malformed DTEND, fabricated a 1h span"},
		{Path: "/cal/multi.ics", Message: "VEVENT other-uid: dropped alarm with unusable trigger"},
	})

	got := buf.String()
	want := "import warning: /cal/warned.ics (uid warned-uid): VEVENT warned-uid: malformed DTEND, fabricated a 1h span\n" +
		"import warning: /cal/multi.ics: VEVENT other-uid: dropped alarm with unusable trigger\n"
	if got != want {
		t.Errorf("fprintImportWarnings output =\n%q\nwant\n%q", got, want)
	}

	buf.Reset()
	fprintImportWarnings(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no warnings must print nothing (this runs after every write); got %q", buf.String())
	}
}

// The opportunistic post-write push must emit import warnings through the
// injected warning writer, not a hard-wired os.Stderr. It must keep them
// off the stdout writer. JSON callers pass io.Discard for stdout to keep it
// clean. The warnings still belong on the ERROR stream.
func TestReportOpportunisticPushEmitsWarningsThroughInjectedWriter(t *testing.T) {
	t.Parallel()

	var out, warn bytes.Buffer
	reportOpportunisticPush(&out, &warn, "Work", &syncPkg.SyncResult{
		Pushed: 1,
		Warnings: []syncPkg.ImportWarning{
			{Path: "/cal/warned.ics", UID: "warned-uid", Message: "malformed DTEND, fabricated a 1h span"},
		},
	})
	if !strings.Contains(warn.String(), "import warning:") || !strings.Contains(warn.String(), "DTEND") {
		t.Errorf("warning writer got %q, want the DTEND import warning", warn.String())
	}
	if strings.Contains(out.String(), "import warning") {
		t.Errorf("stdout writer got %q; warnings must stay on the warning writer", out.String())
	}
	if !strings.Contains(out.String(), "Synced to Work") {
		t.Errorf("stdout writer got %q, want the push confirmation", out.String())
	}

	// A JSON caller discards stdout; the warning must still reach warnW.
	warn.Reset()
	reportOpportunisticPush(io.Discard, &warn, "Work", &syncPkg.SyncResult{
		Warnings: []syncPkg.ImportWarning{{Message: "dropped alarm with unusable trigger"}},
	})
	if !strings.Contains(warn.String(), "dropped alarm") {
		t.Errorf("warning writer got %q, want the warning despite discarded stdout", warn.String())
	}

	// And a clean push after every write must stay silent on both writers.
	out.Reset()
	warn.Reset()
	reportOpportunisticPush(&out, &warn, "Work", &syncPkg.SyncResult{})
	if out.Len() != 0 || warn.Len() != 0 {
		t.Errorf("clean no-op push wrote out=%q warn=%q, want silence", out.String(), warn.String())
	}
}

// A 412 during the opportunistic post-write push records a conflict and keeps
// the local edit (issue #610). The note that says so must reach the warning
// stream in every output mode: a JSON caller discards stdout to keep it
// parseable, so stdout must carry nothing for a conflicts-only result, and
// the note must still print. The note points at the listing command because
// that is where the conflict ID comes from.
func TestReportOpportunisticPushConflictNoteOnWarnStream(t *testing.T) {
	t.Parallel()

	// Text caller: nothing was pushed (the write conflicted), so stdout stays
	// clean and the note lands on warnW.
	var out, warn bytes.Buffer
	reportOpportunisticPush(&out, &warn, "Work", &syncPkg.SyncResult{Conflicts: 1})
	if !strings.Contains(warn.String(), `1 local change(s) conflicted with the server; run "chroncal sync conflicts" to resolve`) {
		t.Errorf("warning writer got %q, want the conflict note", warn.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout writer got %q; a conflicts-only result must not write to stdout", out.String())
	}

	// JSON caller (io.Discard stdout): the note must still reach warnW.
	warn.Reset()
	reportOpportunisticPush(io.Discard, &warn, "Work", &syncPkg.SyncResult{Conflicts: 2})
	if !strings.Contains(warn.String(), "2 local change(s) conflicted") {
		t.Errorf("warning writer got %q, want the conflict note despite discarded stdout", warn.String())
	}

	// An auto-resolved conflict (server-wins full pass folded into the
	// result) reports on warnW too, worded as a resolution, not an open row.
	warn.Reset()
	reportOpportunisticPush(io.Discard, &warn, "Work", &syncPkg.SyncResult{AutoResolved: 1})
	if !strings.Contains(warn.String(), "1 conflict(s) resolved in favor of the server") {
		t.Errorf("warning writer got %q, want the auto-resolved note", warn.String())
	}
}

// `sync resolve <id> --pick server` imports the recorded server body through
// the same importICal as the auto server-wins paths. The engine logger is nil
// (silent). The warnings ResolveConflict returns are then the only place
// a fabricated value (here a made-up DTEND span) can surface. They must land
// on stderr, one line each, like the other silent sync entry points.
func TestSyncResolveServerPrintsImportWarnings(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	serverIcal := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:resolve-warned-uid\r\n" +
		"DTSTAMP:20260403T120000Z\r\n" +
		"DTSTART:20260403T120000Z\r\n" +
		"DTEND:garbage\r\n" +
		"SUMMARY:Server event with unparseable DTEND\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	if _, err := a.Queries.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: cals[0].ID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "resolve-warned-uid",
		LocalIcal:  "local",
		ServerIcal: serverIcal,
		ServerEtag: "etag-1",
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	conflicts, err := a.Queries.ListSyncConflicts(ctx)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("ListSyncConflicts = %d conflicts, err %v; want 1", len(conflicts), err)
	}
	conflictID := conflicts[0].ID
	a.Close()

	stdout, stderr, err := runChroncalCommand(t,
		"--allow-plaintext", "sync", "resolve", strconv.FormatInt(conflictID, 10), "--pick", "server")
	if err != nil {
		t.Fatalf("sync resolve: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, "resolved") {
		t.Fatalf("sync resolve stdout = %q, want the resolution confirmation", stdout)
	}
	if !strings.Contains(stderr, "import warning:") || !strings.Contains(stderr, "DTEND") {
		t.Fatalf("sync resolve stderr = %q, want the DTEND import warning", stderr)
	}
	if !strings.Contains(stderr, "resolve-warned-uid") {
		t.Fatalf("sync resolve stderr = %q, want the warning labeled with the component UID", stderr)
	}
}

// TestSyncResolveServerOfAGoneSourceExitsZero runs the server pick on a move
// whose source object is gone (the source_gone mark). The target can create
// the copy, so the pick finishes the move. The command must exit with 0 and
// print the gone-source note as a warning on stderr.
func TestSyncResolveServerOfAGoneSourceExitsZero(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	targetID, accountID := createLinkedCalendarForTest(t, dbPath)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	ctx := context.Background()
	source, err := a.Calendars.Create(ctx, "Home", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if err := a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: source.ID, AccountID: &accountID, RemoteUrl: storage.StringToNullable("https://cal.example.com/dav/calendars/home/"),
	}); err != nil {
		t.Fatalf("link the source calendar: %v", err)
	}
	const uid = "gone-source-cli@example.com"
	res, err := a.DB.ExecContext(ctx,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, targetID, "Moved", "2026-04-03T10:00:00Z", "2026-04-03T11:00:00Z", "CONFIRMED", "OPAQUE", "PUBLIC")
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	eventID, _ := res.LastInsertId()
	for _, stmt := range []string{
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy) VALUES (` + strconv.FormatInt(targetID, 10) + `, '` + uid + `', 'event', '', '', 1, 'sync-token')`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy) VALUES (` + strconv.FormatInt(source.ID, 10) + `, '` + uid + `', 'event', 'https://cal.example.com/dav/calendars/home/x.ics', 'e1', 0, 'sync-token')`,
		`INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id, source_gone) VALUES (` + strconv.FormatInt(source.ID, 10) + `, '` + uid + `', 'https://cal.example.com/dav/calendars/home/x.ics', ` + strconv.FormatInt(targetID, 10) + `, 1)`,
	} {
		if _, err := a.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	sourceID := source.ID
	if _, err := a.Queries.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: targetID, OwnerType: "event", OwnerID: eventID, Uid: uid,
		LocalIcal: "local", ServerIcal: "server", ServerEtag: "e1", MoveSourceCalendarID: &sourceID,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	conflicts, err := a.Queries.ListSyncConflicts(ctx)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("ListSyncConflicts = %d, %v; want 1", len(conflicts), err)
	}
	conflictID := conflicts[0].ID
	a.Close()

	stdout, stderr, err := runChroncalCommand(t,
		"--allow-plaintext", "sync", "resolve", strconv.FormatInt(conflictID, 10), "--pick", "server")
	if err != nil {
		t.Fatalf("sync resolve = %v, want exit 0 (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, "the source is gone, so the move finished; the next sync creates the moved copy") || strings.Contains(stdout, "picked server") {
		t.Fatalf("sync resolve stdout = %q, want the finished move and no picked side", stdout)
	}
	// The JSON output names the outcome.
	if _, err := reopenMoveConflictForTest(t, dbPath, uid); err != nil {
		t.Fatalf("reopen the move: %v", err)
	}
	stdout, stderr, err = runChroncalCommand(t,
		"--allow-plaintext", "sync", "resolve", strconv.FormatInt(conflictID, 10), "--pick", "server", "--output", "json")
	if err != nil {
		t.Fatalf("sync resolve --output json = %v (stderr: %s)", err, stderr)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(stdout), &item); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if item["outcome"] != "finished" || item["picked"] != "none" || item["resolved"] != true || item["note"] != "the source is gone, so the move finished; the next sync creates the moved copy" {
		t.Fatalf("json = %v, want the finished outcome, picked none, and its note", item)
	}
}

// reopenMoveConflictForTest opens the move conflict of uid again, so a second pick
// runs on the same rows.
func reopenMoveConflictForTest(t *testing.T, dbPath, uid string) (int64, error) {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		return 0, err
	}
	defer a.Close()
	res, err := a.DB.ExecContext(context.Background(), `UPDATE sync_conflicts SET move_state = 'open', resolved_at = NULL, resolution = NULL WHERE uid = ?`, uid)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
