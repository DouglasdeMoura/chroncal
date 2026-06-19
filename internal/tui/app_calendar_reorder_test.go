package tui

import (
	"slices"
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
)

// sidebarOrderIDs returns the calendar IDs in the order the sidebar list
// currently renders them.
func sidebarOrderIDs(m Model) []int64 {
	items := m.sidebar.List().items
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

// TestCalendarReorder_PendingOrderSurvivesStaleReload locks in the fix for the
// optimistic-reorder race: a calendar reload that lands while the async
// SetOrder is still in flight (e.g. a sync finishing mid-save) returns the
// stale DB order, and must NOT snap the just-moved calendar back. The pending
// order is overlaid until the save confirms, then released.
func TestCalendarReorder_PendingOrderSurvivesStaleReload(t *testing.T) {
	// Redirect the UI-state write the reload handler performs to a temp dir.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	items := []CalendarListItem{
		{ID: 1, Name: "Alpha", Color: "#111", Order: 0},
		{ID: 2, Name: "Bravo", Color: "#222", Order: 1},
	}
	m := Model{
		sidebar: NewSidebarModel(NewMiniMonthModel(now), NewCalendarListModel(items, nil)),
		calendars: map[int64]CalendarInfo{
			1: {Name: "Alpha", Color: "#111", DisplayOrder: 0},
			2: {Name: "Bravo", Color: "#222", DisplayOrder: 1},
		},
	}

	// User moves Bravo (id 2) above Alpha (id 1).
	updated, _ := m.Update(CalendarReorderedMsg{IDs: []int64{2, 1}})
	m = updated.(Model)
	if m.pendingOrder[2] != 0 || m.pendingOrder[1] != 1 {
		t.Fatalf("pendingOrder not recorded: %v", m.pendingOrder)
	}

	// A reload races the unsaved write and returns the OLD DB order.
	stale := map[int64]CalendarInfo{
		1: {Name: "Alpha", Color: "#111", DisplayOrder: 0},
		2: {Name: "Bravo", Color: "#222", DisplayOrder: 1},
	}
	updated, _ = m.Update(calendarsLoadedMsg{calendars: stale})
	m = updated.(Model)
	if got := sidebarOrderIDs(m); !slices.Equal(got, []int64{2, 1}) {
		t.Fatalf("stale reload reverted reorder: got %v want [2 1]", got)
	}

	// The save confirms; the matching pending entries clear.
	updated, _ = m.Update(calendarOrderSavedMsg{ids: []int64{2, 1}})
	m = updated.(Model)
	if len(m.pendingOrder) != 0 {
		t.Fatalf("pendingOrder not cleared after save: %v", m.pendingOrder)
	}

	// A later reload carrying the now-persisted order keeps Bravo first.
	fresh := map[int64]CalendarInfo{
		1: {Name: "Alpha", Color: "#111", DisplayOrder: 1},
		2: {Name: "Bravo", Color: "#222", DisplayOrder: 0},
	}
	updated, _ = m.Update(calendarsLoadedMsg{calendars: fresh})
	m = updated.(Model)
	if got := sidebarOrderIDs(m); !slices.Equal(got, []int64{2, 1}) {
		t.Fatalf("post-save reload wrong order: got %v want [2 1]", got)
	}
}

// TestCalendarReorder_DialogOriginatedSyncsSidebarAndDialog verifies a reorder
// emitted by the manage dialog (where the sidebar list was NOT pre-swapped)
// re-sorts the background sidebar from m.calendars and keeps the open dialog in
// sync — the two branches the dialog feature added to the handler.
func TestCalendarReorder_DialogOriginatedSyncsSidebarAndDialog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	items := []CalendarListItem{
		{ID: 1, Name: "Alpha", Order: 0},
		{ID: 2, Name: "Bravo", Order: 1},
		{ID: 3, Name: "Charlie", Order: 2},
	}
	cals := map[int64]CalendarInfo{
		1: {Name: "Alpha", DisplayOrder: 0},
		2: {Name: "Bravo", DisplayOrder: 1},
		3: {Name: "Charlie", DisplayOrder: 2},
	}
	m := Model{
		sidebar:                NewSidebarModel(NewMiniMonthModel(now), NewCalendarListModel(items, nil)),
		calendars:              cals,
		calendarListDialogOpen: true,
		calendarListDialog:     NewCalendarListDialogModel(cals, nil, help.New()),
	}

	// Dialog moved Bravo above Alpha; only the dialog swapped locally, so the
	// sidebar must be re-sorted by the handler, not by a prior list swap.
	updated, _ := m.Update(CalendarReorderedMsg{IDs: []int64{2, 1, 3}})
	m = updated.(Model)

	if got := sidebarOrderIDs(m); !slices.Equal(got, []int64{2, 1, 3}) {
		t.Fatalf("sidebar not synced to dialog reorder: got %v want [2 1 3]", got)
	}
	if got := m.calendarListDialog.order; !slices.Equal(got, []int64{2, 1, 3}) {
		t.Fatalf("open dialog not synced to reorder: got %v want [2 1 3]", got)
	}
}

// TestCalendarReorder_StaleSaveDoesNotClearNewerReorder verifies that when a
// second reorder happens while the first save is still in flight, the late
// confirmation of the first save does not drop the newer pending order.
func TestCalendarReorder_StaleSaveDoesNotClearNewerReorder(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	items := []CalendarListItem{
		{ID: 1, Name: "Alpha", Color: "#111", Order: 0},
		{ID: 2, Name: "Bravo", Color: "#222", Order: 1},
	}
	m := Model{
		sidebar: NewSidebarModel(NewMiniMonthModel(now), NewCalendarListModel(items, nil)),
		calendars: map[int64]CalendarInfo{
			1: {Name: "Alpha", DisplayOrder: 0},
			2: {Name: "Bravo", DisplayOrder: 1},
		},
	}

	// First reorder: [2, 1]. Second reorder (before the first save lands): [1, 2].
	updated, _ := m.Update(CalendarReorderedMsg{IDs: []int64{2, 1}})
	m = updated.(Model)
	updated, _ = m.Update(CalendarReorderedMsg{IDs: []int64{1, 2}})
	m = updated.(Model)

	// The first save confirms late. It must not clear the newer pending order,
	// whose positions differ from what the first save wrote.
	updated, _ = m.Update(calendarOrderSavedMsg{ids: []int64{2, 1}})
	m = updated.(Model)
	if m.pendingOrder[1] != 0 || m.pendingOrder[2] != 1 {
		t.Fatalf("newer pending order dropped by stale save: %v", m.pendingOrder)
	}
}
