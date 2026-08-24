package sync

import (
	"context"
	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/model"
	"strings"
	"testing"
	"time"
)

// A failure that is not an invalid alarm still fails the resource, so the
// caller keeps it dirty and retries.
func TestPersistImportedStillFailsOnOtherErrors(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	start := time.Date(2026, 6, 18, 17, 0, 0, 0, time.UTC)
	// An attendee outside the PARTSTAT CHECK constraint fails the write.
	// That is not an alarm error, so it must still fail the resource.
	result := icalPkg.ImportResult{
		Events: []event.Event{{
			UID:        "bad-attendee-uid",
			CalendarID: calendarID,
			Title:      "Carries a bad attendee",
			StartTime:  start,
			EndTime:    start.Add(time.Hour),
			Attendees: []model.Attendee{
				{Email: "alice@example.com", RSVPStatus: "NOT-A-PARTSTAT", Role: "REQ-PARTICIPANT"},
			},
		}},
	}
	if _, _, err := engine.persistImported(ctx, calendarID, result); err == nil {
		t.Fatal("persistImported must still fail for a non-alarm error")
	}
}

func TestBuildRemoteResourcePathUsesUID(t *testing.T) {
	got := buildRemoteResourcePath("https://caldav.example.com/cal/123/", "some-uid-42")
	want := "https://caldav.example.com/cal/123/some-uid-42.ics"
	if got != want {
		t.Errorf("buildRemoteResourcePath = %q, want %q", got, want)
	}

	// A UID with path-hostile characters must not escape its segment. The
	// URL encoder escapes the '%' of each escape sequence once more.
	got = buildRemoteResourcePath("https://caldav.example.com/cal", "a/b c?d")
	want = "https://caldav.example.com/cal/a%252Fb%20c%253Fd.ics"
	if got != want {
		t.Errorf("buildRemoteResourcePath = %q, want %q", got, want)
	}
}

// TestBuildRemoteResourcePathDistinctForEscapeBytes is a regression test.
// The old sanitizer replaced '/', '?', '#', and '%' with '_', so the UID
// pairs below collapsed to one name. Two resources then wrote the same
// remote object, and one of them clobbered the other. The percent encoding
// keeps every pair on a distinct href.
func TestBuildRemoteResourcePathDistinctForEscapeBytes(t *testing.T) {
	pairs := [][2]string{
		{"x/y", "x_y"},
		{"x?y", "x_y"},
		{"x#y", "x_y"},
		{"x%y", "x_y"},
		{"x/y", "x%2Fy"},
	}
	for _, pair := range pairs {
		a := buildRemoteResourcePath("https://caldav.example.com/cal/", pair[0])
		b := buildRemoteResourcePath("https://caldav.example.com/cal/", pair[1])
		if a == b {
			t.Errorf("UIDs %q and %q map to the same href %q", pair[0], pair[1], a)
		}
	}

	// The encoded name must stay one path segment: the href gains no '/'
	// beyond the calendar collection delimiter.
	href := buildRemoteResourcePath("https://caldav.example.com/cal/", "x/y")
	if strings.Count(href, "/") != strings.Count("https://caldav.example.com/cal/x_y.ics", "/") {
		t.Errorf("href %q adds a path segment; the UID must stay in one segment", href)
	}
}

func TestBuildRemoteResourcePathDeterministic(t *testing.T) {
	a := buildRemoteResourcePath("https://caldav.example.com/cal/", "uid-1")
	b := buildRemoteResourcePath("https://caldav.example.com/cal/", "uid-1")
	if a != b {
		t.Errorf("two calls disagree: %q vs %q; the name must be deterministic so a lost bookkeeping write cannot create a second object for the same UID", a, b)
	}
}

func TestBuildRemoteResourcePathEmptyUIDStillWorks(t *testing.T) {
	overrideRemoteObjectNameGenerator(t, "fallback-name.ics")
	got := buildRemoteResourcePath("https://caldav.example.com/cal/", "")
	if !strings.HasSuffix(got, "/fallback-name.ics") {
		t.Errorf("buildRemoteResourcePath with empty UID = %q, want fallback name suffix", got)
	}
}
