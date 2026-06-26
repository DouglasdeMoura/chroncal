package main

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
)

// resolveCalendarID must accept a numeric calendar ID, matching the
// reference grammar of sibling commands (sync run/reset, calendar
// update/delete/set-default) which route through findCalendarByRef. Before
// the fix it only matched by name, so `--calendar 2` failed with
// `calendar "2" not found` even when calendar ID 2 existed. See issue #308.
func TestResolveCalendarIDAcceptsNumericID(t *testing.T) {
	dir := t.TempDir()
	a, err := app.New(filepath.Join(dir, "chroncal.db"))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	ctx := context.Background()
	cal, err := a.Calendars.Create(ctx, "Work", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	gotByID, err := resolveCalendarID(ctx, a, strconv.FormatInt(cal.ID, 10))
	if err != nil {
		t.Fatalf("resolveCalendarID by numeric ID: %v", err)
	}
	if gotByID != cal.ID {
		t.Fatalf("resolveCalendarID by ID = %d, want %d", gotByID, cal.ID)
	}

	gotByName, err := resolveCalendarID(ctx, a, "Work")
	if err != nil {
		t.Fatalf("resolveCalendarID by name: %v", err)
	}
	if gotByName != cal.ID {
		t.Fatalf("resolveCalendarID by name = %d, want %d", gotByName, cal.ID)
	}
}
