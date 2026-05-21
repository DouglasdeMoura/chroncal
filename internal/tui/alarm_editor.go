package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/model"
)

type alarmEditorMode int

const (
	alarmModeList alarmEditorMode = iota
	alarmModeEdit
)

type alarmEditorMsg int

const (
	alarmEditorSaveForm alarmEditorMsg = iota
	alarmEditorCancelForm
)

const alarmEditorBoxWidth = 56

// alarmUnits defines the single-unit durations the editor can represent.
// RFC 5545 DURATION values with M/H live after "PT", while D/W live after "P".
var alarmUnits = []struct {
	Label string
	Code  byte
}{
	{"minutes", 'M'},
	{"hours", 'H'},
	{"days", 'D'},
	{"weeks", 'W'},
}

var alarmActionOpts = []SelectOption{
	{Label: "Notification", Value: "DISPLAY"},
	{Label: "Email", Value: "EMAIL"},
	{Label: "Audio", Value: "AUDIO"},
}

// parseOffsetTrigger parses a relative-trigger string like "-PT15M" into
// (qty, unitIdx, before). Only single-unit offsets are supported so the
// editor stays lossless; absolute triggers and mixed forms return ok=false.
func parseOffsetTrigger(t string) (qty, unitIdx int, before, ok bool) {
	if t == "" {
		return 0, 0, true, false
	}
	before = strings.HasPrefix(t, "-")
	s := strings.TrimPrefix(t, "-")
	s = strings.TrimPrefix(s, "+")
	if !strings.HasPrefix(s, "P") {
		return 0, 0, before, false
	}
	s = s[1:]
	if s == "" {
		return 0, 0, before, false
	}
	if strings.HasPrefix(s, "T") {
		s = s[1:]
		for i, u := range alarmUnits {
			if u.Code != 'M' && u.Code != 'H' {
				continue
			}
			if n, ok := matchDurationUnit(s, u.Code); ok {
				return n, i, before, true
			}
		}
		return 0, 0, before, false
	}
	for i, u := range alarmUnits {
		if u.Code != 'D' && u.Code != 'W' {
			continue
		}
		if n, ok := matchDurationUnit(s, u.Code); ok {
			return n, i, before, true
		}
	}
	return 0, 0, before, false
}

