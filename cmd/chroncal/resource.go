package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/uidmove"
)

// row is the neutral view of one event, todo, or journal row. It carries
// exactly the fields that the shared verb builders read. No builder then
// depends on a domain type.
type row struct {
	ID         int64
	UID        string
	CalendarID int64
	Summary    string
}

// resource adapts one domain service to the shared delete, restore, and
// purge verb builders. Each domain declares one resource next to its
// command wiring. Every step beyond the service calls — the confirm
// question, the error taxonomy, the output shape — lives once in the
// builders below.
type resource struct {
	// name is the noun for confirm questions, text output, and error
	// wraps. Example: "event".
	name string

	// resolve maps a CLI reference to the target row. recurrenceID carries
	// the raw --recurrence-id flag value. Pass it through when the domain
	// resolves an override row by it.
	resolve func(ctx context.Context, a *app.App, ref, recurrenceID string) (row, error)

	// resolveIn maps a CLI reference to the target row in the named
	// calendar. A domain whose UID can live in more than one calendar sets
	// it (issue #842). runDelete uses it when deleteOpts names a calendar.
	// series reports a delete of the whole series; a plain delete names
	// one row of the calendar.
	resolveIn func(ctx context.Context, a *app.App, ref, calendarName string, series bool) (row, error)
	// resolveSeriesUID maps the UID of a series delete with no --calendar to
	// its target. nil uses resolve.
	resolveSeriesUID func(ctx context.Context, a *app.App, uid string) (row, error)

	// del soft-deletes one row by ID. delSeries soft-deletes the master row
	// of the resolved row and every override of its series. The row carries
	// the calendar: two calendars can hold the same event UID (issue #842).
	// del and delSeries return the calendar that the delete wrote, so the
	// push sends it. A legacy event override writes the calendar of its
	// master.
	del       func(ctx context.Context, a *app.App, target row) (int64, error)
	delSeries func(ctx context.Context, a *app.App, target row) ([]int64, error)

	// restoreByID clears the deletion marker on one row. restoreByUID
	// reverses the delete of the UID by the rule of the resource service.
	// Each returns the calendars that the push after the restore sends.
	restoreByID  func(ctx context.Context, a *app.App, id int64) ([]int64, error)
	restoreByUID func(ctx context.Context, a *app.App, uid string) ([]int64, error)

	// purgeCandidate loads one row for the purge confirm. The bool reports
	// whether the row carries a soft-delete marker.
	purgeCandidate func(ctx context.Context, a *app.App, id int64) (row, bool, error)

	// purgeByID hard-deletes one soft-deleted row. purgeDeleted hard-deletes
	// every soft-deleted row older than the cutoff. It returns the count.
	purgeByID    func(ctx context.Context, a *app.App, id int64) error
	purgeDeleted func(ctx context.Context, a *app.App, cutoff time.Time) (int64, error)

	// errNotDeleted is the domain sentinel error. Restore and purge map it
	// to the "not_found" error code.
	errNotDeleted error
}

// verbHelp carries the per-domain text of one shared verb. The builders fix
// Use and Args. Short, Long, and Example keep the voice of each resource.
type verbHelp struct {
	short   string
	long    string
	example string
}

// reportWritten writes one delete or restore result. Machine output prints
// the JSON payload. Text output prints the line. Both paths then push the
// changed calendars.
func reportWritten(cmd *cobra.Command, a *app.App, w io.Writer, calendarIDs []int64, payload map[string]any, line string) error {
	if outputFmt != "text" {
		if err := printOutput(w, payload); err != nil {
			return err
		}
	} else {
		fmt.Fprint(w, line)
	}
	// A series delete or a restore can write more than one calendar: each
	// one pushes.
	for _, id := range calendarIDs {
		opportunisticPush(a, id, cmd)
	}
	return nil
}

