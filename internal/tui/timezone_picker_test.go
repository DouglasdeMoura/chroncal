package tui

import (
	"testing"
)

// TestTimezonePicker_ApplyFilterClampsOffset is a regression test for the bug
// where applyFilter clamped cursor but left offset unbounded. If the picker was
// scrolled to the bottom and then the filter was narrowed to a few top results,
// ensureVisible would snap offset to the clamped cursor position rather than
// to the maximum valid scroll position, hiding all results above that cursor.
//
// Repro: scroll to bottom (offset = total-visibleRows, cursor = total-1), then
// filter to "Africa/" (11 results, all near the top of the master list).
// cursor is clamped to 10 (last of 11), ensureVisible sets offset = cursor = 10,
// but the valid maximum offset for 11 results in an 8-row window is only 3.
// Items 0–9 are scrolled off the top.
func TestTimezonePicker_ApplyFilterClampsOffset(t *testing.T) {
	m := NewTimezonePickerModel("UTC", Theme{})
	total := len(m.filtered)

	// Scroll to the very bottom of the full list.
	m.offset = max(total-tzPickerVisibleRows, 0)
	m.cursor = total - 1

	// Apply a filter that returns a small number of results all near the top.
	// "Africa/" matches all 11 Africa/* timezones (indices 1–11 in ianaTimezones).
	m.filter.SetValue("Africa/")
	m.applyFilter()

	// Maximum valid scroll offset for the filtered result set.
	maxValidOffset := max(len(m.filtered)-tzPickerVisibleRows, 0)

	if m.offset > maxValidOffset {
		t.Errorf("offset = %d after narrowing filter, want <= %d (max valid for %d results in %d-row window); top entries are scrolled off-screen",
			m.offset, maxValidOffset, len(m.filtered), tzPickerVisibleRows)
	}
	// Cursor must be inside the visible window.
	if m.cursor < m.offset || m.cursor >= m.offset+tzPickerVisibleRows {
		t.Errorf("cursor %d not visible in window [%d, %d)",
			m.cursor, m.offset, m.offset+tzPickerVisibleRows)
	}
}
