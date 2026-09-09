package todo

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// The todo_relations CHECK must accept any RELTYPE token, like the event
// table. RELTYPE is extensible in RFC 5545 (issue #768).
func TestTodoService_ReplaceRelations_AcceptsUnknownRelType(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	td := createTodo(t, svc)

	if err := svc.ReplaceRelations(ctx, td.ID, []model.Relation{
		{RelType: "X-APPLE-SOMETHING", RelUID: "apple-uid"},
	}); err != nil {
		t.Fatalf("ReplaceRelations: %v", err)
	}

	got, err := svc.ListRelations(ctx, td.ID)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(got) != 1 || got[0].RelType != "X-APPLE-SOMETHING" || got[0].RelUID != "apple-uid" {
		t.Fatalf("relations = %+v, want one X-APPLE-SOMETHING:apple-uid", got)
	}
}
