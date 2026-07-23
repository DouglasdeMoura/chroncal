package tui

import (
	"image"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// calendarManagerAddItem is one row of the anchored Add menu. The table is
// fixed: there is no heap-built command model, so selection maps directly to a
// typed manager target.
type calendarManagerAddItem struct {
	label  string
	target CalendarManagerTarget
}

// calendarManagerAddItems is the canonical, order-stable Add menu. Order
// matters: the cursor index selects the target, and tests assert the exact
// rendered rows.
var calendarManagerAddItems = [...]calendarManagerAddItem{
	{label: "New Calendar…", target: CalendarManagerTargetLocalCreate},
	{label: "Add Account…", target: CalendarManagerTargetAccountConnect},
	{label: "Import Calendar File…", target: CalendarManagerTargetImport},
}

// calendarManagerMenuTrailing is the blank padding reserved after the longest
// label so the menu reads as a balanced pill rather than a tight fit.
const calendarManagerMenuTrailing = 3

// openAddMenu opens the anchored Add menu with the cursor on the first row and
// root focus on the + Add action, so a dismissed menu (or one whose row pushed
// a screen) always returns there. It emits no command: the host only acts once
// a row is chosen.
func (m CalendarManagerModel) openAddMenu() CalendarManagerModel {
	if !m.sourceAddActionActive() {
		return m
	}
	m.addMenuOpen = true
	m.addMenuCursor = 0
	m.rootFocus = rootFocusAdd
	return m.applyRootFocus()
}

// closeAddMenu dismisses the anchored Add menu and re-establishes root focus
// on the + Add action without touching the rest of the manager state, so Esc,
// outside clicks, and row activations all return there.
func (m CalendarManagerModel) closeAddMenu() CalendarManagerModel {
	m.addMenuOpen = false
	m.rootFocus = rootFocusAdd
	return m.applyRootFocus()
}

// updateAddMenu owns all input while the menu is open. Up/Down and j/k clamp
// within the three rows; Tab/Shift-Tab wrap the cursor one row around using the
// same focus bindings as the root ring; Enter/Space activate the selected row
// through the shared activation binding and dismiss; Esc dismisses without
// closing the manager; mouse hits are routed to handleAddMenuMouse.
// Unrecognized keys are swallowed so they never reach the root list underneath.
func (m CalendarManagerModel) updateAddMenu(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.Code {
		case tea.KeyEscape:
			return m.closeAddMenu(), nil
		case tea.KeyUp, 'k':
			m.addMenuCursor = max(m.addMenuCursor-1, 0)
			return m, nil
		case tea.KeyDown, 'j':
			m.addMenuCursor = min(m.addMenuCursor+1, len(calendarManagerAddItems)-1)
			return m, nil
		}
		n := len(calendarManagerAddItems)
		// Tab/Shift-Tab wrap the cursor one row around, reusing the root
		// focus ring bindings so the keys stay consistent across the manager.
		if key.Matches(msg, m.keys.Next) {
			m.addMenuCursor = (m.addMenuCursor + 1) % n
			return m, nil
		}
		if key.Matches(msg, m.keys.Prev) {
			m.addMenuCursor = (m.addMenuCursor - 1 + n) % n
			return m, nil
		}
		// Enter/Space activate the selected row and emit its typed target.
		if key.Matches(msg, m.keys.Activate) {
			return m.closeAddMenu(), m.requestTarget(calendarManagerAddItems[m.addMenuCursor].target)
		}
		return m, nil
	case tea.MouseClickMsg:
		return m.handleAddMenuMouse(msg)
	}
	return m, nil
}

// requestTarget builds the typed command for a chosen menu target. It is the
// single emission point for every Add-menu action.
func (m CalendarManagerModel) requestTarget(target CalendarManagerTarget) tea.Cmd {
	return func() tea.Msg { return CalendarManagerRequestedMsg{Target: target} }
}

// addMenuContentWidth returns the menu's inner width (between its own borders)
// in terminal cells: the longest label plus a leading space and trailing
// padding, capped to the manager's interior so the whole menu always fits
// inside the box even on very narrow terminals. ANSI styling never affects the
// measurement because lipgloss.Width ignores escapes.
func (m CalendarManagerModel) addMenuContentWidth() int {
	longest := 0
	for _, item := range calendarManagerAddItems {
		longest = max(longest, lipgloss.Width(item.label))
	}
	natural := longest + 1 + calendarManagerMenuTrailing
	boxW, _ := m.boxSize()
	// The menu (content + 2 borders) must fit between the box's borders.
	maxContent := max(boxW-4, 1)
	return min(natural, maxContent)
}

// addMenuWidth returns the menu's outer width: content width plus the two
// rounded-border cells.
func (m CalendarManagerModel) addMenuWidth() int {
	return m.addMenuContentWidth() + 2
}

