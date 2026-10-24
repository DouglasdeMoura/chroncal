package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

// TestDateOnlyTUIReaders_EveryZone guards issue #875 in the TUI. The
// palette row, the open-event jump, and the trash detail pane name the
// date of a date-only value in every zone.
func TestDateOnlyTUIReaders_EveryZone(t *testing.T) {
	testutil.ForEachLocalZone(t, func(t *testing.T) {
		holiday := event.Event{
			ID: 7, Title: "Holiday", AllDay: true,
			StartTime: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		}
		date := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local)

		want := "[Event] Oct 28"
		if time.Now().Year() != 2026 {
			want += " 2026"
		}
		if got := paletteEventDate(holiday); got != want {
			t.Fatalf("palette date = %q, want %q", got, want)
		}

		m := NewModel(nil, "").WithOpenEvent(holiday)
		for name, cursor := range map[string]time.Time{
			"month": m.calendar.Cursor(), "day": m.day.Cursor(),
			"week": m.week.Cursor(), "agenda": m.agenda.Cursor(),
		} {
			if !sameDay(cursor, date) {
				t.Fatalf("%s cursor = %s, want the day %s", name, cursor, date.Format(time.DateOnly))
			}
		}

		todoEntry := trash.Entry{Kind: trash.KindTodo, Title: "Water plants",
			DueDate: todo.Todo{DueDate: "2026-10-28"}.LocalDue(), DateOnly: true}
		text := ansi.Strip(strings.Join(trashDetailLines(todoEntry, CalendarInfo{}, 120, 10, nil), "\n"))
		if !strings.Contains(text, "Wed, Oct 28") || strings.Contains(text, "Oct 27") {
			t.Fatalf("todo trash detail:\n%s\nwant the due date Wed, Oct 28", text)
		}
	})
}
