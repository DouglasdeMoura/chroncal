package event

import (
	"testing"
	"time"
)

func TestEvent_Span(t *testing.T) {
	t.Parallel()
	e := Event{
		StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 15, 30, 0, 0, time.UTC),
	}
	got := e.Span()
	want := 90 * time.Minute
	if got != want {
		t.Errorf("Span() = %v, want %v", got, want)
	}
}

func TestEvent_IsRecurrenceOverride(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		recurrenceID string
		want         bool
	}{
		{"empty", "", false},
		{"set", "2026-04-01T10:00:00Z", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{RecurrenceID: tt.recurrenceID}
			if got := e.IsRecurrenceOverride(); got != tt.want {
				t.Errorf("IsRecurrenceOverride() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseTimeList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty", "", 0},
		{"single", "2026-04-01T10:00:00Z", 1},
		{"multiple", "2026-04-01T10:00:00Z,2026-04-02T10:00:00Z", 2},
		{"with spaces", " 2026-04-01T10:00:00Z , 2026-04-02T10:00:00Z ", 2},
		{"invalid entries skipped", "2026-04-01T10:00:00Z,invalid,2026-04-02T10:00:00Z", 2},
		{"all invalid", "bad,worse", 0},
		{"date-only", "2026-04-03", 1},
		{"mixed rfc3339 and date-only", "2026-04-01T10:00:00Z,2026-04-03", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTimeList(tt.input)
			if len(got) != tt.want {
				t.Errorf("ParseTimeList(%q) returned %d items, want %d", tt.input, len(got), tt.want)
			}
		})
	}
}

func TestParseCategoryList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "work", []string{"work"}},
		{"multiple", "work,personal", []string{"work", "personal"}},
		{"whitespace trimmed", " work , personal ", []string{"work", "personal"}},
		{"empty entries skipped", "work,,personal", []string{"work", "personal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCategoryList(tt.input)
			if tt.want == nil && got != nil {
				t.Errorf("ParseCategoryList(%q) = %v, want nil", tt.input, got)
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("ParseCategoryList(%q) returned %d items, want %d", tt.input, len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseCategoryList(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEvent_ParseExDates(t *testing.T) {
	t.Parallel()
	e := Event{ExDates: "2026-04-01T10:00:00Z,2026-04-02T10:00:00Z"}
	got := e.ParseExDates()
	if len(got) != 2 {
		t.Fatalf("ParseExDates() returned %d items, want 2", len(got))
	}
}

func TestParseTimeList_DateOnlyUsesLocal(t *testing.T) {
	t.Parallel()
	got := ParseTimeList("2026-04-03")
	if len(got) != 1 {
		t.Fatalf("ParseTimeList date-only returned %d items, want 1", len(got))
	}
	want := time.Date(2026, 4, 3, 0, 0, 0, 0, time.Local)
	if !got[0].Equal(want) {
		t.Errorf("ParseTimeList(\"2026-04-03\") = %v, want %v", got[0], want)
	}
	if got[0].Location() != time.Local {
		t.Errorf("ParseTimeList date-only location = %v, want time.Local", got[0].Location())
	}
}

func TestEvent_ParseRDates(t *testing.T) {
	t.Parallel()
	e := Event{RDates: ""}
	got := e.ParseRDates()
	if len(got) != 0 {
		t.Errorf("ParseRDates() on empty returned %d items, want 0", len(got))
	}
}

func TestSerializeTimeList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		times []time.Time
		want  string
	}{
		{"nil", nil, ""},
		{"empty", []time.Time{}, ""},
		{"single rfc3339", []time.Time{time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)}, "2026-04-01T10:00:00Z"},
		{"multiple rfc3339", []time.Time{
			time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		}, "2026-04-01T10:00:00Z,2026-04-02T10:00:00Z"},
		{"date-only", []time.Time{time.Date(2026, 4, 3, 0, 0, 0, 0, time.Local)}, "2026-04-03"},
		{"mixed", []time.Time{
			time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 3, 0, 0, 0, 0, time.Local),
		}, "2026-04-01T10:00:00Z,2026-04-03"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SerializeTimeList(tt.times)
			if got != tt.want {
				t.Errorf("SerializeTimeList() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSerializeTimeList_RoundTrip(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"2026-04-01T10:00:00Z,2026-04-02T10:00:00Z",
		"2026-04-03",
		"2026-04-01T10:00:00Z,2026-04-03",
		"",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			parsed := ParseTimeList(input)
			serialized := SerializeTimeList(parsed)
			if serialized != input {
				t.Errorf("round-trip: ParseTimeList(%q) → SerializeTimeList() = %q", input, serialized)
			}
		})
	}
}

func TestEvent_ParseCategories(t *testing.T) {
	t.Parallel()
	e := Event{Categories: "work,dev"}
	got := e.ParseCategories()
	if len(got) != 2 || got[0] != "work" || got[1] != "dev" {
		t.Errorf("ParseCategories() = %v, want [work dev]", got)
	}
}
