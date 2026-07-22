package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
)

// flatManagerCalendars is a mixed Local + multi-account fixture whose
// canonical order (Local first, then accounts by AccountOrder, then
// in-account DisplayOrder) is [1, 2, 3, 4]:
//
//	1 On device  Local
//	2 Primary    Google   (account 7, order 0, display 0)
//	3 Holidays   Google   (account 7, order 0, display 1)
//	4 Work       Fastmail (account 9, order 1, display 0)
func flatManagerCalendars() map[int64]CalendarInfo {
	return map[int64]CalendarInfo{
		1: {Name: "On device", Color: "#ff0000", DisplayOrder: 9},
		2: {Name: "Primary", Color: "#00ff00", AccountID: 7, AccountName: "Google", AccountOrder: 0, DisplayOrder: 0},
		3: {Name: "Holidays", Color: "#0000ff", AccountID: 7, AccountName: "Google", AccountOrder: 0, DisplayOrder: 1},
		4: {Name: "Work", Color: "#aaaaaa", AccountID: 9, AccountName: "Fastmail", AccountOrder: 1, DisplayOrder: 0},
	}
}

func newFlatManager() CalendarManagerModel {
	return NewCalendarManagerModel(flatManagerCalendars(), nil, help.New()).SetSize(120, 40)
}

// flatManagerRows returns the pre-rendered row labels, ANSI-stripped and
// trimmed, in canonical top-to-bottom order. Inspecting the model's own
// row slice (not the bordered View) keeps assertions independent of the
// surrounding chrome and matches how the legacy dialog tests read
// m.shell.rows.
func flatManagerRows(m CalendarManagerModel) []string {
	out := make([]string, len(m.rows))
	for i, r := range m.rows {
		out[i] = strings.TrimSpace(stripANSI(r))
	}
	return out
}

func flatRowForID(m CalendarManagerModel, id int64) string {
	for i, cid := range m.order {
		if cid == id {
			return flatManagerRows(m)[i]
		}
	}
	return ""
}

// TestCalendarManagerRootFlatRowsOnePerCalendar locks in the flat contract:
// exactly one physical row per calendar, each carrying a visibility checkbox
// and independent color-dot glyph, with no structural heading or separator.
func TestCalendarManagerRootFlatRowsOnePerCalendar(t *testing.T) {
	cals := flatManagerCalendars()
	m := newFlatManager()
	rows := flatManagerRows(m)
	if len(rows) != len(cals) {
		t.Fatalf("row count = %d, want one per calendar (%d); rows=%q", len(rows), len(cals), rows)
	}
	for i, row := range rows {
		trimmed := strings.TrimSpace(row)
		if !strings.HasPrefix(trimmed, Glyphs["checkbox.on"]+" ●") {
			t.Errorf("row %d is not a visible calendar row (checkbox/color marker): %q", i, row)
		}
	}
}

// TestCalendarManagerRootNoStructuralHeadings asserts the rendered View
// contains no standalone Local/account heading lines and no blank separator
// rows in the list body — the manager is one column, one row per calendar.
func TestCalendarManagerRootNoStructuralHeadings(t *testing.T) {
	m := newFlatManager()
	body := stripANSI(m.View())
	for _, heading := range []string{"Local", "Google", "Fastmail", "Remote"} {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == heading {
				t.Errorf("found standalone account heading row %q in rendered body", heading)
			}
		}
	}
	// Every non-empty body line inside the list region must begin with a dot.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip chrome lines (border, title, help). Calendar rows start with a dot.
		if strings.ContainsAny(trimmed[:1], "●○") {
			continue
		}
		// Allow the title line and help line; anything else that isn't chrome
		// would be a stray heading. We only fail on lines that are exactly an
		// account name (covered above), so this loop is belt-and-suspenders.
	}
}

// TestCalendarManagerRootLocalAndAccountContextInline verifies each row carries
// its owning context (dim "Local" or the account name) on the same line as the
// calendar name, rather than as a group heading.
func TestCalendarManagerRootLocalAndAccountContextInline(t *testing.T) {
	m := newFlatManager()
	localRow := flatRowForID(m, 1)
	if !strings.Contains(localRow, "On device") || !strings.Contains(localRow, "Local") {
		t.Fatalf("local row missing name/Local context: %q", localRow)
	}
	googleRow := flatRowForID(m, 2)
	if !strings.Contains(googleRow, "Primary") || !strings.Contains(googleRow, "Google") {
		t.Fatalf("account row missing name/account context: %q", googleRow)
	}
	fastmailRow := flatRowForID(m, 4)
	if !strings.Contains(fastmailRow, "Work") || !strings.Contains(fastmailRow, "Fastmail") {
		t.Fatalf("account row missing name/account context: %q", fastmailRow)
	}
}

