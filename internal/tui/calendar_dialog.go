package tui

import (
	"image/color"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// CalendarSavedMsg is emitted when the user saves the dialog. ID == 0 means
// "create a new calendar"; otherwise it's an update.
type CalendarSavedMsg struct {
	ID          int64
	Name        string
	Color       string
	Description string
}

// CalendarDeleteRequestedMsg is emitted when the user presses Delete in the
// dialog. The parent is responsible for showing the confirm dialog.
type CalendarDeleteRequestedMsg struct {
	ID   int64
	Name string
}

// CalendarDialogClosedMsg is emitted when the user cancels the dialog.
type CalendarDialogClosedMsg struct{}

var hexRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Layout constants shared between View and handleMouse so click-to-field
// hit-testing lines up with what the user actually sees.
const (
	cdContentWidth = 50
	cdPadX         = 2
	cdPadTop       = 1
	cdLabelWidth   = 6 // "Color " — widest label + trailing space
	cdMarkerWidth  = 2 // "> " / "  "
	cdFieldColX    = cdLabelWidth + cdMarkerWidth
)

// paletteSwatches is the preset color grid shown in the calendar dialog.
var paletteSwatches = []string{
	"#0074D9", "#7FDBFF", "#39CCCC", "#B10DC9",
	"#F012BE", "#85144b", "#FF4136", "#FF851B",
	"#FFDC00", "#3D9970", "#2ECC40", "#01FF70",
	"#111111", "#AAAAAA",
}

type calendarDialogField int

const (
	cdFieldName calendarDialogField = iota
	cdFieldPalette
	cdFieldHex
	cdFieldDelete
	cdFieldCancel
	cdFieldSave
)

type calendarDialogKeyMap struct {
	Tab, ShiftTab, Save, Cancel key.Binding
	PaletteLeft, PaletteRight   key.Binding
	Confirm                     key.Binding
}

func defaultCalendarDialogKeys() calendarDialogKeyMap {
	return calendarDialogKeyMap{
		Tab:          key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		ShiftTab:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		Save:         key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
		Cancel:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		PaletteLeft:  key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "prev swatch")),
		PaletteRight: key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next swatch")),
		Confirm:      key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "confirm")),
	}
}

// ShortHelp returns the bindings shown in the dialog's footer. Order mirrors
// the user's typical flow: navigate, confirm, cancel.
func (k calendarDialogKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.Confirm, k.Cancel}
}

// CalendarDialogModel is a modal dialog for creating/editing a calendar.
// id == 0 means the user is creating a new calendar (Delete field is hidden).
type CalendarDialogModel struct {
	id           int64
	nameInput    textinput.Model
	hexInput     textinput.Model
	paletteIdx   int // -1 when the current hex is off-palette (custom color)
	field        calendarDialogField
	keys         calendarDialogKeyMap
	help         help.Model
	width        int
	height       int
	accentColor  color.Color
	errorColor   color.Color
	mutedColor   color.Color
	textDimColor color.Color
	errorMsg     string
}

// NewCalendarDialogModel builds a dialog for create (id==0) or edit. Pass the
// current theme so focused fields can highlight correctly.
func NewCalendarDialogModel(id int64, name, hex string, theme Theme) CalendarDialogModel {
	nm := textinput.New()
	nm.Prompt = ""
	nm.SetValue(name)
	nm.SetWidth(28)
	nm.Focus()

	hx := textinput.New()
	hx.Prompt = ""
	hx.SetValue(hex)
	hx.SetWidth(10)
	// "#rrggbb" is the only accepted form, so cap the buffer and filter
	// keystrokes in Update so the field can never hold more than 7 chars
	// or any non-hex characters.
	hx.CharLimit = 7

	return CalendarDialogModel{
		id:           id,
		nameInput:    nm,
		hexInput:     hx,
		paletteIdx:   paletteIndexFor(hex),
		field:        cdFieldName,
		keys:         defaultCalendarDialogKeys(),
		help:         newThemedHelp(theme),
		accentColor:  theme.Selected,
		errorColor:   theme.Error,
		mutedColor:   theme.Muted,
		textDimColor: theme.TextDim,
	}
}

