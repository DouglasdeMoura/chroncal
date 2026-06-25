package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

type jsonEvent struct {
	ID             int64            `json:"id"`
	UID            string           `json:"uid"`
	CalendarID     int64            `json:"calendar_id"`
	Title          string           `json:"title"`
	Description    string           `json:"description,omitempty"`
	Location       string           `json:"location,omitempty"`
	StartTime      string           `json:"start_time"`
	EndTime        string           `json:"end_time"`
	AllDay         bool             `json:"all_day"`
	RecurrenceRule string           `json:"recurrence_rule,omitempty"`
	Timezone       string           `json:"timezone,omitempty"`
	Status         string           `json:"status"`
	Transp         string           `json:"transp"`
	Sequence       int64            `json:"sequence"`
	Priority       int64            `json:"priority,omitempty"`
	Class          string           `json:"class"`
	URL            string           `json:"url,omitempty"`
	Categories     string           `json:"categories,omitempty"`
	ExDates        string           `json:"exdates,omitempty"`
	RDates         string           `json:"rdates,omitempty"`
	RecurrenceID   string           `json:"recurrence_id,omitempty"`
	Geo            string           `json:"geo,omitempty"`
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
	Name       string `json:"name,omitempty"`
	RSVPStatus string `json:"rsvp_status"`
	Role       string `json:"role"`
	Organizer  bool   `json:"organizer,omitempty"`
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
	Description string `json:"description,omitempty"`
	OwnerEmail  string `json:"owner_email,omitempty"`
	IsDefault   bool   `json:"is_default,omitempty"`
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
		StartTime:      e.StartTime.UTC().Format(time.RFC3339),
		EndTime:        e.EndTime.UTC().Format(time.RFC3339),
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
		CreatedAt:      e.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      e.UpdatedAt.UTC().Format(time.RFC3339),
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
		OwnerEmail:  c.OwnerEmail,
		IsDefault:   c.IsDefault,
		CreatedAt:   c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printOutput emits v as the active machine-readable format. Callers
// should already have checked outputFmt != "text"; this only covers the
// non-text branch.
func printOutput(w io.Writer, v any) error {
	return printJSON(w, v)
}

func fmtStrings(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = textsafe.Display(s)
	}
	return strings.Join(parts, ", ")
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

func printDetailCount(w io.Writer, label string, count int) {
	if count <= 0 {
		return
	}
	printDetailField(w, 10, label, fmt.Sprintf("%d", count))
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

// parseListDate normalizes a stored date string (YYYY-MM-DD or RFC 3339)
// to YYYY-MM-DD for list-style output, returning "" when stored is empty
// or unparseable. Compact callers wrap the empty case with a "-"
// placeholder so the column still aligns under fixed-width padding.
func parseListDate(stored string) string {
	if stored == "" {
		return ""
	}
	if timeutil.IsDateOnly(stored) {
		return stored
	}
	if t, err := time.Parse(time.RFC3339, stored); err == nil {
		return t.Local().Format("2006-01-02")
	}
	return ""
}

// compactDateColumn is parseListDate with a "-" placeholder so the
// column has a printable cell even when the underlying value is empty.
func compactDateColumn(stored string) string {
	if d := parseListDate(stored); d != "" {
		return d
	}
	return "-"
}

func formatTodoDate(date string) string {
	if date == "" {
		return ""
	}
	if timeutil.IsDateOnly(date) {
		d, _ := time.Parse("2006-01-02", date)
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
	printDetailCount(w, "reminders", len(e.Alarms))
	printDetailCount(w, "participants", len(e.Attendees))
}

func printCalendar(w io.Writer, c calendar.Calendar) {
	const labelWidth = 13

	title := c.Name
	if c.IsDefault {
		title += " (Default)"
	}
	printDetailTitle(w, title)
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
	Description     string           `json:"description,omitempty"`
	Location        string           `json:"location,omitempty"`
	DueDate         string           `json:"due_date,omitempty"`
	StartDate       string           `json:"start_date,omitempty"`
	Duration        string           `json:"duration,omitempty"`
	CompletedAt     string           `json:"completed_at,omitempty"`
	PercentComplete int64            `json:"percent_complete,omitempty"`
	Status          string           `json:"status"`
	Priority        int64            `json:"priority,omitempty"`
	Class           string           `json:"class"`
	URL             string           `json:"url,omitempty"`
	Categories      string           `json:"categories,omitempty"`
	RecurrenceRule  string           `json:"recurrence_rule,omitempty"`
	ExDates         string           `json:"exdates,omitempty"`
	RDates          string           `json:"rdates,omitempty"`
	RecurrenceID    string           `json:"recurrence_id,omitempty"`
	Timezone        string           `json:"timezone,omitempty"`
	Geo             string           `json:"geo,omitempty"`
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
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
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
	printDetailCount(w, "reminders", len(t.Alarms))
	printDetailCount(w, "participants", len(t.Attendees))
	printDetailCount(w, "attachments", len(t.Attachments))
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
	Description    string           `json:"description,omitempty"`
	StartDate      string           `json:"start_date,omitempty"`
	Status         string           `json:"status"`
	Class          string           `json:"class"`
	URL            string           `json:"url,omitempty"`
	Categories     string           `json:"categories,omitempty"`
	RecurrenceRule string           `json:"recurrence_rule,omitempty"`
	ExDates        string           `json:"exdates,omitempty"`
	RDates         string           `json:"rdates,omitempty"`
	RecurrenceID   string           `json:"recurrence_id,omitempty"`
	Timezone       string           `json:"timezone,omitempty"`
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
		CreatedAt: j.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: j.UpdatedAt.UTC().Format(time.RFC3339),
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
	printDetailCount(w, "participants", len(j.Attendees))
	printDetailCount(w, "attachments", len(j.Attachments))
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