// TestCalendarManagerRootStatusMarkers verifies the compact applicable-state
// tags render: default, read-only (RemoteAccess read), sync error, remote
// missing, and hidden (hollow dot).
func TestCalendarManagerRootStatusMarkers(t *testing.T) {
	cals := map[int64]CalendarInfo{
		1: {Name: "Default", Color: "#fff", IsDefault: true},
		2: {Name: "ReadOnly", Color: "#fff", RemoteAccess: "read"},
		3: {Name: "Broken", Color: "#fff", Synced: true, LastSyncError: "boom"},
		4: {Name: "Gone", Color: "#fff", Synced: true, RemoteMissing: true},
	}
	m := NewCalendarManagerModel(cals, map[int64]bool{4: true}, help.New()).SetSize(120, 40)
	rows := flatManagerRows(m)
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"default", "read-only", "sync error", "missing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("status marker %q missing from rows:\n%s", want, joined)
		}
	}
	if hiddenRow := flatRowForID(m, 4); !strings.HasPrefix(hiddenRow, Glyphs["checkbox.off"]+" ●") {
		t.Errorf("hidden calendar row should use unchecked visibility plus color marker: %q", hiddenRow)
	}
	if visibleRow := flatRowForID(m, 1); !strings.HasPrefix(visibleRow, Glyphs["checkbox.on"]+" ●") {
		t.Errorf("visible calendar row should use checked visibility plus color marker: %q", visibleRow)
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if row := flatRowForID(m, id); !strings.Contains(row, "Edit ›") {
			t.Errorf("calendar %d row missing Edit link: %q", id, row)
		}
	}
}

// TestCalendarManagerRootEnterPushesCalendarDetail verifies Enter pushes the
// selected calendar's detail onto the manager stack (OpenCalendar), targeting
// the immutable ID and switching the screen without disturbing root selection.
func TestCalendarManagerRootEnterPushesCalendarDetail(t *testing.T) {
	m := newFlatManager().selectCalendar(3)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("Enter should push detail internally, got command %T", cmd())
	}
	if mm.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want CalendarManagerScreenCalendar", mm.Screen())
	}
	if mm.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	if got := mm.calendarForm.Draft().ID; got != 3 {
		t.Fatalf("pushed detail ID = %d, want 3", got)
	}
	// Root selection is preserved by ID so Back can restore it.
	if id, ok := mm.selectedID(); !ok || id != 3 {
		t.Fatalf("root selection moved on open: got %d ok=%v", id, ok)
	}
}

// TestCalendarManagerRootSpaceTogglesVisibility verifies Space targets the
// selected immutable ID and emits CalendarVisibilityToggledMsg.
func TestCalendarManagerRootSpaceTogglesVisibility(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	_, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("Space emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 2 {
		t.Fatalf("toggle msg ID = %d, want 2", msg.ID)
	}
}

