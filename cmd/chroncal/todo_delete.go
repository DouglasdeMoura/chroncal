package main

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// todoResource adapts the todo service to the shared verb builders.
var todoResource = resource{
	name: "todo",
	resolve: func(ctx context.Context, a *app.App, ref, recurrenceID string) (row, error) {
		t, create, err := resolveTodoOccurrence(ctx, a, ref, recurrenceID)
		if err != nil {
			return row{}, err
		}
		if create {
			// The occurrence has no row yet: delOccurrence writes it.
			t.ID = 0
		}
		return row{ID: t.ID, UID: t.UID, CalendarID: t.CalendarID, Summary: t.Summary}, nil
	},
	del: func(ctx context.Context, a *app.App, target row) (int64, error) {
		return target.CalendarID, a.Todos.Delete(ctx, target.ID)
	},
	delOccurrence: func(ctx context.Context, a *app.App, ref, recurrenceID string) (int64, int64, error) {
		occ, create, err := resolveTodoOccurrence(ctx, a, ref, recurrenceID)
		if err != nil {
			return 0, 0, err
		}
		if !create {
			// A row of the occurrence came first.
			return occ.CalendarID, occ.ID, a.Todos.Delete(ctx, occ.ID)
		}
		t, err := a.Todos.DeleteInstance(ctx, occ.UID, occ.RecurrenceID, todoParamsOf(occ))
		return t.CalendarID, t.ID, err
	},
	delSeries: func(ctx context.Context, a *app.App, target row) ([]int64, error) {
		return []int64{target.CalendarID}, a.Todos.DeleteSeries(ctx, target.UID)
	},
	restoreByID: func(ctx context.Context, a *app.App, id int64) (restorePush, error) {
		if err := a.Todos.RestoreByID(ctx, id); err != nil {
			return restorePush{}, err
		}
		r, err := a.Todos.GetIncludingDeleted(ctx, id)
		if err != nil {
			return restorePush{err: err}, nil
		}
		return todoRestorePush(ctx, a, r.UID, r.CalendarID), nil
	},
	restoreByUID: func(ctx context.Context, a *app.App, uid string) (restorePush, error) {
		if err := a.Todos.RestoreByUID(ctx, uid); err != nil {
			return restorePush{}, err
		}
		return todoRestorePush(ctx, a, uid, 0), nil
	},
	purgeCandidate: func(ctx context.Context, a *app.App, id int64) (row, bool, error) {
		td, err := a.Todos.GetIncludingDeleted(ctx, id)
		if err != nil {
			return row{}, false, err
		}
		return row{Summary: td.Summary}, td.DeletedAt != nil, nil
	},
	purgeByID: func(ctx context.Context, a *app.App, id int64) error { return a.Todos.PurgeByID(ctx, id) },
	purgeDeleted: func(ctx context.Context, a *app.App, cutoff time.Time) (int64, error) {
		n, err := a.Todos.PurgeDeleted(ctx, cutoff)
		return int64(n), err
	},
	errNotDeleted: todo.ErrNotDeleted,
}

func todoDeleteCmd() *cobra.Command {
	return newDeleteCmd(todoResource, verbHelp{
		short: "Delete a todo",
		long: `Delete a single todo, one occurrence of a recurring todo, or an entire
recurring series.

With --recurrence-id and the UID of a recurring todo, the command deletes
one occurrence. The trash keeps the occurrence, so "chroncal todo restore"
brings it back.`,
		example: `  chroncal todo delete 7
  chroncal todo delete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z
  chroncal todo delete weekly-review-uid --series`,
	})
}

// todoRestorePush lists the candidate calendars of a todo restore of uid
// (see seriesRestoreCandidates).
func todoRestorePush(ctx context.Context, a *app.App, uid string, rowCalendarID int64) restorePush {
	return seriesRestoreCandidates(ctx, uid, rowCalendarID,
		func(ctx context.Context, uid string) (int64, error) {
			m, err := a.Queries.GetTodoByUIDIncludingDeleted(ctx, uid)
			return m.CalendarID, err
		},
		func(ctx context.Context, uid string) ([]int64, error) {
			rows, err := a.Queries.ListTodoOverridesByUID(ctx, uid)
			ids := make([]int64, 0, len(rows))
			for _, r := range rows {
				ids = append(ids, r.CalendarID)
			}
			return ids, err
		})
}
