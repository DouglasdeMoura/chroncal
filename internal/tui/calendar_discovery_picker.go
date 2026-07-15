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
	discovery account.Discovery
	selected  map[string]bool
	shell     ListDialogModel
	theme     Theme
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
			SetTitle("Choose calendars · " + textsafe.Display(discovery.Account.DisplayName)).
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
	idx := m.shell.Selected()
	if idx < 0 || idx >= len(m.discovery.Calendars) {
		return m
	}
	remote := m.discovery.Calendars[idx]
	if !remote.Importable || remote.Imported {
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
			m.shell = m.shell.FocusAction(idx)
			m = m.refresh()
			return m, m.shell.actions[idx].Msg
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
	rows := make([]string, 0, len(m.discovery.Calendars))
	for _, remote := range m.discovery.Calendars {
		checkbox := Glyphs["checkbox.off"]
		if m.selected[remote.Path] {
			checkbox = Glyphs["checkbox.on"]
		}
		name := remote.Name
		if name == "" {
			name = remote.Path
		}
		tags := make([]string, 0, 2)
		switch {
		case remote.Imported:
			tags = append(tags, "imported")
		case !remote.Importable:
			tags = append(tags, "unsupported")
		}
		if remote.Access == caldav.CalendarAccessRead {
			tags = append(tags, "read-only")
		}
		row := checkbox + " "
		if len(tags) > 0 {
			row += lipgloss.NewStyle().Foreground(m.theme.Muted).Render("["+strings.Join(tags, ", ")+"]") + " "
		}
		row += textsafe.Display(name)
		rows = append(rows, row)
	}
	m.shell = m.shell.SetRows(rows)

	if len(m.discovery.Calendars) == 0 {
		m.shell = m.shell.SetEmptyList("No calendar collections found.", []string{"The server returned no CalDAV calendar collections."})
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	} else {
		idx := m.shell.Selected()
		remote := m.discovery.Calendars[idx]
		name := remote.Name
		if name == "" {
			name = remote.Path
		}
		access := string(remote.Access)
		if access == "" {
			access = "unknown"
		}
		components := strings.Join(remote.SupportedComponentSet, ", ")
		if components == "" {
			components = "not advertised"
		}
		details := []string{
			"URL: " + textsafe.Display(remote.Path),
			"Access: " + access,
			"Components: " + components,
		}
		if remote.Description != "" {
			details = append(details, "", textsafe.Display(remote.Description))
		}
		m.shell = m.shell.SetDetailTitle(textsafe.Display(name)).SetDetailLines(details)
	}

	count := 0
	for _, remote := range m.discovery.Calendars {
		if remote.Importable && !remote.Imported && m.selected[remote.Path] {
			count++
		}
	}
	label := "Import"
	if count > 0 {
		label = fmt.Sprintf("Import (%d)", count)
	}
	m.shell = m.shell.SetActions([]ListDialogAction{
		{Label: label, Primary: true, Msg: m.importSelected()},
		{Label: "Cancel", Msg: func() tea.Msg { return AccountCalendarPickerClosedMsg{} }},
	})
	keys := m.shell.Keys()
	m.shell = m.shell.SetShortHelp([]key.Binding{
		key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓", "navigate")),
		key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all")),
		keys.Tab,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "import")),
		keys.Close,
	})
	return m
}
