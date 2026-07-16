package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

// AccountCalendarsImportRequestedMsg applies the picker selection.
type AccountCalendarsImportRequestedMsg struct {
	AccountID int64
	Paths     []string
}

// AccountCalendarPickerClosedMsg closes the discovery picker without importing.
type AccountCalendarPickerClosedMsg struct{}

// AccountCalendarPickerModel presents every discovered collection, including
// read-only and unsupported rows, while only allowing usable event calendars
// to be selected for import.
type AccountCalendarPickerModel struct {
	discovery   account.Discovery
	selected    map[string]bool
	shell       ListDialogModel
	rowCalendar []int

	theme Theme
}

func NewAccountCalendarPickerModel(discovery account.Discovery, theme Theme) AccountCalendarPickerModel {
	selected := make(map[string]bool, len(discovery.Calendars))
	for _, remote := range discovery.Calendars {
		if remote.Importable && !remote.Imported {
			selected[remote.Path] = true
		}
	}
	m := AccountCalendarPickerModel{
		discovery: discovery,
		selected:  selected,
		shell: NewListDialogModel(newThemedHelp(theme)).
			SetTitle("Add Calendars").
			SetTitleContext(accountPickerIdentity(discovery.Account)).
			SetSelectedColor(theme.Selected),
		theme: theme,
	}
	return m.refresh()
}

func (m AccountCalendarPickerModel) SetSize(w, h int) AccountCalendarPickerModel {
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m AccountCalendarPickerModel) BoxSize() (int, int) { return m.shell.BoxSize() }

func (m AccountCalendarPickerModel) toggleCurrent() AccountCalendarPickerModel {
	remote, ok := m.currentRemote()
	if !ok || !remote.Importable || remote.Imported {
		return m
	}
	m.selected[remote.Path] = !m.selected[remote.Path]
	return m.refresh()
}

func (m AccountCalendarPickerModel) toggleAll() AccountCalendarPickerModel {
	allSelected := true
	for _, remote := range m.discovery.Calendars {
		if remote.Importable && !remote.Imported {
			allSelected = allSelected && m.selected[remote.Path]
		}
	}
	for _, remote := range m.discovery.Calendars {
		if !remote.Importable || remote.Imported {
			continue
		}
		m.selected[remote.Path] = !allSelected
	}
	return m.refresh()
}

func (m AccountCalendarPickerModel) importSelected() tea.Cmd {
	paths := make([]string, 0, len(m.selected))
	for _, remote := range m.discovery.Calendars {
		if remote.Importable && !remote.Imported && m.selected[remote.Path] {
			paths = append(paths, remote.Path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	accountID := m.discovery.Account.ID
	return func() tea.Msg { return AccountCalendarsImportRequestedMsg{AccountID: accountID, Paths: paths} }
}

func (m AccountCalendarPickerModel) Update(msg tea.Msg) (AccountCalendarPickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.SetSize(msg.Width, msg.Height), nil
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("space"))):
			if m.shell.FocusZone() == ListZoneList {
				return m.toggleCurrent(), nil
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("a"))):
			if m.shell.FocusZone() == ListZoneList {
				return m.toggleAll(), nil
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if m.shell.FocusZone() == ListZoneList {
				return m, m.importSelected()
			}
		}
		var cmd tea.Cmd
		var handled bool
		m.shell, cmd, handled = m.shell.HandleKey(msg, func() tea.Msg { return AccountCalendarPickerClosedMsg{} })
		if handled {
			return m.refresh(), cmd
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		if idx, ok := m.shell.RowAtPosition(msg.X, msg.Y); ok {
			m.shell = m.shell.SetFocusZone(ListZoneList).SetSelected(idx)
			return m.toggleCurrent(), nil
		}
		if idx, ok := m.shell.ActionAtPosition(msg.X, msg.Y); ok {
			var cmd tea.Cmd
			m.shell, cmd = m.shell.ClickAction(idx)
			return m.refresh(), cmd
		}

	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.shell, cmd = m.shell.HandleMouseWheel(msg)
		return m, cmd
	}
	return m, nil
}

func (m AccountCalendarPickerModel) View() string { return m.shell.View() }

