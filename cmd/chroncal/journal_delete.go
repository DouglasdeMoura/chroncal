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
	restoreByID: func(ctx context.Context, a *app.App, id int64) ([]int64, error) {
		if err := a.Journals.RestoreByID(ctx, id); err != nil {
			return nil, err
		}
		r, err := a.Journals.GetIncludingDeleted(ctx, id)
		if err != nil {
			// The restore committed. The push is best-effort, so a failed
			// read skips it, and the next sync sends the rows.
			return nil, nil
		}
		return journalRestorePushCalendars(ctx, a, r.UID, r.CalendarID), nil
	},
	restoreByUID: func(ctx context.Context, a *app.App, uid string) ([]int64, error) {
		if err := a.Journals.RestoreByUID(ctx, uid); err != nil {
			return nil, err
		}
		return journalRestorePushCalendars(ctx, a, uid, 0), nil
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

// journalRestorePushCalendars returns the calendars that the push after a
// restore of uid sends: rowCalendarID (0 for none) and the calendar of the
// master of the UID. The restore marks the calendar of the master dirty
// (see storage.ReconcileAfterRestore). A failed read of the master returns
// rowCalendarID only: the push is best-effort.
func journalRestorePushCalendars(ctx context.Context, a *app.App, uid string, rowCalendarID int64) []int64 {
	var ids []int64
	if rowCalendarID != 0 {
		ids = append(ids, rowCalendarID)
	}
	if m, err := a.Queries.GetJournalByUIDIncludingDeleted(ctx, uid); err == nil && m.CalendarID != rowCalendarID {
		ids = append(ids, m.CalendarID)
	}
	return ids
}
