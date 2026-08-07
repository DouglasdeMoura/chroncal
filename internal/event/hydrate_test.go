package event

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// Hydrate is the single definition of a fully populated event, and every
// consumer that writes iCal relies on it failing loudly: a swallowed relation
// error yields a record with alarms or attendees silently missing, which
// CalDAV push then writes over the server copy.
//
// Hiding one relation table at a time proves each branch checks its own error
// rather than riding on a neighbor's. Renaming rather than dropping keeps this
// to a single database — the migrations are the expensive part under -race.
func TestEventService_Hydrate_PropagatesRelationErrors(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	e := createEvent(t, svc)

	tables := []string{
		"event_alarms",
		"event_attendees",
		"event_attachments",
		"event_comments",
		"event_contacts",
		"event_resources",
		"event_relations",
		"x_properties",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			hideTable(t, svc, table)
			if err := svc.Hydrate(ctx, &e); err == nil {
				t.Fatalf("Hydrate returned nil after %s became unreadable; "+
					"the export would omit that relation and overwrite the server copy", table)
			}
		})
	}

	// Every table is back: the loop must leave the database usable.
	if err := svc.Hydrate(ctx, &e); err != nil {
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

func TestEventService_Hydrate_PopulatesRelations(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	e := createEvent(t, svc)

	if err := svc.ReplaceAlarms(ctx, e.ID, []model.Alarm{{
		Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Reminder", Related: "START",
	}}); err != nil {
		t.Fatalf("replace alarms: %v", err)
	}

	fetched, err := svc.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(fetched.Alarms) != 0 {
		t.Fatal("Get must not populate transient relations; Hydrate owns that")
	}
	if err := svc.Hydrate(ctx, &fetched); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(fetched.Alarms) != 1 {
		t.Fatalf("Alarms = %d, want 1 after Hydrate", len(fetched.Alarms))
	}
}