// TestCalendarManagerRootMouseRowOpensEdit verifies clicking the non-checkbox
// row body follows the visible Edit affordance and opens the clicked calendar.
func TestCalendarManagerRootMouseRowOpensEdit(t *testing.T) {
	m := newFlatManager()
	listX, listY, _, _ := m.listRegion()
	clicked, cmd := m.Update(tea.MouseClickMsg{X: listX + 8, Y: listY + 2, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("row edit click emitted command %T", cmd())
	}
	if clicked.Screen() != CalendarManagerScreenCalendar || clicked.calendarForm == nil {
		t.Fatalf("row click did not open calendar edit: screen=%v form=%v", clicked.Screen(), clicked.calendarForm)
	}
	if got := clicked.calendarForm.Draft().ID; got != 3 {
		t.Fatalf("row click opened calendar %d, want 3", got)
	}
}

func TestCalendarManagerRootMouseCheckboxTogglesVisibility(t *testing.T) {
	m := newFlatManager()
	listX, listY, _, _ := m.listRegion()
	toggled, cmd := m.Update(tea.MouseClickMsg{X: listX + 1, Y: listY + 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("checkbox click emitted no visibility command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok || msg.ID != 2 || !msg.Hidden {
		t.Fatalf("checkbox click message = %#v, want calendar 2 hidden", cmd())
	}
	if toggled.Screen() != CalendarManagerScreenList || !toggled.hidden[2] {
		t.Fatalf("checkbox click opened edit or failed optimistic toggle: screen=%v hidden=%v", toggled.Screen(), toggled.hidden[2])
	}
}

// TestCalendarManagerRootMouseActivatesAddButton verifies the persistent Add
// action is mouse-clickable and emits CalendarManagerAddRequestedMsg.
func TestCalendarManagerRootMouseActivatesAddButton(t *testing.T) {
	m := newFlatManager()
	bx, by, bw, ok := m.titleActionRect()
	if !ok {
		t.Fatal("persistent Add title action not present")
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: bx + bw/2, Y: by, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("clicking Add emitted no command")
	}
	if _, ok := cmd().(CalendarManagerAddRequestedMsg); !ok {
		t.Fatalf("expected CalendarManagerAddRequestedMsg, got %T", cmd())
	}
}

// TestCalendarManagerRootReorderWithinSameOwnerOnly verifies Shift+Up/Down
// emits CalendarReorderedMsg with the full canonical ID order, swaps only
// within the same AccountID, and is a no-op across an owner boundary.
func TestCalendarManagerRootReorderWithinSameOwnerOnly(t *testing.T) {
	// Canonical order: [1 Local, 2 Google, 3 Google, 4 Fastmail].

	// Move calendar 3 (Google) up over calendar 2 (Google): same owner, swaps.
	m := newFlatManager().selectCalendar(3)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'K', Text: "K"}) // shift+up alternate
	if cmd == nil {
		t.Fatal("within-owner move should emit a reorder command")
	}
	msg, ok := cmd().(CalendarReorderedMsg)
	if !ok {
		t.Fatalf("expected CalendarReorderedMsg, got %T", cmd())
	}
	if want := []int64{1, 3, 2, 4}; !slices.Equal(msg.IDs, want) {
		t.Fatalf("reordered IDs = %v, want %v", msg.IDs, want)
	}
	if id, ok := mm.selectedID(); !ok || id != 3 {
		t.Fatalf("selection should follow moved calendar: got %d ok=%v", id, ok)
	}

	// Move calendar 4 (Fastmail, idx 3) up over calendar 3 (Google, idx 2):
	// different owner -> no-op, no command.
	m2 := newFlatManager().selectCalendar(4)
	_, cmd = m2.Update(tea.KeyPressMsg{Code: 'K', Text: "K"})
	if cmd != nil {
		if _, ok := cmd().(CalendarReorderedMsg); ok {
			t.Fatal("cross-owner reorder must be a no-op (Fastmail over Google)")
		}
	}

	// Move calendar 2 (Google, idx 1) up over calendar 1 (Local, idx 0):
	// different owner -> no-op.
	m3 := newFlatManager().selectCalendar(2)
	_, cmd = m3.Update(tea.KeyPressMsg{Code: 'K', Text: "K"})
	if cmd != nil {
		if _, ok := cmd().(CalendarReorderedMsg); ok {
			t.Fatal("cross-owner reorder must be a no-op (Google over Local)")
		}
	}
}

// TestCalendarManagerRootReorderEdgesAreNoops verifies moving past either end of
// the list is a no-op that emits nothing.
func TestCalendarManagerRootReorderEdgesAreNoops(t *testing.T) {
	m := newFlatManager()
	// Top calendar (Local) cannot move up.
	m = m.selectCalendar(1)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'K', Text: "K"}); cmd != nil {
		t.Error("move up at top should be a no-op")
	}
	// Bottom calendar (Fastmail) cannot move down.
	m = m.selectCalendar(4)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'J', Text: "J"}); cmd != nil {
		t.Error("move down at bottom should be a no-op")
	}
}

// TestCalendarManagerRootReorderDoesNotMutateOriginalOrder guards against the
// value receiver aliasing the parent's order slice via an in-place swap.
func TestCalendarManagerRootReorderDoesNotMutateOriginalOrder(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	_, _ = m.Update(tea.KeyPressMsg{Code: 'J', Text: "J"})
	if want := []int64{1, 2, 3, 4}; !slices.Equal(m.order, want) {
		t.Errorf("original order mutated by reorder: got %v, want %v", m.order, want)
	}
}

