package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
)

type CalendarTransferClosedMsg struct{}

type CalendarImportPreviewRequestedMsg struct {
	Generation uint64
	Path       string
}

type CalendarImportRequestedMsg struct {
	Generation uint64
	Path       string
	Preview    icaltransfer.Preview
	CalendarID int64
	NewName    string
	NewColor   string
}

type CalendarExportWriteRequestedMsg struct {
	Generation uint64
	CalendarID int64
	Name       string
	Path       string
}

type calendarImportPreviewReadyMsg struct {
	Generation uint64
	Path       string
	Preview    icaltransfer.Preview
	Err        error
}

type calendarImportFinishedMsg struct {
	Generation uint64
	CalendarID int64
	Summary    icaltransfer.Summary
	Err        error
}

type calendarExportFinishedMsg struct {
	Generation uint64
	Path       string
	Summary    icaltransfer.ExportSummary
	Err        error
}

type calendarTransferMode int

const (
	calendarTransferImport calendarTransferMode = iota
	calendarTransferExport
)

type calendarTransferPhase int

const (
	calendarTransferPath calendarTransferPhase = iota
	calendarTransferDestination
)

type CalendarImportDestination struct {
	ID   int64
	Name string
}

// CalendarTransferDialogModel owns the path, preview, and destination steps
// for one-time iCal import and the path prompt for calendar export.
type CalendarTransferDialogModel struct {
	dialog       Dialog
	form         Form
	help         help.Model
	theme        Theme
	mode         calendarTransferMode
	phase        calendarTransferPhase
	path         string
	preview      icaltransfer.Preview
	calendarID   int64
	calendarName string
	errText      string
	generation   uint64
}

func NewCalendarImportDialogModel(theme Theme, generation ...uint64) CalendarTransferDialogModel {
	gen := firstGeneration(generation)
	m := CalendarTransferDialogModel{
		dialog:     NewDialog("Import iCal file", DefaultDialogStyles()),
		help:       newThemedHelp(theme),
		theme:      theme,
		mode:       calendarTransferImport,
		phase:      calendarTransferPath,
		generation: gen,
	}
	m.dialog.SetWidth(68)
	m.form = m.pathForm("Preview", "Path", "", func(path string) tea.Msg {
		return CalendarImportPreviewRequestedMsg{Generation: gen, Path: path}
	})
	return m
}

func NewCalendarExportDialogModel(calendarID int64, name string, theme Theme, generation ...uint64) CalendarTransferDialogModel {
	gen := firstGeneration(generation)
	m := CalendarTransferDialogModel{
		dialog:       NewDialog("Export calendar", DefaultDialogStyles()),
		help:         newThemedHelp(theme),
		theme:        theme,
		mode:         calendarTransferExport,
		phase:        calendarTransferPath,
		calendarID:   calendarID,
		calendarName: name,
		generation:   gen,
	}
	m.dialog.SetWidth(68)
	defaultPath := sanitizeICalFilename(name) + ".ics"
	m.form = m.pathForm("Export", "Save to", defaultPath, func(path string) tea.Msg {
		return CalendarExportWriteRequestedMsg{Generation: gen, CalendarID: calendarID, Name: name, Path: path}
	})
	return m
}

func firstGeneration(generations []uint64) uint64 {
	if len(generations) == 0 {
		return 0
	}
	return generations[0]
}

func (m CalendarTransferDialogModel) Generation() uint64 { return m.generation }

func (m CalendarTransferDialogModel) pathForm(submit, label, value string, message func(string) tea.Msg) Form {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelTop
	styles.ShowFocusMarker = true
	styles.ButtonAlign = ButtonAlignRight
	styles.ButtonRule = true

	pathField := NewTextField(value)
	pathField.SetValue(value)
	pathField.SetCharLimit(4096)
	form := NewForm(submit, styles, FormItem{Label: label, Field: pathField, Required: true})
	form.OnSubmit(func(f *Form) tea.Cmd {
		path := strings.TrimSpace(f.Field(0).(*TextField).Value())
		return func() tea.Msg { return message(path) }
	})
	form.OnCancel(func(*Form) tea.Cmd {
		return func() tea.Msg { return CalendarTransferClosedMsg{} }
	})
	return form
}

