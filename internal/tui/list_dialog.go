package tui

import (
	"image/color"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// ListDialogAction is a button rendered in the detail-pane action bar.
type ListDialogAction struct {
	Label    string
	Msg      func() tea.Msg
	Danger   bool
	Primary  bool
	Disabled bool
}

// ListDialogZone identifies the focused region inside the dialog.
type ListDialogZone int

const (
	ListZoneList ListDialogZone = iota
	ListZoneActions
	// ListZoneTitleAction means the right-aligned title-line button owns
	// focus. Participates in Tab cycling so every focusable element in the
	// dialog is reachable by keyboard.
	ListZoneTitleAction
	// ListZoneCustom lets callers signal "focus is in a region the shell
	// doesn't manage" (e.g. the RSVP row in the event dialog). In that
	// state the shell renders list and actions as unfocused.
	ListZoneCustom
)

// ListDialogModel is the shared two-column (list + details) dialog chrome
// reused by the calendar-management and day-events dialogs. It owns the
// outer border, title, list render, divider, action bar, help row, and
// the narrow/stacked fallback. Callers supply:
//
//   - pre-rendered row labels (swatch + name, time + title, …)
//   - pre-rendered detail lines for the selected row
//   - action buttons
//
// Everything else lives here: selection tint, scroll, zone cycle, and
// hit-test. Each dialog then collapses to its domain concerns.
type ListDialogModel struct {
	title        string
	titleContext string

	subtitle string

	titleAction *ListDialogAction
	rows        []string
	rowDisabled []bool

	detailTitle   string
	detailLines   []string
	emptyList     string
	emptyDetails  []string
	actions       []ListDialogAction
	shortHelp     []key.Binding
	keys          ListDialogKeys
	help          help.Model
	selected      int
	scroll        int
	focusedAction int
	focusZone     ListDialogZone
	selectedColor color.Color
	width, height int
	body          viewport.Model
	// cache holds per-frame memoized sub-renders that don't depend on
	// the selection (action bar, help line). It's a pointer so that
	// value copies of the model — which the bubbletea Update cycle
	// produces continuously — share the same cache and don't have to
	// re-render unchanging chrome on every keystroke.
	cache *viewRenderCache
}

// viewRenderCache memoizes the action bar and help line. Each entry
// stores both the rendered string and a fingerprint computed from the
// inputs that affect it. The cache is invalidated lazily by a compare
// of fingerprints on read, not eagerly on Set*.
type viewRenderCache struct {
	actionsKey uint64
	actions    string
	helpKey    uint64
	help       string
}

func (m ListDialogModel) SetSize(w, h int) ListDialogModel {
	m.width, m.height = w, h
	m.syncBody()
	return m
}
func (m ListDialogModel) SetTitle(t string) ListDialogModel    { m.title = t; return m }
func (m ListDialogModel) SetSubtitle(s string) ListDialogModel { m.subtitle = s; return m }
func (m ListDialogModel) SetTitleContext(s string) ListDialogModel {
	m.titleContext = s
	return m
}

// SetTitleAction installs a right-aligned button on the title line, or clears
// it when a is nil. Use for creation actions ("New", …) that belong to the
// dialog as a whole rather than the currently selected row.
func (m ListDialogModel) SetTitleAction(a *ListDialogAction) ListDialogModel {
	m.titleAction = a
	if (a == nil || a.Disabled) && m.focusZone == ListZoneTitleAction {
		m.focusZone = ListZoneList
	}
	return m
}

func (m ListDialogModel) SetSelectedColor(c color.Color) ListDialogModel {
	m.selectedColor = c
	return m
}

// SetRows replaces the list rows. The caller is responsible for a pre-render
// of each row (swatch, time prefix, …). Scroll and selection are clamped.
// Disabled-row state is cleared because it belongs to the row set.
// It does not touch the body viewport. Rows live in the left column only.
// The body shows the right-column details. Other setters
// (SetDetailLines, SetActions, SetDetailTitle, SetEmptyList, SetSize)
// trigger syncBody when they actually need it.
func (m ListDialogModel) SetRows(rows []string) ListDialogModel {
	m.rows = rows
	m.rowDisabled = nil
	if m.selected >= len(rows) {
		m.selected = max(len(rows)-1, 0)
	}
	return m
}

// SetDisabledRows marks structural or unavailable rows as non-selectable.
// Keyboard and mouse navigation skip them while the labels remain visible.
func (m ListDialogModel) SetDisabledRows(indices []int) ListDialogModel {
	m.rowDisabled = make([]bool, len(m.rows))
	for _, idx := range indices {
		if idx >= 0 && idx < len(m.rowDisabled) {
			m.rowDisabled[idx] = true
		}
	}
	if m.rowIsDisabled(m.selected) {
		m.selected = m.firstSelectableRow()
		m.body.GotoTop()
	}
	return m
}

func (m ListDialogModel) rowIsDisabled(idx int) bool {
	return idx >= 0 && idx < len(m.rowDisabled) && m.rowDisabled[idx]
}

func (m ListDialogModel) firstSelectableRow() int {
	for idx := range m.rows {
		if !m.rowIsDisabled(idx) {
			return idx
		}
	}
	return 0
}

// SetSelected moves the selection to idx (clamped). Disabled rows resolve to
// the next selectable row, then the previous one when there is none below.
// The detail viewport scrolls back to the top when the selection changes.
func (m ListDialogModel) SetSelected(idx int) ListDialogModel {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.rows) {
		idx = max(len(m.rows)-1, 0)
	}
	if m.rowIsDisabled(idx) {
		next := idx + 1
		for next < len(m.rows) && m.rowIsDisabled(next) {
			next++
		}
		if next < len(m.rows) {
			idx = next
		} else {
			previous := idx - 1
			for previous >= 0 && m.rowIsDisabled(previous) {
				previous--
			}
			if previous >= 0 {
				idx = previous
			}
		}
	}
	if idx != m.selected {
		m.body.GotoTop()
	}
	m.selected = idx
	return m
}

