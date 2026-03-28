package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/todo"
)

type jsonEvent struct {
	ID             int64           `json:"id"`
	UID            string          `json:"uid"`
	CalendarID     int64           `json:"calendar_id"`
	Title          string          `json:"title"`
	Description    string          `json:"description"`
	Location       string          `json:"location"`
	StartTime      string          `json:"start_time"`
	EndTime        string          `json:"end_time"`
	AllDay         bool            `json:"all_day"`
	RecurrenceRule string          `json:"recurrence_rule"`
	Timezone       string          `json:"timezone"`
	Status         string          `json:"status"`
	Transp         string          `json:"transp"`
	Sequence       int64           `json:"sequence"`
	Priority       int64           `json:"priority"`
	Class          string          `json:"class"`
	URL            string          `json:"url"`
	Categories     string          `json:"categories"`
	ExDates        string          `json:"exdates"`
	RDates         string          `json:"rdates"`
	RecurrenceID   string          `json:"recurrence_id"`
	Geo            string          `json:"geo"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
	Alarms         []jsonAlarm       `json:"alarms,omitempty"`
	Attendees      []jsonAttendee    `json:"attendees,omitempty"`
	Attachments    []jsonAttachment  `json:"attachments,omitempty"`
	Comments       []string          `json:"comments,omitempty"`
	Contacts       []string          `json:"contacts,omitempty"`
	Resources      []string          `json:"resources,omitempty"`
	Relations      []jsonRelation    `json:"relations,omitempty"`
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

type jsonAttachment struct {
	URI     string `json:"uri"`
	FmtType string `json:"fmttype,omitempty"`
}

type jsonRelation struct {
	RelType string `json:"rel_type"`
	RelUID  string `json:"rel_uid"`
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
		Geo:            e.Geo,
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
	for _, a := range e.Attachments {
		je.Attachments = append(je.Attachments, jsonAttachment{URI: a.URI, FmtType: a.FmtType})
	}
	je.Comments = e.Comments
	je.Contacts = e.Contacts
	je.Resources = e.Resources
	for _, r := range e.Relations {
		je.Relations = append(je.Relations, jsonRelation{RelType: r.RelType, RelUID: r.RelUID})
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

func printYAML(w io.Writer, v any) error {
	// Serialize through JSON to ensure YAML keys match JSON field names.
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var data any
	if err := json.Unmarshal(jsonBytes, &data); err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	return enc.Encode(data)
}

// printOutput writes v as JSON or YAML based on the global outputFmt flag.
func printOutput(w io.Writer, v any) error {
	switch outputFmt {
	case "yaml":
		return printYAML(w, v)
	default:
		return printJSON(w, v)
	}
}

func printEvent(w io.Writer, e event.Event) {
	fmt.Fprintf(w, "  ID:         %d\n", e.ID)
	fmt.Fprintf(w, "  Title:      %s\n", e.Title)
	if e.AllDay {
		fmt.Fprintf(w, "  When:       %s (all day)\n", e.StartTime.Local().Format("Mon, Jan 2 2006"))
	} else {
		fmt.Fprintf(w, "  When:       %s – %s\n",
			e.StartTime.Local().Format("Mon, Jan 2 2006 15:04"),
			e.EndTime.Local().Format("15:04"))
	}
	if e.Location != "" {
		fmt.Fprintf(w, "  Where:      %s\n", e.Location)
	}
	if e.Description != "" {
		fmt.Fprintf(w, "  Notes:      %s\n", e.Description)
	}
	if e.Status != "CONFIRMED" {
		fmt.Fprintf(w, "  Status:     %s\n", e.Status)
	}
	if e.URL != "" {
		fmt.Fprintf(w, "  URL:        %s\n", e.URL)
	}
	if e.Categories != "" {
		fmt.Fprintf(w, "  Categories: %s\n", e.Categories)
	}
	if e.Timezone != "" {
		fmt.Fprintf(w, "  Timezone:   %s\n", e.Timezone)
	}
	fmt.Fprintf(w, "  Calendar:   %d\n", e.CalendarID)
	fmt.Fprintf(w, "  UID:        %s\n", e.UID)
	if len(e.Alarms) > 0 {
		fmt.Fprintf(w, "  Alarms:     %d reminder(s)\n", len(e.Alarms))
	}
	if len(e.Attendees) > 0 {
		fmt.Fprintf(w, "  Attendees:  %d participant(s)\n", len(e.Attendees))
	}
}

func printEvents(w io.Writer, events []event.Event) {
	if len(events) == 0 {
		fmt.Fprintln(w, "No events found.")
		return
	}

	var currentDate string
	for _, e := range events {
		dateLabel := e.StartTime.Local().Format("Mon, Jan 2 2006")
		if dateLabel != currentDate {
			if currentDate != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "  %s\n", dateLabel)
			fmt.Fprintf(w, "  %s\n", strings.Repeat("─", len(dateLabel)))
			currentDate = dateLabel
		}
		if e.AllDay {
			fmt.Fprintf(w, "  ● all day   [%d] %s\n", e.ID, e.Title)
		} else {
			fmt.Fprintf(w, "  ● %s  [%d] %s\n", e.StartTime.Local().Format("15:04"), e.ID, e.Title)
		}
	}
	fmt.Fprintln(w)
}

func printCalendar(w io.Writer, c calendar.Calendar) {
	fmt.Fprintf(w, "  ID:          %d\n", c.ID)
	fmt.Fprintf(w, "  Name:        %s\n", c.Name)
	fmt.Fprintf(w, "  Color:       %s\n", c.Color)
	if c.Description != "" {
		fmt.Fprintf(w, "  Description: %s\n", c.Description)
	}
}

func printCalendars(w io.Writer, cals []calendar.Calendar) {
	if len(cals) == 0 {
		fmt.Fprintln(w, "No calendars found.")
		return
	}
	for i, c := range cals {
		if i > 0 {
			fmt.Fprintln(w)
		}
		printCalendar(w, c)
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
	Alarms          []jsonAlarm       `json:"alarms,omitempty"`
	Attendees       []jsonAttendee    `json:"attendees,omitempty"`
	Attachments     []jsonAttachment  `json:"attachments,omitempty"`
	Comments        []string          `json:"comments,omitempty"`
	Contacts        []string          `json:"contacts,omitempty"`
	Resources       []string          `json:"resources,omitempty"`
	Relations       []jsonRelation    `json:"relations,omitempty"`
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
	for _, a := range t.Attachments {
		jt.Attachments = append(jt.Attachments, jsonAttachment{URI: a.URI, FmtType: a.FmtType})
	}
	jt.Comments = t.Comments
	jt.Contacts = t.Contacts
	jt.Resources = t.Resources
	for _, r := range t.Relations {
		jt.Relations = append(jt.Relations, jsonRelation{RelType: r.RelType, RelUID: r.RelUID})
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
	fmt.Fprintf(w, "  ID:       %d\n", t.ID)
	fmt.Fprintf(w, "  Summary:  %s\n", t.Summary)
	fmt.Fprintf(w, "  Status:   %s\n", t.Status)
	if t.DueDate != "" {
		due := t.ParseDueDate().Local()
		fmt.Fprintf(w, "  Due:      %s\n", due.Format("Mon, Jan 2 2006 15:04"))
	}
	if t.PercentComplete > 0 {
		fmt.Fprintf(w, "  Progress: %d%%\n", t.PercentComplete)
	}
	if t.Location != "" {
		fmt.Fprintf(w, "  Where:    %s\n", t.Location)
	}
	if t.Description != "" {
		fmt.Fprintf(w, "  Notes:    %s\n", t.Description)
	}
	if t.URL != "" {
		fmt.Fprintf(w, "  URL:      %s\n", t.URL)
	}
	if t.Categories != "" {
		fmt.Fprintf(w, "  Tags:     %s\n", t.Categories)
	}
	if t.Priority > 0 {
		fmt.Fprintf(w, "  Priority: %d\n", t.Priority)
	}
	fmt.Fprintf(w, "  Calendar: %d\n", t.CalendarID)
}

func printTodos(w io.Writer, todos []todo.Todo) {
	if len(todos) == 0 {
		fmt.Fprintln(w, "No todos found.")
		return
	}
	for _, t := range todos {
		check := "○"
		if t.IsCompleted() {
			check = "●"
		}
		var dueStr string
		if t.DueDate != "" {
			dueStr = "  due " + t.ParseDueDate().Local().Format("Jan 2")
		}
		fmt.Fprintf(w, "  %s [%d] %s%s\n", check, t.ID, t.Summary, dueStr)
	}
}