// matchDurationUnit returns (n, true) when s is exactly "<digits><code>".
func matchDurationUnit(s string, code byte) (int, bool) {
	if len(s) < 2 || s[len(s)-1] != code {
		return 0, false
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// buildOffsetTrigger returns an RFC 5545 duration for the given offset.
func buildOffsetTrigger(qty, unitIdx int, before bool) string {
	if unitIdx < 0 || unitIdx >= len(alarmUnits) {
		unitIdx = 0
	}
	if qty <= 0 {
		qty = 1
	}
	u := alarmUnits[unitIdx]
	sign := ""
	if before {
		sign = "-"
	}
	if u.Code == 'M' || u.Code == 'H' {
		return sign + "PT" + strconv.Itoa(qty) + string(u.Code)
	}
	return sign + "P" + strconv.Itoa(qty) + string(u.Code)
}

// alarmSummary returns the one-line summary used on the event-form row.
func alarmSummary(alarms []model.Alarm) string {
	switch len(alarms) {
	case 0:
		return "None"
	case 1:
		return formatAlarm(alarms[0])
	default:
		return strconv.Itoa(len(alarms)) + " alarms"
	}
}

// AlarmListEditorModel manages the list of alarms attached to an event.
// It has two internal modes: list (picker) and edit (single-alarm form).
// Follows the RecurrenceEditorModel paradigm: Done()/Cancelled()/Alarms()
// are polled by the parent after each Update.
type AlarmListEditorModel struct {
	alarms []model.Alarm

	mode     alarmEditorMode
	cursor   int
	btnFocus int // -1 = list cursor, 0 = New, 1 = Cancel, 2 = Save
	editIdx  int

	actionField *SelectField
	offsetField *QuantitySelectField

	form      Form
	fieldKeys []string

	done      bool
	cancelled bool

	help   help.Model
	width  int
	height int
	theme  Theme
}

// NewAlarmListEditorModel creates the editor with a copy of the given alarms.
func NewAlarmListEditorModel(existing []model.Alarm, w, h int, theme Theme) AlarmListEditorModel {
	alarms := append([]model.Alarm(nil), existing...)
	btnFocus := -1
	if len(alarms) == 0 {
		btnFocus = 0 // focus "New" button when there are no alarms to select
	}
	return AlarmListEditorModel{
		alarms:   alarms,
		mode:     alarmModeList,
		cursor:   0,
		btnFocus: btnFocus,
		editIdx:  -1,
		help:     newThemedHelp(theme),
		width:    w,
		height:   h,
		theme:    theme,
	}
}

func (m AlarmListEditorModel) Done() bool      { return m.done }
func (m AlarmListEditorModel) Cancelled() bool { return m.cancelled }

// Alarms returns a copy of the current alarm list.
func (m AlarmListEditorModel) Alarms() []model.Alarm {
	out := make([]model.Alarm, len(m.alarms))
	copy(out, m.alarms)
	return out
}

func (m AlarmListEditorModel) SetSize(w, h int) AlarmListEditorModel {
	m.width = w
	m.height = h
	m.form.SetWidth(m.formWidth())
	return m
}

func (m AlarmListEditorModel) formWidth() int {
	styles := DefaultDialogStyles()
	return alarmEditorBoxWidth - 2 - 2*styles.PaddingX
}

// BoxSize returns the outer dimensions of the editor dialog.
func (m AlarmListEditorModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

func (m *AlarmListEditorModel) enterEditMode(idx int) {
	var a model.Alarm
	if idx >= 0 && idx < len(m.alarms) {
		a = m.alarms[idx]
	} else {
		a = model.Alarm{
			Action:       "DISPLAY",
			TriggerValue: "-PT15M",
			Related:      "START",
		}
	}
	m.editIdx = idx
	m.buildEditForm(a)
	m.mode = alarmModeEdit
}

func (m *AlarmListEditorModel) buildEditForm(a model.Alarm) {
	m.actionField = NewSelectField(alarmActionOpts)
	for i, opt := range alarmActionOpts {
		if strings.EqualFold(opt.Value, a.Action) {
			m.actionField.SetSelected(i)
			break
		}
	}

	unitOpts := make([]SelectOption, len(alarmUnits))
	for i, u := range alarmUnits {
		unitOpts[i] = SelectOption{Label: u.Label, Value: string(u.Code)}
	}
	qty, unitIdx, _, ok := parseOffsetTrigger(a.TriggerValue)
	if !ok {
		qty, unitIdx = 15, 0
	}
	m.offsetField = NewQuantitySelectField(unitOpts, unitIdx)
	m.offsetField.SetAmount(strconv.Itoa(qty))
	m.offsetField.SetSuffix("before")

	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInline
	styles.ShowFocusMarker = true
	styles.ButtonAlign = ButtonAlignRight
	styles.ButtonRule = true

	items := []FormItem{
		{Label: "Remind me", Field: m.offsetField},
		{Label: actionLabelFor(m.actionField.Value()), Field: m.actionField},
	}
	m.fieldKeys = []string{"offset", "action"}

	m.form = NewForm("Save", styles, items...)
	m.form.SetSubmitVariant(ButtonSecondary)
	m.form.OnSubmit(func(f *Form) tea.Cmd {
		return func() tea.Msg { return alarmEditorSaveForm }
	})
	m.form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return alarmEditorCancelForm }
	})
	m.form.SetWidth(m.formWidth())
}

func (m *AlarmListEditorModel) applyEditForm() {
	qty, _ := strconv.Atoi(strings.TrimSpace(m.offsetField.Amount()))
	if qty <= 0 {
		qty = 1
	}
	unitIdx := m.offsetField.Selected()
	trigger := buildOffsetTrigger(qty, unitIdx, true)

	action := strings.ToUpper(m.actionField.Value())

	alarm := model.Alarm{
		Action:       action,
		TriggerValue: trigger,
		Related:      "START",
	}

	if m.editIdx >= 0 && m.editIdx < len(m.alarms) {
		orig := m.alarms[m.editIdx]
		alarm.ID = orig.ID
		alarm.EventID = orig.EventID
		alarm.UID = orig.UID
		alarm.Repeat = orig.Repeat
		alarm.Duration = orig.Duration
		alarm.Acknowledged = orig.Acknowledged
		alarm.AttachURI = orig.AttachURI
		alarm.AttachFmtType = orig.AttachFmtType
		alarm.Attendees = orig.Attendees
		alarm.Description = orig.Description
		alarm.Summary = orig.Summary
		m.alarms[m.editIdx] = alarm
	} else {
		m.alarms = append(m.alarms, alarm)
	}
}