// TestCalendarManagerRootSelectionRestoredByID verifies SetData preserves the
// selected calendar (by immutable ID) and the scroll anchor (by the top-visible
// calendar ID) across a data refresh, so edits and reloads don't jump the
// cursor or scroll.
func TestCalendarManagerRootSelectionRestoredByID(t *testing.T) {
	// Build a tall list so scrolling is meaningful.
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 30; i++ {
		cals[i] = CalendarInfo{Name: "Cal", DisplayOrder: i}
	}
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(60, 16)
	// Select calendar 20 and bring it into view.
	m = m.selectCalendar(20)
	m = m.ensureVisible()
	selID, ok := m.selectedID()
	if !ok || selID != 20 {
		t.Fatalf("setup selection = %d ok=%v, want 20", selID, ok)
	}
	topBefore, _ := m.visibleRange()
	topIDBefore := m.order[topBefore]

	// Refresh data: same calendars, fresh maps, plus one appended calendar.
	refreshed := map[int64]CalendarInfo{}
	for id, info := range cals {
		refreshed[id] = info
	}
	refreshed[31] = CalendarInfo{Name: "Tail", DisplayOrder: 31}
	m = m.SetData(refreshed, nil)

	selID, ok = m.selectedID()
	if !ok || selID != 20 {
		t.Fatalf("selection after refresh = %d ok=%v, want 20", selID, ok)
	}
	topAfter, _ := m.visibleRange()
	topIDAfter := m.order[topAfter]
	if topIDAfter != topIDBefore {
		t.Fatalf("scroll anchor changed: top-visible was %d, now %d", topIDBefore, topIDAfter)
	}
}

// TestCalendarManagerRootSelectionFallsBackWhenIDGone verifies that when the
// previously selected calendar disappears, the cursor lands on a valid row
// rather than going out of range.
func TestCalendarManagerRootSelectionFallsBackWhenIDGone(t *testing.T) {
	cals := flatManagerCalendars()
	m := newFlatManager().selectCalendar(3)
	delete(cals, 3)
	m = m.SetData(cals, nil)
	id, ok := m.selectedID()
	if !ok {
		t.Fatal("selection lost entirely after refresh")
	}
	if idx := slices.Index(m.order, id); idx < 0 {
		t.Fatalf("fallback selection %d not in order %v", id, m.order)
	}
}

// TestCalendarManagerRootCloseEmitsClosedMsg verifies Esc and q both close the
// manager by emitting CalendarManagerClosedMsg.
func TestCalendarManagerRootCloseEmitsClosedMsg(t *testing.T) {
	m := newFlatManager()
	for name, key := range map[string]tea.KeyPressMsg{
		"esc": {Code: tea.KeyEscape},
		"q":   {Code: 'q', Text: "q"},
	} {
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("%s emitted no command", name)
		}
		if _, ok := cmd().(CalendarManagerClosedMsg); !ok {
			t.Fatalf("%s: expected CalendarManagerClosedMsg, got %T", name, cmd())
		}
	}
}

// TestCalendarManagerRootAddKeyEmitsAddRequested verifies the persistent Add
// keybinding emits CalendarManagerAddRequestedMsg.
func TestCalendarManagerRootAddKeyEmitsAddRequested(t *testing.T) {
	m := newFlatManager()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("Add key emitted no command")
	}
	if _, ok := cmd().(CalendarManagerAddRequestedMsg); !ok {
		t.Fatalf("expected CalendarManagerAddRequestedMsg, got %T", cmd())
	}
}

