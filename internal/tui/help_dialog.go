package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// HelpDialogRequestedMsg requests opening the help dialog.
type HelpDialogRequestedMsg struct{}

// HelpDialogClosedMsg is emitted when the help dialog is dismissed.
type HelpDialogClosedMsg struct{}

// HelpDialogModel renders a keyboard-shortcut reference organized by context.
type HelpDialogModel struct {
	dialog Dialog
	theme  Theme
	width  int
	height int
}

func NewHelpDialogModel(theme Theme) HelpDialogModel {
	dialog := NewDialog("Keyboard Shortcuts", DefaultDialogStyles())
	dialog.SetWidth(80)
	dialog.SetFooter("esc · q · ? to close")
	return HelpDialogModel{dialog: dialog, theme: theme}
}

func (m HelpDialogModel) SetSize(w, h int) HelpDialogModel {
	m.width = w
	m.height = h
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func (m HelpDialogModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

func (m HelpDialogModel) Update(msg tea.Msg) (HelpDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if key.Matches(msg, key.NewBinding(key.WithKeys("esc", "q", "?"))) {
			return m, func() tea.Msg { return HelpDialogClosedMsg{} }
		}
	}
	return m, nil
}

type helpEntry struct {
	keys string
	desc string
}

type helpSection struct {
	title   string
	entries []helpEntry
}

func (m HelpDialogModel) sections() []helpSection {
	return []helpSection{
		{
			title: "Global",
			entries: []helpEntry{
				{"/  ·  ctrl+k", "command palette"},
				{"?", "this help"},
				{"q", "quit"},
				{"ctrl+c", "force quit"},
				{"tab  ·  shift+tab", "switch focus (main ↔ sidebar)"},
				{"\\", "toggle sidebar"},
			},
		},
		{
			title: "Views",
			entries: []helpEntry{
				{"m", "month view"},
				{"w", "week view"},
				{"d", "day view"},
				{"a", "agenda view"},
				{"t", "go to today"},
				{"c", "new event"},
			},
		},
		{
			title: "Navigation",
			entries: []helpEntry{
				{"↑ ↓ ← →  ·  h j k l", "move cursor / scroll"},
				{"[  ·  pgup", "previous week / month"},
				{"]  ·  pgdn", "next week / month"},
				{"enter", "select day / view event"},
				{"e", "toggle empty days (agenda)"},
			},
		},
		{
			title: "Calendars",
			entries: []helpEntry{
				{"l", "add calendar"},
				{"r", "manage calendars"},
				{"s", "sync all calendars"},
			},
		},
		{
			title: "Command palette",
			entries: []helpEntry{
				{"↑ ↓  ·  ctrl+k/j", "move selection"},
				{"pgup  ·  pgdn", "jump by page"},
				{"enter", "run command"},
				{"esc", "close"},
			},
		},
		{
			title: "Event popup",
			entries: []helpEntry{
				{"e", "edit"},
				{"d", "duplicate"},
				{"t", "delete"},
				{"y  ·  n  ·  m", "RSVP yes / no / maybe"},
				{"tab  ·  shift+tab", "cycle sections"},
				{"← →  ·  h l", "previous / next event"},
				{"esc  ·  q", "close"},
			},
		},
		{
			title: "Calendar list",
			entries: []helpEntry{
				{"a", "add"},
				{"e", "edit"},
				{"t", "delete"},
				{"space", "toggle visibility"},
				{"↑ ↓  ·  tab", "move selection"},
			},
		},
		{
			title: "Forms & editors",
			entries: []helpEntry{
				{"tab  ·  shift+tab", "next / previous field"},
				{"enter", "confirm / submit"},
				{"ctrl+s", "save"},
				{"esc", "cancel / close"},
			},
		},
	}
}

func (m HelpDialogModel) View() string {
	sections := m.sections()
	width := m.dialog.ContentWidth()
	if width <= 0 {
		width = 72
	}

	header := lipgloss.NewStyle().Foreground(m.theme.Primary).Bold(true)
	keyStyle := lipgloss.NewStyle().Foreground(m.theme.Text)
	descStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)

	var keyW int
	for _, s := range sections {
		for _, e := range s.entries {
			if w := lipgloss.Width(e.keys); w > keyW {
				keyW = w
			}
		}
	}
	if maxKey := width - 10; keyW > maxKey && maxKey > 0 {
		keyW = maxKey
	}
	descW := max(width-keyW-2, 1)

	var out strings.Builder
	for i, s := range sections {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(header.Render(s.title))
		out.WriteString("\n")
		for _, e := range s.entries {
			left := keyStyle.Width(keyW).Render(e.keys)
			right := descStyle.Width(descW).Render(e.desc)
			row := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
			out.WriteString(row)
			out.WriteString("\n")
		}
	}
	return m.dialog.Box(strings.TrimRight(out.String(), "\n"))
}
