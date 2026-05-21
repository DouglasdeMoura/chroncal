package tui

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ListDialogAction is a button rendered in the detail-pane action bar.
type ListDialogAction struct {
	Label   string
	Msg     func() tea.Msg
	Danger  bool
	Primary bool
}

// ListDialogKeys is the minimal key map the shell understands. Callers embed
// it in their own dialog-specific key map and wire additional hotkeys
// (e.g. Edit/Delete/RSVP) on top.
type ListDialogKeys struct {
	Up       key.Binding
	Down     key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Enter    key.Binding
	Close    key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding
}

func defaultListDialogKeys() ListDialogKeys {
	return ListDialogKeys{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "sections")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev section")),
		Enter:    key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "select")),
		Close:    key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "close")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f")),
		Home:     key.NewBinding(key.WithKeys("home")),
		End:      key.NewBinding(key.WithKeys("end")),
	}
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
// outer border, title, list rendering, divider, action bar, help row, and
// the narrow/stacked fallback. Callers supply:
//
//   - pre-rendered row labels (swatch + name, time + title, …)
//   - pre-rendered detail lines for the selected row
//   - action buttons
//
// Everything else (selection tint, scroll, zone cycling, hit-testing) lives
// here so each dialog collapses to its domain concerns.
type ListDialogModel struct {
	title         string
	titleAction   *ListDialogAction
	rows          []string
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
// inputs that affect it; the cache is invalidated lazily by comparing
// fingerprints on read, not eagerly on Set*.
type viewRenderCache struct {
	actionsKey uint64
	actions    string
	helpKey    uint64
	help       string
}

// NewListDialogModel builds an empty shell. Callers call the Setters on the
// returned value before rendering.
func NewListDialogModel(h help.Model) ListDialogModel {
	vp := viewport.New()
	vp.MouseWheelEnabled = true
	return ListDialogModel{
		keys:  defaultListDialogKeys(),
		help:  h,
		body:  vp,
		cache: &viewRenderCache{},
	}
}

func (m ListDialogModel) SetSize(w, h int) ListDialogModel {
	m.width, m.height = w, h
	m.syncBody()
	return m
}
func (m ListDialogModel) SetTitle(t string) ListDialogModel { m.title = t; return m }

// SetTitleAction installs a right-aligned button on the title line, or clears
// it when a is nil. Use for creation actions ("New", …) that belong to the
// dialog as a whole rather than the currently selected row.
func (m ListDialogModel) SetTitleAction(a *ListDialogAction) ListDialogModel {
	m.titleAction = a
	if a == nil && m.focusZone == ListZoneTitleAction {
		m.focusZone = ListZoneList
	}
	return m
}
func (m ListDialogModel) SetSelectedColor(c color.Color) ListDialogModel {
	m.selectedColor = c
	return m
}

// SetRows replaces the list rows. The caller is responsible for pre-rendering
// each row (swatch, time prefix, …). Scroll and selection are clamped.
func (m ListDialogModel) SetRows(rows []string) ListDialogModel {
	m.rows = rows
	if m.selected >= len(rows) {
		m.selected = max(len(rows)-1, 0)
	}
	m.syncBody()
	return m
}

// SetSelected moves the selection to idx (clamped). The detail viewport
// scrolls back to the top when the selection actually changes so a freshly
// selected row's content starts from line one.
func (m ListDialogModel) SetSelected(idx int) ListDialogModel {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.rows) {
		idx = max(len(m.rows)-1, 0)
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
// when the list does not own focus. Callers apply it themselves so the
// tint composes with their own row-level styling (calendar swatch, RSVP
// indicators, etc.) without the shell needing to know about those.
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
// place while the body scrolls — same anchor users see in the single-event
// dialog. Empty string clears the pinned title.
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
	if m.focusedAction >= len(actions) {
		m.focusedAction = max(len(actions)-1, 0)
	}
	if m.focusZone == ListZoneActions && len(actions) == 0 {
		m.focusZone = ListZoneList
	}
	m.syncBody()
	return m
}

// syncBody pushes the current detail dimensions and content into the body
// viewport so HandleKey/HandleMouseWheel can scroll without waiting for the
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
// own messaging.
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

// MoveUp/MoveDown advance the selection inside the list zone. No-ops when the
// list is empty or the focus is elsewhere. Selection-change resets the detail
// viewport scroll so the new row's content starts from the top.
func (m ListDialogModel) MoveUp() ListDialogModel {
	if m.focusZone == ListZoneList && m.selected > 0 {
		m.selected--
		m.body.GotoTop()
	}
	return m
}

func (m ListDialogModel) MoveDown() ListDialogModel {
	if m.focusZone == ListZoneList && m.selected < len(m.rows)-1 {
		m.selected++
		m.body.GotoTop()
	}
	return m
}

// CycleZone advances focus to the next (or previous) focusable element in
// the dialog — the list, each individual action button, and the title
// action button — so Tab walks every control the way a webpage would. The
// cycle order is: list → actions[0] → … → actions[n-1] → title action (if
// present) → list.
func (m ListDialogModel) CycleZone(forward bool) ListDialogModel {
	total := 1 + len(m.actions)
	if m.titleAction != nil {
		total++
	}
	if total == 1 {
		m.focusZone = ListZoneList
		return m
	}

	cur := 0
	switch m.focusZone {
	case ListZoneActions:
		cur = 1 + m.focusedAction
	case ListZoneTitleAction:
		cur = 1 + len(m.actions)
	case ListZoneList, ListZoneCustom:
		cur = 0
	}

	delta := 1
	if !forward {
		delta = -1
	}
	next := (cur + delta + total) % total

	switch {
	case next == 0:
		m.focusZone = ListZoneList
	case next <= len(m.actions):
		m.focusZone = ListZoneActions
		m.focusedAction = next - 1
	default:
		m.focusZone = ListZoneTitleAction
	}
	return m
}

// FocusAction focuses the action bar and sets the focused button index.
func (m ListDialogModel) FocusAction(idx int) ListDialogModel {
	if idx < 0 || idx >= len(m.actions) {
		return m
	}
	m.focusZone = ListZoneActions
	m.focusedAction = idx
	return m
}

// ActivateFocused returns the command for whichever zone currently has focus
// (the focused action button, or the title-action button).
func (m ListDialogModel) ActivateFocused() tea.Cmd {
	switch m.focusZone {
	case ListZoneActions:
		if m.focusedAction >= 0 && m.focusedAction < len(m.actions) {
			return m.actions[m.focusedAction].Msg
		}
	case ListZoneTitleAction:
		if m.titleAction != nil {
			return m.titleAction.Msg
		}
	case ListZoneList, ListZoneCustom:
	}
	return nil
}

// RowAtPosition hit-tests a screen-space (x, y) against the rendered list.
// Returns the row index when the click lands on a row, false otherwise.
func (m ListDialogModel) RowAtPosition(x, y int) (int, bool) {
	if len(m.rows) == 0 || m.width <= 0 || m.height <= 0 {
		return 0, false
	}

	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	listX := dialogX + 2
	listY := dialogY + 4
	listW := innerW
	listH := bodyH

	if m.isNarrow() {
		listH = min(max(len(m.rows)+1, 3), max(bodyH/3, 3))
	} else {
		listW = listColumnWidth(innerW)
	}

	if x < listX || x >= listX+listW || y < listY || y >= listY+listH {
		return 0, false
	}

	row := y - listY
	if len(m.rows) > listH && row == listH-1 {
		return 0, false
	}

	idx := m.scroll + row
	if idx < 0 || idx >= len(m.rows) {
		return 0, false
	}
	return idx, true
}

// ActionAtPosition hit-tests the action bar. Returns the clicked button index.
func (m ListDialogModel) ActionAtPosition(x, y int) (int, bool) {
	ox, oy := m.actionBarOrigin()
	if y != oy {
		return 0, false
	}
	cx := ox
	for i, a := range m.actions {
		w := len(a.Label) + 2
		if x >= cx && x < cx+w {
			return i, true
		}
		cx += w + 1
	}
	return 0, false
}

// ClickRow selects idx and focuses the list zone. Resets the detail viewport
// scroll on selection change so the freshly clicked row's content starts at
// the top.
func (m ListDialogModel) ClickRow(idx int) ListDialogModel {
	if idx < 0 || idx >= len(m.rows) {
		return m
	}
	if idx != m.selected {
		m.body.GotoTop()
	}
	m.selected = idx
	m.focusZone = ListZoneList
	return m
}

// ClickAction focuses the action bar at idx and returns its command.
func (m ListDialogModel) ClickAction(idx int) (ListDialogModel, tea.Cmd) {
	if idx < 0 || idx >= len(m.actions) {
		return m, nil
	}
	m.focusZone = ListZoneActions
	m.focusedAction = idx
	return m, m.actions[idx].Msg
}

// DetailsOrigin returns the screen-space (x, y) of the first line of the
// detail pane, so callers can hit-test buttons they composed into the
// detail lines (e.g. RSVP buttons in the event dialog).
func (m ListDialogModel) DetailsOrigin() (int, int) {
	boxW, boxH := m.boxSize()
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	detailsX := dialogX + 2
	detailsY := dialogY + 4
	if m.isNarrow() {
		rowCount := max(len(m.rows), 1)
		bodyH := max(max(boxH-3, 6)-4, 3)
		listH := min(max(rowCount+1, 3), max(bodyH/3, 3))
		detailsY += listH + 1
	} else {
		innerW := max(boxW-5, 10)
		detailsX += listColumnWidth(innerW) + dialogDividerWidth
	}
	return detailsX, detailsY
}

// BodyRowScreenY translates a content-row index inside the scrollable
// detail body to its screen-space Y, factoring in the pinned title row
// (+2 lines when present) and the current scroll offset. Returns false
// when the row is scrolled out of view.
func (m ListDialogModel) BodyRowScreenY(idx int) (int, bool) {
	_, oy := m.DetailsOrigin()
	if m.hasPinnedTitle() {
		oy += 2
	}
	visible := idx - m.body.YOffset()
	if visible < 0 || visible >= m.body.Height() {
		return 0, false
	}
	return oy + visible, true
}

func (m ListDialogModel) actionBarOrigin() (int, int) {
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2

	contentX := dialogX + 2
	actionsY := dialogY + bodyH + 3

	if m.isNarrow() {
		return contentX, actionsY
	}
	return contentX + listColumnWidth(innerW) + dialogDividerWidth, actionsY
}

// View renders the complete dialog (border, title, body, help row).
func (m ListDialogModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	title := m.renderTitleRow(innerW)
	helpText := m.renderHelpLine(innerW)

	var body string
	if m.isNarrow() {
		body = m.viewStacked(innerW, bodyH)
	} else {
		body = m.viewColumns(innerW, bodyH)
	}

	// Build the framed dialog manually instead of going through
	// lipgloss.NewStyle().Border().Render(content): that path forces
	// lipgloss to re-wrap and re-measure every grapheme of the styled
	// content, which is by far the biggest single cost on a dense
	// dialog (96+ rows). Each content line is already innerW cells
	// wide, so we can splice them between hand-built border + padding
	// strings and skip the global measurement pass entirely.
	blank := strings.Repeat(" ", innerW)
	contentLines := make([]string, 0, innerH)
	contentLines = append(contentLines, title, blank)
	contentLines = append(contentLines, strings.Split(body, "\n")...)
	contentLines = append(contentLines, blank, helpText)
	return framedDialog(boxW, contentLines)
}

// framedDialog wraps innerLines with the rounded border + (1,2,0,1)
// padding the dialog has always used. innerLines MUST already be at the
// inner content width (boxW - 5): the title row, helpText, and the
// row-zipped body are all width-padded by their producers, so skipping
// the per-line measurement here is safe — and saves ~25% of View on a
// dense dialog because lipgloss.Width is the single biggest cost.
func framedDialog(boxW int, innerLines []string) string {
	const (
		padLeft  = 1
		padRight = 2
	)
	innerW := boxW - 2 - padLeft - padRight
	top := "╭" + strings.Repeat("─", boxW-2) + "╮"
	bottom := "╰" + strings.Repeat("─", boxW-2) + "╯"
	leftPad := strings.Repeat(" ", padLeft)
	rightPad := strings.Repeat(" ", padRight)
	emptyRow := "│" + leftPad + strings.Repeat(" ", innerW) + rightPad + "│"

	var b strings.Builder
	b.Grow(boxW * (len(innerLines) + 3))
	b.WriteString(top)
	b.WriteByte('\n')
	b.WriteString(emptyRow)
	b.WriteByte('\n')
	for _, line := range innerLines {
		b.WriteString("│")
		b.WriteString(leftPad)
		b.WriteString(line)
		b.WriteString(rightPad)
		b.WriteString("│\n")
	}
	b.WriteString(bottom)
	return b.String()
}

func (m *ListDialogModel) viewColumns(innerW, bodyH int) string {
	listW := listColumnWidth(innerW)
	detailsW := detailColumnWidth(innerW)

	m.adjustScroll(bodyH)
	// All three column renderers now produce lines that are exactly w
	// cells wide (renderList via padLines, renderDivider by construction,
	// renderDetails by padding its variable-width parts internally), so
	// we can split each without the per-line lipgloss.Width measurement
	// splitAndPad would do.
	listCol := trustedSplit(m.renderList(listW, bodyH), listW, bodyH)
	dividerCol := trustedSplit(m.renderDivider(dialogDividerWidth, bodyH), dialogDividerWidth, bodyH)
	detailsCol := trustedSplit(m.renderDetails(detailsW, bodyH), detailsW, bodyH)

	// Manual row-zip: lipgloss.JoinHorizontal re-measures every grapheme
	// of every line in every column to align them. We've already padded
	// each column to its fixed cell width, so straight concatenation
	// produces the same visual output without the measurement pass.
	var b strings.Builder
	b.Grow((listW + dialogDividerWidth + detailsW + 1) * bodyH)
	for i := 0; i < bodyH; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(listCol[i])
		b.WriteString(dividerCol[i])
		b.WriteString(detailsCol[i])
	}
	return b.String()
}

