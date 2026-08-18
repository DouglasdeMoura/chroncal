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
		// A large day count moves through AddDate and never touches the
		// time.Duration sum, so it stays exact (issue #582 round 2).
		{"large day count stays exact", "P200000D", base.AddDate(0, 0, 200000)},
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

		// Range check (issue #581): the hours, minutes, and seconds
		// total must not be more than maxSeconds (math.MaxInt64
		// nanoseconds, about 292 years).
		// PT5124096H wrapped to about +25 minutes before the check.
		{"hours wrap positive", "PT5124096H", true},
		// PT3000000H wrapped to a negative offset before the check.
		{"hours wrap negative", "PT3000000H", true},
		{"negative hours wrap", "-PT5124096H", true},
		{"seconds at the ceiling", "PT9223372036S", false},
		{"seconds above the ceiling", "PT9223372037S", true},
		{"hours just under the ceiling", "PT2562047H", false},
		{"hours just above the ceiling", "PT2562048H", true},
		// The time components pass alone but their sum crosses the
		// ceiling.
		{"time sum crosses the ceiling", "PT2562047H60M", true},
		// The day total has its own bound (maxDays). Days move through
		// AddDate, not the time.Duration sum, so the bound exists only
		// to keep the int conversion safe on a 32-bit build. Whether a
		// span carries a time past the storable range depends on the
		// start, so the callers own that check (issue #582 round 5).
		{"large day count", "P200000D", false},
		{"large week count", "P15251W", false},
		{"day count past the old storage cap", "P400000D", false},
		{"days at the day bound", "P2147483647D", false},
		{"days above the day bound", "P2147483648D", true},
		{"weeks at the day bound", "P306783378W", false},
		{"weeks above the day bound", "P306783379W", true},
		// Days above the bound with a valid time part still fail.
		{"day bound with time part", "P2147483648DT1H", true},
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

// A span (a VEVENT end, a VTODO duration) must be a well-formed,
// positive duration. Triggers stay on Validate, which accepts a sign.
func TestValidateSpan(t *testing.T) {
	tests := []struct {
		name    string
		dur     string
		wantErr bool
	}{
		{"positive time span", "PT1H30M", false},
		{"positive day span", "P200000D", false},
		{"negative span", "-PT1H", true},
		{"signed positive span", "+PT1H", false},
		{"zero span", "PT0S", true},
		{"zero day span", "P0D", true},
		{"malformed span", "5 minutes", true},
		{"empty span", "", true},
		{"span above the day bound", "P2147483648D", true},
		// A span past the old 1000-year cap is valid again. Whether it
		// is storable depends on the start (issue #582 round 5).
		{"span past the old storage cap", "P400000D", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSpan(tt.dur)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSpan(%q) error = %v, wantErr %v", tt.dur, err, tt.wantErr)
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