// isHexInputAllowed reports whether the printable text `t` can be inserted
// into the hex input at cursor position `pos` given the current value. It
// accepts only hex digits and a single leading '#' — matching what the
// #rrggbb regex expects on save.
func isHexInputAllowed(t string, pos int, current string) bool {
	for _, r := range t {
		switch {
		case r == '#':
			if pos != 0 || strings.ContainsRune(current, '#') {
				return false
			}
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			// ok
		default:
			return false
		}
	}
	return true
}

func paletteIndexFor(hex string) int {
	h := strings.TrimSpace(hex)
	for i, c := range paletteSwatches {
		if strings.EqualFold(c, h) {
			return i
		}
	}
	return -1
}

func (m CalendarDialogModel) SetSize(w, h int) CalendarDialogModel {
	m.width = w
	m.height = h
	return m
}

// BoxSize returns the dialog's actual rendered dimensions so the parent's
// overlay compositor doesn't reserve empty space around the content.
func (m CalendarDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m CalendarDialogModel) isEditing() bool { return m.id > 0 }

// actionButton carries everything the View and mouse/keyboard handlers need
// to place, render, and dispatch a single action button without recomputing
// widths or underline indices at each site.
type actionButton struct {
	field        calendarDialogField
	label        string
	underlineIdx int
	rendered     string
	start, end   int // column positions within cdContentWidth
}

// actionLayout builds the action-row button list in render order (Delete only
// when editing) and assigns each button a column range so View renders and
// handleMouse hit-tests against the same numbers.
func (m CalendarDialogModel) actionLayout() []actionButton {
	var labels []string
	var fields []calendarDialogField
	if m.isEditing() {
		labels = []string{"Delete", "Cancel", "Save"}
		fields = []calendarDialogField{cdFieldDelete, cdFieldCancel, cdFieldSave}
	} else {
		labels = []string{"Cancel", "Save"}
		fields = []calendarDialogField{cdFieldCancel, cdFieldSave}
	}
	indices := buttonDialogUnderlineIndices(labels)

	btns := make([]actionButton, len(labels))
	for i, label := range labels {
		b := actionButton{field: fields[i], label: label, underlineIdx: indices[i]}
		switch b.field {
		case cdFieldDelete:
			b.rendered = buttonDanger(label, indices[i], m.field == cdFieldDelete)
		case cdFieldCancel:
			b.rendered = button(label, indices[i], m.field == cdFieldCancel)
		case cdFieldSave:
			b.rendered = buttonStyled(label, indices[i], m.field == cdFieldSave, true)
		default:
			// cdFieldName, cdFieldPalette, cdFieldHex: not action buttons
		}
		btns[i] = b
	}

	const sep = 2 // "  " between Cancel and Save
	widthOf := func(i int) int { return lipgloss.Width(btns[i].rendered) }
	if m.isEditing() {
		delW, cancelW, saveW := widthOf(0), widthOf(1), widthOf(2)
		gap := max(cdContentWidth-delW-(cancelW+sep+saveW), 2)
		btns[0].start, btns[0].end = 0, delW
		btns[1].start = delW + gap
		btns[1].end = btns[1].start + cancelW
		btns[2].start = btns[1].end + sep
		btns[2].end = btns[2].start + saveW
	} else {
		cancelW, saveW := widthOf(0), widthOf(1)
		leftPad := max(cdContentWidth-(cancelW+sep+saveW), 0)
		btns[0].start, btns[0].end = leftPad, leftPad+cancelW
		btns[1].start = btns[0].end + sep
		btns[1].end = btns[1].start + saveW
	}
	return btns
}

// triggerField dispatches the action bound to an action-row field. Shared by
// the mouse click handler, the underlined-letter shortcut handler, and the
// Enter-on-focused-button path.
func (m CalendarDialogModel) triggerField(f calendarDialogField) (CalendarDialogModel, tea.Cmd) {
	switch f {
	case cdFieldSave:
		return m.tryEmitSave()
	case cdFieldCancel:
		return m, func() tea.Msg { return CalendarDialogClosedMsg{} }
	case cdFieldDelete:
		if m.isEditing() {
			id, name := m.id, m.nameInput.Value()
			return m, func() tea.Msg { return CalendarDeleteRequestedMsg{ID: id, Name: name} }
		}
	default:
		// cdFieldName, cdFieldPalette, cdFieldHex: no action on trigger
	}
	return m, nil
}

