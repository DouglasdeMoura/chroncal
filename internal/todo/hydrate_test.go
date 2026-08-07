package todo

import (
	"context"
	"testing"
)

// See event.TestEventService_Hydrate_PropagatesRelationErrors: a swallowed
// relation error here produces a VTODO missing its alarms or attendees, which
// the next CalDAV push writes over the server copy.
func TestTodoService_Hydrate_PropagatesRelationErrors(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	td := createTodo(t, svc)

	tables := []string{
		"todo_alarms",
		"todo_attendees",
		"todo_attachments",
		"todo_comments",
		"todo_contacts",
		"todo_resources",
		"todo_relations",
		"x_properties",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			hideTable(t, svc, table)
			if err := svc.Hydrate(ctx, &td); err == nil {
				t.Fatalf("Hydrate returned nil after %s became unreadable", table)
			}
		})
	}

	if err := svc.Hydrate(ctx, &td); err != nil {
		t.Fatalf("Hydrate after restoring every table: %v", err)
	}
}

// hideTable renames a table out of the way for the duration of the test, so the
// queries that read it fail the way a real I/O error would.
func hideTable(t *testing.T, svc *Service, table string) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.db.ExecContext(ctx, "ALTER TABLE "+table+" RENAME TO "+table+"_hidden"); err != nil {
		t.Fatalf("hide %s: %v", table, err)
	}
	t.Cleanup(func() {
		if _, err := svc.db.ExecContext(ctx, "ALTER TABLE "+table+"_hidden RENAME TO "+table); err != nil {
			t.Fatalf("restore %s: %v", table, err)
		}
	})
}
