package main

import (
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/calendar"
)

func TestFindCalendarByRef(t *testing.T) {
	cals := []calendar.Calendar{
		{ID: 1, Name: "Personal"},
		{ID: 2, Name: "Work"},
	}

	tests := []struct {
		ref     string
		wantID  int64
		wantErr bool
	}{
		{ref: "1", wantID: 1},
		{ref: "Work", wantID: 2},
		{ref: "Missing", wantErr: true},
	}

	for _, tt := range tests {
		got, err := findCalendarByRef(cals, tt.ref)
		if (err != nil) != tt.wantErr {
			t.Fatalf("findCalendarByRef(%q) error = %v, wantErr %v", tt.ref, err, tt.wantErr)
		}
		if tt.wantErr {
			continue
		}
		if got.ID != tt.wantID {
			t.Fatalf("findCalendarByRef(%q) ID = %d, want %d", tt.ref, got.ID, tt.wantID)
		}
	}
}

// TestFindCalendarByRefErrorTaxonomy locks the error codes: an unknown
// reference (numeric or by name) is not_found, and an ambiguous name is
// invalid_input. Every caller then reports the same code without
// re-wrapping, so --output json consumers can dispatch on it.
func TestFindCalendarByRefErrorTaxonomy(t *testing.T) {
	cals := []calendar.Calendar{
		{ID: 1, Name: "Work"},
		{ID: 2, Name: "Work"},
		{ID: 3, Name: "Personal"},
	}

	for _, ref := range []string{"999", "Ghost"} {
		_, err := findCalendarByRef(cals, ref)
		var ce *cliError
		if !errors.As(err, &ce) || ce.Code != "not_found" {
			t.Fatalf("findCalendarByRef(%q) = %#v, want a not_found cliError", ref, err)
		}
	}

	_, err := findCalendarByRef(cals, "work")
	var ce *cliError
	if !errors.As(err, &ce) || ce.Code != "invalid_input" {
		t.Fatalf("findCalendarByRef(%q) = %#v, want an invalid_input cliError for an ambiguous name", "work", err)
	}
}

// TestFindCalendarByRefRejectsAmbiguousName proves that two calendars that
// share the same case-insensitive name are never resolved to the first
// match in silence. An exact match wins (calendar.FindByName, the rule
// that Apply shares). A name that matches several calendars in another
// case and none exactly must error, and the caller disambiguates with a
// numeric ID.
func TestFindCalendarByRefRejectsAmbiguousName(t *testing.T) {
	t.Parallel()
	cals := []calendar.Calendar{
		{ID: 1, Name: "Work"},
		{ID: 2, Name: "work"}, // case-insensitive duplicate
		{ID: 3, Name: "Personal"},
	}

	// The exact spelling resolves to its own row, never to the other one.
	for _, tc := range []struct {
		ref  string
		want int64
	}{{"Work", 1}, {"work", 2}} {
		got, err := findCalendarByRef(cals, tc.ref)
		if err != nil || got.ID != tc.want {
			t.Fatalf("findCalendarByRef(%q) = %v, %v; want ID %d", tc.ref, got.ID, err, tc.want)
		}
	}
	// No exact match and two case variants is ambiguous.
	if _, err := findCalendarByRef(cals, "WORK"); err == nil {
		t.Fatal("case-insensitive ambiguous name should be rejected")
	}

	// Unique name still resolves.
	got, err := findCalendarByRef(cals, "Personal")
	if err != nil {
		t.Fatalf("unique name: %v", err)
	}
	if got.ID != 3 {
		t.Fatalf("unique name ID = %d, want 3", got.ID)
	}

	// Numeric ID disambiguates even among ambiguous names.
	got, err = findCalendarByRef(cals, "2")
	if err != nil {
		t.Fatalf("numeric ID among ambiguous names: %v", err)
	}
	if got.ID != 2 {
		t.Fatalf("numeric ID = %d, want 2", got.ID)
	}
}
