package ical

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// radicaleURL is the Radicale CalDAV server used for integration tests.
// Set RADICALE_URL to override (e.g. http://localhost:5232).
const defaultRadicaleURL = "http://localhost:5232"

func radicaleURL() string { return defaultRadicaleURL }

// radicaleAvailable checks whether the Radicale server is reachable.
func radicaleAvailable(t *testing.T) {
	t.Helper()
	resp, err := http.Get(radicaleURL() + "/")
	if err != nil {
		t.Skipf("Radicale not available at %s: %v", radicaleURL(), err)
	}
	resp.Body.Close()
}

// radicaleCalendar creates (or reuses) a shared calendar on Radicale and
// returns the collection URL. All tests share one calendar to avoid
// creating too many collections.
func radicaleCalendar(t *testing.T) string {
	t.Helper()
	base := radicaleURL()

	// Ensure user root exists (MKCOL).
	userURL := base + "/qauser/"
	req, _ := http.NewRequest("MKCOL", userURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCOL %s: %v", userURL, err)
	}
	resp.Body.Close()

	// Create calendar (MKCALENDAR).
	calURL := base + "/qauser/qa-integration/"
	req, _ = http.NewRequest("MKCALENDAR", calURL, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCALENDAR: %v", err)
	}
	resp.Body.Close()
	// 201 = created, 409 = already exists (both fine)
	if resp.StatusCode != 201 && resp.StatusCode != 409 {
		t.Fatalf("MKCALENDAR %s: %d", calURL, resp.StatusCode)
	}
	return calURL
}

// radicaleRoundtrip PUTs ics data to Radicale, GETs it back, and returns
// the re-imported ImportResult.
func radicaleRoundtrip(t *testing.T, calURL, filename string, icsData []byte) ImportResult {
	t.Helper()
	itemURL := calURL + filename

	// PUT
	req, _ := http.NewRequest("PUT", itemURL, strings.NewReader(string(icsData)))
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", itemURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 204 {
		t.Fatalf("PUT %s: status %d", itemURL, resp.StatusCode)
	}

	// GET
	resp, err = http.Get(itemURL)
	if err != nil {
		t.Fatalf("GET %s: %v", itemURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", itemURL, resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	t.Logf("=== Radicale returned for %s ===\n%s", filename, string(body))

	result, err := ImportFile(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("re-import %s: %v", filename, err)
	}
	return result
}

// ---------- VEVENT Tests ----------

func TestRadicale_VEVENT_Basic(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:         "rad-vevent-basic",
		Title:       "Basic Event",
		Description: "A simple test event",
		Location:    "Room 42",
		StartTime:   time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 10, 15, 30, 0, 0, time.UTC),
		Status:      "CONFIRMED",
		Transp:      "OPAQUE",
		Sequence:    1,
		Class:       "PUBLIC",
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "basic-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "UID", got.UID, original.UID)
	assertEqual(t, "Title", got.Title, original.Title)
	assertEqual(t, "Description", got.Description, original.Description)
	assertEqual(t, "Location", got.Location, original.Location)
	assertEqual(t, "Status", got.Status, original.Status)
	assertEqual(t, "Transp", got.Transp, original.Transp)
	assertEqualInt(t, "Sequence", got.Sequence, original.Sequence)
	assertTimeEqual(t, "StartTime", got.StartTime, original.StartTime)
	assertTimeEqual(t, "EndTime", got.EndTime, original.EndTime)
}

func TestRadicale_VEVENT_AllDay(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-vevent-allday",
		Title:     "All Day Event",
		StartTime: time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC),
		AllDay:    true,
		Status:    "CONFIRMED",
		Transp:    "TRANSPARENT",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "allday-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if !got.AllDay {
		t.Error("AllDay should be true")
	}
	assertEqual(t, "Title", got.Title, original.Title)
}

func TestRadicale_VEVENT_Recurring(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:            "rad-vevent-recurring",
		Title:          "Weekly Standup",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 6, 9, 30, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10;BYDAY=MO",
		ExDates:        "2026-04-13T09:00:00Z",
		Status:         "CONFIRMED",
		Transp:         "OPAQUE",
		DtStamp:        "2026-04-01T00:00:00Z",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "recurring-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "RRULE", got.RecurrenceRule, original.RecurrenceRule)
	if got.ExDates == "" {
		t.Error("ExDates should not be empty")
	}
}

