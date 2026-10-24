package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
)

// The tests of this file guard the messages and the JSON fields of issue
// #888.

// cliSeed adds the calendar Work, then runs each command of cmds.
func cliSeed(t *testing.T, cmds ...[]string) {
	t.Helper()
	if _, stderr, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v (stderr: %s)", err, stderr)
	}
	for _, args := range cmds {
		if _, stderr, err := runChroncalCommand(t, args...); err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
	}
}

// cliRowUID reads the UID of the row id of table.
func cliRowUID(t *testing.T, dbPath, table string, id int64) string {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	var uid string
	if err := a.DB.QueryRowContext(context.Background(), `SELECT uid FROM `+table+` WHERE id = ?`, id).Scan(&uid); err != nil {
		t.Fatalf("read uid: %v", err)
	}
	return uid
}

// TestRestoreOfALiveRowSaysThatItIsNotInTheTrash checks that a restore of a
// live row names the cause. Before the fix it said "not found (may have
// been purged)". A missing row keeps the old message.
func TestRestoreOfALiveRowSaysThatItIsNotInTheTrash(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	cliSeed(t,
		[]string{"event", "add", "Standup", "--calendar", "Work", "--date", "2026-05-18", "--time", "09:00"},
		[]string{"todo", "add", "Ship", "--calendar", "Work"},
		[]string{"journal", "add", "Note", "--calendar", "Work"},
	)
	for _, tc := range []struct{ name, table string }{{"event", "events"}, {"todo", "todos"}, {"journal", "journals"}} {
		t.Run(tc.name, func(t *testing.T) {
			uid := cliRowUID(t, dbPath, tc.table, 1)
			_, stderr, err := runChroncalCommand(t, tc.name, "restore", "1")
			if err == nil || !strings.Contains(stderr, tc.name+" 1 is not in the trash; it is live") {
				t.Errorf("restore by ID: err %v, stderr %q", err, stderr)
			}
			_, stderr, err = runChroncalCommand(t, tc.name, "restore", uid)
			if err == nil || !strings.Contains(stderr, fmt.Sprintf("no %s with uid %q is in the trash; it is live", tc.name, uid)) {
				t.Errorf("restore by UID: err %v, stderr %q", err, stderr)
			}
			_, stderr, err = runChroncalCommand(t, tc.name, "restore", "999")
			if err == nil || !strings.Contains(stderr, tc.name+" 999 not found (may have been purged)") {
				t.Errorf("restore of a missing row: err %v, stderr %q", err, stderr)
			}
		})
	}
}

// TestRestoreByUIDOfALiveSeriesWithAnOccurrenceInTheTrash checks the case
// that the restore by UID cannot take: the series is live, and one
// occurrence is in the trash. The message must not say that nothing is in
// the trash. It sends the user to the restore by ID, which works. A UID
// with a live series in two calendars and nothing in the trash says that
// nothing is in the trash.
func TestRestoreByUIDOfALiveSeriesWithAnOccurrenceInTheTrash(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	cliSeed(t,
		[]string{"event", "add", "Daily", "--calendar", "Work", "--date", "2026-05-18", "--time", "09:00", "--rrule", "FREQ=DAILY;COUNT=3"},
	)
	uid := cliRowUID(t, dbPath, "events", 1)
	if _, stderr, err := runChroncalCommand(t, "event", "update", uid, "--recurrence-id", "2026-05-19T12:00:00Z", "--title", "Moved"); err != nil {
		t.Fatalf("event update: %v (stderr: %s)", err, stderr)
	}
	if _, stderr, err := runChroncalCommand(t, "event", "delete", "2", "--yes"); err != nil {
		t.Fatalf("event delete: %v (stderr: %s)", err, stderr)
	}
	want := fmt.Sprintf("event %q is live, and no delete of the UID can come back by UID; restore a row in the trash by its ID", uid)
	if _, stderr, err := runChroncalCommand(t, "event", "restore", uid); err == nil || !strings.Contains(stderr, want) {
		t.Errorf("restore by UID: err %v, stderr %q, want %q", err, stderr, want)
	}
	if _, stderr, err := runChroncalCommand(t, "event", "restore", "2"); err != nil {
		t.Errorf("restore by ID: %v (stderr: %s)", err, stderr)
	}

	// A live series of the UID in two calendars.
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if _, err := a.DB.ExecContext(context.Background(), `INSERT INTO calendars (name) VALUES ('Home')`); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	if _, err := a.DB.ExecContext(context.Background(), `INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_rule)
		SELECT ?, id, 'Copy', '2026-05-18T12:00:00Z', '2026-05-18T13:00:00Z', 'FREQ=DAILY' FROM calendars WHERE name = 'Home'`, uid); err != nil {
		t.Fatalf("insert copy: %v", err)
	}
	a.Close()
	want = fmt.Sprintf("no event with uid %q is in the trash; it is live", uid)
	if _, stderr, err := runChroncalCommand(t, "event", "restore", uid); err == nil || !strings.Contains(stderr, want) {
		t.Errorf("restore by UID in two calendars: err %v, stderr %q, want %q", err, stderr, want)
	}
}

