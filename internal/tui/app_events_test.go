package tui

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestLoadTrash_NewestFirstAcrossCalendars checks that the merged trash of
// all calendars is newest-first. Each calendar gives a newest-first list,
// and the merge must keep that order across calendars (issue #879). Two
// entries with the same deletion time sort by the higher ID first. A hidden
// calendar gives no entries.
func TestLoadTrash_NewestFirstAcrossCalendars(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()

	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	// Each event goes to the trash at its own time. The calendars
	// alternate, so no calendar order gives the newest-first order. The
	// last two share a deletion time.
	deletions := []struct {
		title, calendar string
		at              time.Time
	}{
		{"home-3h", "Home", base.Add(3 * time.Hour)},
		{"work-2h", "Work", base.Add(2 * time.Hour)},
		{"home-1h", "Home", base.Add(1 * time.Hour)},
		{"work-0h", "Work", base},
		{"hidden-4h", "Hidden", base.Add(4 * time.Hour)},
		{"work-tie", "Work", base.Add(5 * time.Hour)},
		{"home-tie", "Home", base.Add(5 * time.Hour)},
	}
	want := []string{"home-tie", "work-tie", "home-3h", "work-2h", "home-1h", "work-0h"}
	calendarIDs := map[string]int64{}
	for i, d := range deletions {
		if _, ok := calendarIDs[d.calendar]; !ok {
			cal, err := a.Calendars.Create(ctx, d.calendar, "", "")
			if err != nil {
				t.Fatalf("calendar create: %v", err)
			}
			calendarIDs[d.calendar] = cal.ID
		}
		ev, err := a.Events.Create(ctx, event.CreateParams{
			CalendarID: calendarIDs[d.calendar],
			Title:      d.title,
			StartTime:  base.AddDate(0, 0, i),
			EndTime:    base.AddDate(0, 0, i).Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("event create: %v", err)
		}
		if err := a.Events.Delete(ctx, ev.ID); err != nil {
			t.Fatalf("event delete: %v", err)
		}
		if _, err := a.DB.ExecContext(ctx, `UPDATE events SET deleted_at = ? WHERE id = ?`,
			d.at.Format(time.RFC3339), ev.ID); err != nil {
			t.Fatalf("set deleted_at: %v", err)
		}
	}
	m, _ = step(t, m, m.loadCalendars()())
	m.hiddenCalendars[calendarIDs["Hidden"]] = true

	msg, ok := m.loadTrash()().(trashLoadedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("loadTrash = %#v, want the trash entries", msg)
	}
	got := make([]string, len(msg.entries))
	for i, e := range msg.entries {
		got[i] = e.Title
	}
	if !slices.Equal(got, want) {
		t.Errorf("loadTrash order = %v, want %v", got, want)
	}
}
