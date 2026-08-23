package ical

import "testing"

func TestParseUTCOffsetRange(t *testing.T) {
	valid := map[string]int{
		"+0530":   5*3600 + 30*60,
		"-0800":   -8 * 3600,
		"+0000":   0,
		"+2359":   23*3600 + 59*60,
		"-233059": -(23*3600 + 30*60 + 59),
	}
	for in, want := range valid {
		got, err := parseUTCOffset(in)
		if err != nil || got != want {
			t.Errorf("parseUTCOffset(%q) = %d, %v; want %d, nil", in, got, err, want)
		}
	}

	invalid := []string{"+9930", "+0599", "-006099", "+2400"}
	for _, in := range invalid {
		if got, err := parseUTCOffset(in); err == nil {
			t.Errorf("parseUTCOffset(%q) = %d, nil; want out-of-range error", in, got)
		}
	}
}
