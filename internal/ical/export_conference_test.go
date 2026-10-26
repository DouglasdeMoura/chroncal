package ical

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestExportEvents_ConferenceURI covers issue #934. The export of an event
// with a CONFERENCE URI panicked on a nil Params map. The URI must also
// survive an import of the export.
func TestExportEvents_ConferenceURI(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	out, err := ExportEvents([]event.Event{{
		UID: "call", Title: "Call", StartTime: start, EndTime: start.Add(time.Hour),
		ConferenceURI: "https://meet.example.com/x",
	}}, "")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	if !strings.Contains(string(out), "CONFERENCE;VALUE=URI:https://meet.example.com/x") {
		t.Fatalf("export lacks the CONFERENCE property:\n%s", out)
	}
	res, err := ImportFile(bytes.NewReader(out))
	if err != nil || len(res.Events) != 1 || res.Events[0].ConferenceURI != "https://meet.example.com/x" {
		t.Fatalf("import of the export = %+v, %v; want the conference URI back", res.Events, err)
	}
}