func TestRadicale_VEVENT_WithTimezone(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-vevent-tz",
		Title:     "NYC Meeting",
		StartTime: time.Date(2026, 4, 10, 18, 0, 0, 0, time.UTC), // 14:00 EDT
		EndTime:   time.Date(2026, 4, 10, 19, 0, 0, 0, time.UTC),
		Timezone:  "America/New_York",
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "tz-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "Timezone", got.Timezone, "America/New_York")
	assertTimeEqual(t, "StartTime", got.StartTime, original.StartTime)
}

func TestRadicale_VEVENT_WithDuration(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:           "rad-vevent-duration",
		Title:         "Duration Event",
		StartTime:     time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 4, 10, 11, 30, 0, 0, time.UTC),
		DurationValue: "PT1H30M",
		Status:        "CONFIRMED",
		Transp:        "OPAQUE",
		DtStamp:       "2026-04-01T00:00:00Z",
		CreatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "duration-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "DurationValue", got.DurationValue, "PT1H30M")
}

func TestRadicale_VEVENT_WithAttendees(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-vevent-attendees",
		Title:     "Team Meeting",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Attendees: []model.Attendee{
			{
				Email:      "organizer@example.com",
				Name:       "Organizer",
				RSVPStatus: "ACCEPTED",
				Role:       "CHAIR",
				Organizer:  true,
			},
			{
				Email:      "alice@example.com",
				Name:       "Alice",
				RSVPStatus: "ACCEPTED",
				Role:       "REQ-PARTICIPANT",
			},
			{
				Email:      "bob@example.com",
				Name:       "Bob",
				RSVPStatus: "TENTATIVE",
				Role:       "OPT-PARTICIPANT",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "attendees-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	// At minimum we should get the organizer + 2 attendees back
	if len(got.Attendees) < 3 {
		t.Errorf("expected at least 3 attendees, got %d", len(got.Attendees))
	}
}

func TestRadicale_VEVENT_WithCommentsContactsRelations(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:        "rad-vevent-extras",
		Title:      "Event with extras",
		StartTime:  time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:     "CONFIRMED",
		Transp:     "OPAQUE",
		Priority:   5,
		URL:        "https://example.com/meeting",
		Categories: "work,meeting",
		Geo:        "37.386013;-122.082932",
		DtStamp:    "2026-04-01T00:00:00Z",
		CreatedAt:  time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Comments:   []string{"First comment", "Second comment"},
		Contacts:   []string{"John Doe <john@example.com>"},
		Resources:  []string{"Projector", "Whiteboard"},
		Relations: []model.Relation{
			{RelType: "PARENT", RelUID: "parent-uid-123"},
			{RelType: "SIBLING", RelUID: "sibling-uid-456"},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "extras-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqualInt(t, "Priority", got.Priority, int64(5))
	assertEqual(t, "URL", got.URL, original.URL)
	assertEqual(t, "Categories", got.Categories, original.Categories)
	assertEqual(t, "Geo", got.Geo, original.Geo)

	if len(got.Comments) != 2 {
		t.Errorf("expected 2 comments, got %d: %v", len(got.Comments), got.Comments)
	}
	if len(got.Contacts) != 1 {
		t.Errorf("expected 1 contact, got %d", len(got.Contacts))
	}
	if len(got.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(got.Resources), got.Resources)
	}
	if len(got.Relations) != 2 {
		t.Errorf("expected 2 relations, got %d", len(got.Relations))
	}
}

func TestRadicale_VEVENT_WithXProperties(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-vevent-xprops",
		Title:     "Event with X-props",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		XProperties: []model.XProperty{
			{Name: "X-CUSTOM-FIELD", Value: "custom-value", Params: "{}"},
			{Name: "X-APPLE-STRUCTURED-LOCATION", Value: "geo:37.33,-122.03", Params: "{}"},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "xprops-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	xpropMap := make(map[string]string)
	for _, xp := range got.XProperties {
		xpropMap[xp.Name] = xp.Value
	}
	if xpropMap["X-CUSTOM-FIELD"] != "custom-value" {
		t.Errorf("X-CUSTOM-FIELD: got %q, want %q", xpropMap["X-CUSTOM-FIELD"], "custom-value")
	}
}

// ---------- VTODO Tests ----------

func TestRadicale_VTODO_Basic(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:         "rad-vtodo-basic",
		Summary:     "Basic Todo",
		Description: "A simple test todo",
		Location:    "Home",
		DueDate:     "2026-04-15T17:00:00Z",
		Status:      "NEEDS-ACTION",
		Priority:    3,
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "basic-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "UID", got.UID, original.UID)
	assertEqual(t, "Summary", got.Summary, original.Summary)
	assertEqual(t, "Description", got.Description, original.Description)
	assertEqual(t, "Location", got.Location, original.Location)
	assertEqual(t, "Status", got.Status, original.Status)
	assertEqualInt(t, "Priority", got.Priority, original.Priority)
}

func TestRadicale_VTODO_DateOnly(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:       "rad-vtodo-dateonly",
		Summary:   "Date-only Todo",
		DueDate:   "2026-04-20",
		StartDate: "2026-04-10",
		Status:    "NEEDS-ACTION",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "dateonly-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "DueDate", got.DueDate, "2026-04-20")
	assertEqual(t, "StartDate", got.StartDate, "2026-04-10")
}

func TestRadicale_VTODO_Completed(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:             "rad-vtodo-completed",
		Summary:         "Completed Todo",
		DueDate:         "2026-04-10T12:00:00Z",
		CompletedAt:     "2026-04-09T15:30:00Z",
		PercentComplete: 100,
		Status:          "COMPLETED",
		DtStamp:         "2026-04-01T00:00:00Z",
		CreatedAt:       time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "completed-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "Status", got.Status, "COMPLETED")
	assertEqualInt(t, "PercentComplete", got.PercentComplete, int64(100))
	if got.CompletedAt == "" {
		t.Error("CompletedAt should not be empty")
	}
}

func TestRadicale_VTODO_WithDuration(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:       "rad-vtodo-duration",
		Summary:   "Duration Todo",
		StartDate: "2026-04-10T09:00:00Z",
		Duration:  "PT2H",
		Status:    "IN-PROCESS",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "duration-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "Duration", got.Duration, "PT2H")
}

func TestRadicale_VTODO_Recurring(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:            "rad-vtodo-recurring",
		Summary:        "Weekly Review",
		DueDate:        "2026-04-10T17:00:00Z",
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
		Status:         "NEEDS-ACTION",
		DtStamp:        "2026-04-01T00:00:00Z",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "recurring-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "RecurrenceRule", got.RecurrenceRule, original.RecurrenceRule)
}

func TestRadicale_VTODO_WithCategories(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:        "rad-vtodo-cats",
		Summary:    "Categorized Todo",
		DueDate:    "2026-04-15",
		Status:     "NEEDS-ACTION",
		Categories: "work,urgent,project-x",
		Class:      "CONFIDENTIAL",
		URL:        "https://example.com/task/123",
		DtStamp:    "2026-04-01T00:00:00Z",
		CreatedAt:  time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "cats-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "Categories", got.Categories, original.Categories)
	assertEqual(t, "Class", got.Class, original.Class)
	assertEqual(t, "URL", got.URL, original.URL)
}

// ---------- VJOURNAL Tests ----------

func TestRadicale_VJOURNAL_Basic(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := journal.Journal{
		UID:         "rad-vjournal-basic",
		Summary:     "Daily Log",
		Description: "Today I worked on the CalDAV sync implementation.",
		StartDate:   "2026-04-10T08:00:00Z",
		Status:      "FINAL",
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportJournals([]journal.Journal{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "basic-journal.ics", data)
	if len(result.Journals) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(result.Journals))
	}

	got := result.Journals[0]
	assertEqual(t, "UID", got.UID, original.UID)
	assertEqual(t, "Summary", got.Summary, original.Summary)
	assertEqual(t, "Description", got.Description, original.Description)
	assertEqual(t, "Status", got.Status, original.Status)
}

func TestRadicale_VJOURNAL_DateOnly(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := journal.Journal{
		UID:         "rad-vjournal-dateonly",
		Summary:     "Date-only Journal",
		Description: "Just a date.",
		StartDate:   "2026-04-10",
		Status:      "FINAL",
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportJournals([]journal.Journal{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "dateonly-journal.ics", data)
	if len(result.Journals) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(result.Journals))
	}

	got := result.Journals[0]
	assertEqual(t, "StartDate", got.StartDate, "2026-04-10")
}

func TestRadicale_VJOURNAL_WithCategories(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := journal.Journal{
		UID:         "rad-vjournal-cats",
		Summary:     "Categorized Journal",
		Description: "Journal with categories",
		StartDate:   "2026-04-10T08:00:00Z",
		Status:      "DRAFT",
		Categories:  "dev,notes",
		Class:       "PRIVATE",
		URL:         "https://example.com/journal/1",
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportJournals([]journal.Journal{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "cats-journal.ics", data)
	if len(result.Journals) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(result.Journals))
	}

	got := result.Journals[0]
	assertEqual(t, "Categories", got.Categories, original.Categories)
	assertEqual(t, "Class", got.Class, original.Class)
	assertEqual(t, "URL", got.URL, original.URL)
	assertEqual(t, "Status", got.Status, "DRAFT")
}

func TestRadicale_VJOURNAL_Recurring(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := journal.Journal{
		UID:            "rad-vjournal-recurring",
		Summary:        "Weekly Summary",
		Description:    "Recurring journal",
		StartDate:      "2026-04-10T08:00:00Z",
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
		Status:         "FINAL",
		DtStamp:        "2026-04-01T00:00:00Z",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportJournals([]journal.Journal{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "recurring-journal.ics", data)
	if len(result.Journals) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(result.Journals))
	}

	got := result.Journals[0]
	assertEqual(t, "RecurrenceRule", got.RecurrenceRule, original.RecurrenceRule)
}

func TestRadicale_VJOURNAL_WithComments(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := journal.Journal{
		UID:         "rad-vjournal-comments",
		Summary:     "Journal with comments",
		Description: "Main description",
		StartDate:   "2026-04-10T08:00:00Z",
		Status:      "FINAL",
		Comments:    []string{"First comment", "Second comment"},
		Contacts:    []string{"Jane Smith <jane@example.com>"},
		Relations: []model.Relation{
			{RelType: "PARENT", RelUID: "parent-journal-001"},
		},
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportJournals([]journal.Journal{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "comments-journal.ics", data)
	if len(result.Journals) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(result.Journals))
	}

	got := result.Journals[0]
	if len(got.Comments) != 2 {
		t.Errorf("expected 2 comments, got %d: %v", len(got.Comments), got.Comments)
	}
	if len(got.Contacts) != 1 {
		t.Errorf("expected 1 contact, got %d", len(got.Contacts))
	}
	if len(got.Relations) != 1 {
		t.Errorf("expected 1 relation, got %d", len(got.Relations))
	}
}

// ---------- VALARM Tests ----------

func TestRadicale_VALARM_DisplayOnEvent(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-display",
		Title:     "Event with DISPLAY alarm",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT15M",
				Description:  "Meeting in 15 minutes",
				Related:      "START",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "display-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	alarm := got.Alarms[0]
	assertEqual(t, "Action", alarm.Action, "DISPLAY")
	assertEqual(t, "TriggerValue", alarm.TriggerValue, "-PT15M")
	assertEqual(t, "Description", alarm.Description, "Meeting in 15 minutes")
}

func TestRadicale_VALARM_RelatedEnd(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-related-end",
		Title:     "Event with END-relative alarm",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "PT0S",
				Description:  "Event ending now",
				Related:      "END",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "end-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	assertEqual(t, "Related", got.Alarms[0].Related, "END")
}

func TestRadicale_VALARM_EmailOnEvent(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-email",
		Title:     "Event with EMAIL alarm",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "EMAIL",
				TriggerValue: "-PT30M",
				Summary:      "Meeting Reminder",
				Description:  "Your meeting starts in 30 minutes",
				Related:      "START",
				Attendees: []model.AlarmAttendee{
					{Email: "notify@example.com", Name: "Notifier"},
				},
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "email-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	alarm := got.Alarms[0]
	assertEqual(t, "Action", alarm.Action, "EMAIL")
	assertEqual(t, "Summary", alarm.Summary, "Meeting Reminder")
	assertEqual(t, "Description", alarm.Description, "Your meeting starts in 30 minutes")
	if len(alarm.Attendees) != 1 {
		t.Errorf("expected 1 alarm attendee, got %d", len(alarm.Attendees))
	}
}

func TestRadicale_VALARM_AudioWithAttach(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-audio",
		Title:     "Event with AUDIO alarm",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:        "AUDIO",
				TriggerValue:  "-PT5M",
				Related:       "START",
				AttachURI:     "http://example.com/alarm.wav",
				AttachFmtType: "audio/wav",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "audio-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	alarm := got.Alarms[0]
	assertEqual(t, "Action", alarm.Action, "AUDIO")
	assertEqual(t, "AttachURI", alarm.AttachURI, "http://example.com/alarm.wav")
}

func TestRadicale_VALARM_RepeatDuration(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-repeat",
		Title:     "Event with repeating alarm",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT15M",
				Description:  "Repeated alarm",
				Related:      "START",
				Repeat:       3,
				Duration:     "PT5M",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "repeat-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	alarm := got.Alarms[0]
	if alarm.Repeat != 3 {
		t.Errorf("Repeat: got %d, want 3", alarm.Repeat)
	}
	assertEqual(t, "Duration", alarm.Duration, "PT5M")
}

func TestRadicale_VALARM_AbsoluteTrigger(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-absolute",
		Title:     "Event with absolute trigger",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "20260410T133000Z",
				Description:  "Absolute trigger alarm",
				Related:      "START",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "absolute-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	alarm := got.Alarms[0]
	assertEqual(t, "TriggerValue", alarm.TriggerValue, "20260410T133000Z")
}

func TestRadicale_VALARM_WithUID_RFC9074(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-uid",
		Title:     "Event with alarm UID",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				UID:          "alarm-uid-123",
				Action:       "DISPLAY",
				TriggerValue: "-PT10M",
				Description:  "Alarm with UID",
				Related:      "START",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "uid-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	assertEqual(t, "Alarm UID", got.Alarms[0].UID, "alarm-uid-123")
}

func TestRadicale_VALARM_Acknowledged_RFC9074(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-ack",
		Title:     "Event with acknowledged alarm",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT10M",
				Description:  "Already acknowledged",
				Related:      "START",
				Acknowledged: "20260410T135500Z",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "ack-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	if got.Alarms[0].Acknowledged == "" {
		t.Error("Acknowledged should not be empty after round-trip")
	}
}

func TestRadicale_VALARM_MultipleOnEvent(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-valarm-multi",
		Title:     "Event with multiple alarms",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT30M",
				Description:  "30 min reminder",
				Related:      "START",
			},
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT5M",
				Description:  "5 min reminder",
				Related:      "START",
			},
			{
				Action:       "EMAIL",
				TriggerValue: "-PT1H",
				Summary:      "1h Email Reminder",
				Description:  "Event in 1 hour",
				Related:      "START",
				Attendees: []model.AlarmAttendee{
					{Email: "user@example.com"},
				},
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "multi-alarm.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 3 {
		t.Errorf("expected 3 alarms, got %d", len(got.Alarms))
		for i, a := range got.Alarms {
			t.Logf("  alarm[%d]: action=%s trigger=%s", i, a.Action, a.TriggerValue)
		}
	}
}

