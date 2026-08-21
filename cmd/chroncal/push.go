package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

const opportunisticPushTimeout = 30 * time.Second

// opportunisticPush is what every write path calls. It derives the two
// streams once. stdout is the human sync note (discarded in JSON mode so
// nothing trails the JSON object, issue #255). stderr is import warnings.
// The ~27 call sites then do not repeat that wiring. The
// pushCalendarAfterWrite seam below stays a package var. Tests can stub
// the push while this derivation remains real code under test.
func opportunisticPush(a *app.App, calendarID int64, cmd *cobra.Command) {
	outW := cmd.OutOrStdout()
	if outputFmt != "text" {
		outW = io.Discard
	}
	pushCalendarAfterWrite(a, calendarID, outW, cmd.ErrOrStderr())
}

// pushCalendarAfterWrite opportunistically pushes unpushed changes for one
// calendar upstream after a CLI write. It is best-effort. Failures are
// reported to outW but do not affect the command's exit status. The dirty
// flag survives. The periodic `chroncal service run` tick will retry.
// Local-only calendars (no CalDAV account linked) are silent no-ops.
// warnW receives import warnings (see reportOpportunisticPush). Call sites
// pass cmd.ErrOrStderr(). JSON callers that discard outW still see them.
//
// It is a package var so tests can substitute a record stub. Tests then
// assert that write paths opportunistically push, with no CalDAV
// server.
var pushCalendarAfterWrite = func(a *app.App, calendarID int64, outW, warnW io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), opportunisticPushTimeout)
	defer cancel()

	cal, err := a.Calendars.Get(ctx, calendarID)
	if err != nil || cal.AccountID == 0 {
		return
	}

	credStore, err := auth.NewCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
	if err != nil {
		fmt.Fprintf(outW, "note: skipped auto-sync (%v)\n", err)
		return
	}

	svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, nil)

	// syncStrategy() defaults to ConflictServerWins. The config key
	// sync.conflict_strategy (env CHRONCAL_SYNC_CONFLICT_STRATEGY) set to
	// "prompt" opts every opportunistic push into prompt mode instead.
	result, err := svc.PushCalendar(ctx, calendarID, syncStrategy())
	if err != nil {
		fmt.Fprintf(outW, "note: auto-sync failed (%v); change will upload on next sync\n", err)
		return
	}
	reportOpportunisticPush(outW, warnW, cal.Name, result)
}

// reportOpportunisticPush renders the outcome of an opportunistic push. The
// engine runs with a discarded logger. The result is then the only place import
// warnings from a server-wins conflict import surface. They go to warnW, not
// outW. JSON callers (which pass io.Discard to keep stdout clean) still
// see them on the ERROR stream. They never mix into stdout.
//
// A server-wins conflict replaces the just-written local row with the server
// version. That overwrite is silent by design (the push converges), so the
// result's conflict count is the only signal it happened. reportOpportunisticPush
// prints one note per count on warnW with the resolve command. Without that
// note an edit appears to save and then vanishes (issue #610).
func reportOpportunisticPush(outW, warnW io.Writer, calName string, result *syncPkg.SyncResult) {
	fprintImportWarnings(warnW, result.Warnings)
	if result.Conflicts > 0 {
		fmt.Fprintf(warnW, "note: %d local change(s) conflicted with the server and were replaced by server versions; see chroncal sync conflicts\n", result.Conflicts)
	}
	if result.Pushed == 0 && result.Deleted == 0 && len(result.Errors) == 0 {
		return
	}
	if len(result.Errors) > 0 {
		fmt.Fprintf(outW, "note: auto-sync partial (%d error(s)); change will retry on next sync\n", len(result.Errors))
		return
	}
	fmt.Fprintf(outW, "Synced to %s · pushed %d · deleted %d\n", calName, result.Pushed, result.Deleted)
}
