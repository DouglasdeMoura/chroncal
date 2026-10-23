package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// seedWedgedCLIResource inserts one dirty event whose relations relation
// cannot load, then renames that table. The doctor then lists exactly one
// wedge. event_relations is safe to rename: only hydration reads it, so the
// helper process still opens the database afterwards.
func seedWedgedCLIResource(t *testing.T, dbPath string) {
	t.Helper()
	seedWedgedCLIResourceWithUID(t, dbPath, "wedge-cli@example.com")
}

// seedWedgedCLIResourceWithUID seeds one wedged resource whose sync UID is
// the given string. The doctor then lists exactly that UID. The helper
// renames event_relations back at cleanup like the fixed-UID variant.
func seedWedgedCLIResourceWithUID(t *testing.T, dbPath string, uid string) {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	cals, err := a.Queries.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, cals[0].ID, "Wedged",
		"2026-04-03T10:00:00Z", "2026-04-03T11:00:00Z", "CONFIRMED", "OPAQUE", "PUBLIC",
	); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if err := a.Queries.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   cals[0].ID,
		Uid:          uid,
		OwnerType:    "event",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `ALTER TABLE event_relations RENAME TO event_relations_broken`); err != nil {
		t.Fatalf("rename event_relations: %v", err)
	}
}

// bumpPushFailCount stores a failed-push count on the wedged resource, like
// the engine bookkeeping does after failed push attempts.
func bumpPushFailCount(t *testing.T, dbPath string, count int) {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	if _, err := a.DB.ExecContext(context.Background(),
		`UPDATE sync_resources SET push_fail_count = ?, last_push_error = ?`,
		count, "export wedge-cli@example.com: unreadable relation(s) relations",
	); err != nil {
		t.Fatalf("bump push_fail_count: %v", err)
	}
}

func TestSyncDoctorEmptyReportsNone(t *testing.T) {
	setupCalendarCLITestEnv(t)

	stdout, _, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v", err)
	}
	if !strings.Contains(stdout, "No wedged resources.") {
		t.Errorf("output %q misses the empty report", stdout)
	}
}

func TestSyncDoctorJSONEmptyIsArray(t *testing.T) {
	setupCalendarCLITestEnv(t)

	stdout, _, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--output", "json")
	if err != nil {
		t.Fatalf("sync doctor --output json: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if len(items) != 0 {
		t.Errorf("items = %v, want an empty array", items)
	}
}

func TestSyncDoctorListsWedgedResource(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedWedgedCLIResource(t, dbPath)

	stdout, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, "uid wedge-cli@example.com (event): unreadable relation(s) relations; 0 failed push attempt(s)") {
		t.Errorf("output %q misses the wedged line with its push-failure count", stdout)
	}
	// The whole recovery hint belongs to stdout. A consumer that captures
	// stdout then also captures the command it needs.
	for _, want := range []string{
		"1 wedged resource(s). Recover one with:",
		"chroncal sync doctor --push <uid> --yes",
		"The push drops the unreadable relations from the server copy.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout %q misses hint %q", stdout, want)
		}
	}
	if strings.Contains(stderr, "chroncal sync doctor") {
		t.Errorf("stderr %q carries part of the hint; the hint belongs on stdout", stderr)
	}

	bumpPushFailCount(t, dbPath, 3)
	stdout, _, err = runChroncalCommand(t, "sync", "--allow-plaintext", "doctor")
	if err != nil {
		t.Fatalf("sync doctor after bumps: %v", err)
	}
	if !strings.Contains(stdout, "3 failed push attempt(s)") {
		t.Errorf("output %q misses the recorded push-failure count", stdout)
	}
	// The wedged line names the count, so no push-failure line repeats it.
	if strings.Contains(stdout, "last error:") {
		t.Errorf("output %q lists the wedged resource as a push failure too", stdout)
	}
}

// TestSyncDoctorSanitizesRemoteDerivedStrings guards the doctor output.
// The UID and owner type come from remote iCal data. A UID with terminal
// escape bytes must not reach the terminal raw. Every other wedged line
// passes through safeText like the sync status and conflict lines.
func TestSyncDoctorSanitizesRemoteDerivedStrings(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedWedgedCLIResourceWithUID(t, dbPath, "\x1b[31mwedge\x1b[0m@example.com")

	stdout, _, err := runChroncalCommand(t, "sync", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v", err)
	}
	if strings.Contains(stdout, "\x1b") {
		t.Fatalf("doctor line leaked a raw escape byte:\n%q", stdout)
	}
	if !strings.Contains(stdout, "uid wedge@example.com (event):") {
		t.Fatalf("output %q misses the sanitized wedged line", stdout)
	}
}