func TestRadicale_VALARM_OnTodo(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:     "rad-vtodo-alarm",
		Summary: "Todo with alarm",
		DueDate: "2026-04-15T17:00:00Z",
		Status:  "NEEDS-ACTION",
		DtStamp: "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "-PT1H",
				Description:  "Todo due in 1 hour",
				Related:      "START",
			},
		},
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "alarm-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm on todo, got %d", len(got.Alarms))
	}
	alarm := got.Alarms[0]
	assertEqual(t, "Action", alarm.Action, "DISPLAY")
	assertEqual(t, "TriggerValue", alarm.TriggerValue, "-PT1H")
}

// ---------- Edge case: Radicale ingests third-party iCal ----------

func TestRadicale_Ingest_ThirdPartyVEVENT(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	// Simulate a Google Calendar-style VEVENT with many properties
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Google Inc//Google Calendar 70.9054//EN
CALSCALE:GREGORIAN
BEGIN:VEVENT
UID:google-event-abc123@google.com
DTSTART;TZID=America/Los_Angeles:20260410T100000
DTEND;TZID=America/Los_Angeles:20260410T110000
DTSTAMP:20260401T000000Z
SUMMARY:Google Calendar Event
DESCRIPTION:Event created in Google Calendar
LOCATION:Googleplex
STATUS:CONFIRMED
TRANSP:OPAQUE
SEQUENCE:0
ORGANIZER;CN=Owner:mailto:owner@example.com
ATTENDEE;CN=Guest;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:guest@example.com
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT10M
DESCRIPTION:Reminder
END:VALARM
BEGIN:VALARM
ACTION:EMAIL
TRIGGER:-PT30M
SUMMARY:Email Reminder
DESCRIPTION:Your event starts soon
ATTENDEE:mailto:guest@example.com
END:VALARM
END:VEVENT
END:VCALENDAR`

	result := radicaleRoundtrip(t, calURL, "google-event.ics", []byte(ics))
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "UID", got.UID, "google-event-abc123@google.com")
	assertEqual(t, "Title", got.Title, "Google Calendar Event")
	assertEqual(t, "Timezone", got.Timezone, "America/Los_Angeles")
	if len(got.Alarms) != 2 {
		t.Errorf("expected 2 alarms, got %d", len(got.Alarms))
	}
	if len(got.Attendees) < 2 {
		t.Errorf("expected at least 2 attendees (org + guest), got %d", len(got.Attendees))
	}
}

func TestRadicale_Ingest_ThirdPartyVTODO(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Apple Inc.//Mac OS X 14.0//EN
BEGIN:VTODO
UID:apple-todo-xyz@apple.com
DTSTAMP:20260401T000000Z
SUMMARY:Buy groceries
DUE;VALUE=DATE:20260415
PRIORITY:1
STATUS:NEEDS-ACTION
CATEGORIES:Shopping,Personal
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER;VALUE=DURATION:-PT2H
DESCRIPTION:Buy groceries reminder
END:VALARM
END:VTODO
END:VCALENDAR`

	result := radicaleRoundtrip(t, calURL, "apple-todo.ics", []byte(ics))
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "UID", got.UID, "apple-todo-xyz@apple.com")
	assertEqual(t, "Summary", got.Summary, "Buy groceries")
	assertEqualInt(t, "Priority", got.Priority, 1)
	if got.Categories == "" {
		t.Error("Categories should not be empty")
	}
	if len(got.Alarms) != 1 {
		t.Errorf("expected 1 alarm, got %d", len(got.Alarms))
	}
}

