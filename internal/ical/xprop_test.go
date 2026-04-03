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

func TestXPropertyRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("event", func(t *testing.T) {
		t.Parallel()
		original := event.Event{
			UID:       "xprop-event-1",
			Title:     "X-Prop Event",
			StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
			Status:    "CONFIRMED",
			Transp:    "OPAQUE",
			CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
			XProperties: []model.XProperty{
				{Name: "X-GOOGLE-CONFERENCE", Value: "https://meet.google.com/abc-def", Params: "{}"},
				{Name: "X-APPLE-TRAVEL-DURATION", Value: "PT30M", Params: `{"X-APPLE-TRAVEL-DURATION-TYPE":["DRIVING"]}`},
				{Name: "X-CUSTOM-FOO", Value: "bar-baz", Params: `{"LANGUAGE":["en"]}`},
			},
		}

		data, err := ExportEvents([]event.Event{original}, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}

		result, err := ImportFile(strings.NewReader(string(data)))
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if len(result.Events) != 1 {
			t.Fatalf("reimported %d events, want 1", len(result.Events))
		}

		got := result.Events[0]
		if len(got.XProperties) != len(original.XProperties) {
			t.Fatalf("XProperties count: got %d, want %d", len(got.XProperties), len(original.XProperties))
		}

		xmap := make(map[string]model.XProperty)
		for _, xp := range got.XProperties {
			xmap[xp.Name] = xp
		}

		for _, want := range original.XProperties {
			gotXP, ok := xmap[want.Name]
			if !ok {
				t.Errorf("missing X-property %q", want.Name)
				continue
			}
			if gotXP.Value != want.Value {
				t.Errorf("%s value: got %q, want %q", want.Name, gotXP.Value, want.Value)
			}
		}

		icsStr := string(data)
		if !strings.Contains(icsStr, "X-GOOGLE-CONFERENCE") {
			t.Error("exported iCal missing X-GOOGLE-CONFERENCE")
		}
		if !strings.Contains(icsStr, "X-APPLE-TRAVEL-DURATION") {
			t.Error("exported iCal missing X-APPLE-TRAVEL-DURATION")
		}
		if !strings.Contains(icsStr, "X-CUSTOM-FOO") {
			t.Error("exported iCal missing X-CUSTOM-FOO")
		}
	})

	t.Run("todo", func(t *testing.T) {
		t.Parallel()
		original := todo.Todo{
			UID:       "xprop-todo-1",
			Summary:   "X-Prop Todo",
			Status:    "NEEDS-ACTION",
			CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
			XProperties: []model.XProperty{
				{Name: "X-GOOGLE-CONFERENCE", Value: "https://meet.google.com/xyz", Params: "{}"},
				{Name: "X-CUSTOM-PRIORITY", Value: "critical", Params: "{}"},
			},
		}

		data, err := ExportTodos([]todo.Todo{original}, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}

		result, err := ImportFile(strings.NewReader(string(data)))
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if len(result.Todos) != 1 {
			t.Fatalf("reimported %d todos, want 1", len(result.Todos))
		}

		got := result.Todos[0]
		if len(got.XProperties) != len(original.XProperties) {
			t.Fatalf("XProperties count: got %d, want %d", len(got.XProperties), len(original.XProperties))
		}
	})

	t.Run("journal", func(t *testing.T) {
		t.Parallel()
		original := journal.Journal{
			UID:       "xprop-journal-1",
			Summary:   "X-Prop Journal",
			Status:    "FINAL",
			CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
			XProperties: []model.XProperty{
				{Name: "X-NC-GROUP-ID", Value: "group-42", Params: "{}"},
			},
		}

		data, err := ExportJournals([]journal.Journal{original}, "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}

		result, err := ImportFile(strings.NewReader(string(data)))
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if len(result.Journals) != 1 {
			t.Fatalf("reimported %d journals, want 1", len(result.Journals))
		}

		got := result.Journals[0]
		if len(got.XProperties) != len(original.XProperties) {
			t.Fatalf("XProperties count: got %d, want %d", len(got.XProperties), len(original.XProperties))
		}
		if got.XProperties[0].Name != "X-NC-GROUP-ID" {
			t.Errorf("XProperty name: got %q, want %q", got.XProperties[0].Name, "X-NC-GROUP-ID")
		}
		if got.XProperties[0].Value != "group-42" {
			t.Errorf("XProperty value: got %q, want %q", got.XProperties[0].Value, "group-42")
		}
	})

	t.Run("raw_ics_import", func(t *testing.T) {
		t.Parallel()
		icsData := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//test//EN\r\n" +
			"BEGIN:VEVENT\r\n" +
			"UID:raw-xprop-test\r\n" +
			"DTSTART:20260401T140000Z\r\n" +
			"DTEND:20260401T150000Z\r\n" +
			"SUMMARY:Raw X-Prop Test\r\n" +
			"X-GOOGLE-CONFERENCE:https://meet.google.com/abc-def\r\n" +
			"X-APPLE-TRAVEL-DURATION;X-APPLE-TRAVEL-DURATION-TYPE=DRIVING:PT30M\r\n" +
			"X-CUSTOM-FOO;LANGUAGE=en:bar-baz\r\n" +
			"END:VEVENT\r\n" +
			"END:VCALENDAR\r\n"

		result, err := ImportFile(strings.NewReader(icsData))
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if len(result.Events) != 1 {
			t.Fatalf("imported %d events, want 1", len(result.Events))
		}

		got := result.Events[0]
		if len(got.XProperties) != 3 {
			t.Fatalf("XProperties count: got %d, want 3", len(got.XProperties))
		}

		// Re-export and verify X-properties survive
		data, err := ExportEvents(result.Events, "")
		if err != nil {
			t.Fatalf("re-export: %v", err)
		}
		icsStr := string(data)
		if !strings.Contains(icsStr, "X-GOOGLE-CONFERENCE") {
			t.Error("re-exported iCal missing X-GOOGLE-CONFERENCE")
		}
		if !strings.Contains(icsStr, "X-APPLE-TRAVEL-DURATION") {
			t.Error("re-exported iCal missing X-APPLE-TRAVEL-DURATION")
		}
		if !strings.Contains(icsStr, "X-CUSTOM-FOO") {
			t.Error("re-exported iCal missing X-CUSTOM-FOO")
		}
		if !strings.Contains(icsStr, "bar-baz") {
			t.Error("re-exported iCal missing X-CUSTOM-FOO value")
		}
	})
}
