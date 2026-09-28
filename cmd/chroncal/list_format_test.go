package main

import (
	"testing"

	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/spf13/cobra"
)

func setListFormatConfig(t *testing.T, format string, days int) {
	t.Helper()
	oldCfg := cfg
	cfg = config.Config{UI: config.UIConfig{ListFormat: format, EventListDays: days}}
	t.Cleanup(func() { cfg = oldCfg })
}

func newListFormatCmd(t *testing.T, args ...string) (*cobra.Command, *bool, *bool) {
	t.Helper()
	var compact, detail bool
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().BoolVar(&compact, "compact", false, "")
	cmd.Flags().BoolVar(&detail, "detail", false, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	return cmd, &compact, &detail
}

func TestListCompactUsesConfigWithoutFlags(t *testing.T) {
	cases := map[string]bool{
		"detail":    false,
		"compact":   true,
		" Compact ": true,
		"DETAIL":    false,
		"":          false,
	}
	for format, want := range cases {
		t.Run(format, func(t *testing.T) {
			setListFormatConfig(t, format, 30)
			cmd, compact, detail := newListFormatCmd(t)
			got, err := listCompact(cmd, *compact, *detail)
			if err != nil {
				t.Fatalf("listCompact: %v", err)
			}
			if got != want {
				t.Errorf("listCompact = %v, want %v", got, want)
			}
		})
	}
}

// TestListCompactRejectsInvalidConfig guards the review finding on PR 799.
// A bad ui.list_format fails the list command, not config.Load.
func TestListCompactRejectsInvalidConfig(t *testing.T) {
	setListFormatConfig(t, "table", 30)
	cmd, compact, detail := newListFormatCmd(t)
	if _, err := listCompact(cmd, *compact, *detail); err == nil {
		t.Fatal("listCompact accepted ui.list_format = table")
	}
}

func TestListCompactFlagOverridesInvalidConfig(t *testing.T) {
	setListFormatConfig(t, "table", 30)
	for _, tc := range []struct {
		flag string
		want bool
	}{
		{"--compact", true},
		{"--detail", false},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			cmd, compact, detail := newListFormatCmd(t, tc.flag)
			got, err := listCompact(cmd, *compact, *detail)
			if err != nil {
				t.Fatalf("listCompact with %s: %v", tc.flag, err)
			}
			if got != tc.want {
				t.Errorf("listCompact with %s = %v, want %v", tc.flag, got, tc.want)
			}
		})
	}
}

func TestParseEventListDateRangeRejectsInvalidDays(t *testing.T) {
	setListFormatConfig(t, "detail", 0)
	if _, _, err := parseEventListDateRange("2026-09-01", ""); err == nil {
		t.Fatal("parseEventListDateRange accepted ui.event_list_days = 0")
	}
	// An explicit --to does not use the window, so the bad value is ignored.
	if _, _, err := parseEventListDateRange("2026-09-01", "2026-09-10"); err != nil {
		t.Fatalf("parseEventListDateRange with --to: %v", err)
	}
}
