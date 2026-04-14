package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

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

// PaletteSwatches is the preset color grid shown in the calendar dialog.
// Picked to match the Catppuccin-ish palette the app already uses.
var PaletteSwatches = []string{
	"#a6e3a1", "#f5c2e7", "#89b4fa", "#fab387",
	"#f38ba8", "#94e2d5", "#cba6f7", "#f9e2af",
	"#74c7ec", "#eba0ac", "#a6adc8", "#f2cdcd",
}

type calendarDialogField int

const (
	cdFieldName calendarDialogField = iota
	cdFieldPalette
	cdFieldHex
	cdFieldSave
	cdFieldDelete
)

type calendarDialogKeyMap struct {
	Tab, ShiftTab, Save, Cancel key.Binding
	PaletteLeft, PaletteRight   key.Binding
	Confirm                     key.Binding
}

func defaultCalendarDialogKeys() calendarDialogKeyMap {
	return calendarDialogKeyMap{
		Tab:          key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		ShiftTab:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
		Save:         key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
		Cancel:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		PaletteLeft:  key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "swatch left")),
		PaletteRight: key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "swatch right")),
		Confirm:      key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "confirm")),
	}
}

// CalendarDialogModel is a modal dialog for creating/editing a calendar.
// id == 0 means the user is creating a new calendar (Delete field is hidden).
type CalendarDialogModel struct {
	id          int64
	nameInput   textinput.Model
	hexInput    textinput.Model
	paletteIdx  int
	field       calendarDialogField
	keys        calendarDialogKeyMap
	width       int
	height      int
	accentColor color.Color
	errorColor  color.Color
	errorMsg    string
}

// NewCalendarDialogModel builds a dialog for create (id==0) or edit. Pass the
// current theme so focused fields can highlight correctly.
func NewCalendarDialogModel(id int64, name, hex string, theme Theme) CalendarDialogModel {
	nm := textinput.New()
	nm.SetValue(name)
	nm.Focus()

	hx := textinput.New()
	hx.SetValue(hex)

	palIdx := 0
	for i, c := range PaletteSwatches {
		if strings.EqualFold(c, hex) {
			palIdx = i
			break
		}
	}

	return CalendarDialogModel{
		id:          id,
		nameInput:   nm,
		hexInput:    hx,
		paletteIdx:  palIdx,
		field:       cdFieldName,
		keys:        defaultCalendarDialogKeys(),
		accentColor: theme.Selected,
		errorColor:  theme.Error,
	}
}

func (m CalendarDialogModel) SetSize(w, h int) CalendarDialogModel {
	m.width = w
	m.height = h
	return m
}

// BoxSize returns the dialog's rendered dimensions for the parent's compositor.
func (m CalendarDialogModel) BoxSize() (int, int) { return 50, 14 }

func (m CalendarDialogModel) isEditing() bool { return m.id > 0 }

func (m CalendarDialogModel) Update(msg tea.Msg) (CalendarDialogModel, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		// Forward non-key messages (cursor blink, etc.) to the focused input.
		var cmd tea.Cmd
		switch m.field {
		case cdFieldName:
			m.nameInput, cmd = m.nameInput.Update(msg)
		case cdFieldHex:
			m.hexInput, cmd = m.hexInput.Update(msg)
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

	switch m.field {
	case cdFieldName:
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	case cdFieldHex:
		var cmd tea.Cmd
		m.hexInput, cmd = m.hexInput.Update(msg)
		return m, cmd
	case cdFieldPalette:
		switch {
		case key.Matches(kp, m.keys.PaletteLeft):
			if m.paletteIdx > 0 {
				m.paletteIdx--
			}
		case key.Matches(kp, m.keys.PaletteRight):
			if m.paletteIdx < len(PaletteSwatches)-1 {
				m.paletteIdx++
			}
		case key.Matches(kp, m.keys.Confirm):
			m.hexInput.SetValue(PaletteSwatches[m.paletteIdx])
		}
		return m, nil
	case cdFieldSave:
		if key.Matches(kp, m.keys.Confirm) {
			return m.tryEmitSave()
		}
	case cdFieldDelete:
		if key.Matches(kp, m.keys.Confirm) && m.isEditing() {
			id, name := m.id, m.nameInput.Value()
			return m, func() tea.Msg { return CalendarDeleteRequestedMsg{ID: id, Name: name} }
		}
	}
	return m, nil
}

// advanceField cycles through the active fields. The Delete field is only
// included when editing (id > 0).
func (m CalendarDialogModel) advanceField(delta int) CalendarDialogModel {
	fields := []calendarDialogField{cdFieldName, cdFieldPalette, cdFieldHex, cdFieldSave}
	if m.isEditing() {
		fields = append(fields, cdFieldDelete)
	}
	idx := 0
	for i, f := range fields {
		if f == m.field {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(fields)) % len(fields)
	m.field = fields[idx]
	m.nameInput.Blur()
	m.hexInput.Blur()
	switch m.field {
	case cdFieldName:
		m.nameInput.Focus()
	case cdFieldHex:
		m.hexInput.Focus()
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

func (m CalendarDialogModel) View() string {
	const boxWidth = 48
	const padX = 2
	contentWidth := boxWidth - padX*2

	title := "New calendar"
	if m.isEditing() {
		title = "Edit calendar"
	}
	title = lipgloss.NewStyle().Bold(true).Render(title)

	nameRow := fmt.Sprintf("Name:  %s", m.nameInput.View())

	var swatches []string
	for i, c := range PaletteSwatches {
		sw := lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("●")
		if m.field == cdFieldPalette && i == m.paletteIdx {
			sw = lipgloss.NewStyle().Background(m.accentColor).Foreground(lipgloss.Color(c)).Render("●")
		}
		swatches = append(swatches, sw)
	}
	paletteRow := "Color: " + strings.Join(swatches, " ")
	hexRow := fmt.Sprintf("Hex:   %s", m.hexInput.View())

	save := buttonStyled("Save", -1, m.field == cdFieldSave, true)
	actions := save
	if m.isEditing() {
		del := button("Delete", -1, m.field == cdFieldDelete)
		actions = lipgloss.JoinHorizontal(lipgloss.Top, del, " ", save)
	}
	actionsRow := lipgloss.PlaceHorizontal(contentWidth, lipgloss.Right, actions)

	body := strings.Join([]string{title, "", nameRow, paletteRow, hexRow, "", actionsRow}, "\n")
	if m.errorMsg != "" {
		body += "\n\n" + lipgloss.NewStyle().Foreground(m.errorColor).Render(m.errorMsg)
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, padX).
		Width(boxWidth).
		Render(body)
}
