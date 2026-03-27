package duration

import (
	"testing"
	"time"
)

func TestAdd(t *testing.T) {
	base := time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		dur  string
		want time.Time
	}{
		{"15 min before", "-PT15M", time.Date(2026, 4, 1, 13, 45, 0, 0, time.UTC)},
		{"1 hour before", "-PT1H", time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC)},
		{"1 day before", "-P1D", time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC)},
		{"1 week before", "-P1W", time.Date(2026, 3, 25, 14, 0, 0, 0, time.UTC)},
		{"30 min after", "PT30M", time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC)},
		{"1 day 2 hours", "P1DT2H", time.Date(2026, 4, 2, 16, 0, 0, 0, time.UTC)},
		{"positive prefix", "+PT10M", time.Date(2026, 4, 1, 14, 10, 0, 0, time.UTC)},
		{"hours minutes seconds", "-PT1H30M45S", time.Date(2026, 4, 1, 12, 29, 15, 0, time.UTC)},
		{"empty defaults to 1h", "", time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Add(base, tt.dur)
			if !got.Equal(tt.want) {
				t.Errorf("Add(%v, %q) = %v, want %v", base, tt.dur, got, tt.want)
			}
		})
	}
}
