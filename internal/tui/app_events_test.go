package tui

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestLoadTrash_NewestFirstAcrossCalendars checks that the merged trash of
// all calendars is newest-first. Each calendar gives a newest-first list,
// and the merge must keep that order across calendars (issue #879). The
// calendars come from a map, so the test loads the trash several times.
func TestLoadTrash_NewestFirstAcrossCalendars(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()

	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	// Each event goes to the trash at its own time. The calendars
	// alternate, so no calendar order gives the newest-first order.
	deletions := []struct {
		calendar string
		at       time.Time
	}{
		{"Home", base.Add(3 * time.Hour)},
		{"Work", base.Add(2 * time.Hour)},
		{"Home", base.Add(1 * time.Hour)},
		{"Work", base},
	}
	calendarIDs := map[string]int64{}
	for _, d := range deletions {
		if _, ok := calendarIDs[d.calendar]; ok {
			continue
		}
		cal, err := a.Calendars.Create(ctx, d.calendar, "", "")
		if err != nil {
			t.Fatalf("calendar create: %v", err)
		}
		calendarIDs[d.calendar] = cal.ID
	}
	for i, d := range deletions {
		ev, err := a.Events.Create(ctx, event.CreateParams{
			CalendarID: calendarIDs[d.calendar],
			Title:      d.calendar,
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

	for range 20 {
		msg, ok := m.loadTrash()().(trashLoadedMsg)
		if !ok || msg.err != nil {
			t.Fatalf("loadTrash = %#v, want the trash entries", msg)
		}
		if len(msg.entries) != len(deletions) {
			t.Fatalf("loadTrash gave %d entries, want %d", len(msg.entries), len(deletions))
		}
		for i, e := range msg.entries {
			if !e.DeletedAt.Equal(deletions[i].at) {
				t.Fatalf("entry %d deleted at %v (%s), want %v: the list is not newest-first",
					i, e.DeletedAt, e.Title, deletions[i].at)
			}
		}
	}
}
