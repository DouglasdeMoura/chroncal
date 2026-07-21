package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
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

// AccountCalendarsReconcileRequestedMsg applies an existing account's desired
// final local calendar selection.
type AccountCalendarsReconcileRequestedMsg struct {
	AccountID     int64
	SelectedPaths []string
}

// AccountCalendarPickerClosedMsg closes the discovery picker without importing.
type AccountCalendarPickerClosedMsg struct{}
type accountRenameCancelledMsg struct{}

// AccountRenameRequestedMsg asks the application to persist a new
// human-facing account description.
type AccountRenameRequestedMsg struct {
	AccountID int64
	Name      string
}

type accountRenameFinishedMsg struct {
	account account.Account
	err     error
}

type AccountRenameDialogModel struct {
	dialog Dialog
	form   Form
	help   help.Model
}

func newAccountRenameDialogModel(acct account.Account, theme Theme) AccountRenameDialogModel {
	field := NewTextField("Account name")
	field.SetValue(acct.DisplayName)
	field.SetCharLimit(128)
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelTop
	form := NewForm("Rename", styles, FormItem{Label: "Name", Field: field, Required: true})
	form.OnSubmit(func(f *Form) tea.Cmd {
		name := strings.TrimSpace(f.Field(0).(*TextField).Value())
		return func() tea.Msg { return AccountRenameRequestedMsg{AccountID: acct.ID, Name: name} }
	})
	form.OnCancel(func(*Form) tea.Cmd {
		return func() tea.Msg { return accountRenameCancelledMsg{} }
	})
	return AccountRenameDialogModel{
		dialog: NewDialog("Rename Account", DefaultDialogStyles()),
		form:   form,
		help:   newThemedHelp(theme),
	}
}

func (m AccountRenameDialogModel) SetSize(w, h int) AccountRenameDialogModel {
	const maxWidth = 50
	w = min(w, maxWidth)
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.dialog.SetWidth(w)
	m.form.SetWidth(m.dialog.ContentWidth())
	return m
}

func (m AccountRenameDialogModel) Update(msg tea.Msg) (AccountRenameDialogModel, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(size.Width, size.Height), nil
	}
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok &&
		key.Matches(keyMsg, key.NewBinding(key.WithKeys("esc"))) {
		return m, func() tea.Msg { return accountRenameCancelledMsg{} }
	}
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m AccountRenameDialogModel) View() string {
	m.dialog.SetFooter(m.help.ShortHelpView([]key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}))
	return m.dialog.Box(m.form.View())
}

