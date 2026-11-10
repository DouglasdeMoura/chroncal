package sharefreebusy

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/freebusy"
)

func utc(year int, month time.Month, day int, hour int, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func TestInvertEmptyBusyReturnsFullWindow(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := utc(2026, 4, 10, 17, 0)

	got := Invert(nil, from, to)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !got[0].Start.Equal(from) || !got[0].End.Equal(to) {
		t.Fatalf("got %v/%v, want %v/%v", got[0].Start, got[0].End, from, to)
	}
	if got[0].Type != Free {
		t.Fatalf("type = %q, want %q", got[0].Type, Free)
	}
}

func TestInvertOneBusyReturnsTwoGaps(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := utc(2026, 4, 10, 17, 0)
	busy := []freebusy.Period{
		{Start: utc(2026, 4, 10, 12, 0), End: utc(2026, 4, 10, 13, 0), Type: freebusy.Busy},
	}

	got := Invert(busy, from, to)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if !got[0].Start.Equal(utc(2026, 4, 10, 9, 0)) || !got[0].End.Equal(utc(2026, 4, 10, 12, 0)) {
		t.Fatalf("gap 0 = %v/%v", got[0].Start, got[0].End)
	}
	if !got[1].Start.Equal(utc(2026, 4, 10, 13, 0)) || !got[1].End.Equal(utc(2026, 4, 10, 17, 0)) {
		t.Fatalf("gap 1 = %v/%v", got[1].Start, got[1].End)
	}
}

func TestInvertTwoBusyReturnsThreeGaps(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := utc(2026, 4, 10, 17, 0)
	busy := []freebusy.Period{
		{Start: utc(2026, 4, 10, 14, 0), End: utc(2026, 4, 10, 15, 0), Type: freebusy.Busy},
		{Start: utc(2026, 4, 10, 10, 0), End: utc(2026, 4, 10, 11, 0), Type: freebusy.Busy},
	}

	got := Invert(busy, from, to)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantStarts := []time.Time{utc(2026, 4, 10, 9, 0), utc(2026, 4, 10, 11, 0), utc(2026, 4, 10, 15, 0)}
	wantEnds := []time.Time{utc(2026, 4, 10, 10, 0), utc(2026, 4, 10, 14, 0), utc(2026, 4, 10, 17, 0)}
	for i := range wantStarts {
		if !got[i].Start.Equal(wantStarts[i]) || !got[i].End.Equal(wantEnds[i]) {
			t.Fatalf("gap %d = %v/%v, want %v/%v", i, got[i].Start, got[i].End, wantStarts[i], wantEnds[i])
		}
	}
}

func TestInvertOverlappingBusyMerges(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := utc(2026, 4, 10, 17, 0)
	busy := []freebusy.Period{
		{Start: utc(2026, 4, 10, 11, 0), End: utc(2026, 4, 10, 14, 0), Type: freebusy.Busy},
		{Start: utc(2026, 4, 10, 10, 0), End: utc(2026, 4, 10, 12, 0), Type: freebusy.Busy},
	}

	got := Invert(busy, from, to)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if !got[0].Start.Equal(utc(2026, 4, 10, 9, 0)) || !got[0].End.Equal(utc(2026, 4, 10, 10, 0)) {
		t.Fatalf("gap 0 = %v/%v", got[0].Start, got[0].End)
	}
	if !got[1].Start.Equal(utc(2026, 4, 10, 14, 0)) || !got[1].End.Equal(utc(2026, 4, 10, 17, 0)) {
		t.Fatalf("gap 1 = %v/%v", got[1].Start, got[1].End)
	}
}

func TestCapRangeClampsBeyond31Days(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := from.AddDate(0, 1, 10)

	gotFrom, gotTo, capped := CapRange(from, to)
	if !capped {
		t.Fatal("capped = false, want true")
	}
	if !gotFrom.Equal(from) {
		t.Fatalf("from = %v, want %v", gotFrom, from)
	}
	wantTo := from.AddDate(0, 0, 31)
	if !gotTo.Equal(wantTo) {
		t.Fatalf("to = %v, want %v", gotTo, wantTo)
	}
}

func TestCapRangeWithinLimitKeepsWindow(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := from.AddDate(0, 0, 10)

	gotFrom, gotTo, capped := CapRange(from, to)
	if capped {
		t.Fatal("capped = true, want false")
	}
	if !gotFrom.Equal(from) || !gotTo.Equal(to) {
		t.Fatalf("window = %v/%v, want %v/%v", gotFrom, gotTo, from, to)
	}
}

func TestCapRangeBadRangeKeepsFalse(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := utc(2026, 4, 10, 9, 0)

	_, _, capped := CapRange(from, to)
	if capped {
		t.Fatal("capped = true, want false")
	}

	_, _, capped = CapRange(to, from)
	if capped {
		t.Fatal("capped = true, want false for reverse")
	}
}

func TestInvertNilRangeReturnsNil(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	busy := []freebusy.Period{
		{Start: utc(2026, 4, 10, 10, 0), End: utc(2026, 4, 10, 11, 0), Type: freebusy.Busy},
	}

	if got := Invert(busy, from, from); got != nil {
		t.Fatalf("equal range = %v, want nil", got)
	}
	if got := Invert(busy, from, from.Add(-time.Hour)); got != nil {
		t.Fatalf("reverse range = %v, want nil", got)
	}
}

func TestFormatTextEmpty(t *testing.T) {
	t.Parallel()

	if got := FormatText(nil); got != "no free periods\n" {
		t.Fatalf("got %q, want empty text", got)
	}
	if got := FormatText([]freebusy.Period{}); got != "no free periods\n" {
		t.Fatalf("got %q, want empty text", got)
	}
}

func TestFormatTextNonEmpty(t *testing.T) {
	t.Parallel()

	free := []freebusy.Period{
		{Start: utc(2026, 4, 10, 9, 0), End: utc(2026, 4, 10, 12, 0), Type: Free},
		{Start: utc(2026, 4, 10, 13, 0), End: utc(2026, 4, 10, 17, 0), Type: Free},
	}

	got := FormatText(free)
	want := "2026-04-10T09:00:00Z 2026-04-10T12:00:00Z\n" +
		"2026-04-10T13:00:00Z 2026-04-10T17:00:00Z\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExportFreeRoundTripThroughParse(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := utc(2026, 4, 10, 17, 0)
	busy := []freebusy.Period{
		{Start: utc(2026, 4, 10, 12, 0), End: utc(2026, 4, 10, 13, 0), Type: freebusy.Busy},
	}

	data, err := ExportFree(busy, from, to, "mailto:owner@example.com")
	if err != nil {
		t.Fatalf("ExportFree: %v", err)
	}

	text := string(data)
	if !strings.Contains(text, "FBTYPE=FREE") {
		t.Fatalf("text lacks free type:\n%s", text)
	}
	if !strings.Contains(text, "ORGANIZER:mailto:owner@example.com") {
		t.Fatalf("text lacks organizer:\n%s", text)
	}
	if strings.Contains(text, "URL:") {
		t.Fatalf("text holds URL:\n%s", text)
	}

	results, err := freebusy.ParseCalendar(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ParseCalendar: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	result := results[0]
	if !result.Start.Equal(from) || !result.End.Equal(to) {
		t.Fatalf("window = %v/%v, want %v/%v", result.Start, result.End, from, to)
	}
	if result.Organizer != "mailto:owner@example.com" {
		t.Fatalf("organizer = %q", result.Organizer)
	}
	free := Invert(busy, from, to)
	if len(result.Periods) != len(free) {
		t.Fatalf("periods = %d, want %d", len(result.Periods), len(free))
	}
	for i := range free {
		if !result.Periods[i].Start.Equal(free[i].Start) || !result.Periods[i].End.Equal(free[i].End) {
			t.Fatalf("period %d = %v/%v, want %v/%v", i, result.Periods[i].Start, result.Periods[i].End, free[i].Start, free[i].End)
		}
	}
}

func TestExportFreeNoOrganizerWhenEmpty(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)
	to := utc(2026, 4, 10, 17, 0)

	data, err := ExportFree(nil, from, to, "")
	if err != nil {
		t.Fatalf("ExportFree: %v", err)
	}
	if strings.Contains(string(data), "ORGANIZER:") {
		t.Fatalf("text holds organizer:\n%s", data)
	}

	results, err := freebusy.ParseCalendar(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ParseCalendar: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if len(results[0].Periods) != 1 {
		t.Fatalf("periods = %d, want 1", len(results[0].Periods))
	}
	if !results[0].Periods[0].Start.Equal(from) || !results[0].Periods[0].End.Equal(to) {
		t.Fatalf("period = %v/%v, want %v/%v", results[0].Periods[0].Start, results[0].Periods[0].End, from, to)
	}
}

func TestExportFreeRejectsBadRange(t *testing.T) {
	t.Parallel()

	from := utc(2026, 4, 10, 9, 0)

	if _, err := ExportFree(nil, from, from, ""); err == nil {
		t.Fatal("equal range error = nil, want error")
	}
	if _, err := ExportFree(nil, from, from.Add(-time.Hour), ""); err == nil {
		t.Fatal("reverse range error = nil, want error")
	}
}
