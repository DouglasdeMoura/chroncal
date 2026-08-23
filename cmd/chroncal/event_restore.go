package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func eventRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <id-or-uid>",
		Short: "Restore a soft-deleted event",
		Long: `Restore clears the deletion marker on a soft-deleted event so it
reappears in list and TUI views.

The event must have been deleted via chroncal (soft-delete, not purged).
Use 'events list --include-deleted' to see deletable candidates.

If the event was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		Example: `  chroncal event restore 42
  chroncal event restore my-event-uid`,
		Args: exactOneArg,
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
				if err := a.Events.RestoreByID(ctx, id); err != nil {
					if errors.Is(err, event.ErrNotDeleted) {
						return errNotFoundf("event %d not found (may have been purged)", id)
					}
					return fmt.Errorf("restore event: %w", err)
				}
				if outputFmt != "text" {
					return printOutput(w, map[string]any{"restored": true, "id": id})
				}
				fmt.Fprintf(w, "Restored event %d.\n", id)
				return nil
			}

			// UID path: restore every row sharing the UID.
			if err := a.Events.RestoreByUID(ctx, ref); err != nil {
				if errors.Is(err, event.ErrNotDeleted) {
					return errNotFoundf("event %q not found (may have been purged)", ref)
				}
				return fmt.Errorf("restore event: %w", err)
			}
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"restored": true, "uid": ref})
			}
			fmt.Fprintf(w, "Restored event(s) with uid %q.\n", safeText(ref))
			return nil
		},
	}
	return cmd
}
