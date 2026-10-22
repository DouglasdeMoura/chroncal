package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
)

// TestEventDelete_SeriesPromptNamesTheSeries: the series resolver with
// --calendar gives the prompt the title of the series. The title comes from
// the live master, or else from the first orphan row. A series in the trash
// and a UID with no live row in the calendar get their refusal before the
// prompt.
func TestEventDelete_SeriesPromptNamesTheSeries(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")
	uid := seedSameUIDInWorkAndHome(t)
	if stdout, stderr, err := runChroncalCommand(t, "event", "delete", uid, "--series", "--calendar", "Home", "--yes"); err != nil {
		t.Fatalf("series delete in Home: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	const orphan = "orphan-summary-uid"
	ics := filepath.Join(t.TempDir(), "orphan.ics")
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + orphan +
		"\r\nDTSTAMP:20260401T000000Z\r\nRECURRENCE-ID:20260407T090000Z\r\nDTSTART:20260407T100000Z\r\nDTEND:20260407T103000Z\r\n" +
		"SUMMARY:Orphan\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := os.WriteFile(ics, []byte(body), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	if stdout, stderr, err := runChroncalCommand(t, "ical", "import", ics, "--calendar", "Work"); err != nil {
		t.Fatalf("ical import: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	ctx := context.Background()
	for _, tc := range []struct {
		ref, calendar, want, wantErr string
	}{
		{uid, "Work", "Standup", ""},
		{orphan, "Work", "Orphan", ""},
		{uid, "Home", "", "is in the trash in calendar"},
		{"no-such-uid", "Work", "", "has no live row in calendar"},
	} {
		got, err := eventResource.resolveIn(ctx, a, tc.ref, tc.calendar, true)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("resolveIn(%s, %s) err = %v, want %q before the prompt", tc.ref, tc.calendar, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveIn(%s, %s): %v", tc.ref, tc.calendar, err)
			continue
		}
		if got.Summary != tc.want {
			t.Errorf("resolveIn(%s, %s).Summary = %q, want %q", tc.ref, tc.calendar, got.Summary, tc.want)
		}
	}
}
