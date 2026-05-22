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
	scroll int
}

// helpTwoColThreshold is the minimum terminal width at which the help
// dialog lays out shortcuts in two columns. Below this the dialog shrinks
// to fit and renders a single stacked column.
const helpTwoColThreshold = 80

// helpDialogChrome counts the fixed rows the Dialog wraps around body
// content: border top/bottom (2), padding top (1), title + blank (2),
// blank + footer (2) = 7.
const helpDialogChrome = 7

func NewHelpDialogModel(theme Theme) HelpDialogModel {
	dialog := NewDialog("Keyboard Shortcuts", DefaultDialogStyles())
	return HelpDialogModel{dialog: dialog, theme: theme}
}

func (m HelpDialogModel) footerHint(scrollable bool) string {
	keyStyle := lipgloss.NewStyle().Foreground(m.theme.Text)
	descStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	sepStyle := lipgloss.NewStyle().Foreground(m.theme.Muted)
	sep := sepStyle.Render(" · ")

	pairs := [][2]string{}
	if scrollable {
		pairs = append(pairs, [2]string{"↑↓", "scroll"})
	}
	pairs = append(pairs,
		[2]string{"esc", "close"},
	)

	segs := make([]string, 0, len(pairs))
	for _, p := range pairs {
		segs = append(segs, keyStyle.Render(p[0])+" "+descStyle.Render(p[1]))
	}
	return strings.Join(segs, sep)
}

func (m HelpDialogModel) SetSize(w, h int) HelpDialogModel {
	m.width = w
	m.height = h
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if w >= helpTwoColThreshold {
		m.dialog.SetWidth(min(100, w-2))
	} else if w > 0 {
		m.dialog.SetWidth(w)
	}
	return m
}

func (m HelpDialogModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

func (m HelpDialogModel) Update(msg tea.Msg) (HelpDialogModel, tea.Cmd) {
	total := strings.Count(m.body(), "\n") + 1
	vp := m.viewportHeight()
	maxScroll := max(total-vp, 0)

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if key.Matches(msg, key.NewBinding(key.WithKeys("esc", "q", "?"))) {
			return m, func() tea.Msg { return HelpDialogClosedMsg{} }
		}
		switch msg.String() {
		case "up", "k":
			m.scroll--
		case "down", "j":
			m.scroll++
		case "pgup":
			m.scroll -= vp
		case "pgdown", " ":
			m.scroll += vp
		case "home", "g":
			m.scroll = 0
		case "end", "G":
			m.scroll = maxScroll
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scroll -= 3
		case tea.MouseWheelDown:
			m.scroll += 3
		}
	}
	m.scroll = max(min(m.scroll, maxScroll), 0)
	return m, nil
}

func (m HelpDialogModel) viewportHeight() int {
	if m.height <= 0 {
		return 1
	}
	return max(m.height-helpDialogChrome, 1)
}

type helpEntry struct {
	keys string
	desc string
}

type helpSection struct {
	title   string
	entries []helpEntry
}

// sections groups shortcuts by task — "what is the user trying to do?" —
// rather than by which widget owns the binding. This mirrors the
// macOS Keyboard Shortcuts panel: a shortcut appears exactly once,
// near its sibling actions, and context tags in parens disambiguate
// the few keys whose meaning depends on focus (e.g. "enter (sidebar)").
func (m HelpDialogModel) sections() []helpSection {
	return []helpSection{
		{
			title: "Getting Around",
			entries: []helpEntry{
				{"←→↑↓ · hjkl", "move cursor"},
				{"[ · pgup", "previous week / month"},
				{"] · pgdn", "next week / month"},
				{"t", "today"},
				{"m · w · d · a", "month / week / day / agenda view"},
			},
		},
		{
			title: "Events",
			entries: []helpEntry{
				{"c", "new event"},
				{"enter", "open / select day"},
				{"e", "edit"},
				{"ctrl+d", "duplicate"},
				{"x · delete", "delete"},
				{"u", "undo last delete"},
				{"o", "toggle empty days (agenda)"},
				{"←→ · hl", "previous / next event (popup)"},
				{"y · n · m", "RSVP yes / no / maybe"},
			},
		},
		{
			title: "Calendars",
			entries: []helpEntry{
				{"l", "new calendar"},
				{"C · shift+c", "manage calendars"},
				{"s", "sync all"},
				{"enter (sidebar)", "open calendar"},
				{"space (sidebar)", "toggle visibility"},
				{"*", "set as default"},
			},
		},
		{
			title: "Command Palette",
			entries: []helpEntry{
				{"/ · ctrl+k", "open"},
				{"↑↓ · ctrl+k/j", "move selection"},
				{"pgup · pgdn", "jump by page"},
				{"enter", "run command"},
				{"esc", "close"},
			},
		},
		{
			title: "Windows",
			entries: []helpEntry{
				{"D · shift+d", "recently deleted"},
				{"r (trash)", "restore"},
				{"?", "this help"},
				{"\\", "toggle sidebar"},
				{"#", "toggle week numbers"},
				{"tab · shift+tab", "move focus"},
				{"esc · q", "close (read-only dialogs)"},
				{"esc", "close (forms)"},
				{"ctrl+s", "save form"},
				{"ctrl+c", "force quit"},
				{"q (main view)", "quit"},
			},
		},
	}
}

func (m HelpDialogModel) renderColumn(sections []helpSection, width int) string {
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
	return strings.TrimRight(out.String(), "\n")
}

func (m HelpDialogModel) body() string {
	sections := m.sections()
	width := m.dialog.ContentWidth()
	if width <= 0 {
		width = 96
	}

	if m.width > 0 && m.width < helpTwoColThreshold {
		return m.renderColumn(sections, width)
	}

	mid := (len(sections) + 1) / 2
	gap := 4
	colW := max((width-gap)/2, 20)

	leftBody := m.renderColumn(sections[:mid], colW)
	rightBody := m.renderColumn(sections[mid:], colW)
	leftCol := lipgloss.NewStyle().Width(colW).Render(leftBody)
	rightCol := lipgloss.NewStyle().Width(colW).Render(rightBody)
	spacer := lipgloss.NewStyle().Width(gap).Render("")
	return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, spacer, rightCol)
}

func (m HelpDialogModel) View() string {
	body := m.body()
	lines := strings.Split(body, "\n")
	vp := m.viewportHeight()
	total := len(lines)

	scroll := max(min(m.scroll, max(total-vp, 0)), 0)
	end := min(scroll+vp, total)
	clipped := strings.Join(lines[scroll:end], "\n")

	m.dialog.SetFooter(m.footerHint(total > vp))

	return m.dialog.Box(clipped)
}
