package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/ical"
)

const skipUnreadableSeedICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//chroncal//test//EN\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:broken-relations@test\r\n" +
	"DTSTART:20260401T100000Z\r\n" +
	"DTEND:20260401T110000Z\r\n" +
	"SUMMARY:Broken relations\r\n" +
	"ATTENDEE;CN=Alice;ROLE=REQ-PARTICIPANT:mailto:alice@example.com\r\n" +
	"BEGIN:VALARM\r\n" +
	"ACTION:DISPLAY\r\n" +
	"TRIGGER:-PT15M\r\n" +
	"DESCRIPTION:Reminder\r\n" +
	"END:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:healthy-record@test\r\n" +
	"DTSTART:20260401T120000Z\r\n" +
	"DTEND:20260401T130000Z\r\n" +
	"SUMMARY:Healthy record\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// TestICalExportSkipUnreadable guards issue #571. One unreadable relation
// table stands in for one SQLITE_BUSY hit from concurrent sync. The default
// export must still abort and write nothing. With --skip-unreadable the
// export keeps every readable relation, lists each incomplete record on
// stderr, carries the caveat inside the file as ";" comment lines, and the
// result imports cleanly again.
func TestICalExportSkipUnreadable(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	icsPath := filepath.Join(t.TempDir(), "seed.ics")
	if err := os.WriteFile(icsPath, []byte(skipUnreadableSeedICS), 0o644); err != nil {
		t.Fatalf("write seed ics: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "ical", "import", icsPath, "--calendar", "Work"); err != nil {
		t.Fatalf("ical import: %v", err)
	}

	// Hide one relation table the way internal/event's hydrate tests do. The
	// reads then fail the way a real I/O error would, for every record.
	func() {
		a, err := app.New(dbPath)
		if err != nil {
			t.Fatalf("app.New: %v", err)
		}
		defer a.Close()
		if _, err := a.DB.ExecContext(context.Background(),
			"ALTER TABLE event_attendees RENAME TO event_attendees_hidden"); err != nil {
			t.Fatalf("hide event_attendees: %v", err)
		}
		t.Cleanup(func() {
			b, berr := app.New(dbPath)
			if berr != nil {
				t.Fatalf("app.New for restore: %v", berr)
			}
			defer b.Close()
			if _, err := b.DB.ExecContext(context.Background(),
				"ALTER TABLE event_attendees_hidden RENAME TO event_attendees"); err != nil {
				t.Fatalf("restore event_attendees: %v", err)
			}
		})
	}()

	outPath := filepath.Join(t.TempDir(), "backup.ics")

	// Default: abort on the first failed relation read, write nothing.
	_, stderr, err := runChroncalCommand(t, "ical", "export", "--calendar", "Work", "--file", outPath)
	if err == nil {
		t.Fatal("default export must fail when a relation read fails")
	}
	if !strings.Contains(stderr, "no file written") || !strings.Contains(stderr, "attendees") {
		t.Fatalf("abort message must name the failure and that no file was written:\n%s", stderr)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("aborted export wrote %s anyway", outPath)
	}

	// Flag mode: continue, write the .ics, mark what went missing.
	_, stderr, err = runChroncalCommand(t, "ical", "export",
		"--calendar", "Work", "--file", outPath, "--skip-unreadable")
	if err != nil {
		t.Fatalf("ical export --skip-unreadable: %v\n%s", err, stderr)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	out := string(raw)

	// The file self-describes without terminal context: comment header lines
	// name the run and every incomplete record with its lost relations.
	if !strings.Contains(out, "\r\n; chroncal wrote this file with --skip-unreadable.\r\n") {
		t.Fatalf("output missing the caveat comment header:\n%.200s", out)
	}
	for _, want := range []string{
		"; event ", "(uid broken-relations@test): attendees",
		"(uid healthy-record@test): attendees",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("caveat header missing %q:\n%s", want, out)
		}
	}

	// The stderr summary names every incomplete record too.
	if !strings.Contains(stderr, "2 record(s) are incomplete:") ||
		!strings.Contains(stderr, "broken-relations@test") ||
		!strings.Contains(stderr, "healthy-record@test") ||
		!strings.Contains(stderr, ": attendees") {
		t.Fatalf("stderr summary incomplete:\n%s", stderr)
	}

	// Only the broken relation is gone: the alarm survived.
	f, err := os.Open(outPath)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer f.Close()
	res, err := ical.ImportFile(f)
	if err != nil {
		t.Fatalf("round-trip ImportFile: %v", err)
	}
	if res.SkippedComponents != 0 || len(res.Warnings) != 0 {
		t.Fatalf("round-trip not clean: skipped=%d warnings=%v",
			res.SkippedComponents, res.Warnings)
	}
	if len(res.Events) != 2 {
		t.Fatalf("Events = %d, want 2", len(res.Events))
	}
	var brokenOK, healthyOK bool
	for _, e := range res.Events {
		switch e.UID {
		case "broken-relations@test":
			if len(e.Attendees) != 0 {
				t.Errorf("broken record kept %d attendees", len(e.Attendees))
			}
			if len(e.Alarms) != 1 {
				t.Errorf("Alarms = %d, want 1; the readable relation must survive", len(e.Alarms))
			}
			brokenOK = true
		case "healthy-record@test":
			if len(e.Attendees) != 0 || len(e.Alarms) != 0 {
				t.Errorf("healthy record grew relations: %+v", e)
			}
			healthyOK = true
		}
	}
	if !brokenOK || !healthyOK {
		t.Fatalf("missing records after round-trip: broken=%v healthy=%v", brokenOK, healthyOK)
	}
}