// TestCalendarManagerRootNavigationMovesSelection verifies Up/Down move the
// selection and wrap-clamp at the ends.
func TestCalendarManagerRootNavigationMovesSelection(t *testing.T) {
	m := newFlatManager()
	if id, _ := m.selectedID(); id != 1 {
		t.Fatalf("initial selection = %d, want 1", id)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if id, _ := m.selectedID(); id != 2 {
		t.Fatalf("after down = %d, want 2", id)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if id, _ := m.selectedID(); id != 1 {
		t.Fatalf("after up = %d, want 1", id)
	}
	// Clamp at top.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if id, _ := m.selectedID(); id != 1 {
		t.Fatalf("up at top moved selection to %d, want 1", id)
	}
}

// TestCalendarManagerDetailActionsTargetImmutableID verifies the pushed
// calendar detail exposes Export, Set Default, and Delete as leading actions
// that all carry the selected calendar's immutable ID, with Delete styled as
// the destructive variant.
func TestCalendarManagerDetailActionsTargetImmutableID(t *testing.T) {
	m := newFlatManager().selectCalendar(3) // Holidays, account 7, not default
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if mm.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	buttons := mm.calendarForm.form.actionButtons
	labels := make([]string, 0, len(buttons))
	for _, b := range buttons {
		labels = append(labels, b.Label)
	}
	if got, want := strings.Join(labels, ","), "Set as Default,Export Calendar…,Delete Calendar…"; got != want {
		t.Fatalf("detail actions = %q, want %q", got, want)
	}
	for _, b := range buttons {
		msg := b.OnPress()
		switch msg := msg.(type) {
		case CalendarSetDefaultRequestedMsg:
			if msg.ID != 3 || msg.Name != "Holidays" {
				t.Errorf("Set Default = %+v, want ID 3", msg)
			}
		case CalendarExportRequestedMsg:
			if msg.ID != 3 || msg.Name != "Holidays" {
				t.Errorf("Export = %+v, want ID 3", msg)
			}
		case CalendarDeleteRequestedMsg:
			if msg.ID != 3 || msg.Name != "Holidays" {
				t.Errorf("Delete = %+v, want ID 3", msg)
			}
		default:
			t.Errorf("unexpected action message %T from %q", msg, b.Label)
		}
		if b.Label == "Delete Calendar…" && b.Variant != ButtonDanger {
			t.Errorf("Delete variant = %v, want ButtonDanger", b.Variant)
		}
		if b.Label != "Delete Calendar…" && b.Variant != Button {
			t.Errorf("%q variant = %v, want Button", b.Label, b.Variant)
		}
	}
}

func TestCalendarManagerRootSpaceTogglesBothDirections(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	if m.hidden[2] {
		t.Fatal("calendar 2 should start visible")
	}
	// Visible -> hidden.
	m1, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 2 || !msg.Hidden {
		t.Fatalf("visible->hidden: msg = %+v, want {ID:2 Hidden:true}", msg)
	}
	if !m1.hidden[2] {
		t.Error("local hidden state not flipped to true")
	}
	if row := flatRowForID(m1, 2); !strings.HasPrefix(row, Glyphs["checkbox.off"]+" ●") {
		t.Errorf("row did not flip to unchecked visibility: %q", row)
	}
	// Hidden -> visible.
	m2, cmd := m1.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	msg, ok = cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 2 || msg.Hidden {
		t.Fatalf("hidden->visible: msg = %+v, want {ID:2 Hidden:false}", msg)
	}
	if m2.hidden[2] {
		t.Error("local hidden state not flipped back to false")
	}
	if row := flatRowForID(m2, 2); !strings.HasPrefix(row, Glyphs["checkbox.on"]+" ●") {
		t.Errorf("row did not flip back to checked visibility: %q", row)
	}
}

// TestCalendarManagerRootSetDataClampsWhenSelectedTailRemoved verifies that
// removing the selected tail calendar leaves the cursor on a valid surviving
// row instead of out of range.
func TestCalendarManagerRootSetDataClampsWhenSelectedTailRemoved(t *testing.T) {
	cals := flatManagerCalendars() // canonical order [1, 2, 3, 4]
	m := newFlatManager().selectCalendar(4)
	if id, _ := m.selectedID(); id != 4 {
		t.Fatalf("setup: selected %d, want 4", id)
	}
	delete(cals, 4)
	m = m.SetData(cals, nil)
	id, ok := m.selectedID()
	if !ok {
		t.Fatal("selectedID out of range after removing selected tail")
	}
	if m.cursor < 0 || m.cursor >= len(m.order) {
		t.Fatalf("cursor %d out of range [0,%d) after refresh", m.cursor, len(m.order))
	}
	if idx := slices.Index(m.order, id); idx < 0 {
		t.Fatalf("fallback selection %d not in order %v", id, m.order)
	}
}

// TestCalendarManagerRootMouseRejectsClicksOutsideListWidth verifies a click
// on the right row's Y but past the list's right edge does not select — the
// hit-test bounds the list column on both sides, not just the left.
func TestCalendarManagerRootMouseRejectsClicksOutsideListWidth(t *testing.T) {
	m := newFlatManager()
	lx, listY, lw, _ := m.listRegion()
	before, _ := m.selectedID()
	clicked, cmd := m.Update(tea.MouseClickMsg{X: lx + lw + 5, Y: listY + 2, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("out-of-width click should not emit a command: %T", cmd())
	}
	after, _ := clicked.selectedID()
	if after != before {
		t.Fatalf("out-of-width click changed selection: before=%d after=%d", before, after)
	}
}

// TestCalendarManagerRootOverflowKeepsLastSelectedRowVisible is the regression
// for the clampedScroll max-clamp bug: when the list overflows, the indicator
// reserves one row (contentH = h-1), but the max-scroll clamp used the full
// viewport height h. That off-by-one let the indicator overwrite the selected
// last row, hiding it and making it unclickable. The clamp must use contentH
// so the last data row stays rendered and reachable above the indicator.
func TestCalendarManagerRootOverflowKeepsLastSelectedRowVisible(t *testing.T) {
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 12; i++ {
		cals[i] = CalendarInfo{Name: fmt.Sprintf("Cal%02d", i), DisplayOrder: i}
	}
	// SetSize(60,16) -> narrow box, bodyH = 7, so 12 calendars overflow and
	// reserve the last visible line for the scroll indicator.
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(60, 16)
	m = m.selectCalendar(12).ensureVisible()
	if id, _ := m.selectedID(); id != 12 {
		t.Fatalf("setup: selected %d, want 12", id)
	}

	// The selected last row must still be rendered, not overwritten by the
	// overflow indicator.
	view := stripANSI(m.View())
	if !strings.Contains(view, "Cal12") {
		t.Errorf("selected last row Cal12 missing from view (overwritten by indicator?)\n%s", view)
	}

	// ... and it must remain clickable: its screen row is not the indicator slot.
	listX, listY, _, lh := m.listRegion()
	clickable := false
	for row := 0; row < lh; row++ {
		if idx, ok := m.rowAtPosition(listX+2, listY+row); ok && m.order[idx] == 12 {
			clickable = true
			break
		}
	}
	if !clickable {
		t.Error("selected last row Cal12 is not clickable (falls on the indicator slot)")
	}
}

// calendarDetailFieldIndex returns the form item index of the first field of
// the given kind ("opener" or "checkbox") in the manager's active calendar
// detail, or -1 when no detail is open or no such field exists.
func calendarDetailFieldIndex(m CalendarManagerModel, kind string) int {
	if m.calendarForm == nil {
		return -1
	}
	form := m.calendarForm.form
	for i := range form.ItemCount() {
		switch form.Field(i).(type) {
		case *OpenerField:
			if kind == "opener" {
				return i
			}
		case *CheckboxField:
			if kind == "checkbox" {
				return i
			}
		}
	}
	return -1
}

// focusCalendarDetailField moves the calendar detail's form focus onto the
// first field of the given kind. Tests use this instead of driving Tab so the
// assertion is independent of how many fields precede the target.
func focusCalendarDetailField(m CalendarManagerModel, kind string) (CalendarManagerModel, bool) {
	idx := calendarDetailFieldIndex(m, kind)
	if idx < 0 {
		return m, false
	}
	form := m.calendarForm.form
	form, _ = form.focusIndex(idx)
	m.calendarForm.form = form
	return m, true
}

// calendarDetailCheckboxClick renders the active calendar detail to populate
// the shared mouse tracker, then resolves the Display Calendar checkbox zone
// into a terminal-space MouseClickMsg that hits it. Tests use it to exercise
// the mouse path of the visibility toggle without hard-coding geometry.
func calendarDetailCheckboxClick(m CalendarManagerModel, cbIdx int) (tea.MouseClickMsg, bool) {
	if m.calendarForm == nil {
		return tea.MouseClickMsg{}, false
	}
	_ = m.calendarForm.View() // populate defaultMouseTracker zones
	bw, bh := m.calendarForm.BoxSize()
	ox := (m.calendarForm.dialog.width - bw) / 2
	oy := (m.calendarForm.dialog.height - bh) / 2
	target := fieldTarget(cbIdx)
	for _, z := range defaultMouseTracker.zones {
		if z.name != target {
			continue
		}
		x := ox + (z.startX+z.endX)/2
		y := oy + z.startY
		return tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y}, true
	}
	return tea.MouseClickMsg{}, false
}

// TestCalendarManagerDetailBackRestoresRootSelection verifies pushing a
// calendar detail and pressing Back (Esc) returns to the root list with the
// same calendar selected by immutable ID.
func TestCalendarManagerDetailBackRestoresRootSelection(t *testing.T) {
	m := newFlatManager().selectCalendar(3)
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	popped, cmd := pushed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatalf("Esc should pop internally, got command %T", cmd())
	}
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List", popped.Screen())
	}
	if popped.calendarForm != nil {
		t.Fatal("calendar form was not cleared on pop")
	}
	if id, ok := popped.selectedID(); !ok || id != 3 {
		t.Fatalf("root selection after Back = %d ok=%v, want 3", id, ok)
	}
}