// trustedSplit splits s by newlines and returns exactly h rows. Callers
// must guarantee every emitted line is already w cells wide; missing
// rows are filled with blanks. Skips lipgloss.Width entirely — that's
// the whole point of the "trusted" name, and the fast path is ~60%
// faster than splitAndPad on width-correct input.
func trustedSplit(s string, w, h int) []string {
	out := make([]string, h)
	if h <= 0 {
		return out
	}
	lines := strings.Split(s, "\n")
	n := len(lines)
	if n > h {
		n = h
	}
	for i := 0; i < n; i++ {
		out[i] = lines[i]
	}
	if n < h {
		blank := strings.Repeat(" ", w)
		for i := n; i < h; i++ {
			out[i] = blank
		}
	}
	return out
}

// splitAndPad splits a multi-line column into exactly h rows, each padded
// (with plain spaces) or truncated to w cells. Used by viewColumns to
// guarantee every column line is cell-aligned without going through
// lipgloss.JoinHorizontal — the measurement happens once per line here,
// not once per line per join. Use trustedSplit instead when the column
// renderer already guarantees each line is w cells wide.
func splitAndPad(s string, w, h int) []string {
	if w <= 0 {
		out := make([]string, h)
		return out
	}
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	blank := strings.Repeat(" ", w)
	for i := 0; i < h; i++ {
		if i >= len(lines) {
			out[i] = blank
			continue
		}
		l := lines[i]
		cw := lipgloss.Width(l)
		switch {
		case cw == w:
			out[i] = l
		case cw < w:
			out[i] = l + strings.Repeat(" ", w-cw)
		default:
			t := truncateTo(l, w)
			if tw := lipgloss.Width(t); tw < w {
				t += strings.Repeat(" ", w-tw)
			}
			out[i] = t
		}
	}
	return out
}

