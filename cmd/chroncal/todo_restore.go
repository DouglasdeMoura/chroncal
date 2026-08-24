package main

import (
	"github.com/spf13/cobra"
)

func todoRestoreCmd() *cobra.Command {
	return newRestoreCmd(todoResource, verbHelp{
		short: "Restore a soft-deleted todo",
		long: `Restore clears the deletion marker on a soft-deleted todo so it
reappears in list and TUI views.

The todo must have been deleted via chroncal (soft-delete, not purged).

If the todo was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely (with a fresh resource URL).`,
		example: `  chroncal todo restore 7
  chroncal todo restore weekly-review-uid`,
	})
}
