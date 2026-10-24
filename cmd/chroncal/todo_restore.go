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

If the todo was synced to a remote server, restore marks it dirty
and pushes the calendar at once, as a delete does. The push creates
the todo on the server again (with a fresh resource URL).
When the push fails, the next sync sends it.`,
		example: `  chroncal todo restore 7
  chroncal todo restore weekly-review-uid`,
	})
}
