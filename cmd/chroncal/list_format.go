package main

import (
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/spf13/cobra"
)

// listCompact applies explicit command flags before the configured default.
// JSON output does not call this helper, so the setting affects text only.
// An invalid ui.list_format is an error only when no flag overrides it.
func listCompact(cmd *cobra.Command, compact, detail bool) (bool, error) {
	if cmd.Flags().Changed("compact") {
		return compact, nil
	}
	if cmd.Flags().Changed("detail") {
		return false, nil
	}
	return config.ParseListFormat(cfg.UI.ListFormat)
}
