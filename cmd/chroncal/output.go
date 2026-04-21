package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rodaine/table"
	"gopkg.in/yaml.v3"

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// icons holds display glyphs, switched by the nerd_fonts config flag.
type icons struct {
	Calendar string // date headers
	Title    string // event/todo title
	Clock    string // timed events, due dates
	AllDay   string // all-day events
	Location string // location field
	Notes    string // description field
	Status   string // status field
	Link     string // URL field
	Tags     string // categories field
	Folder   string // calendar reference
	ID       string // numeric ID + UID
	Bell     string // alarms
	People   string // attendees
	Color    string // color swatch
	Priority string // priority field
	Progress string // progress bar
	TodoOpen string // incomplete todo
	TodoDone string // completed todo
	Bullet   string // generic list bullet
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
	ID             int64            `json:"id"`
	UID            string           `json:"uid"`
	CalendarID     int64            `json:"calendar_id"`
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	Location       string           `json:"location"`
	StartTime      string           `json:"start_time"`
	EndTime        string           `json:"end_time"`
	AllDay         bool             `json:"all_day"`
	RecurrenceRule string           `json:"recurrence_rule"`
	Timezone       string           `json:"timezone"`
	Status         string           `json:"status"`
	Transp         string           `json:"transp"`
	Sequence       int64            `json:"sequence"`
	Priority       int64            `json:"priority"`
	Class          string           `json:"class"`
	URL            string           `json:"url"`
	Categories     string           `json:"categories"`
	ExDates        string           `json:"exdates"`
	RDates         string           `json:"rdates"`
	RecurrenceID   string           `json:"recurrence_id"`
	Geo            string           `json:"geo"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
	Alarms         []jsonAlarm      `json:"alarms,omitempty"`
	Attendees      []jsonAttendee   `json:"attendees,omitempty"`
	Attachments    []jsonAttachment `json:"attachments,omitempty"`
	Comments       []string         `json:"comments,omitempty"`
	Contacts       []string         `json:"contacts,omitempty"`
	Resources      []string         `json:"resources,omitempty"`
	Relations      []jsonRelation   `json:"relations,omitempty"`
}

type jsonAlarm struct {
	ID           int64               `json:"id"`
	Action       string              `json:"action"`
	TriggerValue string              `json:"trigger_value"`
	Description  string              `json:"description"`
	Summary      string              `json:"summary,omitempty"`
	Repeat       int                 `json:"repeat,omitempty"`
	Duration     string              `json:"duration,omitempty"`
	Related      string              `json:"related,omitempty"`
	Attendees    []jsonAlarmAttendee `json:"attendees,omitempty"`
}

type jsonAlarmAttendee struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
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

func toJSONAlarmAttendees(attendees []model.AlarmAttendee) []jsonAlarmAttendee {
	if len(attendees) == 0 {
		return nil
	}
	out := make([]jsonAlarmAttendee, len(attendees))
	for i, a := range attendees {
		out[i] = jsonAlarmAttendee{Email: a.Email, Name: a.Name}
	}
	return out
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
			Summary: a.Summary,
			Repeat:  a.Repeat, Duration: a.Duration, Related: a.Related,
			Attendees: toJSONAlarmAttendees(a.Attendees),
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
			parts[i] = textsafe.Display(a.Name)
		} else {
			parts[i] = textsafe.Display(a.Email)
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
		parts[i] = textsafe.Display(a.URI)
	}
	return strings.Join(parts, ", ")
}

func fmtStrings(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = textsafe.Display(s)
	}
	return strings.Join(parts, ", ")
}

func fmtRelations(relations []jsonRelation) string {
	if len(relations) == 0 {
		return ""
	}
	parts := make([]string, len(relations))
	for i, r := range relations {
		parts[i] = textsafe.Display(r.RelType + ":" + r.RelUID)
	}
	return strings.Join(parts, ", ")
}

func safeDisplayValue(v any) any {
	if s, ok := v.(string); ok {
		return textsafe.Display(s)
	}
	return v
}

func safeDisplayValues(vals ...any) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = safeDisplayValue(v)
	}
	return out
}

// printTable renders v as aligned columns using rodaine/table.
// eventTableDateTime renders DATE and TIME columns for the events table,
// expanding the DATE column to a range when the event spans multiple days.
// The end time is exclusive, matching how we store it.
func eventTableDateTime(start, end time.Time, allDay bool) (string, string) {
	if allDay {
		last := end.AddDate(0, 0, -1)
		if start.Year() == last.Year() && start.YearDay() == last.YearDay() {
			return start.Format("2006-01-02"), "all day"
		}
		endFmt := "01-02"
		if start.Year() != last.Year() {
			endFmt = "2006-01-02"
		}
		return start.Format("2006-01-02") + " to " + last.Format(endFmt), "all day"
	}
	timeRange := start.Format("15:04") + " - " + end.Format("15:04")
	if start.Year() == end.Year() && start.YearDay() == end.YearDay() {
		return start.Format("2006-01-02"), timeRange
	}
	endFmt := "01-02"
	if start.Year() != end.Year() {
		endFmt = "2006-01-02"
	}
	return start.Format("2006-01-02") + " to " + end.Format(endFmt), timeRange
}

func printDetailTitle(w io.Writer, title string) {
	fmt.Fprintf(w, "  %s\n", textsafe.Display(title))
}

func printDetailField(w io.Writer, width int, label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(w, "    %-*s %s\n", width, label+":", textsafe.Display(value))
}

func printDetailInt(w io.Writer, width int, label string, value int64) {
	printDetailField(w, width, label, fmt.Sprintf("%d", value))
}

func printDetailCount(w io.Writer, width int, label string, count int) {
	if count <= 0 {
		return
	}
	printDetailField(w, width, label, fmt.Sprintf("%d", count))
}

func formatEventDetailWhen(start, end time.Time, allDay bool) string {
	if allDay {
		last := end.AddDate(0, 0, -1)
		if start.Year() == last.Year() && start.YearDay() == last.YearDay() {
			return start.Format("Mon, Jan 2 2006") + " (all day)"
		}
		return start.Format("Mon, Jan 2 2006") + " to " + last.Format("Mon, Jan 2 2006") + " (all day)"
	}
	if start.Year() == end.Year() && start.YearDay() == end.YearDay() {
		return start.Format("Mon, Jan 2 2006 15:04") + " - " + end.Format("15:04")
	}
	return start.Format("Mon, Jan 2 2006 15:04") + " - " + end.Format("Mon, Jan 2 2006 15:04")
}

func formatEventListTime(start, end time.Time, allDay bool) string {
	if allDay {
		last := end.AddDate(0, 0, -1)
		if start.Year() == last.Year() && start.YearDay() == last.YearDay() {
			return "all day"
		}
		return "all day (" + start.Format("Mon, Jan 2 2006") + " to " + last.Format("Mon, Jan 2 2006") + ")"
	}
	if start.Year() == end.Year() && start.YearDay() == end.YearDay() {
		return start.Format("15:04") + " - " + end.Format("15:04")
	}
	return start.Format("15:04") + " - " + end.Format("Mon, Jan 2 15:04")
}

func fmtModelRelations(relations []model.Relation) string {
	if len(relations) == 0 {
		return ""
	}
	parts := make([]string, len(relations))
	for i, r := range relations {
		parts[i] = textsafe.Display(r.RelType + ":" + r.RelUID)
	}
	return strings.Join(parts, ", ")
}

func formatTodoDate(date string) string {
	if date == "" {
		return ""
	}
	if d, err := time.Parse("2006-01-02", date); err == nil {
		return d.Format("Mon, Jan 2 2006")
	}
	if d, err := time.Parse(time.RFC3339, date); err == nil {
		return d.Local().Format("Mon, Jan 2 2006 15:04")
	}
	return textsafe.Display(date)
}

func formatDateTime(value string) string {
	if value == "" {
		return ""
	}
	if d, err := time.Parse(time.RFC3339, value); err == nil {
		return d.Local().Format("Mon, Jan 2 2006 15:04")
	}
	return textsafe.Display(value)
}

func todoCheckbox(t todo.Todo) string {
	if t.CompletedAt != "" || strings.EqualFold(t.Status, "COMPLETED") {
		return "[x]"
	}
	return "[ ]"
}

func printEventSummary(w io.Writer, e event.Event) {
	fmt.Fprintf(w, "  * %s\n", textsafe.Display(e.Title))
	fmt.Fprintf(w, "    %s\n", formatEventListTime(e.StartTime.Local(), e.EndTime.Local(), e.AllDay))
	if e.Location != "" {
		fmt.Fprintf(w, "    %s\n", textsafe.Display(e.Location))
	}
	if e.Description != "" {
		fmt.Fprintf(w, "    %s\n", textsafe.Display(e.Description))
	}
}

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
			date, timeRange := eventTableDateTime(start.Local(), end.Local(), e.AllDay)
			tbl.AddRow(safeDisplayValues(e.ID, e.UID, e.CalendarID, date, timeRange, e.Title,
				e.Description, e.Location, e.AllDay, e.RecurrenceRule, e.Timezone,
				e.Status, e.Transp, e.Sequence, e.Priority, e.Class, e.URL,
				e.Categories, e.ExDates, e.RDates, e.RecurrenceID, e.Geo,
				fmtAlarms(e.Alarms), fmtAttendees(e.Attendees), fmtAttachments(e.Attachments),
				fmtStrings(e.Comments), fmtStrings(e.Contacts), fmtStrings(e.Resources),
				fmtRelations(e.Relations),
				e.CreatedAt, e.UpdatedAt)...)
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
			tbl.AddRow(safeDisplayValues(c.ID, c.Name, c.Color, c.Description, c.CreatedAt, c.UpdatedAt)...)
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
			tbl.AddRow(safeDisplayValues(t.ID, t.UID, t.CalendarID, t.Summary, t.Description, t.Location,
				due, t.StartDate, t.Duration, t.CompletedAt, t.PercentComplete,
				t.Status, t.Priority, t.Class, t.URL, t.Categories, t.Sequence,
				fmtAlarms(t.Alarms), fmtAttendees(t.Attendees), fmtAttachments(t.Attachments),
				fmtStrings(t.Comments), fmtStrings(t.Contacts), fmtStrings(t.Resources),
				fmtRelations(t.Relations),
				t.CreatedAt, t.UpdatedAt)...)
		}
		tbl.Print()
		return nil

	case jsonTodo:
		return printTable(w, []jsonTodo{data})

	case []jsonJournal:
		if len(data) == 0 {
			fmt.Fprintln(w, "No journal entries found.")
			return nil
		}
		tbl := table.New("ID", "UID", "CAL", "SUMMARY", "DESCRIPTION",
			"DATE", "STATUS", "CLASS", "URL", "CATEGORIES", "SEQ",
			"RRULE", "EXDATES", "RDATES", "REC-ID", "TZ",
			"ATTENDEES", "ATTACHMENTS", "COMMENTS",
			"CONTACTS", "RELATIONS",
			"CREATED", "UPDATED")
		tbl.WithWriter(w)
		for _, j := range data {
			tbl.AddRow(safeDisplayValues(j.ID, j.UID, j.CalendarID, j.Summary, j.Description,
				j.StartDate, j.Status, j.Class, j.URL, j.Categories, j.Sequence,
				j.RecurrenceRule, j.ExDates, j.RDates, j.RecurrenceID, j.Timezone,
				fmtAttendees(j.Attendees), fmtAttachments(j.Attachments),
				fmtStrings(j.Comments), fmtStrings(j.Contacts),
				fmtRelations(j.Relations),
				j.CreatedAt, j.UpdatedAt)...)
		}
		tbl.Print()
		return nil

	case jsonJournal:
		return printTable(w, []jsonJournal{data})

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
	const labelWidth = 10

	printDetailTitle(w, e.Title)

	if showDate {
		printDetailField(w, labelWidth, "when", formatEventDetailWhen(e.StartTime.Local(), e.EndTime.Local(), e.AllDay))
	} else {
		printDetailField(w, labelWidth, "time", formatEventListTime(e.StartTime.Local(), e.EndTime.Local(), e.AllDay))
	}
	printDetailField(w, labelWidth, "location", e.Location)
	printDetailField(w, labelWidth, "notes", e.Description)
	if e.Status != "" && e.Status != "CONFIRMED" {
		printDetailField(w, labelWidth, "status", e.Status)
	}
	printDetailField(w, labelWidth, "url", e.URL)
	printDetailField(w, labelWidth, "tags", e.Categories)
	printDetailField(w, labelWidth, "timezone", e.Timezone)
	printDetailInt(w, labelWidth, "calendar", e.CalendarID)
	printDetailInt(w, labelWidth, "id", e.ID)
	printDetailField(w, labelWidth, "uid", e.UID)
	printDetailCount(w, labelWidth, "reminders", len(e.Alarms))
	printDetailCount(w, labelWidth, "participants", len(e.Attendees))
}

func printEvents(w io.Writer, events []event.Event) {
	if len(events) == 0 {
		fmt.Fprintln(w, "No events found.")
		return
	}

	var currentDate string
	for idx, e := range events {
		dateLabel := e.StartTime.Local().Format("Mon, Jan 2 2006")
		if dateLabel != currentDate {
			if currentDate != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "  %s\n", dateLabel)
			fmt.Fprintf(w, "  %s\n", strings.Repeat("-", len(dateLabel)))
			fmt.Fprintln(w)
			currentDate = dateLabel
		}
		if idx > 0 && events[idx-1].StartTime.Local().Format("Mon, Jan 2 2006") == dateLabel {
			fmt.Fprintln(w)
		}
		printEventSummary(w, e)
	}
	fmt.Fprintln(w)
}

func printCalendar(w io.Writer, c calendar.Calendar) {
	const labelWidth = 13

	printDetailTitle(w, c.Name)
	printDetailField(w, labelWidth, "color", c.Color)
	printDetailField(w, labelWidth, "description", c.Description)
	printDetailInt(w, labelWidth, "id", c.ID)
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
	ID              int64            `json:"id"`
	UID             string           `json:"uid"`
	CalendarID      int64            `json:"calendar_id"`
	Summary         string           `json:"summary"`
	Description     string           `json:"description"`
	Location        string           `json:"location"`
	DueDate         string           `json:"due_date"`
	StartDate       string           `json:"start_date"`
	Duration        string           `json:"duration"`
	CompletedAt     string           `json:"completed_at"`
	PercentComplete int64            `json:"percent_complete"`
	Status          string           `json:"status"`
	Priority        int64            `json:"priority"`
	Class           string           `json:"class"`
	URL             string           `json:"url"`
	Categories      string           `json:"categories"`
	RecurrenceRule  string           `json:"recurrence_rule"`
	ExDates         string           `json:"exdates"`
	RDates          string           `json:"rdates"`
	RecurrenceID    string           `json:"recurrence_id"`
	Timezone        string           `json:"timezone"`
	Geo             string           `json:"geo"`
	Sequence        int64            `json:"sequence"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
	Alarms          []jsonAlarm      `json:"alarms,omitempty"`
	Attendees       []jsonAttendee   `json:"attendees,omitempty"`
	Attachments     []jsonAttachment `json:"attachments,omitempty"`
	Comments        []string         `json:"comments,omitempty"`
	Contacts        []string         `json:"contacts,omitempty"`
	Resources       []string         `json:"resources,omitempty"`
	Relations       []jsonRelation   `json:"relations,omitempty"`
}

