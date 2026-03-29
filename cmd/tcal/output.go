package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rodaine/table"
	"gopkg.in/yaml.v3"

	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/todo"
)

// icons holds display glyphs, switched by the nerd_fonts config flag.
type icons struct {
	Calendar      string // date headers
	Title         string // event/todo title
	Clock         string // timed events, due dates
	AllDay        string // all-day events
	Location      string // location field
	Notes         string // description field
	Status        string // status field
	Link          string // URL field
	Tags          string // categories field
	Folder        string // calendar reference
	ID            string // numeric ID + UID
	Bell          string // alarms
	People        string // attendees
	Color         string // color swatch
	Priority      string // priority field
	Progress      string // progress bar
	TodoOpen      string // incomplete todo
	TodoDone      string // completed todo
	Bullet        string // generic list bullet
}

var nerdIcons = icons{
	Calendar: "󰃭", Title: "󰧆", Clock: "󰥔", AllDay: "󰸗", Location: "󰍎",
	Notes: "󰎞", Status: "󰁪", Link: "󰌷", Tags: "󰓹",
	Folder: "󰉋", ID: "󰻾", Bell: "󱃲", People: "󰡉",
	Color: "󰏘", Priority: "󰁥", Progress: "󰓾",
	TodoOpen: "󰄱", TodoDone: "󰄬", Bullet: "󰧞",
}

var plainIcons = icons{
	Calendar: "#", Title: "*", Clock: "●", AllDay: "◆", Location: "@",
	Notes: "…", Status: "!", Link: "~", Tags: "#",
	Folder: ">", ID: "~", Bell: "♪", People: "&",
	Color: "●", Priority: "!", Progress: "%",
	TodoOpen: "○", TodoDone: "●", Bullet: "●",
}

// ic returns the active icon set based on cfg.NerdFonts.
func ic() icons {
	if cfg.NerdFonts {
		return nerdIcons
	}
	return plainIcons
}

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

// printOutput writes v as table, JSON, or YAML based on the global outputFmt flag.
func printOutput(w io.Writer, v any) error {
	switch outputFmt {
	case "table":
		return printTable(w, v)
	case "yaml":
		return printYAML(w, v)
	default:
		return printJSON(w, v)
	}
}

// Table cell formatters for 1-to-many fields.

func fmtAlarms(alarms []jsonAlarm) string {
	if len(alarms) == 0 {
		return ""
	}
	parts := make([]string, len(alarms))
	for i, a := range alarms {
		parts[i] = a.Action + ":" + a.TriggerValue
	}
	return strings.Join(parts, ", ")
}

func fmtAttendees(attendees []jsonAttendee) string {
	if len(attendees) == 0 {
		return ""
	}
	parts := make([]string, len(attendees))
	for i, a := range attendees {
		if a.Name != "" {
			parts[i] = a.Name
		} else {
			parts[i] = a.Email
		}
	}
	return strings.Join(parts, ", ")
}

func fmtAttachments(attachments []jsonAttachment) string {
	if len(attachments) == 0 {
		return ""
	}
	parts := make([]string, len(attachments))
	for i, a := range attachments {
		parts[i] = a.URI
	}
	return strings.Join(parts, ", ")
}

func fmtStrings(ss []string) string {
	return strings.Join(ss, ", ")
}

func fmtRelations(relations []jsonRelation) string {
	if len(relations) == 0 {
		return ""
	}
	parts := make([]string, len(relations))
	for i, r := range relations {
		parts[i] = r.RelType + ":" + r.RelUID
	}
	return strings.Join(parts, ", ")
}

