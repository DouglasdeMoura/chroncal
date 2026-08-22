package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// SelectField
// ---------------------------------------------------------------------------

// SelectOption is a single entry in a SelectField.
type SelectOption struct {
	Label string
	// PluralLabel, when set, is shown by QuantitySelectField in place of
	// Label whenever the amount is not exactly 1 (e.g. "Days" for "2").
	// Empty means the option has no separate plural form.
	PluralLabel string
	Value       string
}

// selectHighlight tracks which arrow was just pressed.
type selectHighlight int

const (
	selectNone selectHighlight = iota
	selectLeft
	selectRight
)

// selectFlashMsg is sent by a tick to clear the arrow highlight.
type selectFlashMsg struct{ id int }

const selectFlashDuration = 150 * time.Millisecond

// SelectField cycles through a list of options with left/right arrows.
type SelectField struct {
	options     []SelectOption
	selected    int
	maxWidth    int
	focused     bool
	renderLabel func(SelectOption, bool) string
	highlight   selectHighlight
	flashID     int // incremented per flash; stale ticks are ignored
	// arrowIndex keys the prev/next mouse targets by form-field index when
	// arrowIndexed is set. A click on an unfocused select's arrow can then
	// resolve to the owner field (issue #498). Nested selects (e.g. the
	// monthly select inside RecurrenceOnField) leave it unset and keep the
	// generic targets.
	arrowIndex   int
	arrowIndexed bool
}

// SetArrowIndex keys this select's prev/next arrow mouse targets to a form-field
// index, so a click on an unfocused arrow resolves to the right field.
func (f *SelectField) SetArrowIndex(i int) {
	f.arrowIndex = i
	f.arrowIndexed = true
}

func NewSelectField(options []SelectOption) *SelectField {
	f := &SelectField{options: options}
	f.updateMaxWidth()
	return f
}

func (f *SelectField) Selected() int     { return f.selected }
func (f *SelectField) SetSelected(i int) { f.selected = i }
func (f *SelectField) SelectedOption() SelectOption {
	if f.selected < 0 || f.selected >= len(f.options) {
		return SelectOption{}
	}
	return f.options[f.selected]
}
func (f *SelectField) Value() string { return f.SelectedOption().Value }
func (f *SelectField) SetOptions(options []SelectOption) {
	f.options = options
	if len(f.options) == 0 {
		f.selected = 0
		f.maxWidth = 0
		return
	}
	if f.selected >= len(f.options) {
		f.selected = len(f.options) - 1
	}
	if f.selected < 0 {
		f.selected = 0
	}
	f.updateMaxWidth()
}
func (f *SelectField) SetRenderLabel(fn func(SelectOption, bool) string) {
	f.renderLabel = fn
	f.updateMaxWidth()
}

func (f *SelectField) renderOptionLabel(opt SelectOption, focused bool) string {
	if f.renderLabel != nil {
		return f.renderLabel(opt, focused)
	}
	return opt.Label
}

func (f *SelectField) updateMaxWidth() {
	maxW := 0
	for _, o := range f.options {
		if w := lipgloss.Width(f.renderOptionLabel(o, false)); w > maxW {
			maxW = w
		}
		// Reserve room for the plural form too so the unit column does
		// not shift when QuantitySelectField swaps "Day" for "Days".
		if o.PluralLabel != "" {
			plural := SelectOption{Label: o.PluralLabel, Value: o.Value}
			if w := lipgloss.Width(f.renderOptionLabel(plural, false)); w > maxW {
				maxW = w
			}
		}
	}
	f.maxWidth = maxW
}

func (f *SelectField) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case selectFlashMsg:
		if msg.id == f.flashID {
			f.highlight = selectNone
		}
		return nil
	case tea.KeyPressMsg:
		n := len(f.options)
		if n == 0 {
			return nil
		}
		switch msg.String() {
		case "left", "h":
			f.selected = (f.selected - 1 + n) % n
			return f.flash(selectLeft)
		case "right", "l":
			f.selected = (f.selected + 1) % n
			return f.flash(selectRight)
		}
	}
	return nil
}

func (f *SelectField) flash(dir selectHighlight) tea.Cmd {
	f.highlight = dir
	f.flashID++
	id := f.flashID
	return tea.Tick(selectFlashDuration, func(time.Time) tea.Msg {
		return selectFlashMsg{id: id}
	})
}

func (f *SelectField) View() string {
	if len(f.options) == 0 {
		return ""
	}
	labelStyle := lipgloss.NewStyle().Width(f.maxWidth)
	if f.focused && f.renderLabel == nil {
		labelStyle = labelStyle.Reverse(true)
	}
	label := labelStyle.Render(f.renderOptionLabel(f.options[f.selected], f.focused))

	flash := lipgloss.NewStyle().Foreground(activeTheme.FormHighlight)
	prev := Glyphs["select.prev"]
	next := Glyphs["select.next"]

	if f.highlight == selectLeft {
		prev = flash.Render(prev)
	}
	if f.highlight == selectRight {
		next = flash.Render(next)
	}

	prevTarget, nextTarget := "select:prev", "select:next"
	if f.arrowIndexed {
		prevTarget = fmt.Sprintf("select:prev:%d", f.arrowIndex)
		nextTarget = fmt.Sprintf("select:next:%d", f.arrowIndex)
	}

	return label + "  " + mouseMark(prevTarget, prev) + " " + mouseMark(nextTarget, next)
}

func (f *SelectField) Focus() tea.Cmd {
	f.focused = true
	return nil
}

func (f *SelectField) Blur() {
	f.focused = false
	f.highlight = selectNone
}

func (f *SelectField) SetWidth(int) {}

// IsFocusable reports false for an empty option set. A select with nothing
// to choose has no useful interaction. It must not capture focus or input.
func (f *SelectField) IsFocusable() bool { return len(f.options) > 0 }
