package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/tui"
)

// Messages emitted by the form callbacks.
type submitMsg struct{ values string }
type cancelMsg struct{}
type resetMsg struct{}

type model struct {
	form       tui.Form
	dialog     tui.Dialog
	confirmDlg tui.Dialog
	confirmFrm tui.Form
	width      int
	height     int
	result     string
	done       bool
}

func newModel() model {
	hasDark := lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	theme := tui.NewTheme(hasDark)

	// ── Main dialog ──
	ds := tui.DefaultDialogStyles()
	ds.Title = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	dialog := tui.NewDialog("Form Component Test", ds)
	dialog.SetFooter("tab/shift-tab: navigate · enter: advance/confirm · space: toggle · ctrl+c: quit")

	// ── Main form (right-aligned buttons, the default) ──
	styles := formStyles(theme)
	styles.LabelLayout = tui.LabelInlineRight
	styles.ShowFocusMarker = true

	form := tui.NewForm("Save", styles,
		tui.FormItem{Label: "Name", Field: newTextField("Your name", 60), Required: true},
		tui.FormItem{Label: "Age", Field: newDigitsField("Age", 3)},
		tui.FormItem{Label: "Inline", Field: newTextField("LabelInline layout", 40), LabelLayout: tui.LayoutPtr(tui.LabelInline)},
		tui.FormItem{Label: "Email", Field: newTextField("user@example.com", 60), LabelLayout: tui.LayoutPtr(tui.LabelTop), Required: true},
		tui.FormItem{Label: "Quiet", Field: newTextField("No marker", 40), ShowFocusMarker: tui.BoolPtr(false)},
		tui.FormItem{Label: "Repeat", Field: tui.NewSelectField([]tui.SelectOption{
			{Label: "None", Value: ""},
			{Label: "Daily", Value: "daily"},
			{Label: "Weekly", Value: "weekly"},
			{Label: "Monthly", Value: "monthly"},
			{Label: "Yearly", Value: "yearly"},
		})},
		tui.FormItem{Label: "", Field: tui.NewStaticField("  ── Options ──", nil)},
		tui.FormItem{Label: "Subscribe", Field: tui.NewCheckboxField("Send me updates", false)},
		tui.FormItem{Label: "", Field: tui.NewCheckboxField("I accept the terms", false), LabelLayout: tui.LayoutPtr(tui.LabelInline)},
		tui.FormItem{Label: "Bio", Field: tui.NewTextAreaField("Tell us about yourself")},
		tui.FormItem{Label: "Notes", Field: newTextField("Top-placed, no marker", 40), LabelLayout: tui.LayoutPtr(tui.LabelTop), ShowFocusMarker: tui.BoolPtr(false)},
	)

	form.SetActionButton("Secondary", tui.ButtonSecondary, func() tea.Msg { return resetMsg{} })
	form.SetActionButton("Danger", tui.ButtonDanger, func() tea.Msg { return resetMsg{} })
	form.SetActionButton("Ghost", tui.ButtonGhost, func() tea.Msg { return resetMsg{} })

	form.OnSubmit(func(f *tui.Form) tea.Cmd {
		name := f.FormTextField(0).Value()
		age := f.FormTextField(1).Value()
		email := f.FormTextField(3).Value()
		sub := f.FormCheckboxField(6).Checked()
		agree := f.FormCheckboxField(7).Checked()
		summary := fmt.Sprintf("Name: %s\nAge: %s\nEmail: %s\nSubscribe: %v\nAgree: %v",
			name, age, email, sub, agree)
		return func() tea.Msg { return submitMsg{values: summary} }
	})

	form.OnCancel(func(f *tui.Form) tea.Cmd {
		return func() tea.Msg { return cancelMsg{} }
	})

	// ── Centered-buttons dialog (confirmation style) ──
	cds := tui.DefaultDialogStyles()
	cds.Title = lipgloss.NewStyle().Bold(true).Foreground(theme.Error)
	confirmDlg := tui.NewDialog("Discard unsaved changes?", cds)

	cStyles := formStyles(theme)
	cStyles.ButtonAlign = tui.ButtonAlignCenter
	cStyles.ButtonRule = true
	confirmFrm := tui.NewForm("Discard", cStyles)
	confirmFrm.SetCancelVariant(tui.ButtonGhost)

	return model{
		form:       form,
		dialog:     dialog,
		confirmDlg: confirmDlg,
		confirmFrm: confirmFrm,
	}
}

