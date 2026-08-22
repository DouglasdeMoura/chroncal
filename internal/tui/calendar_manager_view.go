package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

func (m CalendarManagerModel) View() string { return m.rootView() }

// rootView renders the persistent grouped hierarchy and active inspector.
func (m CalendarManagerModel) rootView() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	boxW, _ := m.boxSize()
	innerW, bodyH := m.managerBodySize()

	title := m.renderTitleRow(innerW)
	body := m.renderManagerBody(innerW, bodyH)
	help := m.renderHelp(innerW)
	blank := strings.Repeat(" ", innerW)

	contentLines := make([]string, 0, bodyH+4)
	contentLines = append(contentLines, title, blank)
	contentLines = append(contentLines, strings.Split(body, "\n")...)
	contentLines = append(contentLines, blank, help)
	base := mouseSweep(framedDialog(boxW, contentLines))
	if m.addMenuOpen {
		base = m.composeAddMenu(base)
	}
	if m.discardConfirm != nil {
		base = composeCenteredOverlay(base, m.discardConfirm.View(), boxW)
	}
	return base
}

func (m CalendarManagerModel) renderManagerBody(w, h int) string {
	if m.onePaneLayout() {
		if m.screen == CalendarManagerScreenList {
			return strings.Join(m.renderSourceColumn(w, h), "\n")
		}
		return padLines(m.activeInspectorLines(w, h), w, h)
	}
	listW, _ := m.rootPaneSize()
	dividerW := 3
	detailW := max(w-listW-dividerW, 1)
	leftLines := m.renderSourceColumn(listW, h)
	right := padLines(m.activeInspectorLines(detailW, h), detailW, h)
	divider := lipgloss.NewStyle().Foreground(m.theme.Muted).Render(" │ ")
	rightRows := strings.Split(right, "\n")
	lines := make([]string, h)
	for i := range h {
		lines[i] = leftLines[i] + divider + rightRows[i]
	}
	return strings.Join(lines, "\n")
}

// footerBinding builds a display-only footer key binding whose hint label
// doubles as the key. That matches the themed help model's "key · desc"
// format shared by every dialog footer.
func footerBinding(k, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(k), key.WithHelp(k, desc))
}

// managerChild is the pushed-screen contract. The screen that currently owns
// the manager's input renders its own inspector view and advertises its own
// footer help bindings. Screen→child resolve stays in activeChild. The
// footer and the inspector then cannot drift independently when a new
// screen is added. Both read the one mapping.
type managerChild interface {
	HelpBindings() []key.Binding
	InspectorView(w, h int) string
}

// activeChild returns the pushed screen that currently owns input and the
// inspector pane, or nil at the root list. It is the single source of truth
// for the screen→child mapping shared by the footer (helpBindings) and the
// inspector (activeInspectorLines). The two cannot then drift apart when a
// screen is added.
func (m CalendarManagerModel) activeChild() managerChild {
	switch m.screen {
	case CalendarManagerScreenList:
		return nil
	case CalendarManagerScreenCalendar:
		if m.calendarForm != nil {
			return m.calendarForm
		}
	case CalendarManagerScreenAccount:
		if m.accountSettings != nil {
			return m.accountSettings
		}
	case CalendarManagerScreenAccountCalendars:
		if m.accountPicker != nil {
			return m.accountPicker
		}
	case CalendarManagerScreenTransfer:
		if m.transfer != nil {
			return m.transfer
		}
	}
	return nil
}

func (m CalendarManagerModel) activeInspectorLines(w, h int) []string {
	if child := m.activeChild(); child != nil {
		return strings.Split(child.InspectorView(w, h), "\n")
	}
	return m.selectionInspectorLines(w, h)
}

// inspectorPreviewCache memoizes the calendar-selection inspector so the
// edit-form preview is not rebuilt on every root render. Held-arrow idle,
// clock ticks, mouse motion, and a focus cycle all re-render the identical
// preview until one of its inputs changes. The memo skips that rebuild. It
// lives behind a pointer on CalendarManagerModel. A value-receiver render
// (the manager is copied between Update and View) still writes through to
// the model the runtime keeps.
type inspectorPreviewCache struct {
	key   inspectorPreviewKey
	lines []string
}