func TestSyncDoctorJSONListsWedgedResource(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedWedgedCLIResource(t, dbPath)
	bumpPushFailCount(t, dbPath, 2)

	stdout, _, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--output", "json")
	if err != nil {
		t.Fatalf("sync doctor --output json: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %v, want one entry", items)
	}
	item := items[0]
	if item["uid"] != "wedge-cli@example.com" {
		t.Errorf("uid = %v, want wedge-cli@example.com", item["uid"])
	}
	if item["owner_type"] != "event" {
		t.Errorf("owner_type = %v, want event", item["owner_type"])
	}
	relations, _ := item["relations"].([]any)
	if len(relations) != 1 || relations[0] != "relations" {
		t.Errorf("relations = %v, want [relations]", item["relations"])
	}
	if item["push_fail_count"] != float64(2) {
		t.Errorf("push_fail_count = %v, want 2", item["push_fail_count"])
	}
}

func TestSyncDoctorPushRefusesNonInteractiveWithoutYes(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedWedgedCLIResource(t, dbPath)

	// Seed one wedged resource so the push flow reaches its confirmation
	// gate instead of stopping at the not-found check.
	_, _, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--push", "wedge-cli@example.com")
	if err == nil {
		t.Fatal("expected refusal without --yes in a non-interactive shell")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("refusal %q does not mention --yes", err.Error())
	}
}

func TestSyncDoctorPushUnknownUIDIsNotFound(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--push", "missing@example.com", "--yes", "--output", "json")
	if err == nil {
		t.Fatal("push of an unknown uid should fail")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode error payload %q: %v", stderr, jerr)
	}
	if payload.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", payload.Code)
	}
	if !strings.Contains(payload.Error, "missing@example.com") {
		t.Fatalf("error = %q, want it to name the uid", payload.Error)
	}
}

