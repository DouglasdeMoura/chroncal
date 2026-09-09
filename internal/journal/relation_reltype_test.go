package journal

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// The journal_relations CHECK must accept any RELTYPE token, like the event
// table. RELTYPE is extensible in RFC 5545 (issue #768).
func TestJournalService_ReplaceRelations_AcceptsUnknownRelType(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	j := createJournal(t, svc)

	if err := svc.ReplaceRelations(ctx, j.ID, []model.Relation{
		{RelType: "X-APPLE-SOMETHING", RelUID: "apple-uid"},
	}); err != nil {
		t.Fatalf("ReplaceRelations: %v", err)
	}

	got, err := svc.ListRelations(ctx, j.ID)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(got) != 1 || got[0].RelType != "X-APPLE-SOMETHING" || got[0].RelUID != "apple-uid" {
		t.Fatalf("relations = %+v, want one X-APPLE-SOMETHING:apple-uid", got)
	}
}
