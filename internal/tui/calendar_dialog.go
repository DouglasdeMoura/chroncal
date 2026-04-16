package tui

import (
	"image/color"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
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

// paletteSwatches is the preset color grid shown in the calendar dialog.
var paletteSwatches = []string{
	"#0074D9", "#7FDBFF", "#39CCCC", "#B10DC9",
	"#F012BE", "#85144b", "#FF4136", "#FF851B",
	"#FFDC00", "#3D9970", "#2ECC40", "#01FF70",
	"#111111", "#AAAAAA",
}

// Form field indices.
const (
	cdIdxName    = 0
	cdIdxPalette = 1
	cdIdxHex     = 2
)

// ---------------------------------------------------------------------------
// PaletteField
// ---------------------------------------------------------------------------

// PaletteField is a FormField that cycles through color swatches with
// left/right arrows. The selected swatch is wrapped in brackets.
type PaletteField struct {
	swatches    []string
	selected    int // -1 for custom/off-palette
	focused     bool
	accentColor color.Color
	mutedColor  color.Color
}

func NewPaletteField(swatches []string, selected int, accent, muted color.Color) *PaletteField {
	return &PaletteField{
		swatches:    swatches,
		selected:    selected,
		accentColor: accent,
		mutedColor:  muted,
	}
}

func (f *PaletteField) Selected() int     { return f.selected }
func (f *PaletteField) SetSelected(i int) { f.selected = i }

func (f *PaletteField) Value() string {
	if f.selected >= 0 && f.selected < len(f.swatches) {
		return f.swatches[f.selected]
	}
	return ""
}

func (f *PaletteField) Update(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		n := len(f.swatches)
		switch msg.String() {
		case "left", "h":
			idx := f.selected
			if idx < 0 {
				idx = 0
			} else if idx > 0 {
				idx--
			}
			f.selected = idx
		case "right", "l":
			idx := f.selected
			if idx < 0 {
				idx = 0
			} else if idx < n-1 {
				idx++
			}
			f.selected = idx
		}
	}
	return nil
}

func (f *PaletteField) View() string {
	dot := func(c string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("●")
	}
	parts := make([]string, 0, len(f.swatches))
	for i, c := range f.swatches {
		if i == f.selected {
			brCol := f.mutedColor
			if f.focused {
				brCol = f.accentColor
			}
			br := lipgloss.NewStyle().Foreground(brCol).Bold(true)
			parts = append(parts, br.Render("[")+dot(c)+br.Render("]"))
		} else {
			parts = append(parts, dot(c))
		}
	}
	return strings.Join(parts, " ")
}

func (f *PaletteField) Focus() tea.Cmd {
	f.focused = true
	return nil
}

func (f *PaletteField) Blur() {
	f.focused = false
}

func (f *PaletteField) SetWidth(int)      {}
func (f *PaletteField) IsFocusable() bool { return true }

// ---------------------------------------------------------------------------
// HexColorField — TextField with inline preview dot
// ---------------------------------------------------------------------------

// HexColorField wraps a TextField and appends a live color preview dot
// and optional "(custom)" label to its View.
type HexColorField struct {
	input      *TextField
	paletteIdx int // -1 when off-palette
	dimColor   color.Color
}

func NewHexColorField(placeholder string, dimColor color.Color) *HexColorField {
	f := &HexColorField{
		input:    NewTextField(placeholder),
		dimColor: dimColor,
	}
	f.input.SetFilter(func(k tea.Key) bool {
		if k.Text == "" {
			return true
		}
		return isHexInputAllowed(k.Text, f.input.Position(), f.input.Value())
	})
	return f
}

func (f *HexColorField) Value() string             { return f.input.Value() }
func (f *HexColorField) SetValue(v string)         { f.input.SetValue(v) }
func (f *HexColorField) SetPaletteIdx(idx int)     { f.paletteIdx = idx }
func (f *HexColorField) Update(msg tea.Msg) tea.Cmd { return f.input.Update(msg) }
func (f *HexColorField) Focus() tea.Cmd            { return f.input.Focus() }
func (f *HexColorField) Blur()                     { f.input.Blur() }
func (f *HexColorField) SetWidth(w int)            { f.input.SetWidth(max(w-16, 8)) }
func (f *HexColorField) IsFocusable() bool         { return true }

func (f *HexColorField) View() string {
	base := f.input.View()
	hexVal := strings.TrimSpace(f.input.Value())
	if !hexRE.MatchString(hexVal) {
		return base
	}
	preview := "  " + lipgloss.NewStyle().Foreground(lipgloss.Color(hexVal)).Render("●")
	if f.paletteIdx < 0 {
		custom := "  " + lipgloss.NewStyle().Foreground(f.dimColor).Italic(true).Render("(custom)")
		return base + preview + custom
	}
	return base + preview
}

