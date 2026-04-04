package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
)

func TestWriteAlarmCheckLine_SanitizesControlSequences(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeAlarmCheckLine(&buf, time.Date(2026, 4, 4, 9, 30, 0, 0, time.UTC), "DISPLAY", "Bad\x1b]52;c;clip\a\r\nTitle", false)

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("alarm line contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("alarm line contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("alarm line contains OSC payload: %q", out)
	}
	if !strings.Contains(out, "Bad Title") {
		t.Fatalf("alarm line did not contain sanitized title: %q", out)
	}
}

func TestPrintDiscoveredCalendars_SanitizesRemoteMetadata(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDiscoveredCalendars(&buf, "work", []caldav.RemoteCalendar{{
		Name:                  "Cal\x1b]52;c;clip\a",
		Path:                  "/dav/\x1b[31mwork",
		Description:           "Line\r\nbreak",
		SupportedComponentSet: []string{"VEVENT", "VTODO"},
	}})

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("discover output contains escape sequence: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("discover output contains carriage return: %q", out)
	}
	if strings.Contains(out, "]52;c;clip") {
		t.Fatalf("discover output contains OSC payload: %q", out)
	}
	if !strings.Contains(out, "Cal") || !strings.Contains(out, "/dav/work") || !strings.Contains(out, "Line break") {
		t.Fatalf("discover output did not contain sanitized fields: %q", out)
	}
}
