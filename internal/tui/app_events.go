package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

// expectedEventRange returns the [from, to) UTC range the active view
// currently expects from loadEvents. It seeds each query. It also
// validates eventsLoadedMsg that arrive against stale async responses.
func (m Model) expectedEventRange() (time.Time, time.Time) {
	switch m.viewMode {
	case viewDay:
		d := m.day.Cursor()
		return localSpanQueryRange(d, d.AddDate(0, 0, 1))
	case viewWeek:
		start := m.week.WeekStartDate()
		return localSpanQueryRange(start, start.AddDate(0, 0, 7))
	case viewAgenda:
		return localSpanQueryRange(m.agenda.WindowStart(), m.agenda.WindowEnd())
	default:
		anchor := calendarGridAnchor(m.calendar.Month(), m.calendar.WeekStart())
		return localSpanQueryRange(anchor, anchor.AddDate(0, 0, 42))
	}
}

// localSpanQueryRange returns the [from, to) UTC query range that covers
// the local calendar days [fromDay, toDay). The range is the union of two
// spans. The UTC-midnight span covers the all-day events. The database
// stores those as 00:00 UTC datestamps. The local-midnight span, converted
// to UTC, covers the timed events. The database stores those as UTC
// instants, so an evening event in a UTC-negative zone lands on the next
// UTC date. The UTC-midnight span alone misses that event (the day view
// then hides it while the wider views show it).
func localSpanQueryRange(fromDay, toDay time.Time) (time.Time, time.Time) {
	from := time.Date(fromDay.Year(), fromDay.Month(), fromDay.Day(), 0, 0, 0, 0, time.UTC)
	to := time.Date(toDay.Year(), toDay.Month(), toDay.Day(), 0, 0, 0, 0, time.UTC)
	localFrom := time.Date(fromDay.Year(), fromDay.Month(), fromDay.Day(), 0, 0, 0, 0, time.Local).UTC()
	localTo := time.Date(toDay.Year(), toDay.Month(), toDay.Day(), 0, 0, 0, 0, time.Local).UTC()
	if localFrom.Before(from) {
		from = localFrom
	}
	if localTo.After(to) {
		to = localTo
	}
	return from, to
}

// dispatchEditScope routes a deferred EventFormSaveMsg to the right service
// method based on the user's choice in the scope dialog. choice indices match
// the dialog order: 0=this event only, 1=this and following, 2=all events.
// Returns the message the agenda loop should consume next.
func (m Model) dispatchEditScope(editID int64, choice int, save EventFormSaveMsg) tea.Msg {
	ctx := context.Background()
	master, err := m.app.Events.Get(ctx, editID)
	if err != nil {
		return eventUpdateAfterScopeMsg{calendarID: save.CalendarID, err: err}
	}

	params := event.UpdateParams{
		CalendarID:     save.CalendarID,
		Title:          save.Title,
		Description:    save.Description,
		Location:       save.Location,
		ConferenceURI:  save.ConferenceURI,
		StartTime:      save.StartTime,
		EndTime:        save.EndTime,
		AllDay:         save.AllDay,
		RecurrenceRule: save.RecurrenceRule,
		Timezone:       save.Timezone,
		Transp:         save.Transp,
		Class:          save.Class,
		Categories:     save.Categories,
	}

	// Each branch writes the event row together with its attendees and alarms
	// in a single transaction. A failed child write then rolls the whole edit
	// back. It does not leave a half-updated row (issue #87).
	switch choice {
	case 0: // This event only
		_, err = m.app.Events.UpdateInstanceWithRelations(ctx, master.UID, save.InstanceTime, params, save.Attendees, save.Alarms)
	case 1: // This and following
		_, err = m.app.Events.UpdateFromInstanceWithRelations(ctx, master.UID, save.InstanceTime, params, save.Attendees, save.Alarms)
	default: // All events
		// The form opened showing the clicked instance's time, so save.StartTime
		// is relative to that occurrence — not the master's DTSTART. Apply the
		// delta to the master to shift the whole series, and preserve whatever
		// duration the user left on the form.
		delta := save.StartTime.Sub(save.InstanceTime)
		newDuration := save.EndTime.Sub(save.StartTime)
		params.StartTime = master.StartTime.Add(delta)
		params.EndTime = params.StartTime.Add(newDuration)
		_, err = m.app.Events.UpdateWithRelations(ctx, master.ID, params, save.Attendees, save.Alarms)
	}
	if err != nil {
		return eventUpdateAfterScopeMsg{calendarID: save.CalendarID, err: err}
	}
	return eventUpdateAfterScopeMsg{calendarID: save.CalendarID}
}

func (m Model) loadEvents() tea.Cmd {
	from, to := m.expectedEventRange()
	return m.queryEventsRange(from, to, false)
}

// loadEventsIncremental queries only the newly-added slice of an agenda
// expansion when the loaded range shares an edge with the new expected
// range. Infinite-scroll then stays O(1 step) in query cost even after the
// user has scrolled years back. It falls back to a full refresh when the
// ranges do not share an edge (for example after a cursor jump).
func (m Model) loadEventsIncremental() tea.Cmd {
	wantFrom, wantTo := m.expectedEventRange()
	if m.loadedFrom.IsZero() || m.loadedTo.IsZero() {
		return m.queryEventsRange(wantFrom, wantTo, false)
	}
	// Forward extension: near edge unchanged, far edge pushed later.
	if m.loadedFrom.Equal(wantFrom) && m.loadedTo.Before(wantTo) {
		return m.queryEventsRange(m.loadedTo, wantTo, true)
	}
	// Backward extension: far edge unchanged, near edge pushed earlier.
	if m.loadedTo.Equal(wantTo) && wantFrom.Before(m.loadedFrom) {
		return m.queryEventsRange(wantFrom, m.loadedFrom, true)
	}
	// No shared edge — full refresh.
	return m.queryEventsRange(wantFrom, wantTo, false)
}

