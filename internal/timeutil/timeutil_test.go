package timeutil

import (
	"reflect"
	"testing"
	"time"
)

func TestCategoryListRoundtrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cats []string
	}{
		{"plain", []string{"work", "meeting", "urgent"}},
		{"embedded comma", []string{"Foo, Bar", "Baz"}},
		{"embedded backslash", []string{`a\b`, "c"}},
		{"comma and backslash", []string{`x\, y`, "z"}},
		{"only comma value", []string{"a,b,c"}},
		{"empty", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseCategoryList(JoinCategoryList(tc.cats))
			if len(tc.cats) == 0 {
				if len(got) != 0 {
					t.Fatalf("got %v, want empty", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.cats) {
				t.Fatalf("round-trip = %v, want %v", got, tc.cats)
			}
		})
	}
}

func TestParseCategoryList_LegacyAndEdgeCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},      // trimmed
		{"a,,b", []string{"a", "b"}},         // empty segment dropped
		{`Foo\, Bar`, []string{"Foo, Bar"}},  // escaped comma kept
		{`a\\b`, []string{`a\b`}},            // escaped backslash decoded
		{`trailing\`, []string{`trailing\`}}, // lone trailing backslash preserved
	}
	for _, tc := range cases {
		got := ParseCategoryList(tc.in)
		if len(tc.want) == 0 {
			if len(got) != 0 {
				t.Errorf("ParseCategoryList(%q) = %v, want empty", tc.in, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseCategoryList(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestJoinCategoryList_EscapesAndDropsEmpty(t *testing.T) {
	t.Parallel()
	if got := JoinCategoryList([]string{"Foo, Bar", "Baz"}); got != `Foo\, Bar,Baz` {
		t.Errorf("JoinCategoryList = %q, want %q", got, `Foo\, Bar,Baz`)
	}
	if got := JoinCategoryList([]string{"a", "  ", "", "b"}); got != "a,b" {
		t.Errorf("JoinCategoryList dropped-empty = %q, want %q", got, "a,b")
	}
}

func TestRemoveTimeFromList(t *testing.T) {
	t.Parallel()
	mustParse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return v
	}
	keys := func(times []time.Time) []string {
		out := make([]string, len(times))
		for i, v := range times {
			out[i] = v.UTC().Format(time.RFC3339)
		}
		return out
	}
	cases := []struct {
		name   string
		list   []time.Time
		target time.Time
		want   []string
	}{
		{
			name:   "removes single match",
			list:   []time.Time{mustParse("2026-04-01T10:00:00Z"), mustParse("2026-04-02T10:00:00Z")},
			target: mustParse("2026-04-01T10:00:00Z"),
			want:   []string{"2026-04-02T10:00:00Z"},
		},
		{
			name:   "drops only the first of a duplicate pair",
			list:   []time.Time{mustParse("2026-04-01T10:00:00Z"), mustParse("2026-04-01T10:00:00Z")},
			target: mustParse("2026-04-01T10:00:00Z"),
			want:   []string{"2026-04-01T10:00:00Z"},
		},
		{
			name:   "matches across timezone offsets after UTC normalization",
			list:   []time.Time{mustParse("2026-04-01T12:00:00+02:00")},
			target: mustParse("2026-04-01T10:00:00Z"),
			want:   []string{},
		},
		{
			name:   "no match leaves list unchanged",
			list:   []time.Time{mustParse("2026-04-01T10:00:00Z")},
			target: mustParse("2026-04-02T10:00:00Z"),
			want:   []string{"2026-04-01T10:00:00Z"},
		},
		{
			name:   "empty list",
			list:   nil,
			target: mustParse("2026-04-01T10:00:00Z"),
			want:   []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := keys(RemoveTimeFromList(tc.list, tc.target))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RemoveTimeFromList = %v, want %v", got, tc.want)
			}
		})
	}
}

// The storage layout writes four year digits. Storable must reject a
// time the database cannot read back (issue #582 round 5).
func TestStorable(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want bool
	}{
		{"a normal time", time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC), true},
		{"the last storable year", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"the first year past the range", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"a year before the range", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Storable(tt.in); got != tt.want {
				t.Errorf("Storable(%s) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// The check must use the UTC year, because every stored time is UTC. A
// zone west of UTC can hold a 4-digit local year whose UTC form rolls
// into the next year.
func TestStorable_UsesTheUTCYear(t *testing.T) {
	loc := time.FixedZone("west", -12*3600)
	local := time.Date(9999, 12, 31, 20, 0, 0, 0, loc) // 10000-01-01T08:00Z
	if local.UTC().Year() != 10000 {
		t.Fatalf("fixture UTC year = %d, want 10000", local.UTC().Year())
	}
	if Storable(local) {
		t.Error("a local 4-digit year whose UTC form is 5-digit must not be storable")
	}
}
