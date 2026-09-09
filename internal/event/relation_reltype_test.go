package event

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// RELTYPE is an extensible token in RFC 5545. iCloud sends tokens outside
// PARENT, CHILD, and SIBLING. The old CHECK on event_relations refused them.
// The pull then withheld the sync-token and the calendar re-pulled every
// resource on every sync (issue #768).
func TestEventService_ReplaceRelations_AcceptsUnknownRelType(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	e := createEvent(t, svc)

	want := []model.Relation{
		{RelType: "PARENT", RelUID: "parent-uid"},
		{RelType: "X-APPLE-SOMETHING", RelUID: "apple-uid"},
	}
	if err := svc.ReplaceRelations(ctx, e.ID, want); err != nil {
		t.Fatalf("ReplaceRelations: %v", err)
	}

	got, err := svc.ListRelations(ctx, e.ID)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("relations = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].RelType != want[i].RelType || got[i].RelUID != want[i].RelUID {
			t.Errorf("relation[%d] = %q:%q, want %q:%q",
				i, got[i].RelType, got[i].RelUID, want[i].RelType, want[i].RelUID)
		}
	}
}

// An empty RELTYPE is not a token. The schema must still refuse it.
func TestEventService_ReplaceRelations_RefusesEmptyRelType(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	e := createEvent(t, svc)

	err := svc.ReplaceRelations(ctx, e.ID, []model.Relation{{RelUID: "no-type-uid"}})
	if err == nil {
		t.Fatal("ReplaceRelations accepted an empty rel_type")
	}
}
