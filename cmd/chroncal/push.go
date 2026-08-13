package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

const opportunisticPushTimeout = 30 * time.Second

// pushCalendarAfterWrite opportunistically pushes pending changes for one
// calendar upstream after a CLI write. It is best-effort: failures are
// reported to outW but do not affect the command's exit status — the dirty
// flag survives and the periodic `chroncal service run` tick will retry.
// Local-only calendars (no CalDAV account linked) are silent no-ops.
// warnW receives import warnings (see reportOpportunisticPush); call sites
// pass cmd.ErrOrStderr() so JSON callers that discard outW still see them.
//
// It is a package var so tests can substitute a recording stub to assert
// that write paths opportunistically push, without standing up a CalDAV
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

	result, err := svc.PushCalendar(ctx, calendarID, syncPkg.ConflictServerWins)
	if err != nil {
		fmt.Fprintf(outW, "note: auto-sync failed (%v); change will upload on next sync\n", err)
		return
	}
	reportOpportunisticPush(outW, warnW, cal.Name, result)
}

// reportOpportunisticPush renders the outcome of an opportunistic push. The
// engine runs with a discarded logger, so the result is the only place import
// warnings from a server-wins conflict import surface. They go to warnW — not
// outW — so JSON callers (which pass io.Discard to keep stdout clean) still
// see them on the ERROR stream, and so they never mix into stdout.
func reportOpportunisticPush(outW, warnW io.Writer, calName string, result *syncPkg.SyncResult) {
	fprintImportWarnings(warnW, result.Warnings)
	if result.Pushed == 0 && result.Deleted == 0 && len(result.Errors) == 0 {
		return
	}
	if len(result.Errors) > 0 {
		fmt.Fprintf(outW, "note: auto-sync partial (%d error(s)); change will retry on next sync\n", len(result.Errors))
		return
	}
	fmt.Fprintf(outW, "Synced to %s · pushed %d · deleted %d\n", calName, result.Pushed, result.Deleted)
}