// addMenuHeight returns the menu's outer height: one top border, one row per
// item, one bottom border.
func addMenuHeight() int { return len(calendarManagerAddItems) + 2 }

// addMenuBoxRect returns the menu rectangle in box-local coordinates, anchored
// above the source-list Add action and clamped inside the manager box. The
// menu is left-aligned with the action and shifted left or down only as much
// as needed to keep every cell inside the box interior.
func (m CalendarManagerModel) addMenuBoxRect() (int, int, int, int) {
	boxW, boxH := m.boxSize()
	mw := m.addMenuWidth()
	mh := addMenuHeight()
	// The action sits on the last body row; anchor the menu's bottom border
	// one row above it (on the reserved blank spacer).
	actionY := addMenuActionBoxY(m)
	my := actionY - mh
	mx := addMenuContentBoxX()
	if mx+mw > boxW-1 {
		mx = boxW - 1 - mw
	}
	if mx < 1 {
		mx = 1
	}
	if my+mh > boxH-1 {
		my = boxH - 1 - mh
	}
	if my < 1 {
		my = 1
	}
	return mx, my, mw, mh
}

// addMenuRect returns the menu rectangle in screen-space coordinates, for mouse
// hit-testing and geometry assertions.
func (m CalendarManagerModel) addMenuRect() (int, int, int, int) {
	boxW, boxH := m.boxSize()
	mx, my, mw, mh := m.addMenuBoxRect()
	return mx + (m.width-boxW)/2, my + (m.height-boxH)/2, mw, mh
}

// renderAddMenu renders the rounded, single-border menu with three full-width
// rows. The selected row uses the theme's selected-row treatment; unselected
// rows use normal text. No title, help, buttons, or Cancel row.
func (m CalendarManagerModel) renderAddMenu() string {
	contentW := m.addMenuWidth() - 2
	border := strings.Repeat("─", contentW)
	rows := make([]string, 0, addMenuHeight())
	rows = append(rows, "╭"+border+"╮")
	for i, item := range calendarManagerAddItems {
		rows = append(rows, m.renderAddMenuRow(item.label, i == m.addMenuCursor, contentW))
	}
	rows = append(rows, "╰"+border+"╯")
	return strings.Join(rows, "\n")
}

// renderAddMenuRow renders one interior row. The label is left-padded by a
// single space and right-padded to contentW so every row is exactly the menu's
// inner width regardless of label length.
func (m CalendarManagerModel) renderAddMenuRow(label string, selected bool, contentW int) string {
	avail := max(contentW-1, 0)
	label = truncateTo(label, avail)
	trailing := max(avail-lipgloss.Width(label), 0)
	text := " " + label + strings.Repeat(" ", trailing)
	style := lipgloss.NewStyle().Foreground(m.theme.Text)
	if selected {
		style = lipgloss.NewStyle().Background(m.theme.Selected).Foreground(m.theme.SelectedText)
	}
	return "│" + style.Render(text) + "│"
}

// handleAddMenuMouse maps a click to a menu row, consumes border-cell clicks
// without routing, and dismisses on any click outside the menu. Outside and
// border clicks are consumed: they never fall through to activate an
// underlying list row. A click on a border cell (the rounded edges or the
// left/right │ columns) keeps the menu open without activating a row.
func (m CalendarManagerModel) handleAddMenuMouse(msg tea.MouseClickMsg) (CalendarManagerModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	mx, my, mw, mh := m.addMenuRect()
	if msg.X < mx || msg.X >= mx+mw || msg.Y < my || msg.Y >= my+mh {
		return m.closeAddMenu(), nil
	}
	// Consume clicks on the left/right border columns without activating a
	// row or dismissing: the cursor must be strictly inside the borders.
	if msg.X <= mx || msg.X >= mx+mw-1 {
		return m, nil
	}
	row := msg.Y - my - 1 // skip the top border
	if row < 0 || row >= len(calendarManagerAddItems) {
		return m, nil // clicked the top or bottom border row
	}
	return m.closeAddMenu(), m.requestTarget(calendarManagerAddItems[row].target)
}

// composeAddMenu draws the menu over the already-rendered manager shell using
// an Ultraviolet screen buffer local to this component. The buffer is sized to
// the shell's actual rendered height (which can exceed the nominal box height
// on very shallow terminals) so neither the shell nor the menu is clipped.
func (m CalendarManagerModel) composeAddMenu(base string) string {
	boxW, _ := m.boxSize()
	if boxW <= 0 || base == "" {
		return base
	}
	height := strings.Count(base, "\n") + 1
	if height <= 0 {
		return base
	}
	buf := uv.NewScreenBuffer(boxW, height)
	uv.NewStyledString(base).Draw(buf, buf.Bounds())
	mx, my, mw, mh := m.addMenuBoxRect()
	rect := image.Rect(mx, my, mx+mw, my+mh)
	uv.NewStyledString(m.renderAddMenu()).Draw(buf, rect)
	return buf.Render()
}
