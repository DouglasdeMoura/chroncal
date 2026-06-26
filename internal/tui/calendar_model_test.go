package tui

import (
	"testing"
	"time"
)

// TestCalendarMonthNavClampsDay verifies that prev/next-month navigation from a
// day that does not exist in the target month clamps to the last valid day
// rather than rolling forward (Go's AddDate normalizes 2026-02-31 -> 2026-03-03).
func TestCalendarMonthNavClampsDay(t *testing.T) {
	tests := []struct {
		name      string
		cursor    time.Time
		key       string
		wantYear  int
		wantMonth time.Month
		wantDay   int
	}{
		{
			name:      "next month from Jan 31 lands in February",
			cursor:    time.Date(2026, 1, 31, 12, 0, 0, 0, time.Local),
			key:       "]",
			wantYear:  2026,
			wantMonth: time.February,
			wantDay:   28,
		},
		{
			name:      "prev month from Mar 31 lands in February",
			cursor:    time.Date(2026, 3, 31, 12, 0, 0, 0, time.Local),
			key:       "[",
			wantYear:  2026,
			wantMonth: time.February,
			wantDay:   28,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewCalendarModel(tc.cursor)
			m, _ = m.Update(keyPressMsg(tc.key))

			cur := m.Cursor()
			if cur.Year() != tc.wantYear || cur.Month() != tc.wantMonth || cur.Day() != tc.wantDay {
				t.Errorf("cursor = %04d-%02d-%02d, want %04d-%02d-%02d",
					cur.Year(), cur.Month(), cur.Day(), tc.wantYear, tc.wantMonth, tc.wantDay)
			}
			if mo := m.Month(); mo.Year() != tc.wantYear || mo.Month() != tc.wantMonth {
				t.Errorf("displayed month = %04d-%02d, want %04d-%02d",
					mo.Year(), mo.Month(), tc.wantYear, tc.wantMonth)
			}
		})
	}
}