// Selected returns the current selection index (0 when the list is empty).
func (m ListDialogModel) Selected() int { return m.selected }

// FocusZone returns the currently focused region.
func (m ListDialogModel) FocusZone() ListDialogZone { return m.focusZone }

// SetFocusZone lets callers override focus (e.g. to ListZoneCustom when
// owning a region the shell doesn't manage).
func (m ListDialogModel) SetFocusZone(z ListDialogZone) ListDialogModel {
	m.focusZone = z
	return m
}

// HasTitleAction reports whether a title-line button is installed. Callers
// that manage their own Tab order use this to decide whether to include
// the title action as a focus stop.
func (m ListDialogModel) HasTitleAction() bool { return m.titleAction != nil }

// FocusedAction returns the index of the currently focused action button.
// Only meaningful when FocusZone() == ListZoneActions.
func (m ListDialogModel) FocusedAction() int { return m.focusedAction }

// SelectedColor returns the theme color used to tint the selected row
// when the list does not own focus. Callers apply it themselves. The
// tint then composes with their own row-level style (calendar swatch, RSVP
// indicators, and others). The shell does not need to know about those.
func (m ListDialogModel) SelectedColor() color.Color { return m.selectedColor }

// SetDetailLines replaces the detail-pane body lines for the currently
// selected row. The caller rebuilds these whenever selection changes.
func (m ListDialogModel) SetDetailLines(lines []string) ListDialogModel {
	m.detailLines = lines
	m.syncBody()
	return m
}

// SetDetailTitle pins a title row above the scrollable body. The shell
// renders it as a bold line plus a faint horizontal rule that stay in
// place while the body scrolls. That is the same anchor users see in the
// single-event dialog. Empty string clears the pinned title.
func (m ListDialogModel) SetDetailTitle(t string) ListDialogModel {
	m.detailTitle = t
	m.syncBody()
	return m
}

// SetEmptyList configures what shows on the left when rows is empty.
// emptyDetails render in the detail pane in that same state.
func (m ListDialogModel) SetEmptyList(listMsg string, details []string) ListDialogModel {
	m.emptyList = listMsg
	m.emptyDetails = details
	m.syncBody()
	return m
}

// SetActions replaces the action-bar buttons and clamps the focused index.
func (m ListDialogModel) SetActions(actions []ListDialogAction) ListDialogModel {
	m.actions = actions
	if m.focusedAction < 0 || m.focusedAction >= len(actions) || actions[m.focusedAction].Disabled {
		m.focusedAction = m.firstEnabledAction()
	}

	if m.focusZone == ListZoneActions && m.firstEnabledAction() < 0 {
		m.focusZone = ListZoneList
	}
	m.syncBody()
	return m
}

func (m ListDialogModel) firstEnabledAction() int {
	for idx, action := range m.actions {
		if !action.Disabled {
			return idx
		}
	}
	return -1
}

// syncBody pushes the current detail dimensions and content into the body
// viewport. HandleKey/HandleMouseWheel can then scroll with no wait for the
// next View() call to learn about layout. Width/height/content match the
// values renderDetails would compute for the same model state.
func (m *ListDialogModel) syncBody() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	var detailW, detailH int
	if m.isNarrow() {
		rowCount := max(len(m.rows), 1)
		listH := min(max(rowCount+1, 3), max(bodyH/3, 3))
		detailW = innerW
		detailH = max(bodyH-listH-1, 3)
	} else {
		detailW = detailColumnWidth(innerW)
		detailH = bodyH
	}
	if len(m.actions) > 0 {
		detailH = max(detailH-2, 1)
	}
	if m.hasPinnedTitle() {
		detailH = max(detailH-2, 1)
	}

	lines := m.detailLines
	if len(m.rows) == 0 {
		lines = m.emptyDetails
	}
	m.body.SetWidth(detailW)
	m.body.SetHeight(detailH)
	m.body.SetContentLines(lines)
}

// hasPinnedTitle reports whether the shell should reserve two lines at the
// top of the detail pane for a pinned title row. The empty-state pane
// (no rows) intentionally skips the title since emptyDetails carries its
// own message.
func (m ListDialogModel) hasPinnedTitle() bool {
	return m.detailTitle != "" && len(m.rows) > 0
}

// SetShortHelp replaces the bottom help-line key bindings.
func (m ListDialogModel) SetShortHelp(bindings []key.Binding) ListDialogModel {
	m.shortHelp = bindings
	return m
}

// BoxSize returns the rendered dialog's outer dimensions so the caller can
// position it on screen.
func (m ListDialogModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return m.boxSize()
}

// goldenCellRatio keeps the dialog visually close to a golden rectangle on
// screen. Terminal cells are roughly twice as tall as wide, so the cell
// aspect is ~2φ ≈ 3.24 to render as φ:1 to the eye.
const goldenCellRatio = 3.236

func (m ListDialogModel) boxSize() (int, int) {
	if m.isNarrow() {
		return max(m.width-4, 20), max(m.height-4, 14)
	}
	boxH := min(max(m.height*2/3, 14), m.height-2)
	boxW := int(float64(boxH) * goldenCellRatio)
	if boxW > m.width-2 {
		boxW = m.width - 2
		boxH = min(max(int(float64(boxW)/goldenCellRatio), 14), m.height-2)
	}
	if boxW < 50 {
		boxW = 50
	}
	return boxW, boxH
}

func (m ListDialogModel) isNarrow() bool { return m.width < narrowThreshold }