// handleMouse routes a left-click to the field or button at the click
// coordinates. Click-to-focus for Name/Hex; click-to-select for swatches;
// click-to-trigger for Delete/Cancel/Save. All other clicks are ignored so
// dragging outside the dialog doesn't steal focus unexpectedly.
func (m CalendarDialogModel) handleMouse(msg tea.MouseClickMsg) (CalendarDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}

	boxW, boxH := m.BoxSize()
	if m.width <= 0 || m.height <= 0 || boxW <= 0 || boxH <= 0 {
		return m, nil
	}
	dx := (m.width - boxW) / 2
	dy := (m.height - boxH) / 2
	// Top-left of the content area inside the border + padding.
	cx := dx + 1 + cdPadX
	cy := dy + 1 + cdPadTop
	lx := msg.X - cx
	ly := msg.Y - cy

	// Body row layout (0-indexed from cy):
	//   0: title     1: blank     2: name     3: palette     4: hex
	//   with errMsg: 5: err       6: blank    7: rule        8: actions
	//   no errMsg:   5: blank     6: rule     7: actions
	const (
		nameRowY    = 2
		paletteRowY = 3
		hexRowY     = 4
	)

	switch ly {
	case nameRowY:
		if lx >= cdFieldColX {
			return m.focusField(cdFieldName), nil
		}
	case paletteRowY:
		if lx >= cdFieldColX {
			m = m.focusField(cdFieldPalette)
			if idx, ok := swatchIndexAt(lx-cdFieldColX, m.paletteIdx); ok {
				m.paletteIdx = idx
				m.hexInput.SetValue(paletteSwatches[idx])
				m.errorMsg = ""
			}
			return m, nil
		}
	case hexRowY:
		if lx >= cdFieldColX {
			return m.focusField(cdFieldHex), nil
		}
	}

	actionY := 7
	if m.errorMsg != "" {
		actionY = 8
	}
	if ly != actionY {
		return m, nil
	}

	for _, b := range m.actionLayout() {
		if lx >= b.start && lx < b.end {
			return m.triggerField(b.field)
		}
	}
	return m, nil
}

// swatchIndexAt finds which palette swatch covers column x, where x is
// measured from the start of the swatch row. The currently-selected swatch
// renders as "[●]" (3 cells) and the others as "●" (1 cell); neighbors are
// joined by a single space.
func swatchIndexAt(x int, selected int) (int, bool) {
	if x < 0 {
		return 0, false
	}
	col := 0
	for i := range paletteSwatches {
		w := 1
		if i == selected {
			w = 3
		}
		if x >= col && x < col+w {
			return i, true
		}
		col += w + 1
	}
	return 0, false
}

