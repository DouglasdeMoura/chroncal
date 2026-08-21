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

// hideTable renames a table out of the way for the duration of the test. The
// queries that read it then fail the way a real I/O error would.
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

// See event.TestEventService_HydrateBestEffort_PopulatesPastAFailure.
func TestJournalService_HydrateBestEffort_PopulatesPastAFailure(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	j := createJournal(t, svc)

	if err := svc.ReplaceComments(ctx, j.ID, []string{"a note"}); err != nil {
		t.Fatalf("replace comments: %v", err)
	}
	hideTable(t, svc, "journal_attendees") // read before comments

	fetched, err := svc.Get(ctx, j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := svc.HydrateBestEffort(ctx, &fetched); err == nil {
		t.Fatal("HydrateBestEffort must report the unreadable relation")
	}
	if len(fetched.Comments) != 1 {
		t.Errorf("Comments = %d, want 1", len(fetched.Comments))
	}
}

// See event.TestEventService_HydrateSkipUnreadable_NamesLostRelations.
func TestJournalService_HydrateSkipUnreadable_NamesLostRelations(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	j := createJournal(t, svc)

	hideTable(t, svc, "journal_attendees")

	failed := svc.HydrateSkipUnreadable(ctx, &j)
	if len(failed) != 1 || failed[0] != "attendees" {
		t.Fatalf("HydrateSkipUnreadable = %v, want [attendees]", failed)
	}
	if err := svc.Hydrate(ctx, &j); err == nil {
		t.Fatal("Hydrate returned nil with a relation unreadable; the default export must still abort")
	}
}
