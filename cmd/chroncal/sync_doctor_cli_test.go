package main

import (
	"context"
	"encoding/json"
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

	stdout, _, err := runChroncalCommand(t, "sync", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v", err)
	}
	if !strings.Contains(stdout, "No wedged resources.") {
		t.Errorf("output %q misses the empty report", stdout)
	}
}

func TestSyncDoctorJSONEmptyIsArray(t *testing.T) {
	setupCalendarCLITestEnv(t)

	stdout, _, err := runChroncalCommand(t, "sync", "doctor", "--output", "json")
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

	stdout, stderr, err := runChroncalCommand(t, "sync", "doctor")
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
	stdout, _, err = runChroncalCommand(t, "sync", "doctor")
	if err != nil {
		t.Fatalf("sync doctor after bumps: %v", err)
	}
	if !strings.Contains(stdout, "3 failed push attempt(s)") {
		t.Errorf("output %q misses the recorded push-failure count", stdout)
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

	stdout, _, err := runChroncalCommand(t, "sync", "doctor", "--output", "json")
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
	_, _, err := runChroncalCommand(t, "sync", "doctor", "--push", "wedge-cli@example.com")
	if err == nil {
		t.Fatal("expected refusal without --yes in a non-interactive shell")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("refusal %q does not mention --yes", err.Error())
	}
}

func TestSyncDoctorPushUnknownUIDIsNotFound(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t, "sync", "doctor", "--push", "missing@example.com", "--yes", "--output", "json")
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