// printTable renders v as aligned columns using rodaine/table.
func printTable(w io.Writer, v any) error {
	switch data := v.(type) {
	case []jsonEvent:
		if len(data) == 0 {
			fmt.Fprintln(w, "No events found.")
			return nil
		}
		tbl := table.New("ID", "UID", "CAL", "DATE", "TIME", "TITLE",
			"DESCRIPTION", "LOCATION", "ALL DAY", "RRULE", "TZ",
			"STATUS", "TRANSP", "SEQ", "PRI", "CLASS", "URL",
			"CATEGORIES", "EXDATES", "RDATES", "REC-ID", "GEO",
			"ALARMS", "ATTENDEES", "ATTACHMENTS", "COMMENTS",
			"CONTACTS", "RESOURCES", "RELATIONS",
			"CREATED", "UPDATED")
		tbl.WithWriter(w)
		for _, e := range data {
			start, _ := time.Parse(time.RFC3339, e.StartTime)
			end, _ := time.Parse(time.RFC3339, e.EndTime)
			date := start.Local().Format("2006-01-02")
			var timeRange string
			if e.AllDay {
				timeRange = "all day"
			} else {
				timeRange = start.Local().Format("15:04") + "–" + end.Local().Format("15:04")
			}
			tbl.AddRow(e.ID, e.UID, e.CalendarID, date, timeRange, e.Title,
				e.Description, e.Location, e.AllDay, e.RecurrenceRule, e.Timezone,
				e.Status, e.Transp, e.Sequence, e.Priority, e.Class, e.URL,
				e.Categories, e.ExDates, e.RDates, e.RecurrenceID, e.Geo,
				fmtAlarms(e.Alarms), fmtAttendees(e.Attendees), fmtAttachments(e.Attachments),
				fmtStrings(e.Comments), fmtStrings(e.Contacts), fmtStrings(e.Resources),
				fmtRelations(e.Relations),
				e.CreatedAt, e.UpdatedAt)
		}
		tbl.Print()
		return nil

	case jsonEvent:
		return printTable(w, []jsonEvent{data})

	case []jsonCalendar:
		if len(data) == 0 {
			fmt.Fprintln(w, "No calendars found.")
			return nil
		}
		tbl := table.New("ID", "NAME", "COLOR", "DESCRIPTION", "CREATED", "UPDATED")
		tbl.WithWriter(w)
		for _, c := range data {
			tbl.AddRow(c.ID, c.Name, c.Color, c.Description, c.CreatedAt, c.UpdatedAt)
		}
		tbl.Print()
		return nil

	case jsonCalendar:
		return printTable(w, []jsonCalendar{data})

	case []jsonTodo:
		if len(data) == 0 {
			fmt.Fprintln(w, "No todos found.")
			return nil
		}
		tbl := table.New("ID", "UID", "CAL", "SUMMARY", "DESCRIPTION", "LOCATION",
			"DUE", "START", "DURATION", "COMPLETED", "PROGRESS",
			"STATUS", "PRI", "CLASS", "URL", "CATEGORIES", "SEQ",
			"ALARMS", "ATTENDEES", "ATTACHMENTS", "COMMENTS",
			"CONTACTS", "RESOURCES", "RELATIONS",
			"CREATED", "UPDATED")
		tbl.WithWriter(w)
		for _, t := range data {
			due := ""
			if t.DueDate != "" {
				if _, err := time.Parse("2006-01-02", t.DueDate); err == nil {
					due = t.DueDate
				} else if d, err := time.Parse(time.RFC3339, t.DueDate); err == nil {
					due = d.Local().Format("2006-01-02")
				}
			}
			tbl.AddRow(t.ID, t.UID, t.CalendarID, t.Summary, t.Description, t.Location,
				due, t.StartDate, t.Duration, t.CompletedAt, t.PercentComplete,
				t.Status, t.Priority, t.Class, t.URL, t.Categories, t.Sequence,
				fmtAlarms(t.Alarms), fmtAttendees(t.Attendees), fmtAttachments(t.Attachments),
				fmtStrings(t.Comments), fmtStrings(t.Contacts), fmtStrings(t.Resources),
				fmtRelations(t.Relations),
				t.CreatedAt, t.UpdatedAt)
		}
		tbl.Print()
		return nil

	case jsonTodo:
		return printTable(w, []jsonTodo{data})

	default:
		return printJSON(w, v)
	}
}

// printEvent renders a single event detail. When showDate is false the date
// is omitted from the time line (the caller already printed a date header).
func printEvent(w io.Writer, e event.Event) {
	printEventDetail(w, e, true)
}

