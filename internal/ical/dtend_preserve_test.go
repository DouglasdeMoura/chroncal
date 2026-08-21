package ical

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// exchangeDTENDIcal is the shape Exchange emits: DTSTART parses, DTEND
// carries a TZID no tz database resolves. The event must still appear
// locally, and the remote path must keep the original string.
const exchangeDTENDIcal = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:dtend-preserve@example.com
DTSTAMP:20260101T000000Z
DTSTART:20260101T150000Z
DTEND;TZID=Customized Time Zone:20260101T163000
SUMMARY:Exchange meeting
END:VEVENT
END:VCALENDAR
`

func importOneEvent(t *testing.T, data string) event.Event {
	t.Helper()
	result, err := ImportFileRemote(strings.NewReader(data))
	if err != nil {
		t.Fatalf("ImportFileRemote: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	return result.Events[0]
}

func TestImportRemotePreservesUnparseableDTEND(t *testing.T) {
	t.Parallel()
	e := importOneEvent(t, exchangeDTENDIcal)

	if !e.EndTime.After(e.StartTime) {
		t.Fatalf("fabricated span missing: start=%v end=%v", e.StartTime, e.EndTime)
	}
	var preserved *model.XProperty
	for i := range e.XProperties {
		if e.XProperties[i].Name == xpropOriginalDTEND {
			preserved = &e.XProperties[i]
		}
	}
	if preserved == nil {
		t.Fatalf("no %s x-property in %+v", xpropOriginalDTEND, e.XProperties)
	}
	if preserved.Value != "20260101T163000" {
		t.Errorf("preserved value = %q, want the raw server string", preserved.Value)
	}
	if !strings.Contains(preserved.Params, "TZID") || !strings.Contains(preserved.Params, "Customized Time Zone") {
		t.Errorf("preserved params = %q, want the TZID param", preserved.Params)
	}
}

func TestImportFileDoesNotPreserveDTEND(t *testing.T) {
	t.Parallel()
	result, err := ImportFile(strings.NewReader(exchangeDTENDIcal))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(result.Events))
	}
	e := result.Events[0]
	for _, xp := range e.XProperties {
		if xp.Name == xpropOriginalDTEND {
			t.Errorf("file import must not set %s", xpropOriginalDTEND)
		}
	}
}

func TestExportPrefersPreservedDTEND(t *testing.T) {
	t.Parallel()
	e := importOneEvent(t, exchangeDTENDIcal)
	data, err := ExportEvents([]event.Event{e}, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "DTEND;TZID=Customized Time Zone:20260101T163000") {
		t.Errorf("export lost the server's original DTEND:\n%s", out)
	}
	if strings.Contains(out, xpropOriginalDTEND) {
		t.Errorf("export leaked the preservation slot as an x-property:\n%s", out)
	}
}

// A normal event keeps its computed DTEND: the preservation slot only fires
// when import stored it.
func TestExportNormalEventKeepsComputedDTEND(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "normal-dtend@example.com",
		Title:     "Normal",
		Timezone:  "America/New_York",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	}}
	data, err := ExportEvents(events, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	out := string(data)
	if strings.Contains(out, xpropOriginalDTEND) {
		t.Errorf("normal event carries the preservation slot:\n%s", out)
	}
	if !strings.Contains(out, "DTEND") {
		t.Errorf("normal event lost its DTEND:\n%s", out)
	}
}

// A local edit that changes the span must clear the preservation slot.
// Otherwise the next export emits the stale server DTEND and drops the
// user's new end time (issue #649). An edit that keeps the span keeps the
// slot.
func TestLocalSpanEditClearsPreservedDTEND(t *testing.T) {
	t.Parallel()

	db, q := testutil.NewTestDB(t)
	ctx := context.Background()
	calSvc := calendar.NewService(db, q)
	eventSvc := event.NewService(db, q)

	cals, err := calSvc.List(ctx)
	if err != nil {
		t.Fatalf("List calendars: %v", err)
	}
	calID := cals[0].ID

	// Persist a remote import the way the sync engine does: the row first,
	// then the x-properties.
	imported := importOneEvent(t, exchangeDTENDIcal)
	saved, err := eventSvc.UpsertByUID(ctx, event.UpsertParams{
		UID: imported.UID, CalendarID: calID,
		Title: imported.Title, StartTime: imported.StartTime, EndTime: imported.EndTime,
		Timezone: imported.Timezone, DtStamp: imported.DtStamp,
	})
	if err != nil {
		t.Fatalf("UpsertByUID: %v", err)
	}
	if err := eventSvc.ReplaceXProperties(ctx, saved.ID, imported.XProperties); err != nil {
		t.Fatalf("ReplaceXProperties: %v", err)
	}

	hasSlot := func() bool {
		xps, err := eventSvc.ListXProperties(ctx, saved.ID)
		if err != nil {
			t.Fatalf("ListXProperties: %v", err)
		}
		for _, xp := range xps {
			if xp.Name == xpropOriginalDTEND {
				return true
			}
		}
		return false
	}
	if !hasSlot() {
		t.Fatal("setup lost the preservation slot")
	}

	// An edit that keeps the span keeps the slot.
	edit := event.UpdateParams{
		CalendarID: calID,
		Title:      "Exchange meeting, renamed",
		StartTime:  imported.StartTime,
		EndTime:    imported.EndTime,
		Timezone:   imported.Timezone,
	}
	if _, err := eventSvc.Update(ctx, saved.ID, edit); err != nil {
		t.Fatalf("span-preserving update: %v", err)
	}
	if !hasSlot() {
		t.Fatal("a span-preserving edit cleared the slot")
	}

	// A local edit that moves the end time clears the slot.
	edit.EndTime = imported.EndTime.Add(time.Hour)
	edited, err := eventSvc.Update(ctx, saved.ID, edit)
	if err != nil {
		t.Fatalf("end-time edit: %v", err)
	}
	if hasSlot() {
		t.Fatal("the span edit kept the preservation slot")
	}

	// Export emits the edited end time, not the stale server string.
	if err := eventSvc.Hydrate(ctx, &edited); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	data, err := ExportEvents([]event.Event{edited}, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	out := string(data)
	want := "DTEND:" + edited.EndTime.UTC().Format("20060102T150405Z")
	if !strings.Contains(out, want) {
		t.Errorf("export lost the edited DTEND %q:\n%s", want, out)
	}
	if strings.Contains(out, "20260101T163000") {
		t.Errorf("export emitted the stale server DTEND:\n%s", out)
	}
	if strings.Contains(out, xpropOriginalDTEND) {
		t.Errorf("export leaked the preservation slot:\n%s", out)
	}
}