// newDeleteCmd builds the delete verb for one resource. The body
// soft-deletes one row, or the whole series with --series. A domain that
// resolves override rows through --recurrence-id gets the override confirm
// question and the single-row delete for free.
func newDeleteCmd(r resource, h verbHelp) *cobra.Command {
	var (
		recurrenceID string
		series       bool
	)
	cmd := &cobra.Command{
		Use:     "delete <id|uid>",
		Short:   h.short,
		Long:    h.long,
		Example: h.example,
		Args:    exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDelete(cmd, r, args, deleteOpts{RecurrenceID: recurrenceID, Series: series})
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	addConfirmFlag(cmd)
	return cmd
}

// deleteOpts carries the flag values of one delete run. A resource that
// wraps the verb with more flags passes them here, so no state lives on
// the command between runs.
type deleteOpts struct {
	RecurrenceID string
	Series       bool
	// CalendarName names the calendar of the reference. runDelete then
	// resolves the reference with resource.resolveIn (issue #842).
	CalendarName string
}

// runDelete is the body of the shared delete verb. See newDeleteCmd.
func runDelete(cmd *cobra.Command, r resource, args []string, opts deleteOpts) error {
	a, err := initApp()
	if err != nil {
		return err
	}
	defer a.Close()
	ctx := context.Background()

	if opts.Series && opts.RecurrenceID != "" {
		return errInvalidInputf("--series and --recurrence-id are mutually exclusive")
	}

	var target row
	_, numericErr := strconv.ParseInt(args[0], 10, 64)
	switch {
	case opts.CalendarName != "" && r.resolveIn != nil:
		target, err = r.resolveIn(ctx, a, args[0], opts.CalendarName, opts.Series)
	case opts.Series && numericErr != nil && r.resolveSeriesUID != nil:
		target, err = r.resolveSeriesUID(ctx, a, args[0])
	default:
		target, err = r.resolve(ctx, a, args[0], opts.RecurrenceID)
	}
	if err != nil {
		return fmt.Errorf("get %s: %w", r.name, err)
	}

	question := fmt.Sprintf("Delete %s %q?", r.name, safeText(target.Summary))
	switch {
	case opts.Series:
		question = fmt.Sprintf("Delete the entire recurring series %q (master + all overrides)?", safeText(target.Summary))
	case opts.RecurrenceID != "":
		question = fmt.Sprintf("Delete override instance of %q at %s?", safeText(target.Summary), opts.RecurrenceID)
	}
	if err := confirmDestructive(cmd, question); err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	if opts.Series {
		pushCalendarIDs, err := r.delSeries(ctx, a, target)
		if err != nil {
			return fmt.Errorf("delete series: %w", notFoundErrFor(err, r.name, target.UID, r.resolveIn != nil))
		}
		return reportWritten(cmd, a, w, pushCalendarIDs,
			map[string]any{"deleted": true, "uid": target.UID, "series": true},
			fmt.Sprintf("Deleted %s series %q.\n", r.name, safeText(target.UID)))
	}

	pushCalendarID, err := r.del(ctx, a, target)
	if err != nil {
		return fmt.Errorf("delete %s: %w", r.name, err)
	}
	return reportWritten(cmd, a, w, []int64{pushCalendarID},
		map[string]any{"deleted": true, "id": target.ID},
		fmt.Sprintf("Deleted %s %d.\n", r.name, target.ID))
}

// newRestoreCmd builds the restore verb for one resource. A numeric
// reference restores one row. A UID reference reverses the delete of the
// UID by the rule of the resource service. A live or absent row reports
// "not_found".
func newRestoreCmd(r resource, h verbHelp) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "restore <id-or-uid>",
		Short:   h.short,
		Long:    h.long,
		Example: h.example,
		Args:    exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			ref := args[0]
			w := cmd.OutOrStdout()

			if id, parseErr := strconv.ParseInt(ref, 10, 64); parseErr == nil {
				pushCalendarIDs, err := r.restoreByID(ctx, a, id)
				if err != nil {
					if errors.Is(err, r.errNotDeleted) {
						return errNotFoundf("%s %d not found (may have been purged)", r.name, id)
					}
					return fmt.Errorf("restore %s: %w", r.name, notFoundErr(err, r.name, id))
				}
				return reportWritten(cmd, a, w, pushCalendarIDs,
					map[string]any{"restored": true, "id": id},
					fmt.Sprintf("Restored %s %d.\n", r.name, id))
			}

			// UID path: the service reverses the delete of the UID.
			pushCalendarIDs, err := r.restoreByUID(ctx, a, ref)
			if err != nil {
				if errors.Is(err, r.errNotDeleted) {
					return errNotFoundf("%s %q not found (may have been purged)", r.name, ref)
				}
				return fmt.Errorf("restore %s: %w", r.name, notFoundErr(err, r.name, ref))
			}
			return reportWritten(cmd, a, w, pushCalendarIDs,
				map[string]any{"restored": true, "uid": ref},
				fmt.Sprintf("Restored %s(s) with uid %q.\n", r.name, safeText(ref)))
		},
	}
	return cmd
}

// restorePushCalendars returns the calendars of candidates that the push
// after a restore of uid must send, in push order. The restore leaves sync
// state that names them (see storage.ReconcileAfterRestore):
//  1. A calendar with a dirty sync row of the UID. The push creates or
//     updates the server object there.
//  2. A calendar with a tombstone of the UID, for example the calendar of
//     a legacy override. The push deletes the old server object there.
//
// The order is the order of sync.PushMove: the target first, then the
// source. A failed second push then leaves the item on the server. A
// calendar with no such state stays out, so the push sends no unrelated
// edits of another series of the UID. A duplicate or a 0 stays out too.
func restorePushCalendars(ctx context.Context, a *app.App, uid string, candidates []int64) []int64 {
	var dirty, tombstoned []int64
	seen := map[int64]bool{0: true}
	for _, id := range candidates {
		if seen[id] {
			continue
		}
		seen[id] = true
		if sr, err := a.Queries.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: id, Uid: uid}); err == nil && sr.Dirty != 0 {
			dirty = append(dirty, id)
			continue
		}
		if _, err := a.Queries.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: id, Uid: uid}); err == nil {
			tombstoned = append(tombstoned, id)
		}
	}
	return append(dirty, tombstoned...)
}