func (m *ListDialogModel) viewStacked(innerW, bodyH int) string {
	rowCount := max(len(m.rows), 1)
	listH := min(max(rowCount+1, 3), max(bodyH/3, 3))
	detailsH := max(bodyH-listH-1, 3)

	m.adjustScroll(listH)
	list := m.renderList(innerW, listH)
	sep := lipgloss.NewStyle().Faint(true).Width(innerW).
		Render(strings.Repeat("─", innerW))
	details := m.renderDetails(innerW, detailsH)

	return lipgloss.JoinVertical(lipgloss.Left, list, sep, details)
}

func (m *ListDialogModel) adjustScroll(visibleH int) {
	// When the list overflows, renderList reserves the last visible row
	// for the scroll indicator (e.g. "5/96 ▼"), overwriting whatever row
	// would otherwise sit there. Treat that slot as out of bounds for the
	// selection so the highlighted row never lands on it.
	contentH := visibleH
	if len(m.rows) > visibleH && contentH > 1 {
		contentH = visibleH - 1
	}
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if m.selected >= m.scroll+contentH {
		m.scroll = m.selected - contentH + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m ListDialogModel) renderList(w, h int) string {
	if len(m.rows) == 0 {
		if m.emptyList == "" {
			return padLines(nil, w, h)
		}
		msg := lipgloss.NewStyle().Faint(true).Render(m.emptyList)
		return padLines([]string{msg}, w, h)
	}

	total := len(m.rows)
	visibleStart := m.scroll
	visibleEnd := min(visibleStart+h, total)

	lines := make([]string, 0, h)
	for i := visibleStart; i < visibleEnd; i++ {
		lines = append(lines, renderListRow(m.rows[i], w, i == m.selected, m.focusZone == ListZoneList, m.selectedColor))
	}

	if total > h {
		indicator := fmt.Sprintf("%d/%d ", m.selected+1, total)
		arrows := ""
		if m.scroll > 0 {
			arrows += "▲"
		}
		if visibleEnd < total {
			if arrows != "" {
				arrows += " "
			}
			arrows += "▼"
		}
		if arrows != "" {
			indicator += arrows + " "
		}
		indicator = truncateTo(indicator, w)

		faintIndicator := lipgloss.NewStyle().Faint(true).Render(indicator)
		if len(lines) >= h {
			lines[h-1] = faintIndicator
		} else {
			lines = append(lines, faintIndicator)
		}
	}

	return padLines(lines, w, h)
}

func (m ListDialogModel) renderDivider(w, h int) string {
	bar := lipgloss.NewStyle().Faint(true).Render("│")
	pad := strings.Repeat(" ", (w-1)/2)
	rest := strings.Repeat(" ", w-len(pad)-1)
	line := pad + bar + rest
	lines := make([]string, h)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func (m ListDialogModel) renderActions(w int) string {
	key := m.actionsCacheKey(w)
	if m.cache != nil && key == m.cache.actionsKey && m.cache.actions != "" {
		return m.cache.actions
	}
	bs := DefaultButtonStyles()
	parts := make([]string, len(m.actions))
	for i, a := range m.actions {
		focused := m.focusZone == ListZoneActions && i == m.focusedAction
		switch {
		case a.Danger:
			parts[i] = bs.Danger.Render(a.Label, focused)
		case a.Primary:
			parts[i] = bs.Normal.Render(a.Label, focused)
		default:
			parts[i] = bs.Normal.Render(a.Label, focused)
		}
	}
	out := truncateTo(strings.Join(parts, " "), w)
	if m.cache != nil {
		m.cache.actionsKey = key
		m.cache.actions = out
	}
	return out
}

// renderHelpLine produces the centered short-help line at the bottom
// of the dialog. The result is memoized: shortHelp only changes when
// the caller swaps focus zones or transitions between empty/non-empty
// states, so caching it skips a full lipgloss render (and the bubbles
// help layout it wraps) on every key press while the user scrolls.
func (m ListDialogModel) renderHelpLine(innerW int) string {
	key := m.helpCacheKey(innerW)
	if m.cache != nil && key == m.cache.helpKey && m.cache.help != "" {
		return m.cache.help
	}
	m.help.SetWidth(innerW)
	out := lipgloss.NewStyle().
		Width(innerW).
		Align(lipgloss.Center).
		Render(m.help.ShortHelpView(m.shortHelp))
	if m.cache != nil {
		m.cache.helpKey = key
		m.cache.help = out
	}
	return out
}

// helpCacheKey fingerprints the inputs that affect renderHelpLine: the
// inner width and every binding's key + help text. shortHelp is
// rebuilt by the caller on each refresh so identity comparison would
// always miss; content-based fingerprinting hits whenever the rendered
// output would be identical.
func (m ListDialogModel) helpCacheKey(innerW int) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(innerW))
	h.Write(buf[:])
	for _, b := range m.shortHelp {
		help := b.Help()
		h.Write([]byte(help.Key))
		h.Write([]byte{0})
		h.Write([]byte(help.Desc))
		h.Write([]byte{0})
		var flags byte
		if b.Enabled() {
			flags = 1
		}
		h.Write([]byte{flags})
	}
	return h.Sum64()
}

