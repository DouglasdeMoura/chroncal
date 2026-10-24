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
		t, err := resolveTodo(ctx, a, ref, recurrenceID)
		if err != nil {
			return row{}, err
		}
		return row{ID: t.ID, UID: t.UID, CalendarID: t.CalendarID, Summary: t.Summary}, nil
	},
	del: func(ctx context.Context, a *app.App, target row) (int64, error) {
		return target.CalendarID, a.Todos.Delete(ctx, target.ID)
	},
	delSeries: func(ctx context.Context, a *app.App, target row) ([]int64, error) {
		return []int64{target.CalendarID}, a.Todos.DeleteSeries(ctx, target.UID)
	},
	restoreByID: func(ctx context.Context, a *app.App, id int64) ([]int64, error) {
		if err := a.Todos.RestoreByID(ctx, id); err != nil {
			return nil, err
		}
		r, err := a.Todos.GetIncludingDeleted(ctx, id)
		if err != nil {
			// The restore committed. The push is best-effort, so a failed
			// read skips it, and the next sync sends the rows.
			return nil, nil
		}
		return todoRestorePushCalendars(ctx, a, r.UID, r.CalendarID), nil
	},
	restoreByUID: func(ctx context.Context, a *app.App, uid string) ([]int64, error) {
		if err := a.Todos.RestoreByUID(ctx, uid); err != nil {
			return nil, err
		}
		return todoRestorePushCalendars(ctx, a, uid, 0), nil
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
		long: `Delete a single todo, a specific recurring override, or an entire
recurring series.`,
		example: `  chroncal todo delete 7
  chroncal todo delete weekly-review-uid --recurrence-id 2026-04-10T00:00:00Z
  chroncal todo delete weekly-review-uid --series`,
	})
}

// todoRestorePushCalendars returns the calendars that the push after a
// restore of uid sends (see restorePushCalendars). The candidates are
// rowCalendarID (0 for none), the calendar of the master, and the
// calendars of the live overrides of the UID. A failed read drops its
// candidates: the push is best-effort.
func todoRestorePushCalendars(ctx context.Context, a *app.App, uid string, rowCalendarID int64) []int64 {
	candidates := []int64{rowCalendarID}
	if m, err := a.Queries.GetTodoByUIDIncludingDeleted(ctx, uid); err == nil {
		candidates = append(candidates, m.CalendarID)
	}
	if overrides, err := a.Todos.ListOverridesByUID(ctx, uid); err == nil {
		for _, o := range overrides {
			candidates = append(candidates, o.CalendarID)
		}
	}
	return restorePushCalendars(ctx, a, uid, candidates)
}
