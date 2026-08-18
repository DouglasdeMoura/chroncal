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
		{"empty returns zero", "", time.Time{}},
		// A total above maxSeconds wrapped int64 nanoseconds before the
		// range check (issue #581). Add now returns the zero time.
		{"overflow returns zero", "PT5124096H", time.Time{}},
		{"negative overflow returns zero", "PT3000000H", time.Time{}},
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

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		dur     string
		wantErr bool
	}{
		// Valid durations
		{"15 min before", "-PT15M", false},
		{"1 hour before", "-PT1H", false},
		{"1 day before", "-P1D", false},
		{"1 week before", "-P1W", false},
		{"positive prefix", "+PT10M", false},
		{"no sign", "PT30M", false},
		{"complex", "-P1DT2H30M45S", false},
		{"just seconds", "PT0S", false},
		{"day only", "P1D", false},
		{"day and time", "P1DT1H", false},
		{"hours and minutes", "-PT1H30M", false},

		// Invalid durations
		{"empty", "", true},
		{"garbage", "garbage", true},
		{"just P", "P", true},
		{"just minus P", "-P", true},
		{"trailing colon", "-PT15M:junk", true},
		{"colon after trigger", "-PT15M:user@test.com", true},
		{"multiple colons", "-PT30M:::3:PT5M:END", true},
		{"garbage after P", "-PXYZ", true},
		{"trailing X", "-PT15MX", true},
		{"W with other components", "-P1W2D", true},
		{"T without time component", "-P1DT", true},
		{"H without number", "-PTH", true},
		{"M without number", "-PTM", true},
		{"S without number", "-PTS", true},

		// Per-component signs are forbidden by RFC 5545; only the
		// whole-duration may carry a single leading sign.
		{"signed hours", "PT-1H", true},
		{"signed days", "P-1D", true},
		{"signed weeks", "P-1W", true},
		{"signed minutes after prefix", "-PT-15M", true},
		{"signed seconds", "PT-30S", true},
		{"plus signed minutes", "PT+15M", true},
		{"signed trailing component", "PT1H-30M", true},

		// Range check (issue #581): the total must not be more than
		// maxSeconds (math.MaxInt64 nanoseconds, about 292 years).
		// PT5124096H wrapped to about +25 minutes before the check.
		{"hours wrap positive", "PT5124096H", true},
		// PT3000000H wrapped to a negative offset before the check.
		{"hours wrap negative", "PT3000000H", true},
		{"negative hours wrap", "-PT5124096H", true},
		{"seconds at the ceiling", "PT9223372036S", false},
		{"seconds above the ceiling", "PT9223372037S", true},
		{"hours just under the ceiling", "PT2562047H", false},
		{"hours just above the ceiling", "PT2562048H", true},
		{"weeks just under the ceiling", "P15250W", false},
		{"huge weeks", "P15251W", true},
		{"days just under the ceiling", "P106751D", false},
		{"huge days", "P106752D", true},
		// The components pass alone but the sum crosses the ceiling.
		{"component sum crosses the ceiling", "P106751DT24H", true},
		// A component too large for int64 fails in strconv, not in the
		// range check. It must still report an error.
		{"component beyond int64", "PT99999999999999999999H", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.dur)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.dur, err, tt.wantErr)
			}
		})
	}
}

func TestFromGo(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "PT0S"},
		{"15 minutes", 15 * time.Minute, "PT15M"},
		{"negative 15 minutes", -15 * time.Minute, "-PT15M"},
		{"hour and a half", 90 * time.Minute, "PT1H30M"},
		{"90 seconds", 90 * time.Second, "PT1M30S"},
		// Whole days factor into the date form so nominal multi-day
		// spans round-trip through Add without DST drift.
		{"two days", 48 * time.Hour, "P2D"},
		{"day and two hours", 26 * time.Hour, "P1DT2H"},
		{"negative two days", -48 * time.Hour, "-P2D"},
		// Exact whole weeks use the mutually-exclusive week form.
		{"one week", 7 * 24 * time.Hour, "P1W"},
		{"two weeks", 14 * 24 * time.Hour, "P2W"},
		// Eight days is not a whole week: date form, not week form.
		{"eight days", 8 * 24 * time.Hour, "P8D"},
		// Sub-second input truncates to whole seconds (documented).
		{"sub-second truncates", 1500 * time.Millisecond, "PT1S"},
		{"under one second", 500 * time.Millisecond, "PT0S"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromGo(tt.d)
			if got != tt.want {
				t.Errorf("FromGo(%v) = %q, want %q", tt.d, got, tt.want)
			}
			// Round-trip: the emitted string must validate.
			if err := Validate(got); err != nil {
				t.Errorf("Validate(FromGo(%v)=%q) = %v, want nil", tt.d, got, err)
			}
		})
	}
}