func (m AccountCalendarPickerModel) refresh() AccountCalendarPickerModel {
	previousPath := m.currentRemotePath()
	rows, rowCalendar, disabledRows := m.buildRows()
	m.rowCalendar = rowCalendar
	m.shell = m.shell.SetRows(rows).SetDisabledRows(disabledRows)
	if previousPath != "" {
		for row, calendarIndex := range rowCalendar {
			if calendarIndex >= 0 && m.discovery.Calendars[calendarIndex].Path == previousPath {
				m.shell = m.shell.SetSelected(row)
				break
			}
		}
	}

	selectedRow := m.shell.Selected()
	listFocused := m.shell.FocusZone() == ListZoneList
	for row, calendarIndex := range rowCalendar {
		if calendarIndex < 0 {
			continue
		}
		rows[row] = m.calendarRowLabel(m.discovery.Calendars[calendarIndex], row == selectedRow, listFocused)
	}
	m.shell = m.shell.SetRows(rows).SetDisabledRows(disabledRows).SetSelected(selectedRow)

	if len(m.discovery.Calendars) == 0 {
		m.shell = m.shell.SetEmptyList("No calendars found.", []string{"This account did not return any calendar collections."})
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	} else if remote, ok := m.currentRemote(); ok {
		m.shell = m.shell.
			SetDetailTitle(m.calendarDetailTitle(remote)).
			SetDetailLines(m.calendarDetailLines(remote))
	} else {
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	}

	count := m.selectedCount()
	label := "Add Calendars"
	if count == 1 {
		label = "Add Calendar"
	} else if count > 1 {
		label = fmt.Sprintf("Add %d Calendars", count)
	}
	m.shell = m.shell.SetActions([]ListDialogAction{
		{Label: label, Primary: true, Disabled: count == 0, Msg: m.importSelected()},
		{Label: "Cancel", Msg: func() tea.Msg { return AccountCalendarPickerClosedMsg{} }},
	})
	keys := m.shell.Keys()
	m.shell = m.shell.SetShortHelp([]key.Binding{
		key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓", "navigate")),
		key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all")),
		keys.Tab,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "add")),
		keys.Close,
	})
	return m
}

func (m AccountCalendarPickerModel) buildRows() ([]string, []int, []int) {
	available := make([]int, 0, len(m.discovery.Calendars))
	added := make([]int, 0, len(m.discovery.Calendars))
	unavailable := make([]int, 0, len(m.discovery.Calendars))
	for idx, remote := range m.discovery.Calendars {
		switch {
		case remote.Imported:
			added = append(added, idx)
		case remote.Importable:
			available = append(available, idx)
		default:
			unavailable = append(unavailable, idx)
		}
	}

	rows := make([]string, 0, len(m.discovery.Calendars)+6)
	rowCalendar := make([]int, 0, cap(rows))
	disabledRows := make([]int, 0, 6)
	appendSection := func(title string, calendarIndices []int) {
		if len(calendarIndices) == 0 {
			return
		}
		if len(rows) > 0 {
			disabledRows = append(disabledRows, len(rows))
			rows = append(rows, "")
			rowCalendar = append(rowCalendar, -1)
		}
		disabledRows = append(disabledRows, len(rows))
		rows = append(rows, lipgloss.NewStyle().
			Foreground(m.theme.TextDim).
			Bold(true).
			Render(title))
		rowCalendar = append(rowCalendar, -1)
		for _, calendarIndex := range calendarIndices {
			rows = append(rows, "")
			rowCalendar = append(rowCalendar, calendarIndex)
		}
	}
	appendSection("Available", available)
	appendSection("Already Added", added)
	appendSection("Unavailable", unavailable)
	return rows, rowCalendar, disabledRows
}

func (m AccountCalendarPickerModel) currentRemotePath() string {
	remote, ok := m.currentRemote()
	if !ok {
		return ""
	}
	return remote.Path
}

func (m AccountCalendarPickerModel) currentRemote() (account.DiscoveredCalendar, bool) {
	row := m.shell.Selected()
	if row < 0 || row >= len(m.rowCalendar) {
		return account.DiscoveredCalendar{}, false
	}
	calendarIndex := m.rowCalendar[row]
	if calendarIndex < 0 || calendarIndex >= len(m.discovery.Calendars) {
		return account.DiscoveredCalendar{}, false
	}
	return m.discovery.Calendars[calendarIndex], true
}

