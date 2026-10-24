package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTodoICSDir writes a directory with one .ics file that holds one
// VTODO.
func writeTodoICSDir(t *testing.T, uid, summary string) string {
	t.Helper()
	dir := filepath.Join(tempDir(t), "collection")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VTODO\r\nUID:" + uid + "\r\nSUMMARY:" + summary + "\r\nDUE;VALUE=DATE:20261110\r\nEND:VTODO\r\n" +
		"END:VCALENDAR\r\n"
	if err := os.WriteFile(filepath.Join(dir, uid+".ics"), []byte(ics), 0o600); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	return dir
}

// TestIcalImportOfTwoDirectoriesWithOneTodoExitsZero imports two local
// directories that hold the same todo UID (issue #882). A todo UID cannot be
// in two calendars, so the first calendar keeps the todo and the import
// of the second one skips it with a warning. Each batch run succeeds and
// changes nothing, so a timer job does not fail and the todo stays put.
func TestIcalImportOfTwoDirectoriesWithOneTodoExitsZero(t *testing.T) {
	setupCalendarCLITestEnv(t)
	first := writeTodoICSDir(t, "shared-todo", "Shared")
	second := writeTodoICSDir(t, "shared-todo", "Shared")
	for _, args := range [][]string{
		{"calendar", "create", "First", "--path", first},
		{"calendar", "create", "Second", "--path", second},
	} {
		if _, _, err := runChroncalCommand(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	homes := make([]string, 0, 3)
	for run := range 3 {
		stdout, stderr, err := runChroncalCommand(t, "ical", "import")
		if err != nil {
			t.Fatalf("run %d: ical import: %v", run, err)
		}
		if !strings.Contains(stdout, "Skipped 1 item(s) that live in another calendar.") {
			t.Errorf("run %d: output lacks the skip:\n%s", run, stdout)
		}
		if !strings.Contains(stderr, `skipped: shared-todo lives in calendar "First", not in calendar "Second"`) {
			t.Errorf("run %d: warnings lack the skip:\n%s", run, stderr)
		}
		out, _, err := runChroncalCommand(t, "todo", "list", "--all", "-o", "json")
		if err != nil {
			t.Fatalf("run %d: todo list: %v", run, err)
		}
		var todos []struct {
			UID        string `json:"uid"`
			CalendarID int64  `json:"calendar_id"`
		}
		if err := json.Unmarshal([]byte(out), &todos); err != nil || len(todos) != 1 {
			t.Fatalf("run %d: todo list = %s, %v; want one todo", run, out, err)
		}
		homes = append(homes, fmt.Sprint(todos[0].CalendarID))
	}
	// The todo never leaves the first calendar.
	if homes[0] != homes[1] || homes[1] != homes[2] {
		t.Errorf("calendar of the todo per run = %v, want no change", homes)
	}
}