// actionsCacheKey returns a 64-bit fingerprint of every input that
// affects renderActions' output. Each Set* on the model that touches
// one of those inputs naturally changes the fingerprint, so the cache
// invalidates lazily without needing eager bookkeeping.
func (m ListDialogModel) actionsCacheKey(w int) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(w))
	h.Write(buf[:])
	binary.LittleEndian.PutUint64(buf[:], uint64(m.focusZone))
	h.Write(buf[:])
	binary.LittleEndian.PutUint64(buf[:], uint64(m.focusedAction))
	h.Write(buf[:])
	for _, a := range m.actions {
		h.Write([]byte(a.Label))
		var flags byte
		if a.Danger {
			flags |= 1
		}
		if a.Primary {
			flags |= 2
		}
		h.Write([]byte{flags, 0})
	}
	return h.Sum64()
}

func (m *ListDialogModel) renderDetails(w, h int) string {
	lines := m.detailLines
	if len(m.rows) == 0 {
		lines = m.emptyDetails
	}

	bodyH := h
	var pinned string
	if m.hasPinnedTitle() {
		pinned = paneTitle(m.detailTitle, w)
		bodyH = max(bodyH-2, 1)
	}

	if len(m.actions) == 0 {
		m.body.SetWidth(w)
		m.body.SetHeight(bodyH)
		m.body.SetContentLines(lines)
		if pinned != "" {
			return pinned + "\n" + m.body.View()
		}
		return m.body.View()
	}

	bodyH = max(bodyH-2, 1)
	m.body.SetWidth(w)
	m.body.SetHeight(bodyH)
	m.body.SetContentLines(lines)

	parts := make([]string, 0, 4)
	if pinned != "" {
		parts = append(parts, pinned)
	}
	// renderActions returns ≤ w cells (truncateTo); pad it so the whole
	// detail column is width-correct and viewColumns can trustedSplit it
	// without re-measuring every body line.
	parts = append(parts, m.body.View(), m.actionsSeparator(w), padTrailing(m.renderActions(w), w))
	return strings.Join(parts, "\n")
}