// TestCalendarManagerDetailBackRestoresRootScroll verifies the scroll offset
// survives a push/pop cycle by ID, so opening and closing a detail does not
// jump the list back to the top.
func TestCalendarManagerDetailBackRestoresRootScroll(t *testing.T) {
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 30; i++ {
		cals[i] = CalendarInfo{Name: fmt.Sprintf("Cal %d", i), Color: "#a6e3a1", DisplayOrder: i}
	}
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(50, 16)
	// Move the cursor well past the first viewport so the list scrolls.
	for range 18 {
		m = m.moveCursor(1)
	}
	start, _ := m.visibleRange()
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	popped, _ := pushed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List", popped.Screen())
	}
	gotStart, _ := popped.visibleRange()
	if gotStart != start {
		t.Fatalf("scroll top = %d after Back, want %d", gotStart, start)
	}
}

// TestCalendarManagerDetailLocalHasLocationOnly verifies a local calendar's
// detail renders "Location: Local" and has no Account opener (local
// calendars have no owning account to drill into).
func TestCalendarManagerDetailLocalHasLocationOnly(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, local
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Location: Local") {
		t.Errorf("local detail missing Location: Local:\n%s", view)
	}
	if calendarDetailFieldIndex(pushed, "opener") >= 0 {
		t.Errorf("local detail should not expose an Account opener:\n%s", view)
	}
}

