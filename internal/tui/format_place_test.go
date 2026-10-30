package tui

import (
	"testing"
	"time"
)

func TestPlaceEventsRoundsToNearestSlot(t *testing.T) {
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.Local)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 30, h, m, 0, 0, time.Local) }
	lph := defaultLinesPerHour

	tests := []struct {
		name               string
		start, end         time.Time
		wantStart, wantEnd int
	}{
		{"on the slot", at(14, 30), at(15, 0), 14*lph + 2, 15 * lph},
		// 15:40 is nearer to 15:45 than to 15:30.
		{"late in the slot", at(15, 40), at(16, 20), 15*lph + 3, 16*lph + 1},
		{"early in the slot", at(15, 5), at(15, 50), 15 * lph, 15*lph + 3},
		// The start rounds to 24:00. The event keeps the last row.
		{"end of the day", at(23, 55), time.Time{}, totalHours*lph - 1, totalHours * lph},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := CalendarEvent{ID: 1, Title: "x", Day: day, StartTime: tt.start, EndTime: tt.end}
			placed := placeDayEvents([]CalendarEvent{ev}, day, lph)
			if len(placed) != 1 {
				t.Fatalf("placed %d events, want 1", len(placed))
			}
			if placed[0].startRow != tt.wantStart || placed[0].endRow != tt.wantEnd {
				t.Errorf("rows = [%d, %d), want [%d, %d)",
					placed[0].startRow, placed[0].endRow, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestTimeRowMatchesEventStart(t *testing.T) {
	// The now line and an event that starts now share one row.
	now := time.Date(2026, 10, 30, 15, 40, 0, 0, time.Local)
	ev := CalendarEvent{ID: 1, Title: "x", Day: now, StartTime: now, EndTime: now.Add(40 * time.Minute)}
	placed := placeDayEvents([]CalendarEvent{ev}, now, defaultLinesPerHour)
	if got := nowRowFor(now, defaultLinesPerHour); got != placed[0].startRow {
		t.Errorf("now row = %d, event start row = %d", got, placed[0].startRow)
	}
}