// actionsSeparator renders the faint rule that sits between the detail
// body and the action bar. When the body has scrolled-away content above
// or below, a centered "↑↓ more" hint is embedded in the rule to advertise
// the scroll affordance — same treatment used in the single-event dialog.
func (m ListDialogModel) actionsSeparator(w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	hint := m.scrollHint()
	hw := lipgloss.Width(hint)
	if hint == "" || w <= hw+2 {
		return faint.Render(strings.Repeat("─", w))
	}
	left := (w - hw - 2) / 2
	right := w - hw - 2 - left
	return faint.Render(strings.Repeat("─", left)) + " " + faint.Render(hint) + " " + faint.Render(strings.Repeat("─", right))
}

// scrollHint returns "↓ more" / "↑ more" / "↑↓ more" depending on what
// the user can still scroll to. Empty when the body fits without scrolling.
func (m ListDialogModel) scrollHint() string {
	if !m.bodyOverflows() {
		return ""
	}
	switch {
	case m.body.AtTop():
		return "↓ more"
	case m.body.AtBottom():
		return "↑ more"
	default:
		return "↑↓ more"
	}
}

// bodyOverflows reports whether the detail body has more content than
// the viewport can show at once.
func (m ListDialogModel) bodyOverflows() bool {
	return m.body.TotalLineCount() > m.body.VisibleLineCount()
}

