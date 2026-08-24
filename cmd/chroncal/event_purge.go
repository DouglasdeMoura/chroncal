package main

import (
	"github.com/spf13/cobra"
)

func eventPurgeCmd() *cobra.Command {
	return newPurgeCmd(eventResource, verbHelp{
		short: "Hard-delete a single soft-deleted event",
		long: `Purge permanently removes one soft-deleted event from the database.

The event must already be soft-deleted. Purging a live event is refused;
use 'event delete' first. Purging is not reversible — child rows (alarms,
attendees, attachments, overrides) cascade.`,
		example: `  chroncal event purge 42
  chroncal event purge 42 --yes`,
	})
}

func eventPurgeDeletedCmd() *cobra.Command {
	return newPurgeDeletedCmd(eventResource, verbHelp{
		short: "Hard-delete soft-deleted events older than --older-than",
		long: `Purge permanently removes soft-deleted events from the database.

By default, only events soft-deleted more than 30 days ago are purged.
Use --older-than to pick a different age (e.g. 7d, 24h, 720h).

This operation is destructive and not reversible. Attachments and other
child rows cascade. Soft-delete protection is bypassed for anything
matching the age threshold.`,
		example: `  chroncal event purge-deleted                   # 30 days by default
  chroncal event purge-deleted --older-than 7d   # older than a week
  chroncal event purge-deleted --older-than 0s --yes  # purge everything soft-deleted`,
	})
}
