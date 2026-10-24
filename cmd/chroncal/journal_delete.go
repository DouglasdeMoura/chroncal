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
	del: func(ctx context.Context, a *app.App, target row) (int64, error) {
		return target.CalendarID, a.Journals.Delete(ctx, target.ID)
	},
	delSeries: func(ctx context.Context, a *app.App, target row) ([]int64, error) {
		return []int64{target.CalendarID}, a.Journals.DeleteSeries(ctx, target.UID)
	},
	restoreByID: func(ctx context.Context, a *app.App, id int64) (restorePush, error) {
		if err := a.Journals.RestoreByID(ctx, id); err != nil {
			return restorePush{}, err
		}
		r, err := a.Journals.GetIncludingDeleted(ctx, id)
		if err != nil {
			return restorePush{err: err}, nil
		}
		return journalRestorePush(ctx, a, r.UID, r.CalendarID), nil
	},
	restoreByUID: func(ctx context.Context, a *app.App, uid string) (restorePush, error) {
		if err := a.Journals.RestoreByUID(ctx, uid); err != nil {
			return restorePush{}, err
		}
		return journalRestorePush(ctx, a, uid, 0), nil
	},
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
	uidState: func(ctx context.Context, a *app.App, uid string) (bool, bool, error) {
		masterDeletedAt := func(ctx context.Context, uid string) (*string, error) {
			m, err := a.Queries.GetJournalByUIDIncludingDeleted(ctx, uid)
			return m.DeletedAt, err
		}
		return seriesUIDState(ctx, uid, masterDeletedAt, a.Queries.ListJournalOverridesByUID, a.Queries.ListDeletedJournalOverrideRecurrenceIDs)
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

// journalRestorePush lists the candidate calendars of a journal restore of uid
// (see seriesRestoreCandidates).
func journalRestorePush(ctx context.Context, a *app.App, uid string, rowCalendarID int64) restorePush {
	return seriesRestoreCandidates(ctx, uid, rowCalendarID,
		func(ctx context.Context, uid string) (int64, error) {
			m, err := a.Queries.GetJournalByUIDIncludingDeleted(ctx, uid)
			return m.CalendarID, err
		},
		func(ctx context.Context, uid string) ([]int64, error) {
			rows, err := a.Queries.ListJournalOverridesByUID(ctx, uid)
			ids := make([]int64, 0, len(rows))
			for _, r := range rows {
				ids = append(ids, r.CalendarID)
			}
			return ids, err
		})
}
