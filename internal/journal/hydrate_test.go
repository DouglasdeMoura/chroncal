package journal

import (
	"context"
	"testing"
)

// See event.TestEventService_Hydrate_PropagatesRelationErrors. VJOURNAL has no
// alarms or resources, so the relation set is smaller.
func TestJournalService_Hydrate_PropagatesRelationErrors(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	j := createJournal(t, svc)

	tables := []string{
		"journal_attendees",
		"journal_attachments",
		"journal_comments",
		"journal_contacts",
		"journal_relations",
		"x_properties",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			hideTable(t, svc, table)
			if err := svc.Hydrate(ctx, &j); err == nil {
				t.Fatalf("Hydrate returned nil after %s became unreadable", table)
			}
		})
	}

	if err := svc.Hydrate(ctx, &j); err != nil {
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
