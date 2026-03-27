package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/todo"
)

type jsonEvent struct {
	ID             int64          `json:"id"`
	UID            string         `json:"uid"`
	CalendarID     int64          `json:"calendar_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Location       string         `json:"location"`
	StartTime      string         `json:"start_time"`
	EndTime        string         `json:"end_time"`
	AllDay         bool           `json:"all_day"`
	RecurrenceRule string         `json:"recurrence_rule"`
	Timezone       string         `json:"timezone"`
	Status         string         `json:"status"`
	Transp         string         `json:"transp"`
	Sequence       int64          `json:"sequence"`
	Priority       int64          `json:"priority"`
	Class          string         `json:"class"`
	URL            string         `json:"url"`
	Categories     string         `json:"categories"`
	ExDates        string         `json:"exdates"`
	RDates         string         `json:"rdates"`
	RecurrenceID   string         `json:"recurrence_id"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
	Alarms         []jsonAlarm    `json:"alarms,omitempty"`
	Attendees      []jsonAttendee `json:"attendees,omitempty"`
}

type jsonAlarm struct {
	ID           int64  `json:"id"`
	Action       string `json:"action"`
	TriggerValue string `json:"trigger_value"`
	Description  string `json:"description"`
}

type jsonAttendee struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	RSVPStatus string `json:"rsvp_status"`
	Role       string `json:"role"`
	Organizer  bool   `json:"organizer"`
}

type jsonCalendar struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toJSONEvent(e event.Event) jsonEvent {
	je := jsonEvent{
		ID:             e.ID,
		UID:            e.UID,
		CalendarID:     e.CalendarID,
		Title:          e.Title,
		Description:    e.Description,
		Location:       e.Location,
		StartTime:      e.StartTime.Local().Format(time.RFC3339),
		EndTime:        e.EndTime.Local().Format(time.RFC3339),
		AllDay:         e.AllDay,
		RecurrenceRule: e.RecurrenceRule,
		Timezone:       e.Timezone,
		Status:         e.Status,
		Transp:         e.Transp,
		Sequence:       e.Sequence,
		Priority:       e.Priority,
		Class:          e.Class,
		URL:            e.URL,
		Categories:     e.Categories,
		ExDates:        e.ExDates,
		RDates:         e.RDates,
		RecurrenceID:   e.RecurrenceID,
		CreatedAt:      e.CreatedAt.Local().Format(time.RFC3339),
		UpdatedAt:      e.UpdatedAt.Local().Format(time.RFC3339),
	}
	for _, a := range e.Alarms {
		je.Alarms = append(je.Alarms, jsonAlarm{
			ID: a.ID, Action: a.Action,
			TriggerValue: a.TriggerValue, Description: a.Description,
		})
	}
	for _, a := range e.Attendees {
		je.Attendees = append(je.Attendees, jsonAttendee{
			ID: a.ID, Email: a.Email, Name: a.Name,
			RSVPStatus: a.RSVPStatus, Role: a.Role, Organizer: a.Organizer,
		})
	}
	return je
}

