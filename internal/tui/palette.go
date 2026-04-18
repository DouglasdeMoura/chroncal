package tui

import (
	"sort"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// PaletteCommand is a single entry shown in the command palette.
//
// Action is invoked (and its returned tea.Msg dispatched) when the user
// selects the command. Returning nil is allowed for commands that have
// no follow-up message.
type PaletteCommand struct {
	ID       string
	Title    string
	Category string
	Shortcut string
	Action   func() tea.Msg
}

// PaletteSelectedMsg is emitted when the user picks a command.
// The app-level Update dispatches Action as a tea.Cmd.
type PaletteSelectedMsg struct {
	Action func() tea.Msg
}

// PaletteClosedMsg is emitted when the user dismisses the palette.
type PaletteClosedMsg struct{}

type paletteKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Enter  key.Binding
	Close  key.Binding
	PgUp   key.Binding
	PgDown key.Binding
}

func defaultPaletteKeys() paletteKeyMap {
	return paletteKeyMap{
		Up:     key.NewBinding(key.WithKeys("up", "ctrl+p", "ctrl+k")),
		Down:   key.NewBinding(key.WithKeys("down", "ctrl+n", "ctrl+j")),
		Enter:  key.NewBinding(key.WithKeys("enter")),
		Close:  key.NewBinding(key.WithKeys("esc")),
		PgUp:   key.NewBinding(key.WithKeys("pgup")),
		PgDown: key.NewBinding(key.WithKeys("pgdown")),
	}
}

// PaletteModel renders a centered, fuzzy-filterable list of commands.
type PaletteModel struct {
	input    textinput.Model
	commands []PaletteCommand
	filtered []int // indexes into commands, sorted by score desc
	selected int
	keys     paletteKeyMap
	theme    Theme
	width    int
	height   int
}

// NewPaletteModel builds a palette seeded with the given commands.
func NewPaletteModel(commands []PaletteCommand, theme Theme) (PaletteModel, tea.Cmd) {
	in := textinput.New()
	in.Placeholder = "Type to search…"
	in.CharLimit = 100
	in.Prompt = "› "
	cmd := in.Focus()

	m := PaletteModel{
		input:    in,
		commands: commands,
		keys:     defaultPaletteKeys(),
		theme:    theme,
	}
	m.refilter()
	return m, cmd
}

// SetSize stores the available screen dimensions. The palette sizes itself
// against this when rendering.
func (m PaletteModel) SetSize(w, h int) PaletteModel {
	m.width = w
	m.height = h
	boxW, _ := m.boxDims()
	// Account for border + padding + prompt.
	inputW := max(boxW-6, 10)
	m.input.SetWidth(inputW)
	return m
}

// BoxSize returns the outer rendered size of the palette dialog.
func (m PaletteModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

// boxDims computes the target outer dimensions before rendering.
func (m PaletteModel) boxDims() (int, int) {
	w := min(60, max(m.width-4, 30))
	// Prompt line + blank + up to 10 rows + footer.
	rows := max(min(len(m.filtered), 10), 1)
	h := rows + 6
	if h > m.height-2 {
		h = max(m.height-2, 6)
	}
	return w, h
}

// SetCommands replaces the command list (re-applies current filter).
func (m PaletteModel) SetCommands(commands []PaletteCommand) PaletteModel {
	m.commands = commands
	m.refilter()
	return m
}

// Update handles input events for the palette.
func (m PaletteModel) Update(msg tea.Msg) (PaletteModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Close):
			return m, func() tea.Msg { return PaletteClosedMsg{} }
		case key.Matches(msg, m.keys.Enter):
			if len(m.filtered) == 0 {
				return m, nil
			}
			picked := m.commands[m.filtered[m.selected]]
			return m, func() tea.Msg {
				return PaletteSelectedMsg{Action: picked.Action}
			}
		case key.Matches(msg, m.keys.Up):
			if m.selected > 0 {
				m.selected--
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.selected < len(m.filtered)-1 {
				m.selected++
			}
			return m, nil
		case key.Matches(msg, m.keys.PgUp):
			m.selected = 0
			return m, nil
		case key.Matches(msg, m.keys.PgDown):
			if len(m.filtered) > 0 {
				m.selected = len(m.filtered) - 1
			}
			return m, nil
		}
	}

	prev := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != prev {
		m.refilter()
	}
	return m, cmd
}

// refilter recomputes the filtered/sorted index list against the query.
func (m *PaletteModel) refilter() {
	query := strings.TrimSpace(m.input.Value())
	type scored struct {
		idx   int
		score int
	}
	var matches []scored
	if query == "" {
		for i := range m.commands {
			matches = append(matches, scored{idx: i, score: 0})
		}
	} else {
		for i, c := range m.commands {
			hay := c.Title
			if c.Category != "" {
				hay = c.Category + " " + c.Title
			}
			if s, ok := fuzzyScore(query, hay); ok {
				matches = append(matches, scored{idx: i, score: s})
			}
		}
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].score > matches[j].score
		})
	}
	m.filtered = m.filtered[:0]
	for _, s := range matches {
		m.filtered = append(m.filtered, s.idx)
	}
	if m.selected >= len(m.filtered) {
		m.selected = max(0, len(m.filtered)-1)
	}
}