func (m AlarmListEditorModel) Update(msg tea.Msg) (AlarmListEditorModel, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(ws.Width, ws.Height), nil
	}

	switch msg {
	case alarmEditorSaveForm:
		m.applyEditForm()
		newCursor := m.editIdx
		if newCursor < 0 {
			newCursor = len(m.alarms) - 1
		}
		if newCursor < 0 {
			newCursor = 0
		}
		m.cursor = newCursor
		m.btnFocus = -1
		m.editIdx = -1
		m.mode = alarmModeList
		return m, nil
	case alarmEditorCancelForm:
		m.editIdx = -1
		m.mode = alarmModeList
		if len(m.alarms) == 0 {
			m.btnFocus = 0
		}
		return m, nil
	}

	if m.mode == alarmModeEdit {
		return m.updateEditMode(msg)
	}
	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		return m.handleListMouse(mc)
	}
	return m.updateListMode(msg)
}

func (m AlarmListEditorModel) updateEditMode(msg tea.Msg) (AlarmListEditorModel, tea.Cmd) {
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		switch kp.String() {
		case "ctrl+s":
			var cmd tea.Cmd
			m.form, cmd = m.form.Submit()
			return m, cmd
		case "esc":
			return m, func() tea.Msg { return alarmEditorCancelForm }
		}
	}
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	m.syncActionLabel()
	return m, cmd
}

// syncActionLabel updates the "With a/an" label for the action field based
// on the currently selected action value.
func (m *AlarmListEditorModel) syncActionLabel() {
	if m.actionField == nil {
		return
	}
	m.form.SetItemLabel(1, actionLabelFor(m.actionField.Value()))
}

// actionLabelFor returns the grammatically correct article for the action.
func actionLabelFor(action string) string {
	switch strings.ToUpper(strings.TrimSpace(action)) {
	case "EMAIL", "AUDIO":
		return "With an"
	default:
		return "With a"
	}
}

// actionDisplayLabel returns the user-facing label for an alarm action value.
func actionDisplayLabel(action string) string {
	v := strings.ToUpper(strings.TrimSpace(action))
	if v == "" {
		v = "DISPLAY"
	}
	for _, opt := range alarmActionOpts {
		if strings.EqualFold(opt.Value, v) {
			return opt.Label
		}
	}
	return titleCaseAscii(v)
}

func (m AlarmListEditorModel) updateListMode(msg tea.Msg) (AlarmListEditorModel, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	last := len(m.alarms) - 1
	switch kp.String() {
	case "tab":
		m.advanceFocus()
	case "shift+tab":
		m.retreatFocus()
	case "up", "k":
		if m.btnFocus == -1 && m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.btnFocus == -1 && m.cursor < last {
			m.cursor++
		}
	case "enter", "space":
		switch m.btnFocus {
		case 0:
			m.enterEditMode(-1)
		case 1:
			m.cancelled = true
		case 2:
			m.done = true
		default:
			if kp.String() == "enter" && m.cursor >= 0 && m.cursor <= last {
				if _, _, _, ok := parseOffsetTrigger(m.alarms[m.cursor].TriggerValue); ok {
					m.enterEditMode(m.cursor)
				}
			}
		}
	case "n":
		m.enterEditMode(-1)
	case "e":
		if m.cursor >= 0 && m.cursor <= last {
			if _, _, _, ok := parseOffsetTrigger(m.alarms[m.cursor].TriggerValue); ok {
				m.enterEditMode(m.cursor)
			}
		}
	case "d":
		if m.btnFocus == -1 && m.cursor >= 0 && m.cursor <= last {
			m.alarms = append(m.alarms[:m.cursor], m.alarms[m.cursor+1:]...)
			if m.cursor >= len(m.alarms) && m.cursor > 0 {
				m.cursor = len(m.alarms) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}
			if len(m.alarms) == 0 {
				m.btnFocus = 0
			}
		}
	case "ctrl+s":
		m.done = true
	case "esc":
		m.cancelled = true
	}
	return m, nil
}

