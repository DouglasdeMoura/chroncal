package main

import (
	"github.com/spf13/cobra"
)

func journalPurgeCmd() *cobra.Command {
	return newPurgeCmd(journalResource, verbHelp{
		short: "Hard-delete a single soft-deleted journal entry",
		long: `Purge permanently removes one soft-deleted journal entry from the
database.

The entry must already be soft-deleted. Purging a live entry is refused;
use 'journal delete' first. Purging is not reversible — child rows cascade.`,
		example: `  chroncal journal purge 3
  chroncal journal purge 3 --yes`,
	})
}

func journalPurgeDeletedCmd() *cobra.Command {
	return newPurgeDeletedCmd(journalResource, verbHelp{
		short: "Hard-delete soft-deleted journals older than --older-than",
		long: `Purge permanently removes soft-deleted journal entries from the
database. By default only rows soft-deleted more than 30 days ago are
purged. Use --older-than to pick a different age.

This operation is destructive and not reversible. Attachments and other
child rows cascade.`,
		example: `  chroncal journal purge-deleted
  chroncal journal purge-deleted --older-than 7d
  chroncal journal purge-deleted --older-than 0s --yes`,
	})
}