// ---------------------------------------------------------------------------
// Hex input filter
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// CalendarDialogModel
// ---------------------------------------------------------------------------

// CalendarDialogModel is a modal dialog for creating/editing a calendar.
type CalendarDialogModel struct {
	id           int64
	dialog       Dialog
	form         Form
	help         help.Model
	accentColor  color.Color
	mutedColor   color.Color
	textDimColor color.Color
}

// NewCalendarDialogModel builds a dialog for create (id==0) or edit.
func NewCalendarDialogModel(id int64, name, hex string, theme Theme) CalendarDialogModel {
	title := "New calendar"
	if id > 0 {
		title = "Edit calendar"
	}

	styles := DefaultDialogStyles()
	dialog := NewDialog(title, styles)
	dialog.SetWidth(56) // 56 total = 2 border + 4 padding + 50 content

	formStyles := DefaultFormStyles()
	formStyles.LabelLayout = LabelInline
	formStyles.ShowFocusMarker = true
	formStyles.ButtonAlign = ButtonAlignRight
	formStyles.ButtonRule = true

	nameField := NewTextField("")
	nameField.SetValue(name)
	nameField.SetCharLimit(256)

	palette := NewPaletteField(paletteSwatches, paletteIndexFor(hex), theme.Selected, theme.Muted)

	hexField := NewHexColorField("#rrggbb", theme.TextDim)
	hexField.SetValue(hex)
	hexField.input.SetCharLimit(7)
	hexField.SetPaletteIdx(paletteIndexFor(hex))

	form := NewForm("Save", formStyles,
		FormItem{Label: "Name", Field: nameField, Required: true},
		FormItem{Label: "Color", Field: palette},
		FormItem{Label: "Hex", Field: hexField, Required: true},
	)

	savedID := id
	form.OnSubmit(func(f *Form) tea.Cmd {
		hf := f.Field(cdIdxHex).(*HexColorField)
		hexVal := strings.TrimSpace(hf.Value())
		if !hexRE.MatchString(hexVal) {
			f.SetError(cdIdxHex, "Color must be #rrggbb")
			return nil
		}
		nf := f.Field(cdIdxName).(*TextField)
		nameVal := strings.TrimSpace(nf.Value())
		return func() tea.Msg {
			return CalendarSavedMsg{ID: savedID, Name: nameVal, Color: hexVal}
		}
	})

	form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return CalendarDialogClosedMsg{} }
	})

	m := CalendarDialogModel{
		id:           id,
		dialog:       dialog,
		form:         form,
		help:         newThemedHelp(theme),
		accentColor:  theme.Selected,
		mutedColor:   theme.Muted,
		textDimColor: theme.TextDim,
	}

	form.OnRebuild(func(f *Form) {
		pal := f.Field(cdIdxPalette).(*PaletteField)
		hf := f.Field(cdIdxHex).(*HexColorField)

		if f.Focused() == cdIdxPalette {
			if v := pal.Value(); v != "" {
				hf.SetValue(v)
			}
		} else if f.Focused() == cdIdxHex {
			pal.SetSelected(paletteIndexFor(hf.Value()))
		}

		hf.SetPaletteIdx(pal.Selected())
	})
	m.form = form

	return m
}

func (m CalendarDialogModel) SetSize(w, h int) CalendarDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.form.SetWidth(m.dialog.ContentWidth())
	return m
}

func (m CalendarDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m CalendarDialogModel) Update(msg tea.Msg) (CalendarDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			return m, func() tea.Msg { return CalendarDialogClosedMsg{} }
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
			var cmd tea.Cmd
			m.form, cmd = m.form.Submit()
			return m, cmd
		}
	}

	if mc, ok := msg.(tea.MouseClickMsg); ok {
		if mc.Button == tea.MouseLeft {
			// Translate screen coordinates to dialog-local coordinates.
			bw, bh := m.BoxSize()
			ox := (m.dialog.width - bw) / 2
			oy := (m.dialog.height - bh) / 2
			target := mouseResolve(mc.X-ox, mc.Y-oy)
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: target})
			return m, cmd
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m CalendarDialogModel) View() string {
	cw := m.dialog.ContentWidth()
	if cw <= 0 {
		cw = 46
	}
	m.help.SetWidth(cw)
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))

	content := mouseSweep(m.dialog.Box(m.form.View()))
	return content
}