// inspectorPreviewKey captures every input to the calendar-selection
// preview. CalendarDialogParams folds in the selected calendar's immutable
// ID, its display metadata, its default flag, and its sidebar visibility.
// It is a comparable value type, so equality is exact. Any field added
// to calendarDialogParamsFor then participates in invalidation.
// themeFP covers the theme/style. w and h are the inspector pane size.
// Everything else the preview depends on is read live when the key is
// built. A changed selection, resize, data reload, or visibility toggle
// then produces a different key and forces a rebuild. No separate
// invalidation hooks are needed.
type inspectorPreviewKey struct {
	params  CalendarDialogParams
	themeFP string
	w, h    int
}

// fingerprintTheme reduces a Theme to a comparable string that covers every
// color token and the calendar swatch list. color.Color is an interface, so
// RGBA() is the canonical way to tell whether two themes render identically.
// It is computed only when the manager's theme is assigned (construction and
// SetTheme). The per-render cost is a single string compare against the
// cached key, never a re-scan of the palette.
func fingerprintTheme(t Theme) string {
	var b strings.Builder
	fp := func(name string, c color.Color) {
		b.WriteString(name)
		if c == nil {
			b.WriteString("|n;")
			return
		}
		r, g, bl, a := c.RGBA()
		fmt.Fprintf(&b, "|%d,%d,%d,%d;", r, g, bl, a)
	}
	fp("primary", t.Primary)
	fp("secondary", t.Secondary)
	fp("accent", t.Accent)
	fp("muted", t.Muted)
	fp("text", t.Text)
	fp("textdim", t.TextDim)
	fp("border", t.Border)
	fp("today", t.Today)
	fp("selected", t.Selected)
	fp("selectedtext", t.SelectedText)
	fp("surface", t.Surface)
	fp("error", t.Error)
	fp("badgeok", t.BadgeOK)
	fp("badgewarn", t.BadgeWarn)
	fp("badgedanger", t.BadgeDanger)
	fp("badgeinfo", t.BadgeInfo)
	fp("badgeneutral", t.BadgeNeutral)
	fp("formlabel", t.FormLabel)
	fp("formrequired", t.FormRequired)
	fp("formerror", t.FormError)
	fp("formhighlight", t.FormHighlight)
	fp("buttonbg", t.ButtonBg)
	b.WriteString("sw:")
	for _, s := range t.CalendarSwatches {
		b.WriteString(s)
		b.WriteByte(',')
	}
	return b.String()
}