func (m CalendarTransferDialogModel) WithPreview(path string, preview icaltransfer.Preview, destinations []CalendarImportDestination) CalendarTransferDialogModel {
	m.path = path
	m.preview = preview
	m.phase = calendarTransferDestination
	m.errText = ""
	m.dialog = NewDialog("Import iCal file", DefaultDialogStyles())
	m.dialog.SetWidth(68)

	styles := DefaultFormStyles()
	styles.LabelLayout = LabelTop
	styles.ShowFocusMarker = true
	styles.ButtonAlign = ButtonAlignRight
	styles.ButtonRule = true

	options := make([]SelectOption, 1, 1+len(destinations))
	options[0] = SelectOption{Label: "Create New Local Calendar", Value: "new"}
	for _, destination := range destinations {
		options = append(options, SelectOption{Label: destination.Name, Value: strconv.FormatInt(destination.ID, 10)})
	}
	destinationField := NewSelectField(options)
	nameField := NewTextField("Imported calendar")
	nameField.SetValue(icalCalendarName(path))
	nameField.SetCharLimit(256)
	colorField := NewColorField(m.theme.CalendarSwatches, "#a6e3a1", m.theme.TextDim)

	summary := fmt.Sprintf("Preview: %d events · %d todos · %d journals · %d warnings",
		preview.Events, preview.Todos, preview.Journals, len(preview.Warnings))
	form := NewForm("Import", styles,
		FormItem{Label: "", Field: NewStaticField(summary, nil)},
		FormItem{Label: "Destination", Field: destinationField, Required: true},
		FormItem{Label: "New calendar name", Field: nameField},
		FormItem{Label: "New calendar color", Field: colorField},
	)
	form.OnSubmit(func(f *Form) tea.Cmd {
		destination := f.Field(1).(*SelectField).Value()
		name := strings.TrimSpace(f.Field(2).(*TextField).Value())
		if destination == "new" && name == "" {
			f.SetError(2, "Name is required for a new calendar")
			return nil
		}
		var calendarID int64
		if destination != "new" {
			calendarID, _ = strconv.ParseInt(destination, 10, 64)
		}
		request := CalendarImportRequestedMsg{
			Generation: m.generation,
			Path:       path, Preview: preview, CalendarID: calendarID,
			NewName: name, NewColor: f.Field(3).(*ColorField).Value(),
		}
		return func() tea.Msg { return request }
	})
	form.OnCancel(func(*Form) tea.Cmd {
		return func() tea.Msg { return CalendarTransferClosedMsg{} }
	})
	m.form = form
	return m
}

func (m CalendarTransferDialogModel) WithError(err error) CalendarTransferDialogModel {
	if err == nil {
		m.errText = ""
	} else {
		m.errText = err.Error()
	}
	return m
}

func (m CalendarTransferDialogModel) SetSize(width, height int) CalendarTransferDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.form.SetWidth(m.dialog.ContentWidth())
	return m
}

// SetInspectorSize sizes the transfer form for the manager's detail pane.
func (m CalendarTransferDialogModel) SetInspectorSize(width, _ int) CalendarTransferDialogModel {
	m.form.SetWidth(max(width, 1))
	return m
}

// InspectorView renders import/export without a second dialog border.
func (m CalendarTransferDialogModel) InspectorView(w, h int) string {
	w = max(w, 1)
	parts := []string{lipgloss.NewStyle().Bold(true).Render(truncateTo(m.dialog.title, w)), "", m.form.BodyView()}
	if m.errText != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(m.theme.Error).Render(truncateTo(m.errText, w)))
	}
	parts = append(parts, m.form.ButtonRowView())
	return padLines(strings.Split(strings.Join(parts, "\n"), "\n"), w, h)
}

func (m CalendarTransferDialogModel) BoxSize() (int, int) { return lipgloss.Size(m.View()) }

func (m CalendarTransferDialogModel) Update(msg tea.Msg) (CalendarTransferDialogModel, tea.Cmd) {
	if press, ok := msg.(tea.KeyPressMsg); ok && press.Code == tea.KeyEscape {
		return m, func() tea.Msg { return CalendarTransferClosedMsg{} }
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(size.Width, size.Height), nil
	}
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m CalendarTransferDialogModel) View() string {
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	parts := []string{m.form.BodyView()}
	if m.errText != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(m.theme.Error).Render(truncateTo(m.errText, m.dialog.ContentWidth())))
	}
	parts = append(parts, m.form.ButtonRowView())
	return mouseSweep(m.dialog.Box(strings.Join(parts, "\n")))
}

func icalCalendarName(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	ext := filepath.Ext(base)
	name := strings.TrimSpace(strings.TrimSuffix(base, ext))
	if name == "" || name == "." {
		return "Imported calendar"
	}
	return name
}

func sanitizeICalFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "calendar"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "calendar"
	}
	return b.String()
}

func calendarImportDestinations(calendars map[int64]CalendarInfo, preview icaltransfer.Preview) []CalendarImportDestination {
	ids := sortedCalendarIDs(calendars)
	destinations := make([]CalendarImportDestination, 0, len(ids))
	for _, id := range ids {
		info := calendars[id]
		if strings.EqualFold(strings.TrimSpace(info.RemoteAccess), "read") {
			continue
		}
		if !calendarSupportsImportPreview(info, preview) {
			continue
		}
		label := info.Name
		if info.AccountID == 0 {
			label += " · Local"
		} else if info.AccountName != "" {
			label += " · " + info.AccountName
		}
		destinations = append(destinations, CalendarImportDestination{ID: id, Name: label})
	}
	return destinations
}

func calendarSupportsImportPreview(info CalendarInfo, preview icaltransfer.Preview) bool {
	if info.AccountID == 0 || strings.TrimSpace(info.RemoteComponents) == "" {
		return true
	}
	components := make(map[string]bool)
	for _, component := range strings.FieldsFunc(info.RemoteComponents, func(r rune) bool { return r == ',' || r == ' ' }) {
		components[strings.ToUpper(component)] = true
	}
	return (preview.Events == 0 || components[icaltransfer.FamilyEvent]) &&
		(preview.Todos == 0 || components[icaltransfer.FamilyTodo]) &&
		(preview.Journals == 0 || components[icaltransfer.FamilyJournal])
}
