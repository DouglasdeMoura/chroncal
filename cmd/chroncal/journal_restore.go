package main

import (
	"github.com/spf13/cobra"
)

func journalRestoreCmd() *cobra.Command {
	return newRestoreCmd(journalResource, verbHelp{
		short: "Restore a soft-deleted journal entry",
		long: `Restore clears the deletion marker on a soft-deleted journal entry
so it reappears in list and TUI views.

If the journal was synced to a remote server, restore marks it dirty so
the next sync cycle recreates it remotely.`,
		example: `  chroncal journal restore 3
  chroncal journal restore retro-uid`,
	})
}
