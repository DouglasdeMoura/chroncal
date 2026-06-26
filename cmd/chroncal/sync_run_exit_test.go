package main

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/spf13/cobra"

	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// TestRenderSyncRunResultsExitsNonZeroOnPerPhaseErrors guards issue #359:
// renderSyncRunResults must return a non-nil error when any SyncResult
// carries per-phase errors so that `sync run` exits non-zero and scripts
// can detect a partial sync failure via exit code.
//
// Consistent with `ical import` (non-zero when failed > 0) and
// `sync reset` (non-zero when failed > 0).
func TestRenderSyncRunResultsExitsNonZeroOnPerPhaseErrors(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			orig := outputFmt
			outputFmt = format
			defer func() { outputFmt = orig }()

			results := []*syncPkg.SyncResult{
				{
					CalendarID: 1,
					Pushed:     2,
					Pulled:     1,
					Errors:     []error{fmt.Errorf("push phase: 503 Service Unavailable")},
				},
			}

			cmd := &cobra.Command{}
			var out, errOut bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)

			err := renderSyncRunResults(cmd, results, map[int64]string{1: "Work"})
			if err == nil {
				t.Fatalf("renderSyncRunResults(%s) returned nil; want non-nil error when SyncResult.Errors is non-empty", format)
			}
		})
	}
}

// TestRenderSyncRunResultsExitsZeroOnSuccess guards that renderSyncRunResults
// still returns nil when no per-phase errors occurred.
func TestRenderSyncRunResultsExitsZeroOnSuccess(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			orig := outputFmt
			outputFmt = format
			defer func() { outputFmt = orig }()

			results := []*syncPkg.SyncResult{
				{CalendarID: 1, Pushed: 2, Pulled: 1},
			}

			cmd := &cobra.Command{}
			var out, errOut bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)

			err := renderSyncRunResults(cmd, results, map[int64]string{1: "Work"})
			if err != nil {
				t.Fatalf("renderSyncRunResults(%s) returned %v; want nil when no errors", format, err)
			}
		})
	}
}
