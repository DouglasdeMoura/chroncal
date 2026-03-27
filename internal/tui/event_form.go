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

	"github.com/douglasdemoura/tcal/internal/app"
	"github.com/douglasdemoura/tcal/internal/event"
)

type formField int

const (
	fieldTitle formField = iota
	fieldDate
	fieldStartTime
	fieldEndTime
	fieldLocation
	fieldDescription
	fieldCount
)

type eventForm struct {
	fields     [fieldCount]textinput.Model
	active     formField
	allDay     bool
	calendarID int64
	editID     int64 // 0 = new event
	date       time.Time
}

type eventSavedMsg struct {
	event event.Event
}

func newEventForm(date time.Time, calID int64) eventForm {
	var fields [fieldCount]textinput.Model

	fields[fieldTitle] = textinput.New()
	fields[fieldTitle].Placeholder = "Event title"
	fields[fieldTitle].Focus()
	fields[fieldTitle].CharLimit = 100

	fields[fieldDate] = textinput.New()
	fields[fieldDate].Placeholder = "YYYY-MM-DD"
	fields[fieldDate].SetValue(date.Format("2006-01-02"))
	fields[fieldDate].CharLimit = 10

	fields[fieldStartTime] = textinput.New()
	fields[fieldStartTime].Placeholder = "HH:MM"
	fields[fieldStartTime].SetValue("09:00")
	fields[fieldStartTime].CharLimit = 5

	fields[fieldEndTime] = textinput.New()
	fields[fieldEndTime].Placeholder = "HH:MM"
	fields[fieldEndTime].SetValue("10:00")
	fields[fieldEndTime].CharLimit = 5

	fields[fieldLocation] = textinput.New()
	fields[fieldLocation].Placeholder = "Location (optional)"
	fields[fieldLocation].CharLimit = 200

	fields[fieldDescription] = textinput.New()
	fields[fieldDescription].Placeholder = "Description (optional)"
	fields[fieldDescription].CharLimit = 500

	return eventForm{
		fields:     fields,
		active:     fieldTitle,
		calendarID: calID,
		date:       date,
	}
}

func newEditForm(e *event.Event) eventForm {
	f := newEventForm(e.StartTime.Local(), e.CalendarID)
	f.editID = e.ID
	f.fields[fieldTitle].SetValue(e.Title)
	f.fields[fieldDate].SetValue(e.StartTime.Local().Format("2006-01-02"))
	f.allDay = e.AllDay

	if !e.AllDay {
		f.fields[fieldStartTime].SetValue(e.StartTime.Local().Format("15:04"))
		f.fields[fieldEndTime].SetValue(e.EndTime.Local().Format("15:04"))
	}

	f.fields[fieldLocation].SetValue(e.Location)
	f.fields[fieldDescription].SetValue(e.Description)
	return f
}

func (f *eventForm) nextField() {
	f.fields[f.active].Blur()
	f.active = (f.active + 1) % fieldCount
	f.fields[f.active].Focus()
}

func (f *eventForm) prevField() {
	f.fields[f.active].Blur()
	f.active = (f.active - 1 + fieldCount) % fieldCount
	f.fields[f.active].Focus()
}

func (f *eventForm) toggleAllDay() {
	f.allDay = !f.allDay
}

func (f eventForm) update(msg tea.Msg) (eventForm, tea.Cmd) {
	var cmd tea.Cmd
	f.fields[f.active], cmd = f.fields[f.active].Update(msg)
	return f, cmd
}

