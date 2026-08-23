package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func eventPurgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purge <id>",
		Short: "Hard-delete a single soft-deleted event",
		Long: `Purge permanently removes one soft-deleted event from the database.

The event must already be soft-deleted. Purging a live event is refused;
use 'event delete' first. Purging is not reversible — child rows (alarms,
attendees, attachments, overrides) cascade.`,
		Example: `  chroncal event purge 42
  chroncal event purge 42 --yes`,
		Args: exactOneArg,
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

			e, err := a.Events.GetIncludingDeleted(ctx, id)
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}
			if e.DeletedAt == nil {
				return fmt.Errorf("event %d is live; run 'event delete %d' first", id, id)
			}

			question := fmt.Sprintf("Purge event %q (id %d)? This cannot be undone.", safeText(e.Title), id)
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			if err := a.Events.PurgeByID(ctx, id); err != nil {
				if errors.Is(err, event.ErrNotDeleted) {
					return errNotFoundf("event %d not found or not soft-deleted", id)
				}
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": true, "id": id})
			}
			fmt.Fprintf(w, "Purged event %d.\n", id)
			return nil
		},
	}
	addConfirmFlag(cmd)
	return cmd
}

func eventPurgeDeletedCmd() *cobra.Command {
	var olderThanStr string
	cmd := &cobra.Command{
		Use:   "purge-deleted",
		Short: "Hard-delete soft-deleted events older than --older-than",
		Long: `Purge permanently removes soft-deleted events from the database.

By default, only events soft-deleted more than 30 days ago are purged.
Use --older-than to pick a different age (e.g. 7d, 24h, 720h).

This operation is destructive and not reversible. Attachments and other
child rows cascade. Soft-delete protection is bypassed for anything
matching the age threshold.`,
		Example: `  chroncal event purge-deleted                   # 30 days by default
  chroncal event purge-deleted --older-than 7d   # older than a week
  chroncal event purge-deleted --older-than 0s --yes  # purge everything soft-deleted`,
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
				prompt := fmt.Sprintf("Purge ALL events soft-deleted in the last %s? This cannot be undone.", d)
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
			n, err := a.Events.PurgeDeleted(ctx, cutoff)
			if err != nil {
				return fmt.Errorf("purge: %w", err)
			}

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"purged": n, "older_than": d.String()})
			}
			fmt.Fprintf(w, "Purged %d event(s) soft-deleted more than %s ago.\n", n, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&olderThanStr, "older-than", "720h", "age threshold (Go duration, e.g. 30d=720h, 168h=7 days)")
	addConfirmFlag(cmd)
	return cmd
}
