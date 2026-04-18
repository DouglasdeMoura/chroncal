package tui

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// paletteSearchDebounce is the trailing-edge delay between the user's
// last keystroke and the async search firing. Below typing cadence,
// above the perceptible-lag threshold.
const paletteSearchDebounce = 150 * time.Millisecond

// paletteDebounceMsg fires after paletteSearchDebounce. The palette runs
// the search only if gen still matches its current generation counter —
// any keystroke since this tick was scheduled bumped the counter and
// invalidated this message.
type paletteDebounceMsg struct {
	gen   int
	query string
}

// PaletteCommand is a single entry shown in the command palette.
//
// Action is invoked (and its returned tea.Msg dispatched) when the user
// selects the command. Returning nil is allowed for commands that have
// no follow-up message.
//
// PrefixChar is an optional 1-cell leading marker (e.g. "●" for events).
// It's rendered in PrefixColor — kept separate from the Title so the
// fuzzy matcher and the selected-row highlight both work cleanly.
type PaletteCommand struct {
	ID          string
	Title       string
	Category    string
	Shortcut    string
	PrefixChar  string
	PrefixColor string
	Action      func() tea.Msg
}

// PaletteSelectedMsg is emitted when the user picks a command.
// The app-level Update dispatches Action as a tea.Cmd.
type PaletteSelectedMsg struct {
	Action func() tea.Msg
}

// PaletteClosedMsg is emitted when the user dismisses the palette.
type PaletteClosedMsg struct{}

// PaletteSearchFunc returns a tea.Cmd that resolves asynchronously into
// PaletteSearchResultsMsg. The palette invokes it whenever the query
// changes so callers can populate dynamic results (e.g., event matches).
type PaletteSearchFunc func(query string) tea.Cmd

// PaletteSearchResultsMsg delivers async search results back to the
// palette. Query is the input that produced Items; stale results (whose
// Query no longer matches the current input) are discarded.
type PaletteSearchResultsMsg struct {
	Query string
	Items []PaletteCommand
}

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

// Maximum number of rows visible in the list area. The dialog reserves
// space for this many rows so the prompt stays anchored as the filter
// changes; the underlying list handles paging when matches exceed this.
const palettelistMaxRows = 10

// paletteListItem wraps PaletteCommand so it satisfies list.Item.
type paletteListItem struct{ cmd PaletteCommand }

func (i paletteListItem) FilterValue() string { return i.cmd.Title }

// PaletteModel renders a centered, fuzzy-filterable list of commands.
//
// commands are the static entries seeded at construction (navigation,
// view-mode switches, etc.). dynamic holds async results from searchFn
// and is merged into filtered on every refilter. The bubbles list owns
// cursor state and scroll/paging; we drive it explicitly from our own
// keymap so it never fights the textinput for keystrokes.
type PaletteModel struct {
	input     textinput.Model
	list      list.Model
	commands  []PaletteCommand
	dynamic   []PaletteCommand
	dynQuery  string
	searchFn  PaletteSearchFunc
	searchGen int
	spinner   spinner.Model
	filtered  []PaletteCommand
	keys      paletteKeyMap
	theme     Theme
	width     int
	height    int
	ready     bool
}