// selectionInspectorLines composes the root inspector for the current
// selection to exactly h rows. A selected calendar shows a live, unfocused
// preview of its edit form. That is the same surface Enter, Tab, or a pane
// click focuses. The editable fields then appear immediately on selection
// (macOS Settings-style master–detail). Account and empty selections keep
// the summary header plus one bottom action pinned to the final row.
func (m CalendarManagerModel) selectionInspectorLines(w, h int) []string {
	if id, info, ok := m.selectedCalendar(); ok {
		params := calendarDialogParamsFor(id, info, m.list.IsHidden(id))
		key := inspectorPreviewKey{params: params, themeFP: m.themeFP, w: w, h: h}
		if m.inspector != nil && m.inspector.key == key && len(m.inspector.lines) > 0 {
			return m.inspector.lines
		}
		preview := NewCalendarDialogModel(params, m.theme).Blur().SetInspectorSize(w, h)
		lines := strings.Split(preview.InspectorView(w, h), "\n")
		if m.inspector != nil {
			*m.inspector = inspectorPreviewCache{key: key, lines: lines}
		}
		return lines
	}
	identity, ok := m.list.currentIdentity()
	faint := lipgloss.NewStyle().Foreground(m.theme.Muted)
	labelWidth := min(10, max(7, w/4))
	action, hasAction := m.selectionInspectorAction()

	// contentLimit is the last content row; the bottom action pins to the row
	// after it when present, so long content cannot push the action off-screen.
	contentLimit := h
	if hasAction {
		contentLimit = h - 1
	}
	if contentLimit < 1 {
		contentLimit = 1
	}

	lines := m.selectionInspectorHeader(identity, ok, faint, labelWidth, w)

	for len(lines) < contentLimit {
		lines = append(lines, "")
	}
	if len(lines) > contentLimit {
		lines = lines[:contentLimit]
	}
	if hasAction {
		lines = append(lines, m.renderInspectorAction(action))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

// selectionInspectorHeader builds the summary header block for account and
// empty selections: the title row, a blank, and the aligned metadata rows.
// Calendar selections render the edit-form preview instead and only fall
// through here when the selected calendar no longer exists.
func (m CalendarManagerModel) selectionInspectorHeader(identity calendarRowIdentity, ok bool, faint lipgloss.Style, labelWidth, w int) []string {
	if !ok {
		return []string{lipgloss.NewStyle().Faint(true).Render("Select a calendar or account.")}
	}
	if identity.kind == accountHeaderRow {
		return m.accountInspectorHeader(identity, faint, labelWidth, w)
	}
	return []string{lipgloss.NewStyle().Faint(true).Render("Calendar unavailable.")}
}

// accountInspectorHeader builds the account/Local header: the account name
// (or "Local"), a blank, and the aligned metadata. Local shows "On this
// device" and its calendar count; remote accounts add a sync-status line.
func (m CalendarManagerModel) accountInspectorHeader(identity calendarRowIdentity, faint lipgloss.Style, labelWidth, w int) []string {
	name := "Local"
	count := 0
	errors := 0
	for _, info := range m.calendars {
		if info.AccountID != identity.id {
			continue
		}
		count++
		if identity.id != 0 && strings.TrimSpace(info.AccountName) != "" {
			name = strings.TrimSpace(info.AccountName)
		}
		if syncHealthFor(info) == SyncHealthError || info.RemoteMissing {
			errors++
		}
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Render(truncateTo(name, w)), ""}
	if identity.id == 0 {
		lines = append(lines,
			faint.Render("On this device"),
			detailLine(faint, "Calendars", fmt.Sprintf("%d", count), labelWidth, w),
		)
		return lines
	}
	status := "Up to date"
	if errors > 0 {
		status = lipgloss.NewStyle().Foreground(m.theme.Error).Render(fmt.Sprintf("%d need attention", errors))
	}
	lines = append(lines,
		detailLine(faint, "Calendars", fmt.Sprintf("%d", count), labelWidth, w),
		detailLine(faint, "Status", status, labelWidth, w),
	)
	return lines
}

// calendarManagerInspectorAction describes the selection inspector's single
// bottom action: Account Settings… for a remote account heading. Calendar
// selections render the edit-form preview instead of a pinned action, and
// Local and empty selections have none.
type calendarManagerInspectorAction struct {
	label   string
	account int64
}

// selectionInspectorAction resolves the bottom action for the current root
// selection: Account Settings… (remote accounts only) emits a typed account
// target. Calendar, Local, and empty selections have no pinned action.
func (m CalendarManagerModel) selectionInspectorAction() (calendarManagerInspectorAction, bool) {
	identity, ok := m.list.currentIdentity()
	if !ok || identity.kind != accountHeaderRow || identity.id == 0 {
		return calendarManagerInspectorAction{}, false
	}
	return calendarManagerInspectorAction{label: "Account Settings…", account: identity.id}, true
}

// renderInspectorAction renders the bottom action as a neutral pill button
// (ButtonStyles.Normal), the same style the Form action bar uses. It uses the
// focused variant while the action holds root focus.
func (m CalendarManagerModel) renderInspectorAction(action calendarManagerInspectorAction) string {
	return DefaultButtonStyles().Normal.Render(action.label, m.rootFocus == rootFocusInspector)
}

// applyInspectorAction routes a click on the bottom action: an account action
// asks the host to open account settings for the typed account ID.
func (m CalendarManagerModel) applyInspectorAction(action calendarManagerInspectorAction) (CalendarManagerModel, tea.Cmd) {
	if action.account != 0 {
		return m, func() tea.Msg {
			return CalendarManagerRequestedMsg{Target: CalendarManagerTargetAccount, AccountID: action.account}
		}
	}
	return m, nil
}

// inspectorActionRect returns the screen-space rectangle of the selection
// inspector's bottom action button, when one is rendered. The action exists
// only in wide two-pane root mode: narrow root shows the source list alone,
// and every pushed screen owns its own inspector affordances. Geometry uses
// the button's actual rendered width so hit-testing matches the pill exactly.
func (m CalendarManagerModel) inspectorActionRect() (int, int, int, bool) {
	if m.screen != CalendarManagerScreenList || m.onePaneLayout() || m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, false
	}
	action, ok := m.selectionInspectorAction()
	if !ok {
		return 0, 0, 0, false
	}
	dialogX, dialogY := m.dialogOrigin()
	listW := m.sourceColumnWidth()
	// Inspector pane begins after the border, left pad, source column, and the
	// three-cell divider; the action sits on the final body row.
	paneX := dialogX + addMenuContentBoxX() + listW + 3
	actionY := dialogY + 4 + m.managerBodyHeight() - 1
	return paneX, actionY, lipgloss.Width(m.renderInspectorAction(action)), true
}

// inspectorPaneRect returns the screen-space rectangle of the inspector pane:
// beside the source column in wide two-pane layouts, the full interior in
// one-pane layouts.
func (m CalendarManagerModel) inspectorPaneRect() (int, int, int, int) {
	dialogX, dialogY := m.dialogOrigin()
	paneX := dialogX + addMenuContentBoxX()
	if !m.onePaneLayout() {
		paneX += m.sourceColumnWidth() + 3
	}
	paneW, _ := m.inspectorPaneSize()
	return paneX, dialogY + 4, paneW, m.managerBodyHeight()
}

// previewPaneRect returns the screen-space rectangle of the root inspector
// pane while it shows a calendar edit-form preview, for mouse hit-test.
// It exists only in wide two-pane roots with a calendar already selected.
func (m CalendarManagerModel) previewPaneRect() (int, int, int, int, bool) {
	if m.screen != CalendarManagerScreenList || m.onePaneLayout() || m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, 0, false
	}
	if _, _, ok := m.selectedCalendar(); !ok {
		return 0, 0, 0, 0, false
	}
	px, py, pw, ph := m.inspectorPaneRect()
	return px, py, pw, ph, true
}

