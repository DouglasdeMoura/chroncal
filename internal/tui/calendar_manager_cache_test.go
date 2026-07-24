package tui

import (
	"image/color"
	"strings"
	"testing"
)

func renderManagerPreview(t *testing.T, m CalendarManagerModel, w, h int) (inspectorPreviewKey, []string) {
	t.Helper()
	lines := m.selectionInspectorLines(w, h)
	if m.inspector == nil || len(lines) == 0 {
		t.Fatal("calendar preview did not populate the inspector cache")
	}
	return m.inspector.key, lines
}

func TestCalendarManagerPreviewCacheReusesStableRender(t *testing.T) {
	m := newFlatManager().SetSize(120, 40).selectCalendar(1)
	w, h := m.inspectorPaneSize()
	key, first := renderManagerPreview(t, m, w, h)
	secondKey, second := renderManagerPreview(t, m, w, h)

	if secondKey != key {
		t.Fatalf("stable render changed cache key: before=%+v after=%+v", key, secondKey)
	}
	if &first[0] != &second[0] {
		t.Fatal("stable render rebuilt preview lines instead of reusing the memo")
	}
}

func TestCalendarManagerPreviewCacheInvalidatesEveryRenderedInput(t *testing.T) {
	m := newFlatManager().SetSize(120, 40).selectCalendar(1)
	w, h := m.inspectorPaneSize()
	previousKey, previousLines := renderManagerPreview(t, m, w, h)

	assertInvalidated := func(label string, next CalendarManagerModel, nextW, nextH int) CalendarManagerModel {
		t.Helper()
		nextKey, nextLines := renderManagerPreview(t, next, nextW, nextH)
		if nextKey == previousKey {
			t.Fatalf("%s did not invalidate preview key %+v", label, nextKey)
		}
		if len(previousLines) > 0 && len(nextLines) > 0 && &previousLines[0] == &nextLines[0] {
			t.Fatalf("%s reused stale preview lines", label)
		}
		previousKey, previousLines = nextKey, nextLines
		return next
	}

	m = assertInvalidated("selection", m.selectCalendar(2), w, h)
	m = assertInvalidated("pane size", m, w+1, h)
	w++

	m = m.selectCalendar(1)
	info := m.calendars[1]
	info.Name = "Renamed Local"
	m.calendars[1] = info
	m = assertInvalidated("calendar data", m, w, h)
	if got := stripANSI(strings.Join(previousLines, "\n")); !strings.Contains(got, "Renamed Local") {
		t.Fatalf("calendar data invalidated the key but rendered stale text:\n%s", got)
	}

	m.list = m.list.SetHidden(1, true)
	m = assertInvalidated("visibility", m, w, h)

	info = m.calendars[1]
	info.IsDefault = !info.IsDefault
	m.calendars[1] = info
	m = assertInvalidated("default metadata", m, w, h)

	info = m.calendars[1]
	info.Synced = true
	info.AccountID = 7
	info.AccountName = "Work Account"
	info.RemoteAccess = "read-write"
	m.calendars[1] = info
	m = assertInvalidated("remote metadata", m, w, h)

	theme := m.theme
	theme.Text = color.RGBA{R: 1, G: 2, B: 3, A: 255}
	m = m.SetTheme(theme)
	assertInvalidated("theme", m, w, h)
}