// TestListJSONInstanceTimeIsCanonical checks two edge cases of
// instance_time. An override with a date-only recurrence ID gives the UTC
// RFC 3339 form, like a generated occurrence. A master with an RRULE that
// does not parse is no occurrence, so it has no instance_time.
func TestListJSONInstanceTimeIsCanonical(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	cliSeed(t,
		[]string{"todo", "add", "Chore", "--calendar", "Work", "--due", "2026-05-18", "--rrule", "FREQ=DAILY;COUNT=3"},
		[]string{"event", "add", "Broken", "--calendar", "Work", "--date", "2026-05-18", "--time", "09:00", "--rrule", "FREQ=DAILY;COUNT=3"},
	)
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO todos (uid, calendar_id, summary, due_date, recurrence_id)
		 SELECT uid, calendar_id, 'Chore moved', '2026-05-19', '2026-05-19' FROM todos WHERE summary = 'Chore'`,
		`UPDATE events SET recurrence_rule = 'NOT-A-RULE' WHERE title = 'Broken'`,
	} {
		if _, err := a.DB.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	a.Close()

	stdout, stderr, err := runChroncalCommand(t, "todo", "list", "--from", "2026-05-17", "--to", "2026-05-22", "--output", "json")
	if err != nil {
		t.Fatalf("todo list: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, `"instance_time": "2026-05-19T00:00:00Z"`) || strings.Contains(stdout, `"instance_time": "2026-05-19"`) {
		t.Errorf("todo list %q: want the override instance_time in UTC RFC 3339", stdout)
	}
	stdout, stderr, err = runChroncalCommand(t, "event", "list", "--from", "2026-05-18", "--to", "2026-05-18", "--output", "json")
	if err != nil {
		t.Fatalf("event list: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, `"title": "Broken"`) || strings.Contains(stdout, "instance_time") {
		t.Errorf("event list %q: want the broken master with no instance_time", stdout)
	}
}

// TestRestoreNotDeletedErrCases checks each branch of the message of a
// restore that brought back no row, with a fake resource.
func TestRestoreNotDeletedErrCases(t *testing.T) {
	readErr := errors.New("disk I/O error")
	for _, tc := range []struct {
		name          string
		id            int64
		uid           string
		deleted       bool
		purgeErr      error
		live, trashed bool
		stateErr      error
		want          string
		wantCode      string
	}{
		{name: "live row", id: 1, want: "todo 1 is not in the trash; it is live", wantCode: "not_found"},
		{name: "row in the trash", id: 1, deleted: true, want: "todo 1 is in the trash, but no delete of it can come back by this ID; see todo list --include-deleted", wantCode: "not_found"},
		{name: "missing row", id: 1, purgeErr: sql.ErrNoRows, want: "todo 1 not found (may have been purged)", wantCode: "not_found"},
		{name: "read error by ID", id: 1, purgeErr: readErr, want: "disk I/O error"},
		{name: "live UID", uid: "u", live: true, want: `no todo with uid "u" is in the trash; it is live`, wantCode: "not_found"},
		{name: "live UID with rows in the trash", uid: "u", live: true, trashed: true, want: `todo "u" is live, and no delete of the UID can come back by UID; restore a row in the trash by its ID`, wantCode: "not_found"},
		{name: "UID with rows in the trash only", uid: "u", trashed: true, want: `no delete of todo uid "u" can come back by UID; restore each row in the trash by its ID (see todo list --include-deleted)`, wantCode: "not_found"},
		{name: "missing UID", uid: "u", want: `todo "u" not found (may have been purged)`, wantCode: "not_found"},
		{name: "read error by UID", uid: "u", stateErr: readErr, want: "disk I/O error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resource{
				name: "todo",
				purgeCandidate: func(context.Context, *app.App, int64) (row, bool, error) {
					return row{}, tc.deleted, tc.purgeErr
				},
				uidState: func(context.Context, *app.App, string) (bool, bool, error) {
					return tc.live, tc.trashed, tc.stateErr
				},
			}
			err := restoreNotDeletedErr(context.Background(), nil, r, tc.id, tc.uid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			var ce *cliError
			code := ""
			if errors.As(err, &ce) {
				code = ce.Code
			}
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// TestSyncResolveOfAnUnknownConflictIsNotFound checks that a resolve of a
// conflict ID with no row names the cause, not the raw SQL error.
func TestSyncResolveOfAnUnknownConflictIsNotFound(t *testing.T) {
	setupCalendarCLITestEnv(t)
	_, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "resolve", "1", "--pick", "local")
	if err == nil || !strings.Contains(stderr, "no sync conflict with ID 1; run chroncal sync conflicts for the list") {
		t.Errorf("err %v, stderr %q", err, stderr)
	}
	if strings.Contains(stderr, "sql:") {
		t.Errorf("stderr %q shows the raw SQL error", stderr)
	}
	_, stderr, _ = runChroncalCommand(t, "sync", "--allow-plaintext", "resolve", "1", "--pick", "local", "--output", "json")
	if !strings.Contains(stderr, `"not_found"`) {
		t.Errorf("JSON stderr %q misses the not_found code", stderr)
	}
}

// TestCalendarGetOfANameSaysThatItTakesAnID checks the error of a calendar
// name where the command takes a numeric ID.
func TestCalendarGetOfANameSaysThatItTakesAnID(t *testing.T) {
	setupCalendarCLITestEnv(t)
	for _, args := range [][]string{{"get", "Work"}, {"delete", "Work", "--yes"}} {
		verb := args[0]
		_, stderr, err := runChroncalCommand(t, append([]string{"calendar"}, args...)...)
		want := fmt.Sprintf(`calendar %s takes a numeric calendar ID, not "Work"; run chroncal calendar list to see the IDs`, verb)
		if err == nil || !strings.Contains(stderr, want) {
			t.Errorf("calendar %s: err %v, stderr %q, want %q", verb, err, stderr, want)
		}
		if strings.Contains(stderr, "strconv") {
			t.Errorf("calendar %s: stderr %q shows the raw parse error", verb, stderr)
		}
	}
}

// TestServiceStatusJSONIsAnObject checks that service status follows
// --output json. Before the fix it printed the text of the service manager.
func TestServiceStatusJSONIsAnObject(t *testing.T) {
	prevRun := serviceStatusRun
	t.Cleanup(func() { serviceStatusRun = prevRun })
	var ran []string
	serviceStatusRun = func(_ context.Context, argv []string) ([]byte, error) {
		ran = argv
		// A shell exit gives a real *exec.ExitError with code 3, the code
		// of systemctl for a unit that is not active.
		_, err := exec.CommandContext(context.Background(), "sh", "-c", "exit 3").CombinedOutput()
		return []byte("inactive (dead)\n"), err
	}
	prevFmt := outputFmt
	outputFmt = "json"
	t.Cleanup(func() { outputFmt = prevFmt })

	root := &cobra.Command{Use: "chroncal-test"}
	root.AddCommand(serviceStatusCmd())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("service status: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q is not a JSON object: %v", out.String(), err)
	}
	if len(ran) == 0 {
		// The OS has no service integration. The object still has the keys.
		if got["message"] == nil {
			t.Errorf("object %v misses the message", got)
		}
		return
	}
	if got["output"] != "inactive (dead)\n" || got["exit_code"] != float64(3) || got["command"] == nil || got["os"] == nil {
		t.Errorf("object = %v", got)
	}
}

// TestListIncludeDeletedMarksTheTrash checks that a list with
// --include-deleted marks the rows in the trash, in the text and the JSON.
func TestListIncludeDeletedMarksTheTrash(t *testing.T) {
	setupCalendarCLITestEnv(t)
	cliSeed(t,
		[]string{"event", "add", "Live", "--calendar", "Work", "--date", "2026-05-18", "--time", "09:00"},
		[]string{"event", "add", "Gone", "--calendar", "Work", "--date", "2026-05-18", "--time", "11:00"},
		[]string{"event", "delete", "2", "--yes"},
		[]string{"todo", "add", "Live", "--calendar", "Work"},
		[]string{"todo", "add", "Gone", "--calendar", "Work"},
		[]string{"todo", "delete", "2", "--yes"},
		[]string{"journal", "add", "Live", "--calendar", "Work"},
		[]string{"journal", "add", "Gone", "--calendar", "Work"},
		[]string{"journal", "delete", "2", "--yes"},
	)
	window := map[string][]string{"event": {"--from", "2026-05-18", "--to", "2026-05-19"}}
	for _, name := range []string{"event", "todo", "journal"} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{name, "list", "--include-deleted"}, window[name]...)
			for _, extra := range [][]string{nil, {"--compact"}} {
				stdout, stderr, err := runChroncalCommand(t, append(append([]string{}, args...), extra...)...)
				if err != nil {
					t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
				}
				if !strings.Contains(stdout, "[trash] Gone") || strings.Contains(stdout, "[trash] Live") {
					t.Errorf("%v %v: output %q does not mark only the row in the trash", args, extra, stdout)
				}
			}
			stdout, stderr, err := runChroncalCommand(t, append(args, "--output", "json")...)
			if err != nil {
				t.Fatalf("%v --output json: %v (stderr: %s)", args, err, stderr)
			}
			var items []map[string]any
			if err := json.Unmarshal([]byte(stdout), &items); err != nil {
				t.Fatalf("decode %q: %v", stdout, err)
			}
			marked := map[string]bool{}
			for _, it := range items {
				title, _ := it["title"].(string)
				if title == "" {
					title, _ = it["summary"].(string)
				}
				_, has := it["deleted_at"]
				marked[title] = has
			}
			if len(marked) != 2 || !marked["Gone"] || marked["Live"] {
				t.Errorf("deleted_at marks = %v, want only Gone", marked)
			}
		})
	}
}

// TestListJSONNamesTheOccurrence checks that each expanded occurrence in
// the JSON of event list and todo list has instance_time. The value is the
// recurrence ID of the occurrence, so a script can pass it to
// --recurrence-id.
func TestListJSONNamesTheOccurrence(t *testing.T) {
	setupCalendarCLITestEnv(t)
	cliSeed(t,
		[]string{"event", "add", "Daily", "--calendar", "Work", "--date", "2026-05-18", "--time", "09:00", "--rrule", "FREQ=DAILY;COUNT=3"},
		[]string{"event", "add", "Once", "--calendar", "Work", "--date", "2026-05-18", "--time", "12:00"},
		[]string{"todo", "add", "Chore", "--calendar", "Work", "--due", "2026-05-18", "--rrule", "FREQ=DAILY;COUNT=3"},
		[]string{"journal", "add", "Diary", "--calendar", "Work", "--date", "2026-05-18", "--rrule", "FREQ=DAILY;COUNT=3"},
	)
	for _, tc := range []struct {
		name, titleKey string
		args           []string
	}{
		{"event", "title", []string{"event", "list", "--from", "2026-05-18", "--to", "2026-05-21", "--output", "json"}},
		{"todo", "summary", []string{"todo", "list", "--from", "2026-05-17", "--to", "2026-05-22", "--output", "json"}},
		{"journal", "summary", []string{"journal", "list", "--from", "2026-05-17", "--to", "2026-05-22", "--output", "json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := runChroncalCommand(t, tc.args...)
			if err != nil {
				t.Fatalf("%v: %v (stderr: %s)", tc.args, err, stderr)
			}
			var items []map[string]any
			if err := json.Unmarshal([]byte(stdout), &items); err != nil {
				t.Fatalf("decode %q: %v", stdout, err)
			}
			seen := map[string]bool{}
			for _, it := range items {
				instance, has := it["instance_time"].(string)
				switch it[tc.titleKey] {
				case "Once":
					if has {
						t.Errorf("single event %v has instance_time", it)
					}
				default:
					if !has || seen[instance] {
						t.Errorf("occurrence %v has no instance_time of its own", it)
					}
					seen[instance] = true
				}
			}
			if len(seen) < 2 {
				t.Errorf("items %v: want at least two occurrences with instance_time", items)
			}
		})
	}
	// The instance time of an occurrence names it for an edit.
	stdout, _, err := runChroncalCommand(t, "event", "list", "--from", "2026-05-19", "--to", "2026-05-19", "--output", "json")
	if err != nil {
		t.Fatalf("event list: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil || len(items) != 1 {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	instance := items[0]["instance_time"].(string)
	if _, stderr, err := runChroncalCommand(t, "event", "update", items[0]["uid"].(string), "--recurrence-id", instance, "--title", "Moved"); err != nil {
		t.Fatalf("event update --recurrence-id %s: %v (stderr: %s)", instance, err, stderr)
	}
	stdout, _, _ = runChroncalCommand(t, "event", "list", "--from", "2026-05-19", "--to", "2026-05-19", "--output", "json")
	if !strings.Contains(stdout, `"title": "Moved"`) || !strings.Contains(stdout, `"instance_time": "`+instance+`"`) {
		t.Errorf("override %q misses the title or the instance_time %s", stdout, instance)
	}
}
