package main

import (
	"github.com/spf13/cobra"
)

func todoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "todo",
		Short: "Manage todos",
		Long: `Create, organize, and complete tasks stored in chroncal.

Todos support due dates, start dates, progress, recurrence, alarms, and
the same calendar organization model used by events.`,
		Example: `  chroncal todo list
  chroncal todo add "Ship release" --due 2026-04-15
  chroncal todo complete 7`,
		Args: rejectUnknownSubcommand,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		todoListCmd(), todoGetCmd(), todoAddCmd(), todoUpdateCmd(),
		todoDeleteCmd(), todoCompleteCmd(), todoSearchCmd(),
		todoRestoreCmd(), todoPurgeCmd(), todoPurgeDeletedCmd(),
	)
	return cmd
}