// queryEventsRange runs the recurrence-expanded query for [from, to).
// It returns an eventsLoadedMsg tagged with the queried range. The tag
// also says whether the result is a merge (incremental) or a replacement
// (full refresh).
func (m Model) queryEventsRange(from, to time.Time, merge bool) tea.Cmd {
	return func() tea.Msg {
		expanded, err := m.app.Recurrences.ListExpandedEvents(context.Background(), from, to)
		events := make([]event.Event, len(expanded))
		for i, e := range expanded {
			evt := e.Event
			if !evt.EndTime.IsZero() {
				evt.EndTime = e.InstanceTime.Add(evt.EndTime.Sub(evt.StartTime))
			}
			evt.StartTime = e.InstanceTime
			events[i] = evt
		}
		return eventsLoadedMsg{from: from, to: to, merge: merge, events: events, err: err}
	}
}

// mergeEvents dedup-appends new events into the stored list. The dedup key is
// (ID, StartTime.UTC()). That key is unique for both non-recurring events and
// recurrence instances. Needed when a multi-day event straddles the
// incremental slice boundary and is returned by both queries.
func mergeEvents(existing, incoming []event.Event) []event.Event {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]event.Event, 0, len(existing)+len(incoming))
	add := func(e event.Event) {
		key := eventDedupKey(e)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	for _, e := range existing {
		add(e)
	}
	for _, e := range incoming {
		add(e)
	}
	return out
}

func eventDedupKey(e event.Event) string {
	return e.StartTime.UTC().Format(time.RFC3339) + "|" + fmt.Sprint(e.ID)
}

// loadTrash queries the trash aggregator across all visible calendars
// and hands the result to the trash model via trashLoadedMsg.
func (m Model) loadTrash() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var out []trash.Entry
		for id := range m.calendars {
			if m.hiddenCalendars[id] {
				continue
			}
			entries, err := m.app.Trash.List(ctx, id)
			if err != nil {
				return trashLoadedMsg{err: err}
			}
			out = append(out, entries...)
		}
		return trashLoadedMsg{entries: out}
	}
}

func eventsOn(events []event.Event, day time.Time) []event.Event {
	dayKey := day.Local().Format("2006-01-02")
	var out []event.Event
	for _, e := range events {
		// All-day events are stored as midnight UTC; compare in UTC
		// so negative-offset timezones don't shift the date.
		eKey := e.StartTime.Local().Format("2006-01-02")
		if e.AllDay {
			eKey = e.StartTime.UTC().Format("2006-01-02")
		}
		if eKey == dayKey {
			out = append(out, e)
		}
	}
	return out
}

func eventsToCalendar(events []event.Event, calendars map[int64]CalendarInfo, hidden map[int64]bool) []CalendarEvent {
	out := make([]CalendarEvent, 0, len(events))
	for _, e := range events {
		if hidden[e.CalendarID] {
			continue
		}
		color := calendars[e.CalendarID].Color
		for _, day := range eventCalendarDays(e) {
			start, end := clipEventToDay(e, day)
			out = append(out, CalendarEvent{
				ID:        e.ID,
				Title:     e.Title,
				AllDay:    e.AllDay,
				Day:       day,
				Color:     color,
				StartTime: start,
				EndTime:   end,
			})
		}
	}
	return out
}

// eventCalendarDays returns one entry for each local calendar day an event
// touches. All-day events use UTC (their StartTime is a datestamp at 00:00
// UTC, not a point in time). Timed events use local time.
func eventCalendarDays(e event.Event) []time.Time {
	if e.AllDay {
		s := e.StartTime.UTC()
		startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC)
		end := e.EndTime.UTC()
		var days []time.Time
		for d := startDay; d.Before(end); d = d.AddDate(0, 0, 1) {
			days = append(days, d)
		}
		if len(days) == 0 {
			days = []time.Time{startDay}
		}
		return days
	}
	s := e.StartTime.Local()
	end := e.EndTime.Local()
	startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, s.Location())
	if !end.After(s) {
		return []time.Time{startDay}
	}
	var days []time.Time
	for d := startDay; d.Before(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	if len(days) == 0 {
		days = []time.Time{startDay}
	}
	return days
}

// clipEventToDay returns the event's start and end times clipped to the
// given calendar day. For all-day events the times are the event's original
// values (views ignore them). For timed events that span midnight, the end
// of day 1 is pushed one second before midnight. The time-grid renderer
// then sees an in-day hour/minute (placeEvents reads only hour/minute).
func clipEventToDay(e event.Event, day time.Time) (time.Time, time.Time) {
	if e.AllDay {
		return e.StartTime, e.EndTime
	}
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	dayEnd := dayStart.AddDate(0, 0, 1)
	start := e.StartTime.Local()
	if start.Before(dayStart) {
		start = dayStart
	}
	end := e.EndTime.Local()
	if !end.Before(dayEnd) {
		end = dayEnd.Add(-time.Second)
	}
	return start, end
}

func filterVisibleEvents(events []event.Event, hidden map[int64]bool) []event.Event {
	if len(hidden) == 0 {
		return events
	}
	out := make([]event.Event, 0, len(events))
	for _, e := range events {
		if hidden[e.CalendarID] {
			continue
		}
		out = append(out, e)
	}
	return out
}
