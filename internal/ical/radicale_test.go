package ical

import (
	"io"
	"net/http"
	"os"
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

func radicaleURL() string { return radicaleURLFrom(os.Getenv("RADICALE_URL")) }

func radicaleURLFrom(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return defaultRadicaleURL
	}
	return raw
}

func shouldSkipRadicale(err error, status int) bool {
	if err != nil {
		return true
	}
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// radicaleAvailable checks whether the Radicale server is reachable
// for anonymous writes.
func radicaleAvailable(t *testing.T) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, radicaleURL()+"/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("Radicale not available at %s: %v", radicaleURL(), err)
	}
	resp.Body.Close()
	if shouldSkipRadicale(nil, resp.StatusCode) {
		t.Skipf("Radicale at %s is not usable for anonymous writes: HTTP %d", radicaleURL(), resp.StatusCode)
	}
}

// radicaleCalendar creates (or reuses) a shared calendar on Radicale and
// returns the collection URL. All tests share one calendar. That avoids
// too many collections.
func radicaleCalendar(t *testing.T) string {
	t.Helper()
	base := radicaleURL()

	// Ensure user root exists (MKCOL).
	userURL := base + "/qauser/"
	req, _ := http.NewRequestWithContext(t.Context(), "MKCOL", userURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("MKCOL %s: %v", userURL, err)
	}
	resp.Body.Close()

	// Create calendar (MKCALENDAR).
	calURL := base + "/qauser/qa-integration/"
	req, _ = http.NewRequestWithContext(t.Context(), "MKCALENDAR", calURL, nil)
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
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, itemURL, strings.NewReader(string(icsData)))
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
	getReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, itemURL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err = http.DefaultClient.Do(getReq)
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