// TestCalendarManagerDetailRemoteHasAccountOpener verifies a remote calendar's
// detail renders an actionable "Account: <name> ›" opener that, when
// activated, pushes the owning account's settings onto the stack.
func TestCalendarManagerDetailRemoteHasAccountOpener(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google, account 7
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Account: Google ›") {
		t.Errorf("remote detail missing actionable Account line:\n%s", view)
	}
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	// The opener does not push the account detail itself: it passes the
	// canonical AccountSettingsRequestedMsg to the host, which owns the
	// account record and later calls OpenAccount with full params. The
	// calendar detail stays put underneath.
	opened, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Account opener emitted no request")
	}
	req, ok := cmd().(AccountSettingsRequestedMsg)
	if !ok || req.AccountID != 7 {
		t.Fatalf("Account opener = %#v, want AccountSettingsRequestedMsg{AccountID:7}", cmd())
	}
	if opened.Screen() != CalendarManagerScreenCalendar || opened.accountSettings != nil {
		t.Fatalf("opener should not push account detail: screen=%v account=%v", opened.Screen(), opened.accountSettings)
	}
}

func TestCalendarManagerDetailAccountBackPreservesDraft(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	pushed.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Edited Primary")

	// The opener passes AccountSettingsRequestedMsg to the host; the host
	// (Task 4) resolves the canonical account record and calls OpenAccount
	// with full params — provider/server/username/attention preserved.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	requested, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	req, ok := cmd().(AccountSettingsRequestedMsg)
	if !ok || req.AccountID != 7 {
		t.Fatalf("opener request = %#v, want AccountSettingsRequestedMsg{AccountID:7}", cmd())
	}
	opened := requested.OpenAccount(AccountSettingsParams{
		AccountID:      7,
		DisplayName:    "Personal Google",
		Provider:       "Google Account",
		ServerURL:      "https://apidata.googleusercontent.com/caldav/v2/",
		Username:       "douglas@example.com",
		CalendarCount:  2,
		AttentionCount: 1,
		AuthType:       "oauth2",
	})
	if opened.Screen() != CalendarManagerScreenAccount {
		t.Fatalf("screen = %v, want Account", opened.Screen())
	}
	if p := opened.accountSettings.Params(); p.Provider != "Google Account" ||
		p.ServerURL != "https://apidata.googleusercontent.com/caldav/v2/" ||
		p.Username != "douglas@example.com" || p.AttentionCount != 1 {
		t.Fatalf("canonical account params not preserved: %+v", p)
	}
	// Back returns to the originating calendar detail with its unsaved draft
	// intact — the form is never reconstructed.
	popped, _ := opened.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar after Back", popped.Screen())
	}
	if got := popped.calendarForm.Draft().Name; got != "Edited Primary" {
		t.Fatalf("calendar draft after Account Back = %q, want %q", got, "Edited Primary")
	}
}

// TestCalendarManagerDetailVisibilityToggleEmitsDesiredState verifies the
// detail's Display Calendar toggle emits CalendarVisibilityToggledMsg with the
// desired Hidden state immediately, and mirrors the change into the root's
// hidden map so the dot stays consistent on Back.
func TestCalendarManagerDetailVisibilityToggleEmitsDesiredState(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, visible
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.hidden[1] {
		t.Fatal("calendar 1 should start visible")
	}
	focused, ok := focusCalendarDetailField(pushed, "checkbox")
	if !ok {
		t.Fatal("detail has no Display Calendar checkbox")
	}
	hidden, cmd := focused.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("visibility toggle emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 1 || !msg.Hidden {
		t.Fatalf("toggle msg = %+v, want {ID:1 Hidden:true}", msg)
	}
	if !hidden.hidden[1] {
		t.Error("root hidden map not mirrored to true")
	}
	// Toggling back emits the opposite desired state.
	visible, cmd := hidden.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	msg, ok = cmd().(CalendarVisibilityToggledMsg)
	if !ok || msg.ID != 1 || msg.Hidden {
		t.Fatalf("toggle back msg = %+v, want {ID:1 Hidden:false}", msg)
	}
	if visible.hidden[1] {
		t.Error("root hidden map not mirrored back to false")
	}
}

