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
	del:          func(ctx context.Context, a *app.App, id int64) error { return a.Todos.Delete(ctx, id) },
	delSeries:    func(ctx context.Context, a *app.App, uid string) error { return a.Todos.DeleteSeries(ctx, uid) },
	restoreByID:  func(ctx context.Context, a *app.App, id int64) error { return a.Todos.RestoreByID(ctx, id) },
	restoreByUID: func(ctx context.Context, a *app.App, uid string) error { return a.Todos.RestoreByUID(ctx, uid) },
	purgeCandidate: func(ctx context.Context, a *app.App, id int64) (row, bool, error) {
		td, err := a.Todos.GetIncludingDeleted(ctx, id)
		if err != nil {
			return row{}, false, err
		}
		return row{Summary: td.Summary}, td.DeletedAt != nil, nil
	},
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
