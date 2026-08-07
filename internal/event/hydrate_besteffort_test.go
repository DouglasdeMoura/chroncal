package event

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// Hydrate fails fast because a caller that PUTs a partially populated record
// overwrites the server copy with it. The CLI's read-only display paths want
// the opposite: one unreadable relation should degrade that field, not blank
// every field after it. HydrateBestEffort serves those, populating everything
// it can and reporting what it could not.
func TestEventService_HydrateBestEffort_PopulatesPastAFailure(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	e := createEvent(t, svc)

	if err := svc.ReplaceAttendees(ctx, e.ID, []model.Attendee{{
		Email: "someone@example.com", Role: "REQ-PARTICIPANT", RSVPStatus: "NEEDS-ACTION",
	}}); err != nil {
		t.Fatalf("replace attendees: %v", err)
	}

	// Alarms are read first; attendees come after.
	hideTable(t, svc, "event_alarms")

	fetched, err := svc.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	err = svc.HydrateBestEffort(ctx, &fetched)
	if err == nil {
		t.Fatal("HydrateBestEffort must report the unreadable relation")
	}
	if len(fetched.Attendees) != 1 {
		t.Errorf("Attendees = %d, want 1: a failure reading alarms must not blank "+
			"every relation read after it", len(fetched.Attendees))
	}
}
