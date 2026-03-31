package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

type todoFormField int

const (
	todoFieldSummary todoFormField = iota
	todoFieldDueDate
	todoFieldDescription
	todoFieldLocation
	todoFieldCategories
	todoFieldCount
)

type todoForm struct {
	fields     [todoFieldCount]textinput.Model
	active     todoFormField
	calendarID int64
	editID     int64
}

type todoSavedMsg struct {
	todo todo.Todo
}

func newTodoForm(date time.Time, calID int64) todoForm {
	var fields [todoFieldCount]textinput.Model

	fields[todoFieldSummary] = textinput.New()
	fields[todoFieldSummary].Placeholder = "Todo summary"
	fields[todoFieldSummary].Focus()
	fields[todoFieldSummary].CharLimit = 200

	fields[todoFieldDueDate] = textinput.New()
	fields[todoFieldDueDate].Placeholder = "YYYY-MM-DD (optional)"
	fields[todoFieldDueDate].SetValue(date.Format("2006-01-02"))
	fields[todoFieldDueDate].CharLimit = 10

	fields[todoFieldDescription] = textinput.New()
	fields[todoFieldDescription].Placeholder = "Description (optional)"
	fields[todoFieldDescription].CharLimit = 500

	fields[todoFieldLocation] = textinput.New()
	fields[todoFieldLocation].Placeholder = "Location (optional)"
	fields[todoFieldLocation].CharLimit = 200

	fields[todoFieldCategories] = textinput.New()
	fields[todoFieldCategories].Placeholder = "Tags: work,personal (optional)"
	fields[todoFieldCategories].CharLimit = 200

	return todoForm{
		fields:     fields,
		active:     todoFieldSummary,
		calendarID: calID,
	}
}

func newEditTodoForm(t *todo.Todo) todoForm {
	f := newTodoForm(time.Now(), t.CalendarID)
	f.editID = t.ID
	f.fields[todoFieldSummary].SetValue(t.Summary)
	if t.DueDate != "" {
		f.fields[todoFieldDueDate].SetValue(t.ParseDueDate().Local().Format("2006-01-02"))
	} else {
		f.fields[todoFieldDueDate].SetValue("")
	}
	f.fields[todoFieldDescription].SetValue(t.Description)
	f.fields[todoFieldLocation].SetValue(t.Location)
	f.fields[todoFieldCategories].SetValue(t.Categories)
	return f
}

func (f *todoForm) nextField() {
	f.fields[f.active].Blur()
	f.active = (f.active + 1) % todoFieldCount
	f.fields[f.active].Focus()
}

func (f *todoForm) prevField() {
	f.fields[f.active].Blur()
	f.active = (f.active - 1 + todoFieldCount) % todoFieldCount
	f.fields[f.active].Focus()
}

func (f todoForm) update(msg tea.Msg) (todoForm, tea.Cmd) {
	var cmd tea.Cmd
	f.fields[f.active], cmd = f.fields[f.active].Update(msg)
	return f, cmd
}

func (f todoForm) save(a *app.App) tea.Cmd {
	return func() tea.Msg {
		summary := strings.TrimSpace(f.fields[todoFieldSummary].Value())
		if summary == "" {
			return errMsg{fmt.Errorf("summary is required")}
		}

		var dueDate string
		dueDateStr := strings.TrimSpace(f.fields[todoFieldDueDate].Value())
		if dueDateStr != "" {
			d, err := time.ParseInLocation("2006-01-02", dueDateStr, time.Local)
			if err != nil {
				return errMsg{fmt.Errorf("invalid due date")}
			}
			dueDate = time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, time.Local).Format(time.RFC3339)
		}

		ctx := context.Background()

		if f.editID > 0 {
			existing, err := a.Todos.Get(ctx, f.editID)
			if err != nil {
				return errMsg{err}
			}
			t, err := a.Todos.Update(ctx, f.editID, todo.UpdateParams{
				Summary:         summary,
				Description:     f.fields[todoFieldDescription].Value(),
				Location:        f.fields[todoFieldLocation].Value(),
				DueDate:         dueDate,
				StartDate:       existing.StartDate,
				Duration:        existing.Duration,
				CompletedAt:     existing.CompletedAt,
				PercentComplete: existing.PercentComplete,
				Status:          existing.Status,
				CalendarID:      f.calendarID,
				Priority:        existing.Priority,
				Class:           existing.Class,
				URL:             existing.URL,
				Categories:      f.fields[todoFieldCategories].Value(),
				RecurrenceRule:  existing.RecurrenceRule,
				Timezone:        existing.Timezone,
				ExDates:         existing.ExDates,
				RDates:          existing.RDates,
			})
			if err != nil {
				return errMsg{err}
			}
			return todoSavedMsg{t}
		}

		t, err := a.Todos.Create(ctx, todo.CreateParams{
			CalendarID:  f.calendarID,
			Summary:     summary,
			Description: f.fields[todoFieldDescription].Value(),
			Location:    f.fields[todoFieldLocation].Value(),
			DueDate:     dueDate,
			Categories:  f.fields[todoFieldCategories].Value(),
		})
		if err != nil {
			return errMsg{err}
		}
		return todoSavedMsg{t}
	}
}

func (f todoForm) view() string {
	var b strings.Builder

	if f.editID > 0 {
		b.WriteString(titleStyle.Render("Edit Todo"))
	} else {
		b.WriteString(titleStyle.Render("New Todo"))
	}
	b.WriteString("\n\n")

	labels := []string{"Summary", "Due date", "Notes", "Location", "Tags"}
	for i := 0; i < int(todoFieldCount); i++ {
		indicator := "  "
		if todoFormField(i) == f.active {
			indicator = "▸ "
		}
		labelStyle := lipgloss.NewStyle().Width(10).Foreground(DefaultTheme.TextDim)
		b.WriteString(indicator + labelStyle.Render(labels[i]) + " " + f.fields[i].View() + "\n")
	}

	b.WriteString("\n")
	b.WriteString(helpKeyStyle.Render("tab") + helpDescStyle.Render(" next  "))
	b.WriteString(helpKeyStyle.Render("shift+tab") + helpDescStyle.Render(" prev  "))
	b.WriteString(helpKeyStyle.Render("ctrl+s") + helpDescStyle.Render(" save  "))
	b.WriteString(helpKeyStyle.Render("esc") + helpDescStyle.Render(" cancel"))

	return b.String()
}

func handleTodoFormKey(msg tea.KeyMsg, f *todoForm) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
		f.nextField()
		return true, nil
	case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
		f.prevField()
		return true, nil
	}
	return false, nil
}
