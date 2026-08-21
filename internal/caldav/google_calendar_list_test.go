package caldav

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
)

func TestDiscoverGoogleCalendarsListsEveryPage(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("showHidden"); got != "true" {
			t.Fatalf("showHidden = %q, want true", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "next" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"id": "en.brazilian#holiday@group.v.calendar.google.com", "summary": "Holidays in Brazil", "description": "Brazilian holidays", "backgroundColor": "#0b8043", "accessRole": "reader"},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nextPageToken": "next",
			"items": []map[string]any{
				{"id": "me@example.com", "summary": "me@example.com", "summaryOverride": "Personal", "backgroundColor": "#9e69af", "accessRole": "owner", "primary": true},
				{"id": "family@example.com", "summary": "Family", "backgroundColor": "#d50000", "accessRole": "writer"},
			},
		})
	}))
	defer server.Close()

	oldListURL := googleCalendarListURL
	googleCalendarListURL = server.URL
	defer func() { googleCalendarListURL = oldListURL }()

	calendars, err := DiscoverGoogleCalendars(context.Background(), auth.Credential{AccessToken: "access-token"}, nil)
	if err != nil {
		t.Fatalf("DiscoverGoogleCalendars: %v", err)
	}
	if requests != 2 || len(calendars) != 3 {
		t.Fatalf("requests=%d calendars=%d", requests, len(calendars))
	}
	if got := calendars[0]; got.Name != "Personal" || got.Access != CalendarAccessOwner || got.Color != "#9e69af" || got.Path != "https://apidata.googleusercontent.com/caldav/v2/me@example.com/events" {
		t.Fatalf("primary calendar = %+v", got)
	}
	if got := calendars[1]; got.Access != CalendarAccessWrite {
		t.Fatalf("writer calendar access = %q", got.Access)
	}
	if got := calendars[2]; got.Name != "Holidays in Brazil" || got.Access != CalendarAccessRead || got.Description != "Brazilian holidays" || got.Path != "https://apidata.googleusercontent.com/caldav/v2/en.brazilian%23holiday@group.v.calendar.google.com/events" {
		t.Fatalf("holiday calendar = %+v", got)
	}
}

func TestIsGoogleCalendarEndpoint(t *testing.T) {
	for _, raw := range []string{
		"https://apidata.googleusercontent.com/caldav",
		"https://apidata.googleusercontent.com/caldav/v2/user/events",
	} {
		if !IsGoogleCalendarEndpoint(raw) {
			t.Errorf("IsGoogleCalendarEndpoint(%q) = false", raw)
		}
	}
	if IsGoogleCalendarEndpoint("https://cal.example.com/dav") {
		t.Fatal("generic CalDAV server identified as Google")
	}
}

// Deleted calendars and those with only a free-busy reader role must still map
// correctly. Deleted entries are dropped entirely. Google's
// "freeBusyReader" role is read access (not unknown).
func TestDiscoverGoogleCalendarsSkipsDeletedAndMapsFreeBusyReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"id": "deleted@example.com", "summary": "Deleted", "accessRole": "owner", "deleted": true},
			{"id": "freebusy@example.com", "summary": "Availability", "accessRole": "freeBusyReader"},
		}})
	}))
	defer server.Close()

	oldListURL := googleCalendarListURL
	googleCalendarListURL = server.URL
	defer func() { googleCalendarListURL = oldListURL }()

	calendars, err := DiscoverGoogleCalendars(context.Background(), auth.Credential{AccessToken: "tok"}, nil)
	if err != nil {
		t.Fatalf("DiscoverGoogleCalendars: %v", err)
	}
	if len(calendars) != 1 {
		t.Fatalf("calendar count = %d, want 1 (deleted calendar filtered)", len(calendars))
	}
	if got := calendars[0]; got.Name != "Availability" || got.Access != CalendarAccessRead {
		t.Fatalf("freeBusyReader calendar = %+v, want read access", got)
	}
	if got := calendars[0].SupportedComponentSet; !slices.Equal(got, []string{"VFREEBUSY"}) {
		t.Fatalf("freeBusyReader SupportedComponentSet = %v, want VFREEBUSY-only", got)
	}
}

func TestGoogleCalendarIDFromCollectionURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id string
	}{
		{"me@example.com"},
		{"en.brazilian#holiday@group.v.calendar.google.com"},
	}
	for _, tc := range cases {
		got, ok := googleCalendarIDFromCollectionURL(googleCalDAVCollectionURL(tc.id))
		if !ok || got != tc.id {
			t.Errorf("googleCalendarIDFromCollectionURL(%q) = %q, %v", tc.id, got, ok)
		}
	}
	if _, ok := googleCalendarIDFromCollectionURL("https://cal.example.com/dav/work"); ok {
		t.Fatal("non-Google URL parsed as a Google calendar id")
	}
	if _, ok := googleCalendarIDFromCollectionURL("https://apidata.googleusercontent.com/caldav"); ok {
		t.Fatal("account root parsed as a Google calendar id")
	}
}

func TestSetGoogleCalendarListColorPatchesRGB(t *testing.T) {
	calendarID := "en.brazilian#holiday@group.v.calendar.google.com"
	var gotPath, gotQuery, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("method = %s", r.Method)
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backgroundColor":"#112233"}`))
	}))
	defer server.Close()

	oldListURL := googleCalendarListURL
	googleCalendarListURL = server.URL
	defer func() { googleCalendarListURL = oldListURL }()

	collection := googleCalDAVCollectionURL(calendarID)
	if err := SetGoogleCalendarListColor(context.Background(), http.DefaultClient, collection, "#112233"); err != nil {
		t.Fatalf("SetGoogleCalendarListColor: %v", err)
	}
	if strings.Trim(gotPath, "/") != calendarID {
		t.Fatalf("path = %q, want calendar id %q", gotPath, calendarID)
	}
	if !strings.Contains(gotQuery, "colorRgbFormat=true") {
		t.Fatalf("query = %q", gotQuery)
	}
	if !strings.Contains(gotBody, `"backgroundColor":"#112233"`) {
		t.Fatalf("body = %s", gotBody)
	}
	if !strings.Contains(gotBody, `"foregroundColor":"#ffffff"`) {
		t.Fatalf("body missing foreground: %s", gotBody)
	}
}

func TestSetGoogleCalendarListColorForbiddenIncludesBody(t *testing.T) {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"reason":"forbidden"}}`, http.StatusForbidden)
	}))
	defer server.Close()

	oldListURL := googleCalendarListURL
	googleCalendarListURL = server.URL
	defer func() { googleCalendarListURL = oldListURL }()

	err := SetGoogleCalendarListColor(context.Background(), http.DefaultClient, googleCalDAVCollectionURL("me@example.com"), "#112233")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("error = %v, want body", err)
	}
}
