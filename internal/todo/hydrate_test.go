package todo

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
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

// See event.TestEventService_HydrateBestEffort_PopulatesPastAFailure: the CLI's
// display paths need every relation they can read, not everything up to the
// first failure.
func TestTodoService_HydrateBestEffort_PopulatesPastAFailure(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	td := createTodo(t, svc)

	if err := svc.ReplaceAttendees(ctx, td.ID, []model.Attendee{{
		Email: "someone@example.com", Role: "REQ-PARTICIPANT", RSVPStatus: "NEEDS-ACTION",
	}}); err != nil {
		t.Fatalf("replace attendees: %v", err)
	}
	hideTable(t, svc, "todo_alarms") // read before attendees

	fetched, err := svc.Get(ctx, td.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := svc.HydrateBestEffort(ctx, &fetched); err == nil {
		t.Fatal("HydrateBestEffort must report the unreadable relation")
	}
	if len(fetched.Attendees) != 1 {
		t.Errorf("Attendees = %d, want 1", len(fetched.Attendees))
	}
}