func TestRadicale_Ingest_ThirdPartyVJOURNAL(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example//Journal App//EN
BEGIN:VJOURNAL
UID:journal-app-001@example.com
DTSTAMP:20260401T000000Z
DTSTART;VALUE=DATE:20260410
SUMMARY:Sprint Retrospective
DESCRIPTION:What went well: deployment pipeline.\nWhat to improve: test coverage.
STATUS:FINAL
CATEGORIES:Agile,Retro
END:VJOURNAL
END:VCALENDAR`

	result := radicaleRoundtrip(t, calURL, "app-journal.ics", []byte(ics))
	if len(result.Journals) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(result.Journals))
	}

	got := result.Journals[0]
	assertEqual(t, "UID", got.UID, "journal-app-001@example.com")
	assertEqual(t, "Summary", got.Summary, "Sprint Retrospective")
	if got.Description == "" {
		t.Error("Description should not be empty")
	}
	assertEqual(t, "StartDate", got.StartDate, "2026-04-10")
}

// ---------- Edge Cases ----------

func TestRadicale_VEVENT_SpecialCharsInSummary(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:         "rad-special-chars",
		Title:       `Meeting: "Q2 Review" — status & next steps (draft)`,
		Description: "Line 1\nLine 2\n\nParagraph 2\n\tIndented",
		Location:    "Café résumé, São Paulo",
		StartTime:   time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:      "CONFIRMED",
		Transp:      "OPAQUE",
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "special-chars.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "Title", got.Title, original.Title)
	assertEqual(t, "Description", got.Description, original.Description)
	assertEqual(t, "Location", got.Location, original.Location)
}

func TestRadicale_VEVENT_CommaInCategories(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	// Categories with multiple values
	original := event.Event{
		UID:        "rad-comma-cats",
		Title:      "Multi-category event",
		StartTime:  time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Categories: "work,personal,urgent",
		Status:     "CONFIRMED",
		Transp:     "OPAQUE",
		DtStamp:    "2026-04-01T00:00:00Z",
		CreatedAt:  time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "comma-cats.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "Categories", got.Categories, original.Categories)
}

func TestRadicale_VEVENT_UnicodeContent(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:         "rad-unicode",
		Title:       "日本語のイベント — 会議",
		Description: "Описание на русском языке 🇷🇺\n中文描述 🇨🇳\nEmoji: 🎉🔥💡",
		Location:    "東京都渋谷区",
		StartTime:   time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:      "CONFIRMED",
		Transp:      "OPAQUE",
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "unicode.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "Title", got.Title, original.Title)
	assertEqual(t, "Description", got.Description, original.Description)
	assertEqual(t, "Location", got.Location, original.Location)
}

func TestRadicale_VEVENT_EmptyOptionalFields(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	// Minimal event — no description, location, etc.
	original := event.Event{
		UID:       "rad-minimal",
		Title:     "Minimal Event",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "minimal.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "UID", got.UID, original.UID)
	assertEqual(t, "Title", got.Title, original.Title)
	if got.Description != "" {
		t.Errorf("Description should be empty, got %q", got.Description)
	}
	if got.Location != "" {
		t.Errorf("Location should be empty, got %q", got.Location)
	}
}

func TestRadicale_VEVENT_MultipleExDates(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:            "rad-multi-exdates",
		Title:          "Recurring with multiple exclusions",
		StartTime:      time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
		ExDates:        "2026-04-08T09:00:00Z,2026-04-10T09:00:00Z,2026-04-12T09:00:00Z",
		Status:         "CONFIRMED",
		Transp:         "OPAQUE",
		DtStamp:        "2026-04-01T00:00:00Z",
		CreatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "multi-exdates.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	// ExDates should contain all 3 exclusions (order may differ)
	if got.ExDates == "" {
		t.Error("ExDates should not be empty")
	}
	exdateCount := len(strings.Split(got.ExDates, ","))
	if exdateCount != 3 {
		t.Errorf("expected 3 exdates, got %d: %q", exdateCount, got.ExDates)
	}
}

func TestRadicale_VTODO_WithTimezone(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:       "rad-vtodo-tz",
		Summary:   "Todo with timezone",
		DueDate:   "2026-04-15T21:00:00Z", // 17:00 EDT
		StartDate: "2026-04-15T17:00:00Z", // 13:00 EDT
		Timezone:  "America/New_York",
		Status:    "NEEDS-ACTION",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "tz-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	assertEqual(t, "Timezone", got.Timezone, "America/New_York")
}

func TestRadicale_VTODO_WithAttendees(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:     "rad-vtodo-attendees",
		Summary: "Todo with attendees",
		DueDate: "2026-04-15T17:00:00Z",
		Status:  "NEEDS-ACTION",
		DtStamp: "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Attendees: []model.Attendee{
			{
				Email:      "assigner@example.com",
				Name:       "Assigner",
				RSVPStatus: "ACCEPTED",
				Role:       "CHAIR",
				Organizer:  true,
			},
			{
				Email:      "assignee@example.com",
				Name:       "Assignee",
				RSVPStatus: "NEEDS-ACTION",
				Role:       "REQ-PARTICIPANT",
			},
		},
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "attendees-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	if len(got.Attendees) < 2 {
		t.Errorf("expected at least 2 attendees, got %d", len(got.Attendees))
	}
}

func TestRadicale_VJOURNAL_WithTimezone(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := journal.Journal{
		UID:         "rad-vjournal-tz",
		Summary:     "Journal with timezone",
		Description: "Tokyo meeting notes",
		StartDate:   "2026-04-10T00:00:00Z", // 09:00 JST
		Timezone:    "Asia/Tokyo",
		Status:      "FINAL",
		DtStamp:     "2026-04-01T00:00:00Z",
		CreatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportJournals([]journal.Journal{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "tz-journal.ics", data)
	if len(result.Journals) != 1 {
		t.Fatalf("expected 1 journal, got %d", len(result.Journals))
	}

	got := result.Journals[0]
	assertEqual(t, "Timezone", got.Timezone, "Asia/Tokyo")
}

func TestRadicale_VALARM_PositiveDurationTrigger(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	// Positive trigger means "after" the anchor (useful with RELATED=END)
	original := event.Event{
		UID:       "rad-valarm-positive",
		Title:     "Event with post-event alarm",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{
				Action:       "DISPLAY",
				TriggerValue: "PT15M",
				Description:  "Follow-up alarm",
				Related:      "END",
			},
		},
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "positive-trigger.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	assertEqual(t, "TriggerValue", got.Alarms[0].TriggerValue, "PT15M")
	assertEqual(t, "Related", got.Alarms[0].Related, "END")
}

func TestRadicale_VALARM_TriggerWithExplicitValueDuration(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	// Some servers return TRIGGER;VALUE=DURATION:-PT10M (explicit VALUE param)
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:rad-trigger-explicit
DTSTART:20260410T140000Z
DTEND:20260410T150000Z
DTSTAMP:20260401T000000Z
SUMMARY:Explicit VALUE=DURATION
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER;VALUE=DURATION:-PT10M
DESCRIPTION:Explicit duration trigger
END:VALARM
END:VEVENT
END:VCALENDAR`

	result := radicaleRoundtrip(t, calURL, "explicit-duration.ics", []byte(ics))
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}
	got := result.Events[0]
	if len(got.Alarms) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(got.Alarms))
	}
	assertEqual(t, "TriggerValue", got.Alarms[0].TriggerValue, "-PT10M")
}