func (m AccountRenameDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

// AccountCalendarPickerModel presents every discovered collection, including
// read-only and unsupported rows. Add mode selects new imports; management
// mode edits the account's desired final local calendar set.
type AccountCalendarPickerModel struct {
	discovery   account.Discovery
	selected    map[string]bool
	manage      bool
	shell       ListDialogModel
	rowCalendar []int
	width       int
	height      int

	theme Theme
}

func NewAccountCalendarPickerModel(discovery account.Discovery, theme Theme) AccountCalendarPickerModel {
	return newAccountCalendarPickerModel(discovery, theme, false)
}

func NewAccountCalendarManagerModel(discovery account.Discovery, theme Theme) AccountCalendarPickerModel {
	return newAccountCalendarPickerModel(discovery, theme, true)
}

func newAccountCalendarPickerModel(discovery account.Discovery, theme Theme, manage bool) AccountCalendarPickerModel {
	selected := make(map[string]bool, len(discovery.Calendars))
	for _, remote := range discovery.Calendars {
		switch {
		case manage && remote.Imported:
			selected[remote.Path] = true
		case !manage && remote.Importable && !remote.Imported:
			selected[remote.Path] = true
		}
	}
	title := "Add Calendars"
	if manage {
		title = "Manage Calendars"
	}
	m := AccountCalendarPickerModel{
		discovery: discovery,
		selected:  selected,
		manage:    manage,
		shell: NewListDialogModel(newThemedHelp(theme)).
			SetTitle(title).
			SetTitleContext(accountPickerIdentity(discovery.Account)).
			SetSelectedColor(theme.Selected),
		theme: theme,
	}
	return m.refresh()
}

func (m AccountCalendarPickerModel) SetSize(w, h int) AccountCalendarPickerModel {
	m.width, m.height = w, h
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m AccountCalendarPickerModel) BoxSize() (int, int) { return lipgloss.Size(m.View()) }

func (m AccountCalendarPickerModel) toggleCurrent() AccountCalendarPickerModel {
	remote, ok := m.currentRemote()
	if !ok || !m.canToggle(remote) {
		return m
	}
	m.selected[remote.Path] = !m.selected[remote.Path]
	return m.refresh()
}

func (m AccountCalendarPickerModel) toggleAll() AccountCalendarPickerModel {
	if m.manage {
		for _, remote := range m.discovery.Calendars {
			if m.canToggle(remote) {
				m.selected[remote.Path] = true
			}
		}
		return m.refresh()
	}

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

func (m AccountCalendarPickerModel) applySelection() tea.Cmd {
	if !m.manage {
		return m.importSelected()
	}
	if !m.hasChanges() {
		return nil
	}
	paths := m.finalSelectedPaths()
	accountID := m.discovery.Account.ID
	return func() tea.Msg {
		return AccountCalendarsReconcileRequestedMsg{
			AccountID:     accountID,
			SelectedPaths: paths,
		}
	}
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
				return m, m.applySelection()
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

func (m AccountCalendarPickerModel) View() string {
	return m.shell.View()
}

func (m AccountCalendarPickerModel) refresh() AccountCalendarPickerModel {
	m.shell = m.shell.SetTitleContext(accountPickerIdentity(m.discovery.Account))
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

	actionLabel := "Add Calendars"
	actionDisabled := m.selectedCount() == 0
	enterHelp := "add"
	if m.manage {
		actionLabel = "Save Changes"
		actionDisabled = !m.hasChanges()
		enterHelp = "save"
	} else if count := m.selectedCount(); count == 1 {
		actionLabel = "Add Calendar"
	} else if count > 1 {
		actionLabel = fmt.Sprintf("Add %d Calendars", count)
	}
	actions := []ListDialogAction{
		{Label: actionLabel, Primary: true, Disabled: actionDisabled, Msg: m.applySelection()},
		{Label: "Cancel", Msg: func() tea.Msg { return AccountCalendarPickerClosedMsg{} }},
	}
	m.shell = m.shell.SetActions(actions)
	keys := m.shell.Keys()
	m.shell = m.shell.SetShortHelp([]key.Binding{
		key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓", "navigate")),
		key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "select all")),
		keys.Tab,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", enterHelp)),
		keys.Close,
	})
	return m
}

func (m AccountCalendarPickerModel) buildRows() ([]string, []int, []int) {
	available := make([]int, 0, len(m.discovery.Calendars))
	added := make([]int, 0, len(m.discovery.Calendars))
	unavailable := make([]int, 0, len(m.discovery.Calendars))
	for idx, remote := range m.discovery.Calendars {
		if m.manage {
			if remote.Missing || !remote.Importable {
				unavailable = append(unavailable, idx)
			} else {
				available = append(available, idx)
			}
			continue
		}
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
	if m.manage {
		appendSection("Calendars", available)
	} else {
		appendSection("Available", available)
		appendSection("Already Added", added)
	}
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
	case !m.canToggle(remote):
		marker = "–  "
	case m.manage && m.selected[remote.Path]:
		marker = Glyphs["checkbox.on"]
	case !m.manage && remote.Imported:
		marker = Glyphs["status.ok"] + "  "
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
	switch {
	case remote.Missing:
		nameText += lipgloss.NewStyle().Foreground(m.theme.TextDim).Render("  No longer available")
	case remote.Access == caldav.CalendarAccessRead:
		nameText += lipgloss.NewStyle().Foreground(m.theme.TextDim).Render("  Read only")
	}

	nameStyle := lipgloss.NewStyle()
	if remote.Missing || !m.canToggle(remote) || (!m.manage && remote.Imported) {
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
	lines := make([]string, 0, 12)
	if description := strings.TrimSpace(remote.Description); description != "" {
		lines = append(lines, textsafe.Display(description), "")
	}
	lines = append(lines,
		fmt.Sprintf("%-10s%s", "Access", humanCalendarAccess(remote.Access)),
		fmt.Sprintf("%-10s%s", "Contents", humanCalendarComponents(remote.SupportedComponentSet)),
		"",
	)
	if m.manage {
		switch {
		case remote.Missing && !m.selected[remote.Path]:
			lines = append(lines, "No longer available from the server.", "Will be removed from Chroncal.")
		case remote.Missing:
			lines = append(lines, "No longer available from the server.", "Kept in Chroncal until removed.")
		case remote.Imported && !m.selected[remote.Path]:
			lines = append(lines, "Will be removed from Chroncal.")
		case remote.Imported && !remote.Importable:
			lines = append(lines, "Already in Chroncal, but the server no longer advertises supported contents.")
		case remote.Imported:
			lines = append(lines, "Kept in Chroncal.")
		case !remote.Importable:
			lines = append(lines, unsupportedCalendarExplanation(remote.SupportedComponentSet))
		case m.selected[remote.Path]:
			lines = append(lines, "Ready to add.")
		default:
			lines = append(lines, "Not added to Chroncal.")
		}
		if summary := m.changeSummary(); summary != "" {
			lines = append(lines, "", summary)
		}
		return lines
	}

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

func (m AccountCalendarPickerModel) canToggle(remote account.DiscoveredCalendar) bool {
	if m.manage {
		return remote.Imported || (remote.Importable && !remote.Missing)
	}
	return remote.Importable && !remote.Imported
}

func (m AccountCalendarPickerModel) finalSelectedPaths() []string {
	paths := make([]string, 0, len(m.selected))
	for _, remote := range m.discovery.Calendars {
		if m.selected[remote.Path] && (remote.Imported || remote.Importable) {
			paths = append(paths, remote.Path)
		}
	}
	return paths
}

func (m AccountCalendarPickerModel) changeCounts() (added, removed int) {
	for _, remote := range m.discovery.Calendars {
		switch {
		case m.selected[remote.Path] && !remote.Imported:
			added++
		case !m.selected[remote.Path] && remote.Imported:
			removed++
		}
	}
	return added, removed
}

func (m AccountCalendarPickerModel) hasChanges() bool {
	added, removed := m.changeCounts()
	return added > 0 || removed > 0
}

func (m AccountCalendarPickerModel) changeSummary() string {
	added, removed := m.changeCounts()
	switch {
	case added > 0 && removed > 0:
		return fmt.Sprintf("%d to add · %d to remove", added, removed)
	case added > 0:
		return fmt.Sprintf("%d to add", added)
	case removed > 0:
		return fmt.Sprintf("%d to remove", removed)
	default:
		return ""
	}
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