// newPurgeCmd builds the purge verb for one resource. The body refuses a
// live row, confirms, then hard-deletes one soft-deleted row.
func newPurgeCmd(r resource, h verbHelp) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "purge <id>",
		Short:   h.short,
		Long:    h.long,
		Example: h.example,
		Args:    exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return errInvalidInputf("parse id %q: %v", args[0], err)
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			target, deleted, err := r.purgeCandidate(ctx, a, id)
			if err != nil {
				return fmt.Errorf("get %s: %w", r.name, err)
			}
			if !deleted {
				return fmt.Errorf("%s %d is live; run '%s delete %d' first", r.name, id, r.name, id)
			}

			question := fmt.Sprintf("Purge %s %q (id %d)? This cannot be undone.", r.name, safeText(target.Summary), id)
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if err := r.purgeByID(ctx, a, id); err != nil {
				if errors.Is(err, r.errNotDeleted) {
					return errNotFoundf("%s %d not found or not soft-deleted", r.name, id)
				}
				var live *event.SeriesHasLiveOverridesError
				if errors.As(err, &live) {
					return errInvalidInputf("%s %d: %v", r.name, id, live)
				}
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": true, "id": id})
			}
			fmt.Fprintf(w, "Purged %s %d.\n", r.name, id)
			return nil
		},
	}
	addConfirmFlag(cmd)
	return cmd
}

// newPurgeDeletedCmd builds the purge-deleted verb for one resource. The
// body hard-deletes every soft-deleted row older than --older-than. A
// sub-hour window is extra destructive. It always requires a confirm.
func newPurgeDeletedCmd(r resource, h verbHelp) *cobra.Command {
	var olderThanStr string
	cmd := &cobra.Command{
		Use:     "purge-deleted",
		Short:   h.short,
		Long:    h.long,
		Example: h.example,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseCLIDuration("older-than", olderThanStr)
			if err != nil {
				return err
			}
			if d < 0 {
				return errInvalidInputf("--older-than must be non-negative, got %s", d)
			}

			// Sub-hour windows are especially destructive — require --yes
			// or an interactive confirm regardless of scripted-vs-tty.
			if d < time.Hour {
				prompt := fmt.Sprintf("Purge ALL %ss soft-deleted in the last %s? This cannot be undone.", r.name, d)
				if err := confirmDestructive(cmd, prompt); err != nil {
					return err
				}
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			cutoff := time.Now().Add(-d)
			n, err := r.purgeDeleted(ctx, a, cutoff)
			if err != nil {
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": n, "older_than": d.String()})
			}
			fmt.Fprintf(w, "Purged %d %s(s) soft-deleted more than %s ago.\n", n, r.name, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThanStr, "older-than", "720h", "age threshold (Go duration, e.g. 30d=720h, 168h=7 days)")
	addConfirmFlag(cmd)
	return cmd
}

// errOccurrenceMove returns the CLI error for a refused calendar move of
// one occurrence. err is the service error. A uidmove.OccurrenceMoveError
// in it names the masters that the edited row can belong to. kind is
// "event", "todo", or "journal". uid, recurrenceID, and calendarID are the
// fields of the edited row.
//
// One master gives a hint with the numeric ID of the master, because a UID
// can have a master in two calendars. No live master gives no move hint,
// because a move of the series fails the same way. The error says to
// restore the master or to edit without --calendar.
// More masters give an error that names each master and its calendar,
// because the CLI cannot choose the series.
func errOccurrenceMove(ctx context.Context, a *app.App, kind, uid, recurrenceID string, calendarID int64, err error) error {
	var moveErr *uidmove.OccurrenceMoveError
	var masters []uidmove.Master
	if errors.As(err, &moveErr) {
		masters = moveErr.Masters
	}
	const reason = "one occurrence cannot move to another calendar"
	switch len(masters) {
	case 0:
		return fmt.Errorf("update %s: %s. The UID %s has no live series to move: its master is in the trash or gone. "+
			"Restore the master from the trash first, or edit the occurrence without --calendar", kind, reason, uid)
	case 1:
		return fmt.Errorf("update %s: %s. To move the whole series, run: chroncal %s update %d --calendar NAME", kind, reason, kind, masters[0].ID)
	}
	parts := make([]string, len(masters))
	for i, m := range masters {
		parts[i] = fmt.Sprintf("%d (calendar %q)", m.ID, calendarName(ctx, a, m.CalendarID))
	}
	what := "This occurrence can belong to a series"
	if recurrenceID != "" {
		what = fmt.Sprintf("This row is an old split override in calendar %q, and the UID has a series", calendarName(ctx, a, calendarID))
	}
	return fmt.Errorf("update %s: %s. %s in more than one calendar: %s. To move one series, run: chroncal %s update ID --calendar NAME",
		kind, reason, what, strings.Join(parts, ", "), kind)
}

// calendarName returns the name of a calendar, or its ID when the lookup
// fails.
func calendarName(ctx context.Context, a *app.App, calendarID int64) string {
	if c, err := a.Calendars.Get(ctx, calendarID); err == nil {
		return c.Name
	}
	return fmt.Sprint(calendarID)
}