func TestRadicale_VEVENT_RecurrenceOverride(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	// A recurring event with an override instance (different summary for one occurrence)
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:rad-recurrence-override
DTSTART:20260406T090000Z
DTEND:20260406T100000Z
DTSTAMP:20260401T000000Z
RRULE:FREQ=WEEKLY;COUNT=4
SUMMARY:Weekly Standup
STATUS:CONFIRMED
TRANSP:OPAQUE
END:VEVENT
BEGIN:VEVENT
UID:rad-recurrence-override
RECURRENCE-ID:20260413T090000Z
DTSTART:20260413T100000Z
DTEND:20260413T110000Z
DTSTAMP:20260401T000000Z
SUMMARY:Weekly Standup (moved to 10am)
STATUS:CONFIRMED
TRANSP:OPAQUE
END:VEVENT
END:VCALENDAR`

	result := radicaleRoundtrip(t, calURL, "recurrence-override.ics", []byte(ics))
	if len(result.Events) != 2 {
		t.Fatalf("expected 2 events (base + override), got %d", len(result.Events))
	}

	var hasBase, hasOverride bool
	for _, e := range result.Events {
		if e.RecurrenceID == "" {
			hasBase = true
			assertEqual(t, "Base Title", e.Title, "Weekly Standup")
		} else {
			hasOverride = true
			assertEqual(t, "Override Title", e.Title, "Weekly Standup (moved to 10am)")
		}
	}
	if !hasBase {
		t.Error("missing base recurring event")
	}
	if !hasOverride {
		t.Error("missing recurrence override event")
	}
}

func TestRadicale_VEVENT_ClassPrivate(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-class-private",
		Title:     "Private Event",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CONFIRMED",
		Transp:    "OPAQUE",
		Class:     "PRIVATE",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "private-event.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "Class", got.Class, "PRIVATE")
}

func TestRadicale_VEVENT_StatusCancelled(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := event.Event{
		UID:       "rad-cancelled",
		Title:     "Cancelled Meeting",
		StartTime: time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 10, 15, 0, 0, 0, time.UTC),
		Status:    "CANCELLED",
		Transp:    "TRANSPARENT",
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportEvents([]event.Event{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "cancelled.ics", data)
	if len(result.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result.Events))
	}

	got := result.Events[0]
	assertEqual(t, "Status", got.Status, "CANCELLED")
	assertEqual(t, "Transp", got.Transp, "TRANSPARENT")
}

func TestRadicale_VTODO_WithComments(t *testing.T) {
	radicaleAvailable(t)
	calURL := radicaleCalendar(t)

	original := todo.Todo{
		UID:       "rad-vtodo-comments",
		Summary:   "Todo with comments",
		DueDate:   "2026-04-15",
		Status:    "NEEDS-ACTION",
		Comments:  []string{"First update", "Progress note"},
		Contacts:  []string{"Alice <alice@example.com>"},
		Resources: []string{"Laptop", "Whiteboard"},
		Relations: []model.Relation{
			{RelType: "PARENT", RelUID: "parent-todo-uid"},
		},
		DtStamp:   "2026-04-01T00:00:00Z",
		CreatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
	}

	data, err := ExportTodos([]todo.Todo{original}, "QA")
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	result := radicaleRoundtrip(t, calURL, "comments-todo.ics", data)
	if len(result.Todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(result.Todos))
	}

	got := result.Todos[0]
	if len(got.Comments) != 2 {
		t.Errorf("expected 2 comments, got %d: %v", len(got.Comments), got.Comments)
	}
	if len(got.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d: %v", len(got.Resources), got.Resources)
	}
	if len(got.Relations) != 1 {
		t.Errorf("expected 1 relation, got %d", len(got.Relations))
	}
}

// ---------- Helpers ----------

func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", field, got, want)
	}
}

func assertEqualInt(t *testing.T, field string, got, want int64) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d", field, got, want)
	}
}

func assertTimeEqual(t *testing.T, field string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}