func (m CalendarManagerModel) renderTitleRow(w int) string {
	return lipgloss.NewStyle().Bold(true).Width(w).Render("Calendars")
}

// renderHelp renders the centered footer hint line through the shared themed
// help model. Key-binding hints then match every other dialog (key in Text,
// desc in TextDim, " · " separators).
func (m CalendarManagerModel) renderHelp(w int) string {
	m.help.SetWidth(w)
	bindings := m.helpBindings()
	view := m.help.ShortHelpView(bindings)
	// bubbles' short-help truncation keeps an overflowing item when the
	// ellipsis lands exactly on the width boundary. A too-wide line would wrap
	// and shear the dialog frame. Drop trailing hints until the line fits.
	for lipgloss.Width(view) > w && len(bindings) > 1 {
		bindings = bindings[:len(bindings)-1]
		view = m.help.ShortHelpView(bindings)
	}
	return lipgloss.NewStyle().Width(w).Align(lipgloss.Center).Render(view)
}

// helpBindings resolves the footer bindings for the current manager state.
// The discard prompt and Add menu own their own keys. Each pushed screen
// advertises its child's actual keys (HelpBindings). The root shows its
// ring keys plus whatever the focused control activates. Keys listed here
// are display-only. Input route is unchanged.
func (m CalendarManagerModel) helpBindings() []key.Binding {
	if m.discardConfirm != nil {
		return []key.Binding{footerBinding("tab", "switch"), footerBinding("enter", "select"), footerBinding("esc", "keep editing")}
	}
	if child := m.activeChild(); child != nil {
		return child.HelpBindings()
	}
	if m.addMenuOpen {
		return []key.Binding{footerBinding("↑↓", "select"), footerBinding("enter", "choose"), footerBinding("esc", "dismiss")}
	}
	switch m.rootFocus {
	case rootFocusAdd:
		return []key.Binding{footerBinding("tab", "next"), footerBinding("enter", "add"), footerBinding("esc", "close")}
	case rootFocusInspector:
		return []key.Binding{footerBinding("tab", "next"), footerBinding("enter", "activate"), footerBinding("esc", "close")}
	default:
		// "a add" is omitted. + Add is a visible tab stop in the root ring and
		// the accelerator keeps working. The compact set keeps esc visible at
		// the manager's minimum widths.
		return []key.Binding{footerBinding("↑↓", "select"), footerBinding("space", "toggle"), footerBinding("enter", "open"), footerBinding("tab", "next"), footerBinding("esc", "close")}
	}
}

// boxSize mirrors ListDialogModel.boxSize so the manager shares the
// golden-rectangle sizing and narrow fallback with the rest of the dialogs.
func (m CalendarManagerModel) boxSize() (int, int) {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if w < narrowThreshold {
		return max(w-4, 20), max(h-4, 14)
	}
	boxH := min(max(h*2/3, 14), h-2)
	boxW := int(float64(boxH) * goldenCellRatio)
	if boxW > w-2 {
		boxW = w - 2
		boxH = min(max(int(float64(boxW)/goldenCellRatio), 14), h-2)
	}
	if boxW < 50 {
		boxW = 50
	}
	return boxW, boxH
}