func (m CalendarDialogModel) Update(msg tea.Msg) (CalendarDialogModel, tea.Cmd) {
	if mc, ok := msg.(tea.MouseClickMsg); ok {
		return m.handleMouse(mc)
	}
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		// Forward non-key messages (cursor blink, etc.) to the focused input.
		var cmd tea.Cmd
		switch m.field {
		case cdFieldName:
			m.nameInput, cmd = m.nameInput.Update(msg)
		case cdFieldHex:
			m.hexInput, cmd = m.hexInput.Update(msg)
		default:
			// cdFieldPalette, cdFieldDelete, cdFieldCancel, cdFieldSave: no-op
		}
		return m, cmd
	}

	switch {
	case key.Matches(kp, m.keys.Cancel):
		return m, func() tea.Msg { return CalendarDialogClosedMsg{} }
	case key.Matches(kp, m.keys.Save):
		return m.tryEmitSave()
	case key.Matches(kp, m.keys.Tab):
		return m.advanceField(1), nil
	case key.Matches(kp, m.keys.ShiftTab):
		return m.advanceField(-1), nil
	}

	// Underlined-letter shortcuts for the action buttons. Only active when
	// focus isn't on a text input, so typing the letter into Name/Hex still
	// works as normal text entry.
	if m.field != cdFieldName && m.field != cdFieldHex {
		for _, b := range m.actionLayout() {
			if matchesButtonRune(kp, b.label, b.underlineIdx) {
				return m.triggerField(b.field)
			}
		}
	}

	switch m.field {
	case cdFieldName:
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	case cdFieldHex:
		// Swallow keystrokes that would put non-hex characters (or a stray
		// second '#') into the field. CharLimit caps the total length, but
		// the textinput's Validate hook only flags errors — it doesn't
		// block insertion — so we filter at the source instead.
		if kp.Text != "" && !isHexInputAllowed(kp.Text, m.hexInput.Position(), m.hexInput.Value()) {
			return m, nil
		}
		prev := m.hexInput.Value()
		var cmd tea.Cmd
		m.hexInput, cmd = m.hexInput.Update(msg)
		if m.hexInput.Value() != prev {
			// Keep the palette cursor synced with whatever the user typed.
			m.paletteIdx = paletteIndexFor(m.hexInput.Value())
			m.errorMsg = ""
		}
		return m, cmd
	case cdFieldPalette:
		// A custom color (paletteIdx < 0) snaps to index 0 on either arrow so
		// the user lands inside the palette; subsequent arrows move from there.
		delta := 0
		switch {
		case key.Matches(kp, m.keys.PaletteLeft):
			delta = -1
		case key.Matches(kp, m.keys.PaletteRight):
			delta = 1
		}
		if delta != 0 {
			idx := m.paletteIdx
			if idx < 0 {
				idx = 0
			} else {
				idx += delta
			}
			if idx < 0 {
				idx = 0
			} else if idx >= len(paletteSwatches) {
				idx = len(paletteSwatches) - 1
			}
			m.paletteIdx = idx
			m.hexInput.SetValue(paletteSwatches[idx])
			m.errorMsg = ""
		}
		return m, nil
	case cdFieldSave, cdFieldCancel, cdFieldDelete:
		if key.Matches(kp, m.keys.Confirm) {
			return m.triggerField(m.field)
		}
	}
	return m, nil
}