// renderTitleRow composes the bold title with the optional right-aligned
// title-action button, falling back to just the title when no action is set.
func (m ListDialogModel) renderTitleRow(innerW int) string {
	if m.titleAction == nil {
		return lipgloss.NewStyle().Bold(true).Width(innerW).Render(m.title)
	}
	focused := m.focusZone == ListZoneTitleAction
	btn := renderTitleActionButton(*m.titleAction, focused)
	btnW := lipgloss.Width(btn)
	titleW := max(innerW-btnW, 0)
	titleStr := lipgloss.NewStyle().
		Bold(true).
		Width(titleW).
		Render(truncateTo(m.title, titleW))
	return lipgloss.JoinHorizontal(lipgloss.Top, titleStr, btn)
}

// renderTitleActionButton renders a title-line button without the trailing
// margin-right cell used by action-bar buttons so it sits flush with the
// dialog's right edge.
func renderTitleActionButton(a ListDialogAction, focused bool) string {
	bs := DefaultButtonStyles().Normal
	style := bs.Normal
	if focused {
		style = bs.Focused
	}
	return style.UnsetMarginRight().Render(a.Label)
}

// TitleActionAtPosition reports whether (x, y) lies within the title-line
// action button and, if so, returns its command.
func (m ListDialogModel) TitleActionAtPosition(x, y int) (tea.Cmd, bool) {
	if m.titleAction == nil || m.width <= 0 || m.height <= 0 {
		return nil, false
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	titleY := dialogY + 2
	if y != titleY {
		return nil, false
	}
	btnW := lipgloss.Width(renderTitleActionButton(*m.titleAction, false))
	btnStartX := dialogX + 2 + innerW - btnW
	if x < btnStartX || x >= btnStartX+btnW {
		return nil, false
	}
	return m.titleAction.Msg, true
}

// HandleKey is the shell's handler for keys it cares about (navigation, tab,
// enter-on-actions, close). Returns the (maybe-updated) model and the
// resulting command. Callers dispatch their domain keys (New/Edit/Delete/…)
// themselves before falling through to this.
func (m ListDialogModel) HandleKey(msg tea.KeyPressMsg, onClose func() tea.Msg) (ListDialogModel, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return onClose() }, true
	case key.Matches(msg, m.keys.Tab):
		return m.CycleZone(true), nil, true
	case key.Matches(msg, m.keys.ShiftTab):
		return m.CycleZone(false), nil, true
	case key.Matches(msg, m.keys.Up):
		return m.MoveUp(), nil, true
	case key.Matches(msg, m.keys.Down):
		return m.MoveDown(), nil, true
	case key.Matches(msg, m.keys.PageUp):
		m.body.PageUp()
		return m, nil, true
	case key.Matches(msg, m.keys.PageDown):
		m.body.PageDown()
		return m, nil, true
	case key.Matches(msg, m.keys.Home):
		m.body.GotoTop()
		return m, nil, true
	case key.Matches(msg, m.keys.End):
		m.body.GotoBottom()
		return m, nil, true
	case key.Matches(msg, m.keys.Enter):
		return m, m.ActivateFocused(), true
	}
	return m, nil, false
}

// HandleMouseWheel forwards mouse wheel events to the detail body so the
// user can scroll long event content with the wheel — same affordance the
// single-event dialog provides.
func (m ListDialogModel) HandleMouseWheel(msg tea.MouseWheelMsg) (ListDialogModel, tea.Cmd) {
	var cmd tea.Cmd
	m.body, cmd = m.body.Update(msg)
	return m, cmd
}

// ScrollDetailsUp/Down nudge the detail viewport by one line. Callers use
// these when up/down arrows belong to the details pane (focus is on actions
// or RSVP, not on the list itself).
func (m ListDialogModel) ScrollDetailsUp() ListDialogModel {
	m.body.ScrollUp(1)
	return m
}

func (m ListDialogModel) ScrollDetailsDown() ListDialogModel {
	m.body.ScrollDown(1)
	return m
}

// Keys exposes the shell's default bindings so callers can compose ShortHelp.
func (m ListDialogModel) Keys() ListDialogKeys { return m.keys }