// NewPaletteModel builds a palette seeded with the given commands.
// searchFn, if non-nil, is invoked on every query change to fetch
// additional entries asynchronously (e.g., matching events).
func NewPaletteModel(commands []PaletteCommand, theme Theme, searchFn PaletteSearchFunc) (PaletteModel, tea.Cmd) {
	in := textinput.New()
	in.Placeholder = "Type to search…"
	in.CharLimit = 100
	in.Prompt = "› "
	cmd := in.Focus()

	l := list.New(nil, paletteDelegate{theme: theme}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowFilter(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	// Disable list's own keybindings — the palette routes navigation
	// explicitly so the textinput owns every other keypress.
	l.KeyMap = list.KeyMap{}
	// The empty-state string is formed as "No <plural>." — reuse the
	// same wording as the rest of the TUI's search affordances.
	l.SetStatusBarItemName("match", "matches")

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(theme.TextDim)

	m := PaletteModel{
		input:    in,
		list:     l,
		commands: commands,
		searchFn: searchFn,
		spinner:  sp,
		keys:     defaultPaletteKeys(),
		theme:    theme,
		ready:    true,
	}
	m.refilter()
	return m, cmd
}

// isSearching reports whether the palette is awaiting async results for
// the current query (debounce in flight or response not yet returned).
// Used to swap "no matches" for a spinner so the empty state never
// flashes between keystroke and first results.
func (m PaletteModel) isSearching() bool {
	if m.searchFn == nil {
		return false
	}
	q := strings.TrimSpace(m.input.Value())
	return q != "" && q != m.dynQuery
}

// SetSize stores the available screen dimensions. The palette sizes itself
// against this when rendering.
//
// A zero-value PaletteModel is a valid target (the app calls SetSize on
// every resize whether or not the palette is open); ready guards the
// embedded list.Model, whose SetSize panics without a delegate.
func (m PaletteModel) SetSize(w, h int) PaletteModel {
	m.width = w
	m.height = h
	if !m.ready {
		return m
	}
	boxW, boxH := m.boxDims()
	inputW := max(boxW-6, 10)
	m.input.SetWidth(inputW)
	listW := max(boxW-4, 10)
	listH := max(boxH-6, 1)
	m.list.SetSize(listW, listH)
	return m
}

// BoxSize returns the outer rendered size of the palette dialog.
func (m PaletteModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

// boxDims returns the palette's outer dimensions. Height is fixed
// (independent of the filtered count) so the input prompt stays anchored
// at the top of the dialog while the user types.
func (m PaletteModel) boxDims() (int, int) {
	w := min(60, max(m.width-4, 30))
	// border (2) + prompt (1) + sep (1) + list (max rows) + sep (1) + footer (1)
	h := palettelistMaxRows + 6
	if m.height > 0 && h > m.height-2 {
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
	case spinner.TickMsg:
		if !m.isSearching() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case paletteDebounceMsg:
		if msg.gen != m.searchGen || m.searchFn == nil {
			return m, nil
		}
		if msg.query != strings.TrimSpace(m.input.Value()) {
			return m, nil
		}
		return m, m.searchFn(msg.query)
	case PaletteSearchResultsMsg:
		if msg.Query == strings.TrimSpace(m.input.Value()) {
			m.dynamic = msg.Items
			m.dynQuery = msg.Query
			m.refilter()
		}
		return m, nil
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Close):
			return m, func() tea.Msg { return PaletteClosedMsg{} }
		case key.Matches(msg, m.keys.Enter):
			if len(m.filtered) == 0 {
				return m, nil
			}
			idx := m.list.Index()
			if idx < 0 || idx >= len(m.filtered) {
				return m, nil
			}
			picked := m.filtered[idx]
			return m, func() tea.Msg {
				return PaletteSelectedMsg{Action: picked.Action}
			}
		case key.Matches(msg, m.keys.Up):
			m.list.CursorUp()
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.list.CursorDown()
			return m, nil
		case key.Matches(msg, m.keys.PgUp):
			m.list.GoToStart()
			return m, nil
		case key.Matches(msg, m.keys.PgDown):
			m.list.GoToEnd()
			return m, nil
		}
	}

	prev := m.input.Value()
	wasSearching := m.isSearching()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != prev {
		query := strings.TrimSpace(m.input.Value())
		if query != m.dynQuery {
			m.dynamic = nil
			m.dynQuery = ""
		}
		m.refilter()
		// Bump the generation on every keystroke so any in-flight
		// debounce tick from a prior keystroke is dropped when it lands.
		m.searchGen++
		if query != "" && m.searchFn != nil {
			gen := m.searchGen
			tick := tea.Tick(paletteSearchDebounce, func(time.Time) tea.Msg {
				return paletteDebounceMsg{gen: gen, query: query}
			})
			cmd = tea.Batch(cmd, tick)
			// Kick the spinner only on the leading edge — once it's
			// already ticking, the spinner.Update chain keeps it going.
			if !wasSearching {
				cmd = tea.Batch(cmd, m.spinner.Tick)
			}
		}
	}
	return m, cmd
}

// refilter recomputes the merged, sorted list of commands and dynamic
// search results against the current query.
//
// With an empty query we show the static command list only (dumping every
// event on open would be noise). For a non-empty query, static commands
// and dynamic items are scored together so a well-matching event can beat
// a weakly-matching command.
func (m *PaletteModel) refilter() {
	query := strings.TrimSpace(m.input.Value())
	type scored struct {
		cmd   PaletteCommand
		score int
	}
	var matches []scored
	if query == "" {
		for _, c := range m.commands {
			matches = append(matches, scored{cmd: c, score: 0})
		}
	} else {
		pool := make([]PaletteCommand, 0, len(m.commands)+len(m.dynamic))
		pool = append(pool, m.commands...)
		if query == m.dynQuery {
			pool = append(pool, m.dynamic...)
		}
		for _, c := range pool {
			hay := c.Title
			if c.Category != "" {
				hay = c.Category + " " + c.Title
			}
			if s, ok := fuzzyScore(query, hay); ok {
				matches = append(matches, scored{cmd: c, score: s})
			}
		}
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].score > matches[j].score
		})
	}
	m.filtered = m.filtered[:0]
	items := make([]list.Item, 0, len(matches))
	for _, s := range matches {
		m.filtered = append(m.filtered, s.cmd)
		items = append(items, paletteListItem{cmd: s.cmd})
	}
	m.list.SetItems(items)
	if m.list.Index() >= len(items) {
		m.list.Select(max(0, len(items)-1))
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
	boxW, _ := m.boxDims()
	innerW := max(boxW-4, 20)

	promptLine := m.input.View()
	footer := m.renderFooter(innerW)
	sep := lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", innerW))

	listView := m.list.View()
	if m.isSearching() && len(m.filtered) == 0 {
		listView = m.renderSearching(innerW)
	}

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		promptLine,
		sep,
		listView,
		sep,
		footer,
	)

	return lipgloss.NewStyle().
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.NoColor{}).
		Render(body)
}

