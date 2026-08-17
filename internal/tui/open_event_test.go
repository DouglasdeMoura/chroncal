package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestApplyOccurrenceTimes_OverlaysGeneratedInstance(t *testing.T) {
	masterStart := time.Date(2026, 4, 3, 14, 0, 0, 0, time.UTC)
	stored := event.Event{
		ID:             7,
		Title:          "Weekly review",
		StartTime:      masterStart,
		EndTime:        masterStart.Add(time.Hour),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=FR",
	}
	instance := masterStart.AddDate(0, 0, 14)
	requested := event.Event{
		ID:        7,
		StartTime: instance,
		EndTime:   instance.Add(time.Hour),
	}

	got := applyOccurrenceTimes(stored, requested)
	if !got.StartTime.Equal(instance) {
		t.Fatalf("StartTime = %s, want %s", got.StartTime, instance)
	}
	if !got.EndTime.Equal(instance.Add(time.Hour)) {
		t.Fatalf("EndTime = %s, want %s", got.EndTime, instance.Add(time.Hour))
	}
}

func TestApplyOccurrenceTimes_LeavesOneOffUnchanged(t *testing.T) {
	start := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC)
	stored := event.Event{ID: 3, StartTime: start, EndTime: start.Add(30 * time.Minute)}
	requested := event.Event{ID: 3, StartTime: start.Add(24 * time.Hour)}

	got := applyOccurrenceTimes(stored, requested)
	if !got.StartTime.Equal(start) {
		t.Fatalf("one-off StartTime = %s, want stored %s", got.StartTime, start)
	}
}

func TestMatchOpenEvent_PrefersExactStartThenSameDay(t *testing.T) {
	day := time.Date(2026, 4, 17, 14, 0, 0, 0, time.Local)
	first := event.Event{ID: 7, StartTime: day, EndTime: day.Add(time.Hour)}
	second := event.Event{ID: 7, StartTime: day.AddDate(0, 0, 7), EndTime: day.AddDate(0, 0, 7).Add(time.Hour)}
	other := event.Event{ID: 8, StartTime: day, EndTime: day.Add(time.Hour)}
	events := []event.Event{other, first, second}

	exact := matchOpenEvent(events, event.Event{ID: 7, StartTime: second.StartTime})
	if !exact.StartTime.Equal(second.StartTime) {
		t.Fatalf("exact match StartTime = %s, want %s", exact.StartTime, second.StartTime)
	}

	sameDayMatch := matchOpenEvent(events, event.Event{ID: 7, StartTime: time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local)})
	if !sameDayMatch.StartTime.Equal(first.StartTime) {
		t.Fatalf("same-day match StartTime = %s, want %s", sameDayMatch.StartTime, first.StartTime)
	}
}

func TestWithOpenEvent_JumpsEveryViewToTheEventDay(t *testing.T) {
	m := NewModel(nil, "")
	day := time.Date(2026, 4, 21, 9, 0, 0, 0, time.Local)
	ev := event.Event{ID: 7, Title: "Standup", StartTime: day, EndTime: day.Add(30 * time.Minute)}

	m = m.WithOpenEvent(ev)

	if !sameDay(m.calendar.Cursor(), day) {
		t.Fatalf("month cursor = %s, want %s", m.calendar.Cursor(), day)
	}
	if !sameDay(m.day.Cursor(), day) {
		t.Fatalf("day cursor = %s, want %s", m.day.Cursor(), day)
	}
	if !sameDay(m.week.Cursor(), day) {
		t.Fatalf("week cursor = %s, want %s", m.week.Cursor(), day)
	}
	if !sameDay(m.agenda.Cursor(), day) {
		t.Fatalf("agenda cursor = %s, want %s", m.agenda.Cursor(), day)
	}
}

func TestEventsLoaded_OpensPendingEventAndSelectsAgendaRow(t *testing.T) {
	day := time.Date(2026, 4, 21, 9, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:         7,
		Title:      "Standup",
		StartTime:  day,
		EndTime:    day.Add(30 * time.Minute),
		CalendarID: 1,
	}
	m := NewModel(nil, "")
	m.viewMode = viewAgenda
	m = m.WithOpenEvent(ev)

	from, to := m.expectedEventRange()
	updated, cmd := m.Update(eventsLoadedMsg{
		from:   from,
		to:     to,
		events: []event.Event{ev},
	})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected EventViewRequestedMsg after loading the pending event")
	}
	if !batchEmits(cmd, func(msg tea.Msg) bool {
		req, ok := msg.(EventViewRequestedMsg)
		return ok && req.Event.ID == ev.ID
	}) {
		t.Fatal("loaded pending event did not emit EventViewRequestedMsg")
	}
	if got, ok := m.agenda.SelectedEvent(); !ok || got.ID != ev.ID {
		t.Fatalf("agenda selected = (%v, %v), want event %d", got, ok, ev.ID)
	}
}

func TestAgendaSelectEvent_SelectsMatchingOccurrence(t *testing.T) {
	day := time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local)
	first := event.Event{ID: 7, Title: "Weekly review", StartTime: time.Date(2026, 4, 17, 14, 0, 0, 0, time.Local), EndTime: time.Date(2026, 4, 17, 15, 0, 0, 0, time.Local)}
	second := event.Event{ID: 7, Title: "Weekly review", StartTime: time.Date(2026, 4, 24, 14, 0, 0, 0, time.Local), EndTime: time.Date(2026, 4, 24, 15, 0, 0, 0, time.Local)}
	m := NewAgendaModel(day).SetEvents([]event.Event{first, second}, nil)

	m = m.SelectEvent(7, second.StartTime)
	got, ok := m.SelectedEvent()
	if !ok || !got.StartTime.Equal(second.StartTime) {
		t.Fatalf("selected = (%v, %v), want second occurrence", got, ok)
	}
}
