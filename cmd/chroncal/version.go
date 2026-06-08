package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build metadata, overridden at release time via -ldflags
//
//	-X main.version=... -X main.commit=... -X main.date=...
//
// (see .goreleaser.yml). The defaults apply to `go install` / local builds.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func init() {
	// Wire cobra's built-in --version flag and keep its output identical to
	// the `version` subcommand's text form.
	rootCmd.Version = version
	rootCmd.SetVersionTemplate("chroncal {{.Version}}\n")
	rootCmd.AddCommand(versionCmd())
}

type jsonVersion struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(out, jsonVersion{
					Version:   version,
					Commit:    commit,
					Date:      date,
					GoVersion: runtime.Version(),
					OS:        runtime.GOOS,
					Arch:      runtime.GOARCH,
				})
			}
			fmt.Fprintf(out, "chroncal %s\n", version)
			fmt.Fprintf(out, "commit:  %s\n", commit)
			fmt.Fprintf(out, "built:   %s\n", date)
			fmt.Fprintf(out, "go:      %s (%s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