func toJSONCalendar(c calendar.Calendar) jsonCalendar {
	return jsonCalendar{
		ID:          c.ID,
		Name:        c.Name,
		Color:       c.Color,
		Description: c.Description,
		CreatedAt:   c.CreatedAt.Local().Format(time.RFC3339),
		UpdatedAt:   c.UpdatedAt.Local().Format(time.RFC3339),
	}
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printEvent(w io.Writer, e event.Event) {
	fmt.Fprintf(w, "ID: %d\n", e.ID)
	fmt.Fprintf(w, "Title: %s\n", e.Title)
	if e.AllDay {
		fmt.Fprintf(w, "When: %s (all day)\n", e.StartTime.Local().Format("Mon, 02 Jan 2006"))
	} else {
		fmt.Fprintf(w, "When: %s - %s\n",
			e.StartTime.Local().Format("Mon, 02 Jan 2006 15:04"),
			e.EndTime.Local().Format("15:04"))
	}
	if e.Location != "" {
		fmt.Fprintf(w, "Location: %s\n", e.Location)
	}
	if e.Description != "" {
		fmt.Fprintf(w, "Description: %s\n", e.Description)
	}
	if e.Status != "CONFIRMED" {
		fmt.Fprintf(w, "Status: %s\n", e.Status)
	}
	if e.URL != "" {
		fmt.Fprintf(w, "URL: %s\n", e.URL)
	}
	if e.Categories != "" {
		fmt.Fprintf(w, "Categories: %s\n", e.Categories)
	}
	if e.Timezone != "" {
		fmt.Fprintf(w, "Timezone: %s\n", e.Timezone)
	}
	fmt.Fprintf(w, "Calendar: %d\n", e.CalendarID)
	fmt.Fprintf(w, "UID: %s\n", e.UID)
	if len(e.Alarms) > 0 {
		fmt.Fprintf(w, "Alarms: %d\n", len(e.Alarms))
	}
	if len(e.Attendees) > 0 {
		fmt.Fprintf(w, "Attendees: %d\n", len(e.Attendees))
	}
}

func printEvents(w io.Writer, events []event.Event) {
	for _, e := range events {
		if e.AllDay {
			fmt.Fprintf(w, "%s\tall-day\t%s\n",
				e.StartTime.Local().Format("2006-01-02"),
				e.Title)
		} else {
			fmt.Fprintf(w, "%s\t%s-%s\t%s\n",
				e.StartTime.Local().Format("2006-01-02"),
				e.StartTime.Local().Format("15:04"),
				e.EndTime.Local().Format("15:04"),
				e.Title)
		}
	}
}

func printCalendar(w io.Writer, c calendar.Calendar) {
	fmt.Fprintf(w, "ID: %d\n", c.ID)
	fmt.Fprintf(w, "Name: %s\n", c.Name)
	fmt.Fprintf(w, "Color: %s\n", c.Color)
	if c.Description != "" {
		fmt.Fprintf(w, "Description: %s\n", c.Description)
	}
}

func printCalendars(w io.Writer, cals []calendar.Calendar) {
	for _, c := range cals {
		fmt.Fprintf(w, "%d\t%s\t%s\n", c.ID, c.Name, c.Color)
	}
}

// Todo output

type jsonTodo struct {
	ID              int64          `json:"id"`
	UID             string         `json:"uid"`
	CalendarID      int64          `json:"calendar_id"`
	Summary         string         `json:"summary"`
	Description     string         `json:"description"`
	Location        string         `json:"location"`
	DueDate         string         `json:"due_date"`
	StartDate       string         `json:"start_date"`
	Duration        string         `json:"duration"`
	CompletedAt     string         `json:"completed_at"`
	PercentComplete int64          `json:"percent_complete"`
	Status          string         `json:"status"`
	Priority        int64          `json:"priority"`
	Class           string         `json:"class"`
	URL             string         `json:"url"`
	Categories      string         `json:"categories"`
	Sequence        int64          `json:"sequence"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	Alarms          []jsonAlarm    `json:"alarms,omitempty"`
	Attendees       []jsonAttendee `json:"attendees,omitempty"`
}

func toJSONTodo(t todo.Todo) jsonTodo {
	jt := jsonTodo{
		ID: t.ID, UID: t.UID, CalendarID: t.CalendarID,
		Summary: t.Summary, Description: t.Description, Location: t.Location,
		DueDate: t.DueDate, StartDate: t.StartDate, Duration: t.Duration,
		CompletedAt: t.CompletedAt, PercentComplete: t.PercentComplete,
		Status: t.Status, Priority: t.Priority, Class: t.Class,
		URL: t.URL, Categories: t.Categories, Sequence: t.Sequence,
		CreatedAt: t.CreatedAt.Local().Format(time.RFC3339),
		UpdatedAt: t.UpdatedAt.Local().Format(time.RFC3339),
	}
	for _, a := range t.Alarms {
		jt.Alarms = append(jt.Alarms, jsonAlarm{
			ID: a.ID, Action: a.Action,
			TriggerValue: a.TriggerValue, Description: a.Description,
		})
	}
	for _, a := range t.Attendees {
		jt.Attendees = append(jt.Attendees, jsonAttendee{
			ID: a.ID, Email: a.Email, Name: a.Name,
			RSVPStatus: a.RSVPStatus, Role: a.Role, Organizer: a.Organizer,
		})
	}
	return jt
}

func toJSONEvents(events []event.Event) []jsonEvent {
	items := make([]jsonEvent, len(events))
	for i, e := range events {
		items[i] = toJSONEvent(e)
	}
	return items
}

func toJSONTodos(todos []todo.Todo) []jsonTodo {
	items := make([]jsonTodo, len(todos))
	for i, t := range todos {
		items[i] = toJSONTodo(t)
	}
	return items
}

func printTodo(w io.Writer, t todo.Todo) {
	fmt.Fprintf(w, "ID: %d\n", t.ID)
	fmt.Fprintf(w, "Summary: %s\n", t.Summary)
	fmt.Fprintf(w, "Status: %s\n", t.Status)
	if t.DueDate != "" {
		fmt.Fprintf(w, "Due: %s\n", t.ParseDueDate().Local().Format("Mon, 02 Jan 2006 15:04"))
	}
	if t.PercentComplete > 0 {
		fmt.Fprintf(w, "Progress: %d%%\n", t.PercentComplete)
	}
	if t.Location != "" {
		fmt.Fprintf(w, "Location: %s\n", t.Location)
	}
	if t.Description != "" {
		fmt.Fprintf(w, "Description: %s\n", t.Description)
	}
	if t.URL != "" {
		fmt.Fprintf(w, "URL: %s\n", t.URL)
	}
	if t.Categories != "" {
		fmt.Fprintf(w, "Categories: %s\n", t.Categories)
	}
	if t.Priority > 0 {
		fmt.Fprintf(w, "Priority: %d\n", t.Priority)
	}
	fmt.Fprintf(w, "Calendar: %d\n", t.CalendarID)
}

func printTodos(w io.Writer, todos []todo.Todo) {
	for _, t := range todos {
		status := "[ ]"
		if t.IsCompleted() {
			status = "[x]"
		}
		dueStr := "-"
		if t.DueDate != "" {
			dueStr = t.ParseDueDate().Local().Format("2006-01-02")
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", t.ID, status, dueStr, t.Summary)
	}
}
