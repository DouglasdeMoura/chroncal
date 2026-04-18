package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// paletteSearchLimit caps the number of event matches shown in the palette
// so the dialog stays scannable even for broad queries.
const paletteSearchLimit = 15

// makePaletteSearchFunc returns a palette search callback that queries
// events by text and formats them as palette entries. Selecting a result
// opens the event in the edit form via EventEditMsg.
func makePaletteSearchFunc(m Model) PaletteSearchFunc {
	return func(query string) tea.Cmd {
		return func() tea.Msg {
			ctx := context.Background()
			events, err := m.app.Events.Search(ctx, event.SearchParams{Query: query})
			if err != nil {
				return PaletteSearchResultsMsg{Query: query}
			}
			if len(events) > paletteSearchLimit {
				events = events[:paletteSearchLimit]
			}
			items := make([]PaletteCommand, 0, len(events))
			for _, ev := range events {
				items = append(items, eventPaletteCommand(ev, m.calendars[ev.CalendarID]))
			}
			return PaletteSearchResultsMsg{Query: query, Items: items}
		}
	}
}

func eventPaletteCommand(ev event.Event, cal CalendarInfo) PaletteCommand {
	return PaletteCommand{
		ID:          fmt.Sprintf("event.%d", ev.ID),
		PrefixChar:  "●",
		PrefixColor: cal.Color,
		Title:       ev.Title,
		Shortcut:    paletteEventDate(ev),
		Action:      func() tea.Msg { return EventEditMsg{Event: ev} },
	}
}

// paletteEventDate returns a compact, right-aligned date label for an
// event's palette row. All-day events drop the time; cross-year events
// show the year instead of the clock.
func paletteEventDate(ev event.Event) string {
	t := ev.StartTime.Local()
	if t.IsZero() {
		return ""
	}
	if ev.AllDay {
		if t.Year() == time.Now().Year() {
			return t.Format("Jan 2")
		}
		return t.Format("Jan 2 2006")
	}
	if t.Year() == time.Now().Year() {
		return t.Format("Jan 2 15:04")
	}
	return t.Format("Jan 2 2006")
}
