package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ShareExportRequestedMsg asks for a local share export.
// From and To hold the parsed range. Path is the .ics file path.
// WorkHours clamps free times to 09:00 to 17:00 local.
// Calendars is All or a comma name list.
type ShareExportRequestedMsg struct {
	From      time.Time
	To        time.Time
	Path      string
	WorkHours bool
	Calendars string
}

// ShareExportDoneMsg reports a share export result.
// Err is nil on success.
type ShareExportDoneMsg struct {
	Err  error
	Path string
}

// ShareDialogClosedMsg closes the share dialog.
type ShareDialogClosedMsg struct{}

// ShareAvailabilityMsg opens the share dialog.
type ShareAvailabilityMsg struct{}

// ShareDialogModel shows local share fields.
type ShareDialogModel struct {
	form Form
}

// NewShareDialogModel builds a local share dialog.
// Defaults hold today 09:00 to today 17:00 local.
func NewShareDialogModel(theme Theme) ShareDialogModel {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.Local)
	to := time.Date(now.Year(), now.Month(), now.Day(), 17, 0, 0, 0, time.Local)

	fromField := NewTextField("From (RFC3339)")
	fromField.SetValue(from.Format(time.RFC3339))
	toField := NewTextField("To (RFC3339)")
	toField.SetValue(to.Format(time.RFC3339))
	calField := NewTextField("Calendars (All or name, name)")
	calField.SetValue("All")
	calField.SetCharLimit(0)
	pathField := NewTextField("File path (.ics)")
	pathField.SetCharLimit(0)
	workField := NewCheckboxField("Work hours", true)

	form := NewForm("Export", DefaultFormStyles(), FormItem{Field: fromField}, FormItem{Field: toField}, FormItem{Field: workField}, FormItem{Field: calField}, FormItem{Field: pathField})
	form.SetActionButton("Cancel", Button, func() tea.Msg { return ShareDialogClosedMsg{} })
	form.OnSubmit(func(f *Form) tea.Cmd {
		return func() tea.Msg {
			return ShareExportRequestedMsg{From: parseShareTime(stringFieldAt(f, 0)), To: parseShareTime(stringFieldAt(f, 1)), WorkHours: boolFieldAt(f, 2), Calendars: strings.TrimSpace(stringFieldAt(f, 3)), Path: strings.TrimSpace(stringFieldAt(f, 4))}
		}
	})
	return ShareDialogModel{form: form}
}

func (m ShareDialogModel) SetSize(w, h int) ShareDialogModel {
	return m
}

func (m ShareDialogModel) BoxSize() (int, int) {
	return 72, 14
}

func (m ShareDialogModel) Update(msg tea.Msg) (ShareDialogModel, tea.Cmd) {
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m ShareDialogModel) View() string {
	return "Share Availability\n\n" + m.form.View()
}

// stringFieldAt returns the text of a text field by index.
// It returns empty for a missing or non-text field.
func stringFieldAt(f *Form, idx int) string {
	if idx < 0 || idx >= len(f.items) {
		return ""
	}
	if tf, ok := f.items[idx].Field.(*TextField); ok {
		return tf.Value()
	}
	return ""
}

// boolFieldAt returns the checked state of a checkbox field.
func boolFieldAt(f *Form, idx int) bool {
	if idx < 0 || idx >= len(f.items) {
		return false
	}
	if cf, ok := f.items[idx].Field.(*CheckboxField); ok {
		return cf.Checked()
	}
	return false
}

// parseShareTime parses RFC3339 text. It returns zero on error.
func parseShareTime(field string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(field))
	if err != nil {
		return time.Time{}
	}
	return t
}

// ValidateShareExport checks From To and Path.
// It returns the first error text. Empty means valid.
func ValidateShareExport(from, to time.Time, path string) string {
	if from.IsZero() || to.IsZero() {
		return "Enter From and To as RFC3339 times"
	}
	if !to.After(from) {
		return "To must be after From"
	}
	if strings.TrimSpace(path) == "" {
		return "Enter a file path"
	}
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".ics") {
		return "File path must end with .ics"
	}
	return ""
}

// FormatShareError holds an export error name.
func FormatShareError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
