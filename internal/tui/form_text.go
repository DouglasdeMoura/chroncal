package tui

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// TextField
// ---------------------------------------------------------------------------

// TextField wraps a bubbles textinput with an optional keystroke filter.
type TextField struct {
	input    textinput.Model
	filter   func(tea.Key) bool
	suffix   string
	disabled bool
	validate func(string) string
}

func NewTextField(placeholder string) *TextField {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = 256
	applyPlaceholderDefaults(&input)
	return &TextField{input: input}
}

// applyPlaceholderDefaults styles the placeholder in both focus states
// so hints read as hints. Italicized and faint, distinct from entered
// values which use the upright text style. It drops the bubbles default
// colour so the terminal's own faint attribute drives the dimness.
func applyPlaceholderDefaults(input *textinput.Model) {
	hint := lipgloss.NewStyle().Italic(true).Faint(true)
	s := input.Styles()
	s.Focused.Placeholder = hint
	s.Blurred.Placeholder = hint
	input.SetStyles(s)
}

func (f *TextField) Value() string     { return f.input.Value() }
func (f *TextField) SetValue(v string) { f.input.SetValue(v) }

func (f *TextField) SetPlaceholder(p string) { f.input.Placeholder = p }
func (f *TextField) SetCharLimit(n int)      { f.input.CharLimit = n }
func (f *TextField) Position() int           { return f.input.Position() }
func (f *TextField) SetCursor(pos int)       { f.input.SetCursor(pos) }
func (f *TextField) SetSuffix(s string)      { f.suffix = s }

// SetValidate installs a validation hook run by Form.validate before
// submit. The hook receives the trimmed field value and returns an error
// message, or "" when the value is acceptable. When no hook is set the
// field always validates.
func (f *TextField) SetValidate(fn func(string) string) { f.validate = fn }

// Validate implements the validator interface. It delegates to the hook
// installed via SetValidate. It returns "" (valid) when none is set.
func (f *TextField) Validate() string {
	if f.validate == nil {
		return ""
	}
	return f.validate(strings.TrimSpace(f.input.Value()))
}

// SetFilter sets a function that gates printable keystrokes. When set, a key
// with non-empty Text is forwarded to the wrapped input only if fn returns
// true. Special keys (tab, enter, backspace, …) are never filtered.
func (f *TextField) SetFilter(fn func(tea.Key) bool) {
	f.filter = fn
}

// SetDigitsOnly is shorthand for SetFilter(FilterDigits).
func (f *TextField) SetDigitsOnly() {
	f.filter = FilterDigits
}

// SetEchoPassword toggles password mask for the wrapped input.
func (f *TextField) SetEchoPassword(on bool) {
	if on {
		f.input.EchoMode = textinput.EchoPassword
	} else {
		f.input.EchoMode = textinput.EchoNormal
	}
}

func (f *TextField) Update(msg tea.Msg) tea.Cmd {
	if f.disabled {
		return nil
	}
	if f.filter != nil {
		switch m := msg.(type) {
		case tea.KeyPressMsg:
			if k := m.Key(); k.Text != "" && !f.filter(k) {
				return nil
			}
		case tea.PasteMsg:
			// A bracketed paste bypasses the per-keystroke path, so run its
			// content through the same filter (issue #411). The filter
			// inspects only the key's Text, so a synthetic key suffices.
			if !f.filter(tea.Key{Text: m.Content}) {
				return nil
			}
		}
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *TextField) View() string {
	if f.disabled {
		val := f.input.Value()
		if val == "" {
			val = f.input.Placeholder
		}
		out := val
		if f.suffix != "" {
			out += " " + f.suffix
		}
		return out
	}
	if f.suffix == "" {
		return f.input.View()
	}
	return f.input.View() + " " + f.suffix
}
func (f *TextField) Focus() tea.Cmd {
	if f.disabled {
		return nil
	}
	return f.input.Focus()
}
func (f *TextField) Blur() { f.input.Blur() }
func (f *TextField) SetWidth(w int) {
	if f.suffix != "" {
		// Pin the input to CharLimit so the suffix sits at a fixed column
		// regardless of the value's length. Falls back to w minus suffix
		// width when CharLimit is unset.
		if f.input.CharLimit > 0 {
			f.input.SetWidth(f.input.CharLimit)
			return
		}
		w -= lipgloss.Width(f.suffix) + 1
	}
	f.input.SetWidth(max(w, 1))
}
func (f *TextField) IsFocusable() bool { return !f.disabled }

// SetDisabled toggles disabled state. Disabled fields skip focus during
// Tab navigation, ignore input, and render the value in a dimmed style.
func (f *TextField) SetDisabled(v bool) {
	if f.disabled == v {
		return
	}
	f.disabled = v
	if v {
		f.input.Blur()
	}
}

// SetDimStyle sets the style used to render the value when disabled.
// Defaults to the zero style (no visual change beyond a skip of the cursor).
// FilterDigits allows only digit characters (0-9).
// Every rune in the key text must be a digit; a multi-rune event (e.g. a
// paste) is rejected if any rune fails the check.
func FilterDigits(k tea.Key) bool {
	for _, r := range k.Text {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