func toJSONTodo(t todo.Todo) jsonTodo {
	jt := jsonTodo{
		ID: t.ID, UID: t.UID, CalendarID: t.CalendarID,
		Summary: t.Summary, Description: t.Description, Location: t.Location,
		DueDate: t.DueDate, StartDate: t.StartDate, Duration: t.Duration,
		CompletedAt: t.CompletedAt, PercentComplete: t.PercentComplete,
		Status: t.Status, Priority: t.Priority, Class: t.Class,
		URL: t.URL, Categories: t.Categories,
		RecurrenceRule: t.RecurrenceRule, ExDates: t.ExDates, RDates: t.RDates,
		RecurrenceID: t.RecurrenceID, Timezone: t.Timezone, Geo: t.Geo,
		Sequence:  t.Sequence,
		CreatedAt: t.CreatedAt.Local().Format(time.RFC3339),
		UpdatedAt: t.UpdatedAt.Local().Format(time.RFC3339),
	}
	for _, a := range t.Alarms {
		jt.Alarms = append(jt.Alarms, jsonAlarm{
			ID: a.ID, Action: a.Action,
			TriggerValue: a.TriggerValue, Description: a.Description,
			Summary: a.Summary,
			Repeat:  a.Repeat, Duration: a.Duration, Related: a.Related,
			Attendees: toJSONAlarmAttendees(a.Attendees),
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
	const labelWidth = 10

	fmt.Fprintf(w, "  %s %s\n", todoCheckbox(t), textsafe.Display(t.Summary))
	printDetailField(w, labelWidth, "status", t.Status)
	printDetailField(w, labelWidth, "start", formatTodoDate(t.StartDate))
	printDetailField(w, labelWidth, "due", formatTodoDate(t.DueDate))
	printDetailField(w, labelWidth, "duration", t.Duration)
	printDetailField(w, labelWidth, "completed", formatDateTime(t.CompletedAt))
	if t.PercentComplete > 0 {
		printDetailField(w, labelWidth, "progress", fmt.Sprintf("%d%%", t.PercentComplete))
	}
	printDetailField(w, labelWidth, "location", t.Location)
	printDetailField(w, labelWidth, "notes", t.Description)
	printDetailField(w, labelWidth, "url", t.URL)
	printDetailField(w, labelWidth, "tags", t.Categories)
	if t.Class != "" && t.Class != "PUBLIC" {
		printDetailField(w, labelWidth, "class", t.Class)
	}
	if t.Priority > 0 {
		printDetailField(w, labelWidth, "priority", fmt.Sprintf("%d", t.Priority))
	}
	printDetailField(w, labelWidth, "rrule", t.RecurrenceRule)
	printDetailInt(w, labelWidth, "calendar", t.CalendarID)
	printDetailInt(w, labelWidth, "id", t.ID)
	printDetailField(w, labelWidth, "uid", t.UID)
	printDetailCount(w, labelWidth, "reminders", len(t.Alarms))
	printDetailCount(w, labelWidth, "participants", len(t.Attendees))
	printDetailCount(w, labelWidth, "attachments", len(t.Attachments))
	printDetailField(w, labelWidth, "comments", fmtStrings(t.Comments))
	printDetailField(w, labelWidth, "contacts", fmtStrings(t.Contacts))
	printDetailField(w, labelWidth, "resources", fmtStrings(t.Resources))
	printDetailField(w, labelWidth, "relations", fmtModelRelations(t.Relations))
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

// Journal output

type jsonJournal struct {
	ID             int64            `json:"id"`
	UID            string           `json:"uid"`
	CalendarID     int64            `json:"calendar_id"`
	Summary        string           `json:"summary"`
	Description    string           `json:"description"`
	StartDate      string           `json:"start_date"`
	Status         string           `json:"status"`
	Class          string           `json:"class"`
	URL            string           `json:"url"`
	Categories     string           `json:"categories"`
	RecurrenceRule string           `json:"recurrence_rule"`
	ExDates        string           `json:"exdates"`
	RDates         string           `json:"rdates"`
	RecurrenceID   string           `json:"recurrence_id"`
	Timezone       string           `json:"timezone"`
	Sequence       int64            `json:"sequence"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
	Attendees      []jsonAttendee   `json:"attendees,omitempty"`
	Attachments    []jsonAttachment `json:"attachments,omitempty"`
	Comments       []string         `json:"comments,omitempty"`
	Contacts       []string         `json:"contacts,omitempty"`
	Relations      []jsonRelation   `json:"relations,omitempty"`
}

func toJSONJournal(j journal.Journal) jsonJournal {
	jj := jsonJournal{
		ID: j.ID, UID: j.UID, CalendarID: j.CalendarID,
		Summary: j.Summary, Description: j.Description,
		StartDate: j.StartDate, Status: j.Status, Class: j.Class,
		URL: j.URL, Categories: j.Categories,
		RecurrenceRule: j.RecurrenceRule, ExDates: j.ExDates, RDates: j.RDates,
		RecurrenceID: j.RecurrenceID, Timezone: j.Timezone,
		Sequence:  j.Sequence,
		CreatedAt: j.CreatedAt.Local().Format(time.RFC3339),
		UpdatedAt: j.UpdatedAt.Local().Format(time.RFC3339),
	}
	for _, a := range j.Attendees {
		jj.Attendees = append(jj.Attendees, jsonAttendee{
			ID: a.ID, Email: a.Email, Name: a.Name,
			RSVPStatus: a.RSVPStatus, Role: a.Role, Organizer: a.Organizer,
		})
	}
	for _, a := range j.Attachments {
		jj.Attachments = append(jj.Attachments, jsonAttachment{URI: a.URI, FmtType: a.FmtType})
	}
	jj.Comments = j.Comments
	jj.Contacts = j.Contacts
	for _, r := range j.Relations {
		jj.Relations = append(jj.Relations, jsonRelation{RelType: r.RelType, RelUID: r.RelUID})
	}
	return jj
}

func toJSONJournals(journals []journal.Journal) []jsonJournal {
	items := make([]jsonJournal, len(journals))
	for i, j := range journals {
		items[i] = toJSONJournal(j)
	}
	return items
}

func printJournal(w io.Writer, j journal.Journal) {
	const labelWidth = 10

	printDetailTitle(w, j.Summary)
	printDetailField(w, labelWidth, "date", formatTodoDate(j.StartDate))
	printDetailField(w, labelWidth, "status", j.Status)
	printDetailField(w, labelWidth, "notes", j.Description)
	printDetailField(w, labelWidth, "url", j.URL)
	printDetailField(w, labelWidth, "tags", j.Categories)
	if j.Class != "" && j.Class != "PUBLIC" {
		printDetailField(w, labelWidth, "class", j.Class)
	}
	printDetailField(w, labelWidth, "rrule", j.RecurrenceRule)
	printDetailInt(w, labelWidth, "calendar", j.CalendarID)
	printDetailInt(w, labelWidth, "id", j.ID)
	printDetailField(w, labelWidth, "uid", j.UID)
	printDetailCount(w, labelWidth, "participants", len(j.Attendees))
	printDetailCount(w, labelWidth, "attachments", len(j.Attachments))
	printDetailField(w, labelWidth, "comments", fmtStrings(j.Comments))
	printDetailField(w, labelWidth, "contacts", fmtStrings(j.Contacts))
	printDetailField(w, labelWidth, "relations", fmtModelRelations(j.Relations))
}

func printJournals(w io.Writer, journals []journal.Journal) {
	if len(journals) == 0 {
		fmt.Fprintln(w, "No journal entries found.")
		return
	}
	for idx, j := range journals {
		if idx > 0 {
			fmt.Fprintln(w)
		}
		printJournal(w, j)
	}
}
