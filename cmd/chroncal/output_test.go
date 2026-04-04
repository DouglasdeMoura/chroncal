package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestPrintEvent_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printEvent(&buf, event.Event{
		Title:       "Title\x1b]52;c;stolen\a",
		Location:    "Room\r\nB",
		Description: "Notes\x1b[31m",
		StartTime:   time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 4, 13, 0, 0, 0, time.UTC),
		Status:      "CONFIRMED",
	})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("printEvent output contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("printEvent output contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;stolen") {
		t.Fatalf("printEvent output contains OSC payload: %q", out)
	}
}

func TestPrintTable_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := printTable(&buf, []jsonEvent{{
		ID:          1,
		UID:         "uid-1",
		CalendarID:  1,
		Title:       "Bad\x1b[31mTitle",
		Description: "Desc\r\nInjected",
		Location:    "Loc\x1b]52;c;clip\a",
		StartTime:   time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		EndTime:     time.Date(2026, 4, 4, 13, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Status:      "CONFIRMED",
		Transp:      "OPAQUE",
		Class:       "PUBLIC",
		CreatedAt:   time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
		UpdatedAt:   time.Date(2026, 4, 4, 11, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}})
	if err != nil {
		t.Fatalf("printTable: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("printTable output contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("printTable output contains carriage return: %q", out)
	}
}
