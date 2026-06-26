package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	// Embed the timezone database so the re-executed helper subprocess can
	// resolve a fixed non-UTC zone regardless of the host's tzdata.
	_ "time/tzdata"
)

// TestICalExportRangeBoundsUseUTC guards issue #305 on the export path: the CLI
// builds date-range bounds in time.Local, so on a non-UTC host they must be
// normalized to UTC before being compared lexically against the UTC-stored
// start/end strings. The CLI subprocess runs in Etc/GMT+3 (UTC-03:00).
//
// The seeded event ends 2026-04-01T02:00:00Z (= 2026-03-31 23:00 local), before
// the requested window opening 2026-04-01 (= 2026-04-01T03:00:00Z), so it must
// not be exported. With the local-offset bound it lexically out-sorts the UTC
// end string and is wrongly included.
func TestICalExportRangeBoundsUseUTC(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "Etc/GMT+3") // UTC-03:00, no DST

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	ics := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:before-window@test\r\n" +
		"DTSTART:20260401T010000Z\r\n" +
		"DTEND:20260401T020000Z\r\n" +
		"SUMMARY:Before window\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	icsPath := filepath.Join(t.TempDir(), "before.ics")
	if err := os.WriteFile(icsPath, []byte(ics), 0o644); err != nil {
		t.Fatalf("write ics: %v", err)
	}

	if _, _, err := runChroncalCommand(t, "ical", "import", icsPath, "--calendar", "Work"); err != nil {
		t.Fatalf("ical import: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "ical", "export", "--calendar", "Work", "--from", "2026-04-01")
	if err != nil {
		t.Fatalf("ical export: %v", err)
	}

	if strings.Contains(stdout, "Before window") {
		t.Fatalf("export included an event ending before the window; local-offset bound leaked into lexical comparison\noutput:\n%s", stdout)
	}
}
