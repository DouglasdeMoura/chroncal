package ical

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func sampleExportForComments(t *testing.T) []byte {
	t.Helper()
	data, err := ExportEvents([]event.Event{{
		UID:       "caveat-1",
		Title:     "Has relations",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}}, "")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	return data
}

// The --skip-unreadable caveat must sit inside the VCALENDAR as ";" comment
// lines, right after the opening line, so the file self-describes later.
func TestPrependComments_InsertsAfterCalendarBegin(t *testing.T) {
	t.Parallel()
	data := PrependComments(sampleExportForComments(t), []string{"first note", "second note"})

	s := string(data)
	// The encoder sorts the calendar properties alphabetically, so only pin
	// the comments to the opening line and check VERSION separately.
	if !strings.HasPrefix(s, "BEGIN:VCALENDAR\r\n; first note\r\n; second note\r\n") {
		t.Fatalf("comments not inserted right after BEGIN:VCALENDAR:\n%.120s", s)
	}
	if !strings.Contains(s, "VERSION:2.0") || !strings.Contains(s, "UID:caveat-1") {
		t.Fatalf("comment insertion damaged the calendar:\n%s", s)
	}
}

// A CR or LF inside a comment would split one entry into two physical lines
// and desynchronize the header. Each entry stays one line.
func TestPrependComments_SanitizesLineBreaks(t *testing.T) {
	t.Parallel()
	data := PrependComments(sampleExportForComments(t), []string{"one\r\ntwo"})
	for _, line := range strings.Split(string(data), "\r\n") {
		if strings.HasPrefix(line, "; ") && strings.Contains(line[2:], "\n") {
			t.Fatalf("comment kept an embedded newline: %q", line)
		}
	}
	if !strings.Contains(string(data), "; one  two\r\n") {
		t.Fatalf("sanitized comment missing:\n%s", string(data))
	}
}

// Non-calendar input has no anchor line; leave it untouched rather than
// corrupt it.
func TestPrependComments_NoCalendarUnchanged(t *testing.T) {
	t.Parallel()
	data := []byte("not a calendar")
	got := PrependComments(data, []string{"note"})
	if string(got) != "not a calendar" {
		t.Fatalf("data without VCALENDAR changed: %q", got)
	}
}

// go-ical's decoder rejects a ";" line ("empty property name"). chroncal's
// own --skip-unreadable output carries such lines, so the importer strips
// them before the decoder sees the stream.
func TestImportFile_AcceptsCommentLines(t *testing.T) {
	t.Parallel()
	body := sampleExportForComments(t)
	body = PrependComments(body, []string{
		"chroncal wrote this file with --skip-unreadable.",
		"event 7 (uid caveat-1): attendees",
	})
	res, err := ImportFile(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("ImportFile with comment lines: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].UID != "caveat-1" {
		t.Fatalf("Events = %+v, want one event with uid caveat-1", res.Events)
	}
	if res.SkippedComponents != 0 || len(res.Warnings) != 0 {
		t.Fatalf("comments leaked into parsing: skipped=%d warnings=%v",
			res.SkippedComponents, res.Warnings)
	}
}

// Full round-trip sanity: an exported file with a caveat header still imports
// cleanly through the same path the CLI import command uses.
func TestRoundTrip_ExportWithCaveatHeaderImportsCleanly(t *testing.T) {
	t.Parallel()
	data := sampleExportForComments(t)
	data = PrependComments(data, []string{"event 7 (uid caveat-1): attendees"})

	tmp := t.TempDir() + "/caveat.ics"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	f, err := os.Open(tmp)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer f.Close()
	res, err := ImportFile(f)
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("Events = %d, want 1", len(res.Events))
	}
	e := res.Events[0]
	if e.UID != "caveat-1" || e.Title != "Has relations" {
		t.Fatalf("round-trip changed the record: %+v", e)
	}
	if e.StartTime != (time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("StartTime = %v, want the original instant", e.StartTime)
	}
}