// advanceFocus moves focus forward: rows (if any) → New → Cancel → Save → rows.
func (m *AlarmListEditorModel) advanceFocus() {
	if m.btnFocus == -1 {
		if m.cursor < len(m.alarms)-1 {
			m.cursor++
			return
		}
		m.btnFocus = 0
		return
	}
	if m.btnFocus < 2 {
		m.btnFocus++
		return
	}
	if len(m.alarms) > 0 {
		m.btnFocus = -1
		m.cursor = 0
	} else {
		m.btnFocus = 0
	}
}

// retreatFocus moves focus backward: Save → Cancel → New → rows (last to first).
func (m *AlarmListEditorModel) retreatFocus() {
	if m.btnFocus == -1 {
		if m.cursor > 0 {
			m.cursor--
			return
		}
		m.btnFocus = 2
		return
	}
	if m.btnFocus > 0 {
		m.btnFocus--
		return
	}
	if len(m.alarms) > 0 {
		m.btnFocus = -1
		m.cursor = len(m.alarms) - 1
	} else {
		m.btnFocus = 2
	}
}

func (m AlarmListEditorModel) handleListMouse(msg tea.MouseClickMsg) (AlarmListEditorModel, tea.Cmd) {
	bw, bh := m.BoxSize()
	ox := (m.width - bw) / 2
	oy := (m.height - bh) / 2
	target := mouseResolve(msg.X-ox, msg.Y-oy)
	switch target {
	case "alarm:list:save":
		m.done = true
	case "alarm:list:cancel":
		m.cancelled = true
	case "alarm:list:new":
		m.enterEditMode(-1)
	default:
		if rest, ok := strings.CutPrefix(target, "alarm:list:row:"); ok {
			if n, err := strconv.Atoi(rest); err == nil && n >= 0 && n < len(m.alarms) {
				m.cursor = n
				m.btnFocus = -1
			}
		}
	}
	return m, nil
}

// View renders the alarm editor dialog in list or edit mode.
func (m AlarmListEditorModel) View() string {
	styles := DefaultDialogStyles()
	dialog := NewDialog("Alarms", styles)
	dialog = dialog.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	dialog.SetWidth(alarmEditorBoxWidth)
	dialog.SetFooter(m.help.ShortHelpView(m.helpKeys()))

	var content string
	if m.mode == alarmModeEdit {
		form := m.form
		form.SetWidth(dialog.ContentWidth())
		content = form.View()
	} else {
		content = m.renderList()
	}
	return mouseSweep(dialog.Box(content))
}

func (m AlarmListEditorModel) helpKeys() []key.Binding {
	if m.mode == alarmModeEdit {
		return []key.Binding{
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
			key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}
}

func (m AlarmListEditorModel) renderList() string {
	var lines []string
	faint := lipgloss.NewStyle().Faint(true)
	reverse := lipgloss.NewStyle().Reverse(true)

	if len(m.alarms) == 0 {
		lines = append(lines, faint.Render("  No alarms yet."))
	} else {
		for i, a := range m.alarms {
			label := "  " + formatAlarm(a)
			label += faint.Render("  (" + actionDisplayLabel(a.Action) + ")")
			if m.btnFocus == -1 && i == m.cursor {
				label = reverse.Render(label)
			}
			lines = append(lines, mouseMark("alarm:list:row:"+strconv.Itoa(i), label))
		}
	}

	innerW := m.formWidth()
	lines = append(lines, "")
	lines = append(lines, faint.Render(strings.Repeat("─", innerW)))

	bs := DefaultButtonStyles()
	newBtn := mouseMark("alarm:list:new", bs.Secondary.Render("+ New alarm", m.btnFocus == 0))
	cancelBtn := mouseMark("alarm:list:cancel", bs.Secondary.Render("Cancel", m.btnFocus == 1))
	saveBtn := mouseMark("alarm:list:save", bs.Primary.Render("Save", m.btnFocus == 2))

	rightRow := cancelBtn + " " + saveBtn
	gap := max(innerW-lipgloss.Width(newBtn)-lipgloss.Width(rightRow), 1)
	lines = append(lines, newBtn+strings.Repeat(" ", gap)+rightRow)

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func titleCaseAscii(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}
