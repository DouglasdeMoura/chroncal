package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
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

	// del soft-deletes one row by ID. delSeries soft-deletes a master row
	// and every override that shares the UID.
	del       func(ctx context.Context, a *app.App, id int64) error
	delSeries func(ctx context.Context, a *app.App, uid string) error

	// restoreByID clears the deletion marker on one row. restoreByUID
	// clears it on every row that shares the UID.
	restoreByID  func(ctx context.Context, a *app.App, id int64) error
	restoreByUID func(ctx context.Context, a *app.App, uid string) error

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

// reportDeleted writes one delete result. Machine output prints the JSON
// payload. Text output prints the line. Both paths then push the changed
// calendar.
func reportDeleted(cmd *cobra.Command, a *app.App, w io.Writer, calendarID int64, payload map[string]any, line string) error {
	if outputFmt != "text" {
		if err := printOutput(w, payload); err != nil {
			return err
		}
		opportunisticPush(a, calendarID, cmd)
		return nil
	}
	fmt.Fprint(w, line)
	opportunisticPush(a, calendarID, cmd)
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
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			if series && recurrenceID != "" {
				return errInvalidInputf("--series and --recurrence-id are mutually exclusive")
			}

			target, err := r.resolve(ctx, a, args[0], recurrenceID)
			if err != nil {
				return fmt.Errorf("get %s: %w", r.name, err)
			}

			question := fmt.Sprintf("Delete %s %q?", r.name, safeText(target.Summary))
			switch {
			case series:
				question = fmt.Sprintf("Delete the entire recurring series %q (master + all overrides)?", safeText(target.Summary))
			case recurrenceID != "":
				question = fmt.Sprintf("Delete override instance of %q at %s?", safeText(target.Summary), recurrenceID)
			}
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			if series {
				if err := r.delSeries(ctx, a, target.UID); err != nil {
					return fmt.Errorf("delete series: %w", err)
				}
				return reportDeleted(cmd, a, w, target.CalendarID,
					map[string]any{"deleted": true, "uid": target.UID, "series": true},
					fmt.Sprintf("Deleted %s series %q.\n", r.name, safeText(target.UID)))
			}

			if err := r.del(ctx, a, target.ID); err != nil {
				return fmt.Errorf("delete %s: %w", r.name, err)
			}
			return reportDeleted(cmd, a, w, target.CalendarID,
				map[string]any{"deleted": true, "id": target.ID},
				fmt.Sprintf("Deleted %s %d.\n", r.name, target.ID))
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "target a specific override instance (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	addConfirmFlag(cmd)
	return cmd
}

// newRestoreCmd builds the restore verb for one resource. A numeric
// reference restores one row. A UID reference restores every row that
// shares the UID. A live or missing row reports "not_found".
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
				if err := r.restoreByID(ctx, a, id); err != nil {
					if errors.Is(err, r.errNotDeleted) {
						return errNotFoundf("%s %d not found (may have been purged)", r.name, id)
					}
					return fmt.Errorf("restore %s: %w", r.name, err)
				}
				if outputFmt != "text" {
					return printOutput(w, map[string]any{"restored": true, "id": id})
				}
				fmt.Fprintf(w, "Restored %s %d.\n", r.name, id)
				return nil
			}

			// UID path: restore every row that shares the UID.
			if err := r.restoreByUID(ctx, a, ref); err != nil {
				if errors.Is(err, r.errNotDeleted) {
					return errNotFoundf("%s %q not found (may have been purged)", r.name, ref)
				}
				return fmt.Errorf("restore %s: %w", r.name, err)
			}
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"restored": true, "uid": ref})
			}
			fmt.Fprintf(w, "Restored %s(s) with uid %q.\n", r.name, safeText(ref))
			return nil
		},
	}
	return cmd
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
