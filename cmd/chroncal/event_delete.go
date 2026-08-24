package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
)

// eventResource adapts the event service to the shared verb builders.
var eventResource = resource{
	name: "event",
	resolve: func(ctx context.Context, a *app.App, ref, recurrenceID string) (row, error) {
		e, err := resolveEvent(ctx, a, ref, recurrenceID)
		if err != nil {
			return row{}, err
		}
		return row{ID: e.ID, UID: e.UID, CalendarID: e.CalendarID, Summary: e.Title}, nil
	},
	del:          func(ctx context.Context, a *app.App, id int64) error { return a.Events.Delete(ctx, id) },
	delSeries:    func(ctx context.Context, a *app.App, uid string) error { return a.Events.DeleteSeries(ctx, uid) },
	restoreByID:  func(ctx context.Context, a *app.App, id int64) error { return a.Events.RestoreByID(ctx, id) },
	restoreByUID: func(ctx context.Context, a *app.App, uid string) error { return a.Events.RestoreByUID(ctx, uid) },
	purgeCandidate: func(ctx context.Context, a *app.App, id int64) (row, bool, error) {
		e, err := a.Events.GetIncludingDeleted(ctx, id)
		if err != nil {
			return row{}, false, err
		}
		return row{Summary: e.Title}, e.DeletedAt != nil, nil
	},
	purgeByID: func(ctx context.Context, a *app.App, id int64) error { return a.Events.PurgeByID(ctx, id) },
	purgeDeleted: func(ctx context.Context, a *app.App, cutoff time.Time) (int64, error) {
		n, err := a.Events.PurgeDeleted(ctx, cutoff)
		return int64(n), err
	},
	errNotDeleted: event.ErrNotDeleted,
}

// eventDeleteCmd composes the shared delete verb with the two event-only
// scopes. --recurrence-id excludes one occurrence (an EXDATE on the master
// plus an override soft-delete). --following truncates the series from a
// date. Both scopes act on the series master, so this wrapper resolves the
// master row and bypasses the shared single-row flow.
func eventDeleteCmd() *cobra.Command {
	cmd := newDeleteCmd(eventResource, verbHelp{
		short: "Delete an event",
		long: `Delete a single event, this occurrence, this and following
occurrences, or an entire recurring series.

--recurrence-id and --following take an RFC 3339 timestamp and address the
series by ID or UID. --recurrence-id excludes that one date (EXDATE, and
any override at that time). --following truncates the series at that date.`,
		example: `  chroncal event delete 42
  chroncal event delete standup-uid --recurrence-id 2026-04-07T12:00:00Z
  chroncal event delete standup-uid --following 2026-04-07T12:00:00Z
  chroncal event delete standup-uid --series`,
	})

	var following string
	cmd.Flags().StringVar(&following, "following", "", "delete this occurrence and every following one (RFC 3339 timestamp)")
	cmd.Flags().Lookup("recurrence-id").Usage = "delete this occurrence (RFC 3339 timestamp)"

	plainDelete := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		recurrenceID, err := cmd.Flags().GetString("recurrence-id")
		if err != nil {
			return err
		}
		series, err := cmd.Flags().GetBool("series")
		if err != nil {
			return err
		}

		if series && (recurrenceID != "" || following != "") {
			return errInvalidInputf("--series cannot be combined with --recurrence-id or --following")
		}
		if recurrenceID != "" && following != "" {
			return errInvalidInputf("--recurrence-id and --following are mutually exclusive")
		}
		if recurrenceID == "" && following == "" {
			return plainDelete(cmd, args)
		}

		var followingAt, occurrenceAt time.Time
		if following != "" {
			followingAt, err = parseRFC3339Flag("following", following)
			if err != nil {
				return err
			}
		}
		if recurrenceID != "" {
			occurrenceAt, err = parseRFC3339Flag("recurrence-id", recurrenceID)
			if err != nil {
				return err
			}
		}

		a, err := initApp()
		if err != nil {
			return err
		}
		defer a.Close()
		ctx := context.Background()

		// Occurrence scopes address the series through its master row.
		e, err := resolveEvent(ctx, a, args[0], "")
		if err != nil {
			return fmt.Errorf("get event: %w", err)
		}

		question := fmt.Sprintf("Delete this occurrence of %q at %s?", safeText(e.Title), occurrenceAt.Format(time.RFC3339))
		if following != "" {
			question = fmt.Sprintf("Delete %q and following occurrences at %s?", safeText(e.Title), followingAt.Format(time.RFC3339))
		}
		if err := confirmDestructive(cmd, question); err != nil {
			return err
		}

		w := cmd.OutOrStdout()
		if following != "" {
			if err := a.Events.DeleteFromInstance(ctx, e.UID, followingAt); err != nil {
				return fmt.Errorf("delete this and following: %w", err)
			}
			return reportDeleted(cmd, a, w, e.CalendarID,
				map[string]any{"deleted": true, "uid": e.UID, "following": followingAt.UTC().Format(time.RFC3339)},
				fmt.Sprintf("Deleted %q and following occurrences.\n", safeText(e.Title)))
		}

		if err := a.Events.DeleteInstance(ctx, e.UID, occurrenceAt); err != nil {
			return fmt.Errorf("delete occurrence: %w", err)
		}
		return reportDeleted(cmd, a, w, e.CalendarID,
			map[string]any{"deleted": true, "uid": e.UID, "recurrence_id": occurrenceAt.UTC().Format(time.RFC3339)},
			fmt.Sprintf("Deleted occurrence of %q at %s.\n", safeText(e.Title), occurrenceAt.Format(time.RFC3339)))
	}
	return cmd
}