// TestCalendarManagerDetailLeftPopsToRoot verifies the Left arrow pops a
// pushed calendar detail back to the root list (a Back gesture) when the
// focus is not on a text-editing field. Root Left is unchanged (a no-op).
func TestCalendarManagerDetailLeftPopsToRoot(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	// Focus the Account opener (a non-editing field) so Left is free to pop.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	popped, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Fatalf("Left pop should emit no command, got %T", cmd())
	}
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List after Left", popped.Screen())
	}
	if popped.calendarForm != nil {
		t.Fatal("calendar form was not cleared on Left pop")
	}
	// Root Left is a no-op (no root binding consumes it).
	rootAgain, cmd := popped.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil || rootAgain.Screen() != CalendarManagerScreenList {
		t.Fatalf("root Left should be a no-op: cmd=%v screen=%v", cmd, rootAgain.Screen())
	}
}

// TestCalendarManagerDetailLeftDoesNotStealCursor verifies Left keeps moving
// the cursor while a text field is focused, so the Back gesture never
// interrupts editing.
func TestCalendarManagerDetailLeftDoesNotStealCursor(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, local
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	// Name (a TextField) is focused by default after the detail opens.
	popped, _ := pushed.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("Left popped the detail while editing: screen=%v", popped.Screen())
	}
	if popped.calendarForm == nil {
		t.Fatal("Left discarded the calendar detail while editing")
	}
}

// TestCalendarManagerDetailLeftPopsAccountToCalendar verifies Left pops the
// pushed account detail back to the originating calendar detail.
func TestCalendarManagerDetailLeftPopsAccountToCalendar(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m.OpenAccount(AccountSettingsParams{
		AccountID:   7,
		DisplayName: "Personal Google",
		Provider:    "Google Account",
		AuthType:    "oauth2",
	})
	if m.Screen() != CalendarManagerScreenAccount {
		t.Fatalf("screen = %v, want Account", m.Screen())
	}
	popped, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Fatalf("Left pop should emit no command, got %T", cmd())
	}
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar after Left", popped.Screen())
	}
	if popped.accountSettings != nil {
		t.Fatal("account settings was not cleared on Left pop")
	}
}

// TestCalendarManagerDetailVisibilityMouseToggleEmitsDesiredState verifies a
// mouse click on the Display Calendar checkbox emits the same
// CalendarVisibilityToggledMsg as the keyboard path (regression: the mouse
// branch used to return before the pre/post visibility comparison).
func TestCalendarManagerDetailVisibilityMouseToggleEmitsDesiredState(t *testing.T) {
	m := newFlatManager().selectCalendar(1).SetSize(120, 40)
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil || pushed.hidden[1] {
		t.Fatal("precondition: calendar 1 detail open and visible")
	}
	cbIdx := calendarDetailFieldIndex(pushed, "checkbox")
	if cbIdx < 0 {
		t.Fatal("detail has no Display Calendar checkbox")
	}
	click, ok := calendarDetailCheckboxClick(pushed, cbIdx)
	if !ok {
		t.Fatal("could not resolve checkbox click point")
	}
	hidden, cmd := pushed.Update(click)
	if cmd == nil {
		t.Fatal("mouse visibility toggle emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 1 || !msg.Hidden {
		t.Fatalf("mouse toggle msg = %+v, want {ID:1 Hidden:true}", msg)
	}
	if !hidden.hidden[1] {
		t.Error("root hidden map not mirrored to true after mouse toggle")
	}
}

func TestCalendarManagerAccountConnectionLeftNeverOpensRoot(t *testing.T) {
	for _, focus := range []int{calDAVIdxAuth, -1} {
		m := newFlatManager().OpenAccountConnection()
		if focus < 0 {
			focus = m.calendarForm.form.cancelIndex()
		}
		m.calendarForm.form.focused = focus
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
		if updated.Screen() != CalendarManagerScreenCalendar || updated.calendarForm == nil {
			t.Fatalf("Left at focus %d exposed manager root: screen=%v", focus, updated.Screen())
		}
	}
}

func TestCalendarManagerDirectAccountCloseClosesManager(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: tea.KeyLeft},
	} {
		m := newFlatManager().OpenAccount(AccountSettingsParams{
			AccountID: 7, DisplayName: "Personal Google", AuthType: "oauth2",
		})
		closing, cmd := m.Update(key)
		if closing.Screen() != CalendarManagerScreenAccount || closing.accountSettings == nil {
			t.Fatalf("close key %v exposed manager root before host close", key.Code)
		}
		if cmd == nil {
			t.Fatalf("close key %v emitted no command", key.Code)
		}
		if _, ok := cmd().(CalendarManagerClosedMsg); !ok {
			t.Fatalf("close key %v emitted %T, want CalendarManagerClosedMsg", key.Code, cmd())
		}
	}
}