func (m model) Init() tea.Cmd {
	return m.form.Init()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.dialog = m.dialog.Update(msg)
		m.confirmDlg = m.confirmDlg.Update(msg)
		formSize := tea.WindowSizeMsg{Width: m.dialog.ContentWidth(), Height: msg.Height}
		var cmd tea.Cmd
		m.form, cmd = m.form.Update(formSize)
		m.confirmFrm, _ = m.confirmFrm.Update(tea.WindowSizeMsg{
			Width:  m.confirmDlg.ContentWidth(),
			Height: msg.Height,
		})
		return m, cmd

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.done && msg.String() == "q" {
			return m, tea.Quit
		}

	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			target := tui.MouseResolve(msg.X, msg.Y)
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(tui.MouseEvent{IsClick: true, Target: target})
			return m, cmd
		}

	case submitMsg:
		m.result = msg.values
		m.done = true
		return m, nil

	case cancelMsg:
		m.result = "(cancelled)"
		m.done = true
		return m, nil

	case resetMsg:
		w, h := m.width, m.height
		m = newModel()
		m.width = w
		m.height = h
		m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.confirmDlg = m.confirmDlg.Update(tea.WindowSizeMsg{Width: w, Height: h})
		m.form, _ = m.form.Update(tea.WindowSizeMsg{Width: m.dialog.ContentWidth(), Height: h})
		m.confirmFrm, _ = m.confirmFrm.Update(tea.WindowSizeMsg{Width: m.confirmDlg.ContentWidth(), Height: h})
		return m, m.form.Init()
	}

	if !m.done {
		var cmd tea.Cmd
		m.form, cmd = m.form.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) View() tea.View {
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion}
	if m.width == 0 {
		return v
	}

	if m.done {
		ds := tui.DefaultDialogStyles()
		ds.BorderFg = lipgloss.Color("10")
		rd := tui.NewDialog("", ds)
		rd = rd.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		v.Content = rd.Render(tui.Glyphs["status.ok"] + " Form submitted\n\n" + m.result + "\n\nPress q to quit")
		return v
	}

	// Main form dialog (right-aligned buttons).
	mainBox := m.dialog.Box(m.form.View())

	// Small confirmation-style dialog (centered buttons).
	confirmBox := m.confirmDlg.Box(
		"This action cannot be undone.\n\n" + m.confirmFrm.View(),
	)

	// Stack both boxes, then center the combined result.
	combined := lipgloss.JoinVertical(lipgloss.Center, mainBox, "", confirmBox)
	placed := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, combined)

	v.Content = tui.MouseSweep(placed)
	return v
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTextField(placeholder string, charLimit int) *tui.TextField {
	f := tui.NewTextField(placeholder)
	f.SetCharLimit(charLimit)
	return f
}

func newDigitsField(placeholder string, charLimit int) *tui.TextField {
	f := tui.NewTextField(placeholder)
	f.SetCharLimit(charLimit)
	f.SetDigitsOnly()
	return f
}

func formStyles(theme tui.Theme) tui.FormStyles {
	base := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
	return tui.FormStyles{
		Label:           lipgloss.NewStyle().Faint(true),
		ShowFocusMarker: true,
		Error:           lipgloss.NewStyle().Foreground(theme.Error),
		Buttons: tui.ButtonStyles{
			Primary: tui.ButtonStyle{
				Normal:  base.Background(theme.Primary).Foreground(lipgloss.Color("255")).Bold(true),
				Focused: base.Background(theme.Accent).Foreground(lipgloss.Color("255")).Bold(true),
			},
			Secondary: tui.ButtonStyle{
				Normal:  base.Background(lipgloss.Color("240")).Foreground(lipgloss.Color("255")),
				Focused: base.Background(lipgloss.Color("63")).Foreground(lipgloss.Color("255")),
			},
			Danger: tui.ButtonStyle{
				Normal:  base.Background(lipgloss.Color("52")).Foreground(lipgloss.Color("255")),
				Focused: base.Background(lipgloss.Color("160")).Foreground(lipgloss.Color("255")).Bold(true),
			},
			Ghost: tui.ButtonStyle{
				Normal:  base.Foreground(theme.TextDim),
				Focused: base.Foreground(lipgloss.Color("255")).Background(lipgloss.Color("63")),
			},
		},
	}
}

func main() {
	p := tea.NewProgram(newModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