// advanceField cycles through the active fields. The Delete field is only
// included when editing (id > 0). Tab order is left-to-right, top-to-bottom:
// Name → Palette → Hex → [Delete] → Cancel → Save.
func (m CalendarDialogModel) advanceField(delta int) CalendarDialogModel {
	fields := []calendarDialogField{cdFieldName, cdFieldPalette, cdFieldHex}
	if m.isEditing() {
		fields = append(fields, cdFieldDelete)
	}
	fields = append(fields, cdFieldCancel, cdFieldSave)

	idx := 0
	for i, f := range fields {
		if f == m.field {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(fields)) % len(fields)
	return m.focusField(fields[idx])
}

// focusField moves focus to f and updates textinput focus accordingly. Kept
// as a single helper so Tab navigation and click-to-focus stay in sync.
func (m CalendarDialogModel) focusField(f calendarDialogField) CalendarDialogModel {
	m.field = f
	m.nameInput.Blur()
	m.hexInput.Blur()
	switch f {
	case cdFieldName:
		m.nameInput.Focus()
	case cdFieldHex:
		m.hexInput.Focus()
	default:
		// cdFieldPalette, cdFieldDelete, cdFieldCancel, cdFieldSave: no input focus
	}
	return m
}

// tryEmitSave validates inputs; on success returns a cmd emitting CalendarSavedMsg;
// on failure populates errorMsg and returns no cmd.
func (m CalendarDialogModel) tryEmitSave() (CalendarDialogModel, tea.Cmd) {
	name := strings.TrimSpace(m.nameInput.Value())
	if name == "" {
		m.errorMsg = "Name is required"
		return m, nil
	}
	hex := strings.TrimSpace(m.hexInput.Value())
	if !hexRE.MatchString(hex) {
		m.errorMsg = "Color must be #rrggbb"
		return m, nil
	}
	m.errorMsg = ""
	id := m.id
	return m, func() tea.Msg {
		return CalendarSavedMsg{ID: id, Name: name, Color: hex}
	}
}

func (m CalendarDialogModel) titleText() string {
	if m.isEditing() {
		return "Edit calendar"
	}
	return "New calendar"
}

func (m CalendarDialogModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Render(m.titleText())
	ruleStyle := lipgloss.NewStyle().Foreground(m.mutedColor)
	rule := ruleStyle.Render(strings.Repeat("─", cdContentWidth))

	labelStyle := lipgloss.NewStyle().Width(cdLabelWidth).Foreground(m.textDimColor)
	focusMarker := lipgloss.NewStyle().Foreground(m.textDimColor).Bold(true).Render("> ")
	idleMarker := "  "
	marker := func(f calendarDialogField) string {
		if m.field == f {
			return focusMarker
		}
		return idleMarker
	}

	nameRow := labelStyle.Render("Name") + marker(cdFieldName) + m.nameInput.View()

	// Wrap the selected swatch in brackets so its position is visible even
	// when focus is elsewhere, and readable for color-blind users. Brackets
	// glow with the accent color while the palette is focused.
	dotStyle := func(c string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("●")
	}
	swatches := make([]string, 0, len(paletteSwatches))
	for i, c := range paletteSwatches {
		if i == m.paletteIdx {
			brCol := m.mutedColor
			if m.field == cdFieldPalette {
				brCol = m.accentColor
			}
			brStyle := lipgloss.NewStyle().Foreground(brCol).Bold(true)
			swatches = append(swatches, brStyle.Render("[")+dotStyle(c)+brStyle.Render("]"))
		} else {
			swatches = append(swatches, dotStyle(c))
		}
	}
	// Join with a single space; the bracket chars themselves provide the
	// extra cell around the selected dot so the spacing stays uniform.
	paletteRow := labelStyle.Render("Color") + marker(cdFieldPalette) + strings.Join(swatches, " ")

	// Live preview of whatever hex the user is typing, so a custom color
	// is visible before the field passes validation on save. Only shown
	// once the value parses as #rrggbb so partial input doesn't flash
	// garbage colors as the user types.
	hexVal := strings.TrimSpace(m.hexInput.Value())
	hexPreview := ""
	if hexRE.MatchString(hexVal) {
		hexPreview = "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(hexVal)).Render("●")
	}
	customNote := ""
	if m.paletteIdx < 0 && hexRE.MatchString(hexVal) {
		customNote = "  " + lipgloss.NewStyle().Foreground(m.textDimColor).Italic(true).Render("(custom)")
	}
	hexRow := labelStyle.Render("Hex") + marker(cdFieldHex) + m.hexInput.View() + hexPreview + customNote

	// Error sits directly below the form, aligned with the input column, so
	// it points at the field that's wrong instead of floating at the bottom.
	var errBlock string
	if m.errorMsg != "" {
		errBlock = "\n" + strings.Repeat(" ", cdFieldColX) +
			lipgloss.NewStyle().Foreground(m.errorColor).Render(m.errorMsg)
	}

	// Action row: Delete on the far left (destructive, least prominent),
	// Cancel + Save on the far right. Save is the primary action. Layout is
	// computed in actionLayout so View and handleMouse stay in sync.
	var actionsRow string
	prev := 0
	for _, b := range m.actionLayout() {
		actionsRow += strings.Repeat(" ", b.start-prev) + b.rendered
		prev = b.end
	}

	m.help.SetWidth(cdContentWidth)
	helpText := lipgloss.NewStyle().
		Width(cdContentWidth).
		Align(lipgloss.Center).
		Render(m.help.ShortHelpView(m.keys.ShortHelp()))

	body := strings.Join([]string{
		title,
		"",
		nameRow,
		paletteRow,
		hexRow,
	}, "\n") + errBlock + "\n\n" + rule + "\n" + actionsRow + "\n\n" + helpText

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		PaddingTop(cdPadTop).
		PaddingLeft(cdPadX).
		PaddingRight(cdPadX).
		Render(body)
}