func printEventDetail(w io.Writer, e event.Event, showDate bool) {
	i := ic()
	fmt.Fprintf(w, "  %s %s\n", i.Title, e.Title)
	if e.AllDay {
		if showDate {
			fmt.Fprintf(w, "  %s %s (all day)\n", i.Clock, e.StartTime.Local().Format("Mon, Jan 2 2006"))
		} else {
			fmt.Fprintf(w, "  %s all day\n", i.Clock)
		}
	} else {
		if showDate {
			fmt.Fprintf(w, "  %s %s – %s\n", i.Clock,
				e.StartTime.Local().Format("Mon, Jan 2 2006 15:04"),
				e.EndTime.Local().Format("15:04"))
		} else {
			fmt.Fprintf(w, "  %s %s – %s\n", i.Clock,
				e.StartTime.Local().Format("15:04"),
				e.EndTime.Local().Format("15:04"))
		}
	}
	if e.Location != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Location, e.Location)
	}
	if e.Description != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Notes, e.Description)
	}
	if e.Status != "CONFIRMED" {
		fmt.Fprintf(w, "  %s %s\n", i.Status, e.Status)
	}
	if e.URL != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Link, e.URL)
	}
	if e.Categories != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Tags, e.Categories)
	}
	if e.Timezone != "" {
		fmt.Fprintf(w, "  %s TZ: %s\n", i.Clock, e.Timezone)
	}
	fmt.Fprintf(w, "  %s Calendar %d\n", i.Folder, e.CalendarID)
	fmt.Fprintf(w, "  %s %d  %s\n", i.ID, e.ID, e.UID)
	if len(e.Alarms) > 0 {
		fmt.Fprintf(w, "  %s %d reminder(s)\n", i.Bell, len(e.Alarms))
	}
	if len(e.Attendees) > 0 {
		fmt.Fprintf(w, "  %s %d participant(s)\n", i.People, len(e.Attendees))
	}
}

func printEvents(w io.Writer, events []event.Event) {
	if len(events) == 0 {
		fmt.Fprintln(w, "No events found.")
		return
	}

	i := ic()
	var currentDate string
	for idx, e := range events {
		dateLabel := e.StartTime.Local().Format("Mon, Jan 2 2006")
		if dateLabel != currentDate {
			if currentDate != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "  %s %s\n", i.Calendar, dateLabel)
			fmt.Fprintf(w, "  %s\n", strings.Repeat("─", len(dateLabel)+4))
			currentDate = dateLabel
		}
		if idx > 0 && events[idx-1].StartTime.Local().Format("Mon, Jan 2 2006") == dateLabel {
			fmt.Fprintln(w)
		}
		printEventDetail(w, e, false)
	}
	fmt.Fprintln(w)
}

func printCalendar(w io.Writer, c calendar.Calendar) {
	i := ic()
	fmt.Fprintf(w, "  %s %s\n", i.Title, c.Name)
	fmt.Fprintf(w, "  %s %s\n", i.Color, c.Color)
	if c.Description != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Notes, c.Description)
	}
	fmt.Fprintf(w, "  %s %d\n", i.ID, c.ID)
}

func printCalendars(w io.Writer, cals []calendar.Calendar) {
	if len(cals) == 0 {
		fmt.Fprintln(w, "No calendars found.")
		return
	}
	for idx, c := range cals {
		if idx > 0 {
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
	i := ic()
	fmt.Fprintf(w, "  %s %s\n", i.Title, t.Summary)
	fmt.Fprintf(w, "  %s %s\n", i.Status, t.Status)
	if t.DueDate != "" {
		due := t.ParseDueDate()
		if _, err := time.Parse("2006-01-02", t.DueDate); err == nil {
			fmt.Fprintf(w, "  %s Due %s\n", i.Clock, due.Format("Mon, Jan 2 2006"))
		} else {
			fmt.Fprintf(w, "  %s Due %s\n", i.Clock, due.Local().Format("Mon, Jan 2 2006 15:04"))
		}
	}
	if t.PercentComplete > 0 {
		fmt.Fprintf(w, "  %s %d%%\n", i.Progress, t.PercentComplete)
	}
	if t.Location != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Location, t.Location)
	}
	if t.Description != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Notes, t.Description)
	}
	if t.URL != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Link, t.URL)
	}
	if t.Categories != "" {
		fmt.Fprintf(w, "  %s %s\n", i.Tags, t.Categories)
	}
	if t.Priority > 0 {
		fmt.Fprintf(w, "  %s Priority %d\n", i.Priority, t.Priority)
	}
	fmt.Fprintf(w, "  %s Calendar %d\n", i.Folder, t.CalendarID)
	fmt.Fprintf(w, "  %s %d  %s\n", i.ID, t.ID, t.UID)
}

func printTodos(w io.Writer, todos []todo.Todo) {
	if len(todos) == 0 {
		fmt.Fprintln(w, "No todos found.")
		return
	}
	for idx, t := range todos {
		if idx > 0 {
			fmt.Fprintln(w)
		}
		printTodo(w, t)
	}
}
