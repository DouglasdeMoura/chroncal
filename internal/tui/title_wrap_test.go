package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// longWrapTitle is a title that is wider than every dialog in these tests.
// It mixes ASCII words and wide (CJK) words so the wrap must measure the
// display width of each rune.
const longWrapTitle = "Quarterly planning review with the extended leadership team " +
	"and the regional partners 東京 会議室 about the budget for next year finalword"

// dialogInterior returns the plain text of each box line with the border
// glyphs and the padding removed.
func dialogInterior(out string) []string {
	var lines []string
	for line := range strings.SplitSeq(stripANSI(out), "\n") {
		s := strings.TrimSpace(line)
		s = strings.TrimPrefix(s, "│")
		s = strings.TrimSuffix(s, "│")
		lines = append(lines, strings.TrimSpace(s))
	}
	return lines
}

// TestEventViewDialog_LongTitleWraps covers issue #825. The modal shows
// the full event title over more than one line. No line is wider than the
// terminal.
func TestEventViewDialog_LongTitleWraps(t *testing.T) {
	ev := testViewEvent()
	ev.Title = longWrapTitle
	const termW, termH = 70, 40
	m := NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(termW, termH)
	out := m.View()

	bw, bh := m.BoxSize()
	require.LessOrEqual(t, bw, termW)
	require.LessOrEqual(t, bh, termH)
	for line := range strings.SplitSeq(out, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), bw, "line wider than the box: %q", stripANSI(line))
	}

	joined := strings.Join(dialogInterior(out), " ")
	assert.Contains(t, joined, longWrapTitle, "the full title must show across the wrapped lines")
	assert.NotContains(t, joined, "…", "the title must not be truncated")
	assert.Contains(t, out, "Delete")
	assert.Contains(t, out, "esc close")
}

// TestEventViewDialog_LongTitleShortTerminal makes sure that the extra
// title lines do not push the action bar or the footer out of a short
// terminal. The body keeps its scroll affordance.
func TestEventViewDialog_LongTitleShortTerminal(t *testing.T) {
	ev := testViewEvent()
	ev.Title = strings.Repeat(longWrapTitle+" ", 6)
	ev.Description = strings.Repeat("Lorem ipsum dolor sit amet.\n", 40)
	for _, termH := range []int{10, 14, 20} {
		m := NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(60, termH)
		out := m.View()
		_, bh := m.BoxSize()
		assert.LessOrEqual(t, bh, termH, "height %d: box must fit the terminal", termH)
		assert.Contains(t, out, "Delete", "height %d: action bar must show", termH)
		assert.Contains(t, out, "esc close", "height %d: footer must show", termH)
		assert.Contains(t, out, "more", "height %d: scroll hint must show", termH)

		before := m.body.YOffset()
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		assert.Greater(t, m.body.YOffset(), before, "height %d: body must scroll", termH)
	}
}

// TestListDialog_LongDetailTitleWraps covers the detail pane of the
// two-column dialogs. The pinned title wraps, and the body row offset
// includes the extra title lines.
func TestListDialog_LongDetailTitleWraps(t *testing.T) {
	const termW, termH = 120, 30
	m := NewListDialogModel(newThemedHelp(NewTheme(false))).
		SetSize(termW, termH).
		SetTitle("Events").
		SetRows([]string{"Row one", "Row two"}).
		SetDetailTitle(longWrapTitle).
		SetDetailLines([]string{"MARKER-ROW"}).
		SetActions([]ListDialogAction{
			{Label: "Edit", Msg: func() tea.Msg { return "edit" }},
		})
	out := m.View()

	for line := range strings.SplitSeq(out, "\n") {
		assert.LessOrEqual(t, lipgloss.Width(line), termW)
	}
	plain := stripANSI(out)
	assert.Contains(t, plain, "finalword", "the end of the title must show")
	assert.NotContains(t, plain, "…", "the title must not be truncated")

	idx := -1
	for i, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "MARKER-ROW") {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0)
	_, boxH := m.BoxSize()
	y, ok := m.BodyRowScreenY(0)
	require.True(t, ok)
	assert.Equal(t, (termH-boxH)/2+idx, y, "BodyRowScreenY must include the wrapped title lines")
}
