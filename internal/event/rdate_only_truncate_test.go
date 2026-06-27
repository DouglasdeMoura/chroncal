package event

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newRDateOnlyMaster creates a recurring master that has no RRULE and relies
// purely on RDATEs (RFC 5545 §3.8.5.2). Its StartTime is the DTSTART
// occurrence; rdates lists the remaining occurrences.
func newRDateOnlyMaster(t *testing.T, svc *Service, uid string, rdates []time.Time) Event {
	t.Helper()
	parts := make([]string, len(rdates))
	for i, d := range rdates {
		parts[i] = d.UTC().Format(time.RFC3339)
	}
	master, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID:        uid,
		CalendarID: 1,
		Title:      "Irregular Meeting",
		StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		// No RecurrenceRule: pure RDATE recurrence.
		RDates: strings.Join(parts, ","),
	})
	if err != nil {
		t.Fatalf("upsert RDATE-only master: %v", err)
	}
	return master
}

// TestSetRRuleUntil_EmptyRule guards against the malformed ";UNTIL=..." token
// produced when an empty rule is passed through the part filter (issue #414).
// An empty input must never grow a leading ";" separator.
func TestSetRRuleUntil_EmptyRule(t *testing.T) {
	got := setRRuleUntil("", time.Date(2026, 1, 1, 23, 59, 59, 0, time.UTC), false)
	if strings.HasPrefix(got, ";") {
		t.Fatalf("setRRuleUntil(\"\") = %q, must not start with a bare \";\" separator", got)
	}
}

// TestDeleteFromInstance_RDateOnlyMasterNotCorrupted reproduces issue #414: a
// "this and following" delete on an RDATE-only master used to write a synthetic
// ";UNTIL=..." RRULE that fails to parse, collapsing the whole series. The
// master's recurrence_rule must stay empty so expansion keeps working.
func TestDeleteFromInstance_RDateOnlyMasterNotCorrupted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	master := newRDateOnlyMaster(t, svc, "rdate-only-del", []time.Time{rdate1, rdate2})

	if err := svc.DeleteFromInstance(ctx, master.UID, rdate2); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}

	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after delete: %v", err)
	}
	if got.RecurrenceRule != "" {
		t.Errorf("master RecurrenceRule = %q, want empty (RDATE-only master must not gain a synthetic RRULE)", got.RecurrenceRule)
	}
}

// TestDeleteFromInstance_TrimsRDatesPastCutoff reproduces issue #463: a "this
// and following" delete must drop the master's RDATEs at/after the cutoff,
// otherwise the deleted occurrences reappear on the next expansion (rrule-go
// expands RDATEs independently of the RRULE's UNTIL bound).
func TestDeleteFromInstance_TrimsRDatesPastCutoff(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	master := newRDateOnlyMaster(t, svc, "rdate-only-trim", []time.Time{rdate1, rdate2})

	if err := svc.DeleteFromInstance(ctx, master.UID, rdate2); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}

	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after delete: %v", err)
	}
	rdates := got.ParseRDates()
	for _, rd := range rdates {
		if !rd.Before(rdate2) {
			t.Errorf("RDATE %s survived truncation at cutoff %s; deleted occurrence will reappear", rd.Format(time.RFC3339), rdate2.Format(time.RFC3339))
		}
	}
	if len(rdates) != 1 || !rdates[0].Equal(rdate1) {
		t.Errorf("RDates = %v, want only the pre-cutoff occurrence %s", rdates, rdate1.Format(time.RFC3339))
	}
}

// TestUpdateFromInstance_TrimsRDatesPastCutoff mirrors the trim assertion for
// the split/"edit this and following" path (issue #463). Surviving post-cutoff
// RDATEs would duplicate the brand-new split series.
func TestUpdateFromInstance_TrimsRDatesPastCutoff(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	master := newRDateOnlyMaster(t, svc, "rdate-only-split-trim", []time.Time{rdate1, rdate2})

	if _, err := svc.UpdateFromInstance(ctx, master.UID, rdate2, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Irregular Meeting (changed)",
		StartTime:  rdate2,
		EndTime:    rdate2.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after update: %v", err)
	}
	rdates := got.ParseRDates()
	if len(rdates) != 1 || !rdates[0].Equal(rdate1) {
		t.Errorf("old master RDates = %v, want only the pre-cutoff occurrence %s", rdates, rdate1.Format(time.RFC3339))
	}
}

// TestRestoreTruncation_ReAddsRDates verifies the trim is reversible: restoring
// a "this and following" delete from the trash must put the dropped RDATEs back
// on the master (issue #463), mirroring the RRULE/override restore.
func TestRestoreTruncation_ReAddsRDates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	master := newRDateOnlyMaster(t, svc, "rdate-only-restore", []time.Time{rdate1, rdate2})

	if err := svc.DeleteFromInstance(ctx, master.UID, rdate2); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}

	entries, err := svc.ListTrash(ctx, master.CalendarID)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	var trunc *TrashEntry
	for i := range entries {
		if entries[i].Kind == TrashKindTruncation {
			trunc = &entries[i]
			break
		}
	}
	if trunc == nil {
		t.Fatal("no truncation trash entry found")
	}
	if err := svc.RestoreTrash(ctx, *trunc); err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}

	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after restore: %v", err)
	}
	rdates := got.ParseRDates()
	if len(rdates) != 2 {
		t.Fatalf("RDates = %v, want both occurrences restored", rdates)
	}
	foundR2 := false
	for _, rd := range rdates {
		if rd.Equal(rdate2) {
			foundR2 = true
		}
	}
	if !foundR2 {
		t.Errorf("restored RDates = %v, missing dropped occurrence %s", rdates, rdate2.Format(time.RFC3339))
	}
}

// TestUpdateFromInstance_RDateOnlyMasterNotCorrupted mirrors the delete case for
// the split/"edit this and following" path through updateFromInstanceTx.
func TestUpdateFromInstance_RDateOnlyMasterNotCorrupted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	master := newRDateOnlyMaster(t, svc, "rdate-only-upd", []time.Time{rdate1, rdate2})

	_, err := svc.UpdateFromInstance(ctx, master.UID, rdate2, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Irregular Meeting (changed)",
		StartTime:  rdate2,
		EndTime:    rdate2.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after update: %v", err)
	}
	if got.RecurrenceRule != "" {
		t.Errorf("master RecurrenceRule = %q, want empty (RDATE-only master must not gain a synthetic RRULE)", got.RecurrenceRule)
	}
}