// fuzzyScore returns a match score for query against hay.
// The algorithm is a simple subsequence match with bonuses:
//   - Word-start matches (after space/punct/case boundary) score higher.
//   - Contiguous matches score higher.
//   - Earlier matches score slightly higher.
//
// Returns (score, true) on match, (0, false) otherwise.
func fuzzyScore(query, hay string) (int, bool) {
	if query == "" {
		return 0, true
	}
	q := []rune(strings.ToLower(query))
	h := []rune(hay)
	hl := make([]rune, len(h))
	for i, r := range h {
		hl[i] = unicode.ToLower(r)
	}

	var score int
	qi := 0
	lastMatch := -2
	prevRune := rune(' ')
	for i := 0; i < len(hl) && qi < len(q); i++ {
		if hl[i] == q[qi] {
			// Base point per match.
			score += 1
			// Word-start bonus.
			isBoundary := prevRune == ' ' || prevRune == '-' || prevRune == '_' ||
				prevRune == '/' || prevRune == '.' || prevRune == ':'
			isCaseBoundary := unicode.IsUpper(h[i]) && unicode.IsLower(prevRune)
			if isBoundary || isCaseBoundary {
				score += 4
			}
			// Contiguity bonus.
			if lastMatch == i-1 {
				score += 2
			}
			// Prefix bonus (query starts at position 0).
			if qi == 0 && i == 0 {
				score += 3
			}
			lastMatch = i
			qi++
		}
		prevRune = h[i]
	}
	if qi < len(q) {
		return 0, false
	}
	// Slight penalty for longer haystacks so shorter titles win ties.
	score -= len(h) / 10
	return score, true
}

// View renders the palette dialog.
func (m PaletteModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	boxW, boxH := m.boxDims()
	innerW := max(boxW-4, 20)
	// rows area height = boxH - 2 border - 2 padding - 1 prompt - 1 footer
	rowsH := max(boxH-6, 1)

	promptLine := m.input.View()

	// Build rows.
	var rows []string
	start := 0
	if m.selected >= rowsH {
		start = m.selected - rowsH + 1
	}
	end := min(start+rowsH, len(m.filtered))
	for i := start; i < end; i++ {
		cmdIdx := m.filtered[i]
		c := m.commands[cmdIdx]
		row := m.renderRow(c, innerW, i == m.selected)
		rows = append(rows, row)
	}
	for len(rows) < rowsH {
		rows = append(rows, strings.Repeat(" ", innerW))
	}

	footer := m.renderFooter(innerW)

	sep := lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", innerW))
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		promptLine,
		sep,
		lipgloss.JoinVertical(lipgloss.Left, rows...),
		sep,
		footer,
	)

	return lipgloss.NewStyle().
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.NoColor{}).
		Render(body)
}

func (m PaletteModel) renderRow(c PaletteCommand, width int, selected bool) string {
	title := c.Title
	right := c.Shortcut

	// Layout: "  title   ...padding...   right"
	leftPad := "  "
	avail := width - lipgloss.Width(leftPad) - lipgloss.Width(right)
	if avail < lipgloss.Width(title)+1 {
		// Truncate title to fit.
		maxTitle := max(avail-1, 3)
		title = truncate(title, maxTitle)
		avail = width - lipgloss.Width(leftPad) - lipgloss.Width(right)
	}
	gap := max(avail-lipgloss.Width(title), 1)

	if right != "" {
		right = lipgloss.NewStyle().Foreground(m.theme.TextDim).Render(right)
	}
	row := leftPad + title + strings.Repeat(" ", gap) + right
	style := lipgloss.NewStyle().Width(width).Foreground(m.theme.Text)
	if selected {
		style = style.Background(m.theme.Selected)
	}
	return style.Render(row)
}

func (m PaletteModel) renderFooter(width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(m.theme.Text)
	descStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	sepStyle := lipgloss.NewStyle().Foreground(m.theme.Muted)
	sep := sepStyle.Render(" · ")

	pairs := [][2]string{
		{"↑↓", "navigate"},
		{"enter", "select"},
		{"esc", "close"},
	}
	segs := make([]string, 0, len(pairs))
	for _, p := range pairs {
		segs = append(segs, keyStyle.Render(p[0])+" "+descStyle.Render(p[1]))
	}
	hint := strings.Join(segs, sep)
	if len(m.filtered) == 0 && strings.TrimSpace(m.input.Value()) != "" {
		hint = descStyle.Render("No matches.") + " " + hint
	}
	return lipgloss.NewStyle().
		Width(width).
		Align(lipgloss.Center).
		Render(hint)
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
