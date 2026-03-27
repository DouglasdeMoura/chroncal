package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
)

type jsonEvent struct {
	ID             int64  `json:"id"`
	UID            string `json:"uid"`
	CalendarID     int64  `json:"calendar_id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Location       string `json:"location"`
	StartTime      string `json:"start_time"`
	EndTime        string `json:"end_time"`
	AllDay         bool   `json:"all_day"`
	RecurrenceRule string `json:"recurrence_rule"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
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
	return jsonEvent{
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
		CreatedAt:      e.CreatedAt.Local().Format(time.RFC3339),
		UpdatedAt:      e.UpdatedAt.Local().Format(time.RFC3339),
	}
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
	fmt.Fprintf(w, "  ID:       %d\n", e.ID)
	fmt.Fprintf(w, "  Title:    %s\n", e.Title)
	if e.AllDay {
		fmt.Fprintf(w, "  When:     %s (all day)\n", e.StartTime.Local().Format("Mon, Jan 2 2006"))
	} else {
		fmt.Fprintf(w, "  When:     %s – %s\n",
			e.StartTime.Local().Format("Mon, Jan 2 2006 15:04"),
			e.EndTime.Local().Format("15:04"))
	}
	if e.Location != "" {
		fmt.Fprintf(w, "  Where:    %s\n", e.Location)
	}
	if e.Description != "" {
		fmt.Fprintf(w, "  Notes:    %s\n", e.Description)
	}
	fmt.Fprintf(w, "  Calendar: %d\n", e.CalendarID)
	fmt.Fprintf(w, "  UID:      %s\n", e.UID)
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
			fmt.Fprintf(w, "  ● all day   %s\n", e.Title)
		} else {
			fmt.Fprintf(w, "  ● %s  %s\n", e.StartTime.Local().Format("15:04"), e.Title)
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
