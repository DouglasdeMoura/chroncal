package tui

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalLocationLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		in   string
		want string
	}{
		{"", "Local"},
		{"  ", "Local"},
		{"/srv/calendars", "Local: /srv/calendars"},
		{filepath.Join(home, "calendars", "fastmail"), "Local: ~/calendars/fastmail"},
		{home, "Local: ~"},
		{home + "-other", "Local: " + home + "-other"},
	}
	for _, tc := range cases {
		if got := localLocationLabel(tc.in); got != tc.want {
			t.Errorf("localLocationLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCalendarDialogLocalPathInLocationRow verifies the detail of a local
// calendar that a directory feeds names that directory on the Location row
// (issue #809). A user can then see where the events come from.
func TestCalendarDialogLocalPathInLocationRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:        7,
		Name:      "Fastmail",
		Color:     "#a6e3a1",
		LocalPath: "/srv/calendars/fastmail",
	}, Theme{}).SetSize(120, 40)
	view := stripANSI(m.View())
	if !strings.Contains(view, "Local: /srv/calendars/fastmail") {
		t.Errorf("Location row does not show the directory:\n%s", view)
	}
}

func TestCalendarDialogRemoteIgnoresLocalPath(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:           7,
		Name:         "Work",
		Color:        "#a6e3a1",
		RemoteLinked: true,
		AccountName:  "Fastmail account",
		LocalPath:    "/srv/calendars/work",
	}, Theme{}).SetSize(120, 40)
	view := stripANSI(m.View())
	if strings.Contains(view, "/srv/calendars/work") {
		t.Errorf("remote calendar detail shows a local directory:\n%s", view)
	}
}