// paletteDelegate renders one row per item. Each visual segment carries
// the selected background so the highlight covers colored prefixes too —
// rendering the row as a single style would let the prefix's ANSI reset
// terminate the background mid-row.
type paletteDelegate struct {
	theme Theme
}

func (d paletteDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	li, ok := item.(paletteListItem)
	if !ok {
		return
	}
	width := m.Width()
	if width <= 0 {
		return
	}
	fmt.Fprint(w, renderPaletteRow(li.cmd, width, index == m.Index(), d.theme))
}

func (paletteDelegate) Height() int                         { return 1 }
func (paletteDelegate) Spacing() int                        { return 0 }
func (paletteDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func renderPaletteRow(c PaletteCommand, width int, selected bool, theme Theme) string {
	// Fixed 2-cell prefix zone so event titles line up with non-event
	// titles. Rows with a PrefixChar render `<char> `; other rows render
	// two spaces.
	const prefixW = 2

	right := c.Shortcut
	rightW := lipgloss.Width(right)

	title := c.Title
	avail := width - prefixW - rightW
	if avail < lipgloss.Width(title)+1 {
		title = truncate(title, max(avail-1, 3))
	}
	gap := max(avail-lipgloss.Width(title), 1)

	// Apply the selected background to every segment so the highlight
	// extends across the prefix's foreground color too.
	base := lipgloss.NewStyle()
	if selected {
		base = base.Background(theme.Selected)
	}

	out := strings.Builder{}
	if c.PrefixChar != "" {
		ps := base
		if c.PrefixColor != "" {
			ps = ps.Foreground(lipgloss.Color(c.PrefixColor))
		} else {
			ps = ps.Faint(true)
		}
		out.WriteString(ps.Render(c.PrefixChar))
		out.WriteString(base.Render(" "))
	} else {
		out.WriteString(base.Render("  "))
	}
	out.WriteString(base.Foreground(theme.Text).Render(title))
	out.WriteString(base.Render(strings.Repeat(" ", gap)))
	if right != "" {
		out.WriteString(base.Foreground(theme.TextDim).Render(right))
	}
	return out.String()
}

// renderSearching fills the list area with a centered spinner + label so
// the dialog doesn't flash "No matches" between the keystroke and the
// first result. Height matches m.list.Height() to keep the dialog stable.
func (m PaletteModel) renderSearching(width int) string {
	label := m.spinner.View() + " " + lipgloss.NewStyle().Foreground(m.theme.TextDim).Render("Searching…")
	height := m.list.Height()
	if height < 1 {
		height = 1
	}
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(label)
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
