package main

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/journal"
)

// journalResource adapts the journal service to the shared verb builders.
var journalResource = resource{
	name: "journal",
	resolve: func(ctx context.Context, a *app.App, ref, recurrenceID string) (row, error) {
		j, err := resolveJournal(ctx, a, ref, recurrenceID)
		if err != nil {
			return row{}, err
		}
		return row{ID: j.ID, UID: j.UID, CalendarID: j.CalendarID, Summary: j.Summary}, nil
	},
	del:          func(ctx context.Context, a *app.App, id int64) error { return a.Journals.Delete(ctx, id) },
	delSeries:    func(ctx context.Context, a *app.App, uid string) error { return a.Journals.DeleteSeries(ctx, uid) },
	restoreByID:  func(ctx context.Context, a *app.App, id int64) error { return a.Journals.RestoreByID(ctx, id) },
	restoreByUID: func(ctx context.Context, a *app.App, uid string) error { return a.Journals.RestoreByUID(ctx, uid) },
	purgeCandidate: func(ctx context.Context, a *app.App, id int64) (row, bool, error) {
		j, err := a.Journals.GetIncludingDeleted(ctx, id)
		if err != nil {
			return row{}, false, err
		}
		return row{Summary: j.Summary}, j.DeletedAt != nil, nil
	},
	purgeByID: func(ctx context.Context, a *app.App, id int64) error { return a.Journals.PurgeByID(ctx, id) },
	purgeDeleted: func(ctx context.Context, a *app.App, cutoff time.Time) (int64, error) {
		n, err := a.Journals.PurgeDeleted(ctx, cutoff)
		return int64(n), err
	},
	errNotDeleted: journal.ErrNotDeleted,
}

func journalDeleteCmd() *cobra.Command {
	return newDeleteCmd(journalResource, verbHelp{
		short: "Delete a journal entry",
		long: `Delete a single journal entry, a specific recurring override, or an
entire recurring series.`,
		example: `  chroncal journal delete 12
  chroncal journal delete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z
  chroncal journal delete weekly-review-uid --series`,
	})
}