func (f eventForm) save(a *app.App) tea.Cmd {
	return func() tea.Msg {
		title := strings.TrimSpace(f.fields[fieldTitle].Value())
		if title == "" {
			return errMsg{fmt.Errorf("title is required")}
		}

		date, err := time.ParseInLocation("2006-01-02", f.fields[fieldDate].Value(), time.Local)
		if err != nil {
			return errMsg{fmt.Errorf("invalid date format")}
		}

		var startTime, endTime time.Time
		if f.allDay {
			startTime = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
			endTime = startTime.AddDate(0, 0, 1)
		} else {
			st, err := time.Parse("15:04", f.fields[fieldStartTime].Value())
			if err != nil {
				return errMsg{fmt.Errorf("invalid start time")}
			}
			et, err := time.Parse("15:04", f.fields[fieldEndTime].Value())
			if err != nil {
				return errMsg{fmt.Errorf("invalid end time")}
			}
			startTime = time.Date(date.Year(), date.Month(), date.Day(), st.Hour(), st.Minute(), 0, 0, time.Local)
			endTime = time.Date(date.Year(), date.Month(), date.Day(), et.Hour(), et.Minute(), 0, 0, time.Local)
			if endTime.Before(startTime) {
				endTime = endTime.AddDate(0, 0, 1)
			}
		}

		ctx := context.Background()

		if f.editID > 0 {
			e, err := a.Events.Update(ctx, f.editID, event.UpdateParams{
				Title:       title,
				Description: f.fields[fieldDescription].Value(),
				Location:    f.fields[fieldLocation].Value(),
				StartTime:   startTime,
				EndTime:     endTime,
				AllDay:      f.allDay,
				CalendarID:  f.calendarID,
			})
			if err != nil {
				return errMsg{err}
			}
			return eventSavedMsg{e}
		}

		e, err := a.Events.Create(ctx, event.CreateParams{
			CalendarID:  f.calendarID,
			Title:       title,
			Description: f.fields[fieldDescription].Value(),
			Location:    f.fields[fieldLocation].Value(),
			StartTime:   startTime,
			EndTime:     endTime,
			AllDay:      f.allDay,
		})
		if err != nil {
			return errMsg{err}
		}
		return eventSavedMsg{e}
	}
}

func (f eventForm) view() string {
	var b strings.Builder

	if f.editID > 0 {
		b.WriteString(titleStyle.Render("Edit Event"))
	} else {
		b.WriteString(titleStyle.Render("New Event"))
	}
	b.WriteString("\n\n")

	labels := []string{"Title", "Date", "Start", "End", "Location", "Notes"}
	for i := 0; i < int(fieldCount); i++ {
		label := labels[i]
		indicator := "  "
		if formField(i) == f.active {
			indicator = "▸ "
		}

		// Skip time fields if all-day
		if f.allDay && (formField(i) == fieldStartTime || formField(i) == fieldEndTime) {
			continue
		}

		labelStyle := lipgloss.NewStyle().Width(10).Foreground(DefaultTheme.TextDim)
		b.WriteString(indicator + labelStyle.Render(label) + " " + f.fields[i].View() + "\n")
	}

	// All-day toggle
	checkmark := "[ ]"
	if f.allDay {
		checkmark = "[✓]"
	}
	allDayLabel := lipgloss.NewStyle().Foreground(DefaultTheme.TextDim).Render("  All day    ")
	b.WriteString(allDayLabel + checkmark + "\n")

	b.WriteString("\n")
	b.WriteString(helpKeyStyle.Render("tab") + helpDescStyle.Render(" next  "))
	b.WriteString(helpKeyStyle.Render("shift+tab") + helpDescStyle.Render(" prev  "))
	b.WriteString(helpKeyStyle.Render("ctrl+a") + helpDescStyle.Render(" all-day  "))
	b.WriteString(helpKeyStyle.Render("ctrl+s") + helpDescStyle.Render(" save  "))
	b.WriteString(helpKeyStyle.Render("esc") + helpDescStyle.Render(" cancel"))

	return b.String()
}

func handleFormKey(msg tea.KeyMsg, f *eventForm) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
		f.nextField()
		return true, nil
	case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
		f.prevField()
		return true, nil
	case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+a"))):
		f.toggleAllDay()
		return true, nil
	}
	return false, nil
}