// listRegion returns the screen-space rect of the list column. It matches the
// framedDialog layout (border + 1 left pad, then top border + top pad + title
// + blank before the first row). Used for mouse hit-test.
func (m CalendarManagerModel) listRegion() (int, int, int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, 0
	}
	listW, _ := m.rootPaneSize()
	dialogX, dialogY := m.dialogOrigin()
	return dialogX + addMenuContentBoxX(), dialogY + 4, listW, max(m.managerBodyHeight()-2, 1)
}

// managerBodyHeight is the body region height shared by the list viewport and
// inspector. It matches rootView's layout arithmetic.
func (m CalendarManagerModel) managerBodyHeight() int {
	_, h := m.managerBodySize()
	return h
}

// dialogOrigin returns the centered box's screen-space top-left corner — the
// shared base for every mouse hit-test rectangle.
func (m CalendarManagerModel) dialogOrigin() (x, y int) {
	boxW, boxH := m.boxSize()
	return (m.width - boxW) / 2, (m.height - boxH) / 2
}

// addMenuContentBoxX is the box-local x where source-pane content begins
// (after the border cell and the 1-space left pad).
func addMenuContentBoxX() int { return 2 }

// addMenuActionBoxY is the box-local y of the + Add action row. That is the
// last body row, directly above the blank and help rows at the end.
func addMenuActionBoxY(m CalendarManagerModel) int { return 4 + m.managerBodyHeight() - 1 }

// sourceAddActionRendered reports whether the compact + Add action is drawn.
// In wide two-pane mode the source list is always visible; in narrow one-pane
// mode the action belongs to the list screen only.
func (m CalendarManagerModel) sourceAddActionRendered() bool {
	if m.width <= 0 || m.height <= 0 {
		return false
	}
	if !m.onePaneLayout() {
		return true
	}
	return m.screen == CalendarManagerScreenList
}

// sourceAddActionActive reports whether the + Add action can be activated. It
// is active only on the root list screen. Every pushed screen (calendar
// detail, account settings, account calendars, import/export transfer) owns
// its own input. The action is then rendered muted and inert there.
func (m CalendarManagerModel) sourceAddActionActive() bool {
	return m.screen == CalendarManagerScreenList
}

// sourceAddActionRect returns the screen-space rect of the + Add label below
// the source list, for mouse hit-testing and placement assertions.
func (m CalendarManagerModel) sourceAddActionRect() (int, int, int, bool) {
	if !m.sourceAddActionRendered() {
		return 0, 0, 0, false
	}
	dialogX, dialogY := m.dialogOrigin()
	return dialogX + addMenuContentBoxX(), dialogY + addMenuActionBoxY(m), lipgloss.Width(m.renderSourceAddActionCore()), true
}

// renderSourceColumn renders the source-list column: the (possibly empty)
// list viewport, a blank spacer, and the + Add action row. It always returns
// exactly h rows so it composes cleanly into the body grid.
func (m CalendarManagerModel) renderSourceColumn(w, h int) []string {
	listH := max(h-2, 1)
	var listLines []string
	if len(m.calendars) == 0 {
		hint := lipgloss.NewStyle().Faint(true).Render("No calendars yet.")
		listLines = []string{truncateTo(hint, w)}
	} else {
		listLines = strings.Split(m.list.View(), "\n")
	}
	padded := strings.Split(padLines(listLines, w, listH), "\n")
	blank := strings.Repeat(" ", w)
	out := make([]string, 0, h)
	out = append(out, padded...)
	out = append(out, blank, m.renderSourceAddAction(w))
	for len(out) < h {
		out = append(out, blank)
	}
	return out
}

// renderSourceAddAction renders the + Add label, bold/accented when active and
// faint when muted, padded to the full column width. While it holds root focus
// the label uses the neutral button focus pill so the focus ring is visible.
func (m CalendarManagerModel) renderSourceAddAction(w int) string {
	rendered := m.renderSourceAddActionCore()
	return rendered + strings.Repeat(" ", max(w-lipgloss.Width(rendered), 0))
}

// renderSourceAddActionCore renders the bare + Add label (unpadded) so both the
// column row and the mouse hit rectangle share one rendered width.
func (m CalendarManagerModel) renderSourceAddActionCore() string {
	const label = "+ Add"
	active := m.sourceAddActionActive()
	if m.rootFocus == rootFocusAdd && active {
		return DefaultButtonStyles().Normal.Render(label, true)
	}
	if active {
		return lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render(label)
	}
	return lipgloss.NewStyle().Faint(true).Render(label)
}
