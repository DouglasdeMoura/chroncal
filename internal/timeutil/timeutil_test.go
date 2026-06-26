package timeutil

import (
	"reflect"
	"testing"
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
