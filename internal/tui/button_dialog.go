package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

const noShortcut = -1

type buttonDialogKeyMap struct {
	LeftRight  key.Binding
	Tab        key.Binding
	EnterSpace key.Binding
	Close      key.Binding
}

type buttonDialogModel struct {
	message        string
	labels         []string
	selected       int
	keys           buttonDialogKeyMap
	width          int
	height         int
	extraShortcuts map[int][]key.Binding
}

func newButtonDialogModel(message string, selected int, labels ...string) buttonDialogModel {
	return buttonDialogModel{
		message:  message,
		labels:   append([]string(nil), labels...),
		selected: selected,
		keys: buttonDialogKeyMap{
			LeftRight:  key.NewBinding(key.WithKeys("left", "right")),
			Tab:        key.NewBinding(key.WithKeys("tab")),
			EnterSpace: key.NewBinding(key.WithKeys("enter", " ")),
			Close:      key.NewBinding(key.WithKeys("esc")),
		},
	}
}

func (m buttonDialogModel) SetSize(w, h int) buttonDialogModel {
	m.width = w
	m.height = h
	return m
}

func (m buttonDialogModel) BoxSize() (int, int) {
	view := m.View()
	return lipgloss.Size(view)
}

func (m buttonDialogModel) total() int { return len(m.labels) }

func (m buttonDialogModel) cancelIndex() int { return len(m.labels) - 1 }

func (m buttonDialogModel) Update(msg tea.Msg) (buttonDialogModel, int, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	}
	return m, 0, false
}

func (m buttonDialogModel) handleKey(msg tea.KeyPressMsg) (buttonDialogModel, int, bool) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, m.cancelIndex(), true
	case key.Matches(msg, m.keys.LeftRight), key.Matches(msg, m.keys.Tab):
		m.selected = (m.selected + 1) % m.total()
		return m, 0, false
	case key.Matches(msg, m.keys.EnterSpace):
		return m, m.selected, true
	}

	if choice := m.shortcutChoice(msg); choice != noShortcut {
		return m, choice, true
	}
	return m, 0, false
}

func (m buttonDialogModel) shortcutChoice(msg tea.KeyPressMsg) int {
	if msg.Text == "" {
		return noShortcut
	}

	underline := buttonDialogUnderlineIndices(m.labels)
	for i, label := range m.labels {
		if matchesButtonRune(msg, label, underline[i]) {
			return i
		}
		for _, binding := range m.extraShortcuts[i] {
			if key.Matches(msg, binding) {
				return i
			}
		}
	}
	return noShortcut
}

func (m buttonDialogModel) handleMouse(msg tea.MouseClickMsg) (buttonDialogModel, int, bool) {
	if msg.Button != tea.MouseLeft {
		return m, 0, false
	}

	ox, oy := m.buttonBarOrigin()
	if msg.Y != oy {
		return m, 0, false
	}

	x := ox
	for i, label := range m.labels {
		w := lipgloss.Width(button(label, 0, false))
		if msg.X >= x && msg.X < x+w {
			return m, i, true
		}
		x += w + 1
	}

	return m, 0, false
}

func (m buttonDialogModel) buttonBarOrigin() (int, int) {
	boxW, boxH := m.BoxSize()
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2

	buttonsW := 0
	for i, label := range m.labels {
		if i > 0 {
			buttonsW++
		}
		buttonsW += lipgloss.Width(button(label, 0, false))
	}
	contentW := boxW - 8
	centerOffset := (contentW - buttonsW) / 2

	return dialogX + 4 + centerOffset, dialogY + boxH - 3
}

func (m buttonDialogModel) View() string {
	underline := buttonDialogUnderlineIndices(m.labels)

	var buttons string
	for i, label := range m.labels {
		if i > 0 {
			buttons += " "
		}
		buttons += button(label, underline[i], m.selected == i)
	}

	content := lipgloss.JoinVertical(lipgloss.Center, m.message, "", buttons)

	return lipgloss.NewStyle().
		Padding(1, 3).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

func buttonDialogUnderlineIndices(labels []string) []int {
	indices := make([]int, len(labels))
	keys := make([]string, len(labels))
	used := make(map[string]struct{}, len(labels))
	firstCounts := make(map[string]int, len(labels))

	for i, label := range labels {
		idx, k := firstNonSpaceShortcut(label)
		indices[i] = idx
		keys[i] = k
		if k != "" {
			firstCounts[k]++
		}
	}

	for _, k := range keys {
		if k != "" && firstCounts[k] == 1 {
			used[k] = struct{}{}
		}
	}

	for i, k := range keys {
		if k != "" && firstCounts[k] == 1 {
			continue
		}
		idx, resolved := nextUniqueShortcut(labels[i], used)
		if idx >= 0 {
			indices[i] = idx
			used[resolved] = struct{}{}
		}
	}

	return indices
}

func firstNonSpaceShortcut(label string) (int, string) {
	for i, r := range label {
		if unicode.IsSpace(r) {
			continue
		}
		return i, strings.ToLower(string(r))
	}
	return -1, ""
}

func nextUniqueShortcut(label string, used map[string]struct{}) (int, string) {
	seen := make(map[string]struct{})
	for _, idx := range shortcutCandidates(label) {
		r, _ := utf8.DecodeRuneInString(label[idx:])
		if r == utf8.RuneError || unicode.IsSpace(r) {
			continue
		}
		k := strings.ToLower(string(r))
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if _, ok := used[k]; !ok {
			return idx, k
		}
	}
	return -1, ""
}

func shortcutCandidates(label string) []int {
	first := -1
	var wordStarts []int
	var rest []int
	prevSpace := true

	for i, r := range label {
		if unicode.IsSpace(r) {
			prevSpace = true
			continue
		}
		if first == -1 {
			first = i
		} else if prevSpace {
			wordStarts = append(wordStarts, i)
		} else {
			rest = append(rest, i)
		}
		prevSpace = false
	}

	var out []int
	if first >= 0 {
		out = append(out, first)
	}
	out = append(out, wordStarts...)
	out = append(out, rest...)
	return out
}

func matchesButtonRune(msg tea.KeyPressMsg, label string, idx int) bool {
	if msg.Text == "" || idx < 0 || idx >= len(label) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(label[idx:])
	if r == utf8.RuneError {
		return false
	}
	return strings.EqualFold(msg.Text, string(r))
}
