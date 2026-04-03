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
