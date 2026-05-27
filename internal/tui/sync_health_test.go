package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func TestSyncHealthFor(t *testing.T) {
	tests := []struct {
		name string
		info CalendarInfo
		want SyncHealth
	}{
		{"local-only", CalendarInfo{Synced: false, LastSyncError: "boom"}, SyncHealthNone},
		{"error wins over last-sync", CalendarInfo{Synced: true, LastSyncAt: "2026-05-27T00:00:00Z", LastSyncError: "invalid_grant"}, SyncHealthError},
		{"clean", CalendarInfo{Synced: true, LastSyncAt: "2026-05-27T00:00:00Z"}, SyncHealthOK},
		{"never cleanly synced", CalendarInfo{Synced: true}, SyncHealthPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := syncHealthFor(tt.info); got != tt.want {
				t.Errorf("syncHealthFor(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

// themedHealthList builds a list with an error color set, so the ⚠ marker
// renders, mirroring how SidebarModel.SetTheme wires Theme.Error in.
func themedHealthList(items []CalendarListItem, width int) CalendarListModel {
	red := lipgloss.Color("#ff0000")
	m := NewCalendarListModel(items, nil).
		SetTheme(red, red, red, red, red).
		SetWidth(width)
	return m
}

func TestCalendarList_ViewRendersErrorMarkerOnlyForErrorRows(t *testing.T) {
	items := []CalendarListItem{
		{ID: 1, Name: "GMX", Color: "#a6e3a1", Health: SyncHealthOK},
		{ID: 2, Name: "gmail", Color: "#f5c2e7", Health: SyncHealthError},
		{ID: 3, Name: "pending", Color: "#89b4fa", Health: SyncHealthPending},
	}
	m := themedHealthList(items, 24)
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 rows, got %d: %q", len(lines), lines)
	}
	if strings.Contains(lines[0], "⚠") {
		t.Errorf("healthy row should have no marker; got %q", lines[0])
	}
	if !strings.Contains(lines[1], "⚠") {
		t.Errorf("error row should render ⚠; got %q", lines[1])
	}
	if strings.Contains(lines[2], "⚠") {
		t.Errorf("pending row should have no marker; got %q", lines[2])
	}
}

func TestCalendarList_ErrorMarkerRespectsWidth(t *testing.T) {
	const width = 20
	items := []CalendarListItem{
		{ID: 1, Name: "maildodouglas@gmail.com", Color: "#f5c2e7", Health: SyncHealthError},
		{ID: 2, Name: "another-long-calendar-name", Color: "#a6e3a1", Health: SyncHealthError},
	}
	// Exercise both the non-selected and the selected (Reverse chip) paths:
	// focus the list with the cursor on row 0.
	m := themedHealthList(items, width).Focus()
	m.cursor = 0
	for _, line := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("row exceeds width %d (got %d): %q", width, w, line)
		}
		if !strings.Contains(line, "⚠") {
			t.Errorf("expected ⚠ on every (error) row; got %q", line)
		}
	}
}

func TestSyncHealthDialogLines_InvalidGrant(t *testing.T) {
	theme := Theme{}
	params := CalendarDialogParams{
		Name:          "gmail",
		RemoteLinked:  true,
		LastSyncError: `oauth token refresh: token refresh failed (400): {"error": "invalid_grant"}`,
	}
	lines := syncHealthDialogLines(params, theme)
	if len(lines) != 2 {
		t.Fatalf("expected error line + hint, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "Google login expired") {
		t.Errorf("error line should humanize invalid_grant; got %q", lines[0])
	}
	if !strings.Contains(lines[1], "calendar update gmail") || !strings.Contains(lines[1], "--auth oauth2") {
		t.Errorf("hint should name the re-link command with the calendar; got %q", lines[1])
	}
}

func TestSyncHealthDialogLines_HealthyAndUnlinked(t *testing.T) {
	theme := Theme{}

	healthy := CalendarDialogParams{RemoteLinked: true, LastSyncAt: "2026-05-27T17:29:10Z"}
	lines := syncHealthDialogLines(healthy, theme)
	if len(lines) != 1 || !strings.Contains(lines[0], "Last synced") {
		t.Errorf("healthy linked calendar should show one Last-synced line; got %q", lines)
	}

	unlinked := CalendarDialogParams{RemoteLinked: false, LastSyncError: "boom"}
	if got := syncHealthDialogLines(unlinked, theme); got != nil {
		t.Errorf("unlinked calendar should produce no sync lines; got %q", got)
	}

	neverAttempted := CalendarDialogParams{RemoteLinked: true}
	if got := syncHealthDialogLines(neverAttempted, theme); got != nil {
		t.Errorf("linked-but-never-attempted should produce no lines; got %q", got)
	}
}

func TestHumanizeSyncError(t *testing.T) {
	if got := humanizeSyncError("x invalid_grant y"); !strings.Contains(got, "Google login expired") {
		t.Errorf("invalid_grant not humanized: %q", got)
	}
	if got := humanizeSyncError("first line\nsecond line"); got != "first line" {
		t.Errorf("multiline error should reduce to first line; got %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := humanizeSyncError(long); len([]rune(got)) > 80 {
		t.Errorf("long error should be truncated to <=80 runes; got %d", len([]rune(got)))
	}
}