// seedWaitingCLIResource inserts one dirty event row with a wait for the
// source delete of a sibling calendar, as the push records it.
func seedWaitingCLIResource(t *testing.T, dbPath string) {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	cals, err := a.Queries.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	if err := a.Queries.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: cals[0].ID, Uid: "wait-cli@example.com", OwnerType: "event", Dirty: 1, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	// A create waits for the source delete of a sibling calendar. The row
	// is dirty with no href. A tombstone of the UID waits in a second
	// calendar of the same account.
	if _, err := a.DB.ExecContext(ctx, `UPDATE sync_resources SET wait_kind = 2 WHERE uid = ?`, "wait-cli@example.com"); err != nil {
		t.Fatalf("record the wait: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (900, 'wait-acct', 'https://example.com', 'basic', 'u')`,
		`UPDATE calendars SET account_id = 900, remote_url = '/own/' WHERE id = ` + fmt.Sprint(cals[0].ID),
		`INSERT INTO calendars (name, account_id, remote_url) VALUES ('Sibling', 900, '/sibling/')`,
	} {
		if _, err := a.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO tombstones (calendar_id, uid, remote_url) SELECT id, ?, '/sibling/x.ics' FROM calendars WHERE name = 'Sibling'`, "wait-cli@example.com"); err != nil {
		t.Fatalf("record the wait: %v", err)
	}
}

// TestSyncDoctorListsAWaitingResource checks that the doctor shows a row
// whose push waits as a wait, not as a wedge or a failure (issue #841).
func TestSyncDoctorListsAWaitingResource(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedWaitingCLIResource(t, dbPath)

	stdout, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, `uid wait-cli@example.com (event): waiting on the source delete of the UID in another calendar`) {
		t.Errorf("output %q misses the waiting line", stdout)
	}
	if !strings.Contains(stdout, "No wedged resources.") {
		t.Errorf("output %q misses the empty wedge report", stdout)
	}

	stdout, _, err = runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--output", "json")
	if err != nil {
		t.Fatalf("sync doctor --output json: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if len(items) != 1 || items[0]["state"] != "waiting" || items[0]["reason"] != "the source delete of the UID in another calendar" {
		t.Fatalf("items = %v, want one waiting item with its reason", items)
	}
}

// seedNoteCLIResources seeds one sync row with a failed push and one legacy
// override with an edit that no series owns.
func seedNoteCLIResources(t *testing.T, dbPath string) {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	cals, err := a.Queries.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	// The calendar has an account link. A second calendar with no link
	// keeps an old push failure, which the doctor does not list.
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (901, 'note-acct', 'https://example.com', 'basic', 'u')`,
		`UPDATE calendars SET account_id = 901, remote_url = '/own/' WHERE id = ` + fmt.Sprint(cals[0].ID),
		`INSERT INTO calendars (name) VALUES ('Unlinked')`,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, push_fail_count, last_push_error)
		 SELECT id, 'stale-cli@example.com', 'event', '/x/stale.ics', 4, 'HTTP 500' FROM calendars WHERE name = 'Unlinked'`,
	} {
		if _, err := a.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, push_fail_count, last_push_error)
		 VALUES (?, 'fail-cli@example.com', 'event', '/c/fail.ics', 2, 'HTTP 500')`, cals[0].ID); err != nil {
		t.Fatalf("seed a push failure: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id, edit_unowned)
		 VALUES ('unowned-cli@example.com', ?, 't', '2026-05-18T09:00:00Z', '2026-05-18T10:00:00Z', '2026-05-18T09:00:00Z', 1)`,
		cals[0].ID); err != nil {
		t.Fatalf("seed an unowned edit: %v", err)
	}
}

// TestSyncDoctorListsPushFailuresAndUnownedEdits checks that the doctor
// shows a push failure and an edit that no series owns, as the sync status
// does (issue #862). The old push failure of a calendar with no account
// link does not show.
func TestSyncDoctorListsPushFailuresAndUnownedEdits(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	seedNoteCLIResources(t, dbPath)

	stdout, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v (stderr: %s)", err, stderr)
	}
	for _, want := range []string{
		"uid fail-cli@example.com (event): 2 failed push attempt(s); last error: HTTP 500",
		"uid unowned-cli@example.com (event): the edit stays local: " + storage.NoSeriesOwnerMsg,
		"No wedged resources.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output %q misses %q", stdout, want)
		}
	}

	stdout, _, err = runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--output", "json")
	if err != nil {
		t.Fatalf("sync doctor --output json: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	states := map[string]map[string]any{}
	for _, it := range items {
		states[it["state"].(string)] = it
	}
	if strings.Contains(stdout, "stale-cli@example.com") {
		t.Errorf("output %q lists the push failure of an unlinked calendar", stdout)
	}
	if len(items) != 2 {
		t.Fatalf("items = %v, want one push failure and one unowned edit", items)
	}
	if f := states["push_failure"]; f == nil || f["uid"] != "fail-cli@example.com" || f["push_fail_count"] != float64(2) || f["reason"] != "HTTP 500" {
		t.Errorf("push_failure item = %v", f)
	}
	if u := states["unowned"]; u == nil || u["uid"] != "unowned-cli@example.com" || u["reason"] != storage.NoSeriesOwnerMsg {
		t.Errorf("unowned item = %v", u)
	}
}

// TestSyncDoctorListsLegacyOverride checks that the doctor lists a legacy
// override that the startup heal keeps, with the command that moves it home
// (issue #862). The row is in a linked calendar, so the heal does not move
// it. After the move that the hint names, the doctor lists nothing.
func TestSyncDoctorListsLegacyOverride(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	ctx := context.Background()
	var rowID int64
	for _, stmt := range []string{
		`INSERT INTO accounts (id, name, server_url, auth_type, username) VALUES (902, 'legacy-acct', 'https://example.com', 'basic', 'u')`,
		`INSERT INTO calendars (name, account_id, remote_url) VALUES ('Linked', 902, '/linked/')`,
		`INSERT INTO calendars (name) VALUES ('Home')`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_rule)
		 SELECT 'legacy-cli@example.com', id, 't', '2026-05-11T09:00:00Z', '2026-05-11T10:00:00Z', 'FREQ=WEEKLY' FROM calendars WHERE name = 'Home'`,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id)
		 SELECT 'legacy-cli@example.com', id, 't', '2026-05-18T09:30:00Z', '2026-05-18T10:30:00Z', '2026-05-18T09:00:00Z' FROM calendars WHERE name = 'Linked'`,
	} {
		if _, err := a.DB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err := a.DB.QueryRowContext(ctx, `SELECT id FROM events WHERE recurrence_id != '' AND uid = 'legacy-cli@example.com'`).Scan(&rowID); err != nil {
		t.Fatalf("read row id: %v", err)
	}
	a.Close()

	stdout, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v (stderr: %s)", err, stderr)
	}
	want := fmt.Sprintf(`calendar "Linked" uid legacy-cli@example.com (event): row %d is an override with no master in this calendar; the series in calendar "Home" owns it. Move it there with: chroncal event update %d --calendar "Home"`, rowID, rowID)
	if !strings.Contains(stdout, want) {
		t.Errorf("output %q misses %q", stdout, want)
	}

	stdout, _, err = runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--output", "json")
	if err != nil {
		t.Fatalf("sync doctor --output json: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if len(items) != 1 || items[0]["state"] != "legacy_override" || items[0]["id"] != float64(rowID) ||
		items[0]["owner_calendar_name"] != "Home" || items[0]["calendar_name"] != "Linked" {
		t.Fatalf("items = %v, want one legacy_override item", items)
	}

	if _, stderr, err := runChroncalCommand(t, "event", "update", fmt.Sprint(rowID), "--calendar", "Home"); err != nil {
		t.Fatalf("event update: %v (stderr: %s)", err, stderr)
	}
	stdout, _, err = runChroncalCommand(t, "sync", "--allow-plaintext", "doctor", "--output", "json")
	if err != nil {
		t.Fatalf("sync doctor after the move: %v", err)
	}
	if strings.Contains(stdout, "legacy_override") {
		t.Errorf("output %q still lists the legacy override after the move", stdout)
	}
}
