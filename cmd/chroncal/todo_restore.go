package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

func todoRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <id-or-uid>",
		Short: "Restore a soft-deleted todo",
		Long: `Restore clears the deletion marker on a soft-deleted todo so it
reappears in list and TUI views.

The todo must have been deleted via chroncal (soft-delete, not purged).

If the todo was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		Example: `  chroncal todo restore 7
  chroncal todo restore weekly-review-uid`,
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
				if err := a.Todos.RestoreByID(ctx, id); err != nil {
					if errors.Is(err, todo.ErrNotDeleted) {
						return fmt.Errorf("todo %d not found (may have been purged)", id)
					}
					return fmt.Errorf("restore todo: %w", err)
				}
				if outputFmt != "text" {
					return printOutput(w, map[string]any{"restored": true, "id": id})
				}
				fmt.Fprintf(w, "Restored todo %d.\n", id)
				return nil
			}

			if err := a.Todos.RestoreByUID(ctx, ref); err != nil {
				if errors.Is(err, todo.ErrNotDeleted) {
					return fmt.Errorf("todo %q not found (may have been purged)", ref)
				}
				return fmt.Errorf("restore todo: %w", err)
			}
			if outputFmt != "text" {
				return printOutput(w, map[string]any{"restored": true, "uid": ref})
			}
			fmt.Fprintf(w, "Restored todo(s) with uid %q.\n", safeText(ref))
			return nil
		},
	}
	return cmd
}