func (m AccountCalendarPickerModel) calendarRowLabel(remote account.DiscoveredCalendar, current, listFocused bool) string {
	marker := Glyphs["checkbox.off"]
	switch {
	case remote.Imported:
		marker = Glyphs["status.ok"] + "  "
	case !remote.Importable:
		marker = "–  "
	case m.selected[remote.Path]:
		marker = Glyphs["checkbox.on"]
	}

	dot := Glyphs["dot"]
	if remote.Color != "" {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(remote.Color)).Render(dot)
	} else {
		dot = lipgloss.NewStyle().Foreground(m.theme.Muted).Render(dot)
	}
	name := remote.Name
	if strings.TrimSpace(name) == "" {
		name = "Unnamed calendar"
	}
	nameText := textsafe.Display(name)
	if remote.Access == caldav.CalendarAccessRead && !remote.Imported {
		nameText += lipgloss.NewStyle().Foreground(m.theme.TextDim).Render("  Read only")
	}

	nameStyle := lipgloss.NewStyle()
	if remote.Imported || !remote.Importable {
		nameStyle = nameStyle.Foreground(m.theme.TextDim)
	}
	if current {
		if listFocused {
			nameStyle = nameStyle.Reverse(true)
		} else {
			nameStyle = nameStyle.Background(m.theme.Selected).Foreground(m.theme.SelectedText)
		}
	}
	return marker + " " + dot + " " + nameStyle.Render(nameText)
}

func (m AccountCalendarPickerModel) calendarDetailTitle(remote account.DiscoveredCalendar) string {
	name := strings.TrimSpace(remote.Name)
	if name == "" {
		name = "Unnamed calendar"
	}
	dot := Glyphs["dot"]
	if remote.Color != "" {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(remote.Color)).Render(dot)
	}
	return dot + " " + textsafe.Display(name)
}

func (m AccountCalendarPickerModel) calendarDetailLines(remote account.DiscoveredCalendar) []string {
	lines := make([]string, 0, 8)
	if description := strings.TrimSpace(remote.Description); description != "" {
		lines = append(lines, textsafe.Display(description), "")
	}
	lines = append(lines,
		fmt.Sprintf("%-10s%s", "Access", humanCalendarAccess(remote.Access)),
		fmt.Sprintf("%-10s%s", "Contents", humanCalendarComponents(remote.SupportedComponentSet)),
		"",
	)
	switch {
	case remote.Imported:
		lines = append(lines, "Already added to Chroncal.")
	case !remote.Importable:
		lines = append(lines, unsupportedCalendarExplanation(remote.SupportedComponentSet))
	case remote.Access == caldav.CalendarAccessRead:
		lines = append(lines, "Changes made in Chroncal will not be uploaded to this calendar.")
	case m.selected[remote.Path]:
		lines = append(lines, "Ready to add.")
	default:
		lines = append(lines, "Select this calendar to add it.")
	}
	return lines
}

func (m AccountCalendarPickerModel) selectedCount() int {
	count := 0
	for _, remote := range m.discovery.Calendars {
		if remote.Importable && !remote.Imported && m.selected[remote.Path] {
			count++
		}
	}
	return count
}

func accountPickerIdentity(account account.Account) string {
	name := strings.TrimSpace(textsafe.Display(account.DisplayName))
	username := strings.TrimSpace(textsafe.Display(account.Username))
	switch {
	case name == "":
		return username
	case username == "" || strings.EqualFold(name, username):
		return name
	default:
		return name + " · " + username
	}
}

func humanCalendarAccess(access caldav.CalendarAccess) string {
	switch access {
	case caldav.CalendarAccessOwner, caldav.CalendarAccessWrite:
		return "Can edit"
	case caldav.CalendarAccessRead:
		return "Read only"
	default:
		return "Not reported"
	}
}

func humanCalendarComponents(components []string) string {
	names := make([]string, 0, len(components))
	for _, component := range components {
		var name string
		switch strings.ToUpper(strings.TrimSpace(component)) {
		case "VEVENT":
			name = "Events"
		case "VTODO":
			name = "Tasks"
		case "VJOURNAL":
			name = "Journals"
		case "VFREEBUSY":
			name = "Availability"
		default:
			continue
		}
		if !slicesContainsFold(names, name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "Not reported"
	}
	return strings.Join(names, ", ")
}

func unsupportedCalendarExplanation(components []string) string {
	contents := strings.ToLower(humanCalendarComponents(components))
	if contents == "not reported" {
		return "Can’t add this collection because the server did not advertise event support."
	}
	return "Can’t add this collection because it contains " + contents + ", not calendar events."
}

func slicesContainsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
