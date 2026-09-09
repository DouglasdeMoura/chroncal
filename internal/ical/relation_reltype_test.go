package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// RELTYPE is an extensible token in RFC 5545. iCloud sends tokens outside
// PARENT, CHILD, and SIBLING. The import must keep the token as it arrives.
// A map onto a standard token would lose the value on export (issue #768).
const appleRelType = "X-APPLE-SOMETHING"

func TestImport_RelatedToKeepsUnknownRelType(t *testing.T) {
	t.Parallel()
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:reltype-event\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T140000Z\r\n" +
		"DTEND:20260401T150000Z\r\n" +
		"SUMMARY:Event\r\n" +
		"RELATED-TO;RELTYPE=" + appleRelType + ":related-event-uid\r\n" +
		"END:VEVENT\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:reltype-todo\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"SUMMARY:Todo\r\n" +
		"RELATED-TO;RELTYPE=" + appleRelType + ":related-todo-uid\r\n" +
		"END:VTODO\r\n" +
		"BEGIN:VJOURNAL\r\n" +
		"UID:reltype-journal\r\n" +
		"DTSTAMP:20260401T100000Z\r\n" +
		"DTSTART:20260401T090000Z\r\n" +
		"SUMMARY:Journal\r\n" +
		"RELATED-TO;RELTYPE=" + appleRelType + ":related-journal-uid\r\n" +
		"END:VJOURNAL\r\n" +
		"END:VCALENDAR\r\n"

	result, err := ImportFile(strings.NewReader(ics))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Events) != 1 || len(result.Todos) != 1 || len(result.Journals) != 1 {
		t.Fatalf("imported %d events, %d todos, %d journals; want 1 of each",
			len(result.Events), len(result.Todos), len(result.Journals))
	}

	checks := []struct {
		kind      string
		relations []model.Relation
		wantUID   string
	}{
		{"event", result.Events[0].Relations, "related-event-uid"},
		{"todo", result.Todos[0].Relations, "related-todo-uid"},
		{"journal", result.Journals[0].Relations, "related-journal-uid"},
	}
	for _, c := range checks {
		if len(c.relations) != 1 {
			t.Errorf("%s relations = %d, want 1", c.kind, len(c.relations))
			continue
		}
		if c.relations[0].RelType != appleRelType {
			t.Errorf("%s RelType = %q, want %q", c.kind, c.relations[0].RelType, appleRelType)
		}
		if c.relations[0].RelUID != c.wantUID {
			t.Errorf("%s RelUID = %q, want %q", c.kind, c.relations[0].RelUID, c.wantUID)
		}
	}
}

// The export must give the token back unchanged, so a pull and a later push
// carry the same RELATED-TO property.
func TestRoundtrip_UnknownRelTypeSurvivesExport(t *testing.T) {
	t.Parallel()
	relations := []model.Relation{{RelType: appleRelType, RelUID: "related-uid"}}
	want := "RELATED-TO;RELTYPE=" + appleRelType + ":related-uid"

	evtICS, err := ExportEvents([]event.Event{{
		UID:       "reltype-event",
		Title:     "Event",
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Relations: relations,
	}}, "")
	if err != nil {
		t.Fatalf("export events: %v", err)
	}
	todoICS, err := ExportTodos([]todo.Todo{{
		UID:       "reltype-todo",
		Summary:   "Todo",
		Status:    "NEEDS-ACTION",
		Relations: relations,
	}}, "")
	if err != nil {
		t.Fatalf("export todos: %v", err)
	}
	journalICS, err := ExportJournals([]journal.Journal{{
		UID:       "reltype-journal",
		Summary:   "Journal",
		StartDate: "2026-04-01",
		Status:    "FINAL",
		Relations: relations,
	}}, "")
	if err != nil {
		t.Fatalf("export journals: %v", err)
	}

	for kind, data := range map[string][]byte{
		"event":   evtICS,
		"todo":    todoICS,
		"journal": journalICS,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s export missing %q:\n%s", kind, want, data)
		}
	}

	// One full round trip: export, import, and compare the token.
	result, err := ImportFile(strings.NewReader(string(evtICS)))
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Relations) != 1 {
		t.Fatalf("re-imported %d events", len(result.Events))
	}
	if got := result.Events[0].Relations[0].RelType; got != appleRelType {
		t.Errorf("RelType after the round trip = %q, want %q", got, appleRelType)
	}
}
