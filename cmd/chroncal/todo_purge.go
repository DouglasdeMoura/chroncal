package main

import (
	"github.com/spf13/cobra"
)

func todoPurgeCmd() *cobra.Command {
	return newPurgeCmd(todoResource, verbHelp{
		short: "Hard-delete a single soft-deleted todo",
		long: `Purge permanently removes one soft-deleted todo from the database.

The todo must already be soft-deleted. Purging a live todo is refused;
use 'todo delete' first. Purging is not reversible — child rows cascade.`,
		example: `  chroncal todo purge 7
  chroncal todo purge 7 --yes`,
	})
}

func todoPurgeDeletedCmd() *cobra.Command {
	return newPurgeDeletedCmd(todoResource, verbHelp{
		short: "Hard-delete soft-deleted todos older than --older-than",
		long: `Purge permanently removes soft-deleted todos from the database.

By default, only todos soft-deleted more than 30 days ago are purged.
Use --older-than to pick a different age (e.g. 7d, 24h, 720h).

This operation is destructive and not reversible. Attachments and other
child rows cascade.`,
		example: `  chroncal todo purge-deleted                   # 30 days by default
  chroncal todo purge-deleted --older-than 7d   # older than a week
  chroncal todo purge-deleted --older-than 0s --yes  # purge everything soft-deleted`,
	})
}
