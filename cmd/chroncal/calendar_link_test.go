package main

import (
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

// TestFindCalendarByRefRejectsAmbiguousName proves that two calendars sharing
// the same case-insensitive name are never silently resolved to the first
// match. The caller must disambiguate with a numeric ID.
func TestFindCalendarByRefRejectsAmbiguousName(t *testing.T) {
	t.Parallel()
	cals := []calendar.Calendar{
		{ID: 1, Name: "Work"},
		{ID: 2, Name: "work"}, // case-insensitive duplicate
		{ID: 3, Name: "Personal"},
	}

	// Ambiguous name must error, not return ID 1 (the first match).
	if _, err := findCalendarByRef(cals, "Work"); err == nil {
		t.Fatal("ambiguous calendar name should be rejected, not silently resolved to the first match")
	}
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
