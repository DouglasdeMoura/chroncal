package main

import (
	"bytes"
	"context"
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

// A 412 under ConflictPrompt records Conflicts and leaves the local edit
// dirty. The report must not stay silent when that is the only outcome.
func TestReportOpportunisticPushReportsConflicts(t *testing.T) {
	t.Parallel()

	var out, warn bytes.Buffer
	reportOpportunisticPush(&out, &warn, "Work", &syncPkg.SyncResult{
		Conflicts: 2,
		Warnings: []syncPkg.ImportWarning{
			{Message: "dropped alarm with unusable trigger"},
		},
	})
	got := out.String()
	if !strings.Contains(got, "note:") {
		t.Fatalf("stdout writer got %q, want a conflict note", got)
	}
	if !strings.Contains(got, "2") {
		t.Fatalf("stdout writer got %q, want the conflict count", got)
	}
	if !strings.Contains(got, "sync resolve") {
		t.Fatalf("stdout writer got %q, want a pointer to chroncal sync resolve", got)
	}
	if strings.Contains(got, "import warning") {
		t.Errorf("stdout writer got %q; warnings must stay on the warning writer", got)
	}
	if !strings.Contains(warn.String(), "dropped alarm") {
		t.Errorf("warning writer got %q, want the import warning", warn.String())
	}

	// A JSON caller discards stdout; the warning must still reach warnW.
	warn.Reset()
	reportOpportunisticPush(io.Discard, &warn, "Work", &syncPkg.SyncResult{
		Conflicts: 1,
		Warnings:  []syncPkg.ImportWarning{{Message: "dropped alarm with unusable trigger"}},
	})
	if !strings.Contains(warn.String(), "dropped alarm") {
		t.Errorf("warning writer got %q, want the warning despite discarded stdout", warn.String())
	}
}

// A 412 must record a conflict and keep the local edit. ConflictServerWins
// would import the server row and clear dirty before the user can resolve.
func TestOpportunisticCLIPushUsesConflictPrompt(t *testing.T) {
	t.Parallel()
	if opportunisticPushStrategy != syncPkg.ConflictPrompt {
		t.Fatalf("opportunistic CLI push strategy = %q, want %q", opportunisticPushStrategy, syncPkg.ConflictPrompt)
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
	if err := a.Queries.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
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
		"sync", "resolve", strconv.FormatInt(conflictID, 10), "--pick", "server")
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
