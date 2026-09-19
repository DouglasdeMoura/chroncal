package main

import "github.com/spf13/cobra"

// listCompact applies explicit command flags before the configured default.
// JSON output does not call this helper, so the setting affects text only.
func listCompact(cmd *cobra.Command, compact, detail bool) bool {
	if cmd.Flags().Changed("compact") {
		return compact
	}
	if cmd.Flags().Changed("detail") {
		return false
	}
	return cfg.UI.ListFormat == "compact"
}
