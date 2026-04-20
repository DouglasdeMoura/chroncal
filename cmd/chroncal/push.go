package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

const opportunisticPushTimeout = 30 * time.Second

// pushCalendarAfterWrite opportunistically pushes pending changes for one
// calendar upstream after a CLI write. It is best-effort: failures are
// reported to w but do not affect the command's exit status — the dirty
// flag survives and the periodic `chroncal service run` tick will retry.
// Local-only calendars (no CalDAV account linked) are silent no-ops.
func pushCalendarAfterWrite(a *app.App, calendarID int64, w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), opportunisticPushTimeout)
	defer cancel()

	cal, err := a.Calendars.Get(ctx, calendarID)
	if err != nil || cal.AccountID == 0 {
		return
	}

	credStore, err := auth.NewCredentialStore(true)
	if err != nil {
		fmt.Fprintf(w, "note: skipped auto-sync (%v)\n", err)
		return
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, logger)

	result, err := svc.PushCalendar(ctx, calendarID, syncPkg.ConflictServerWins)
	if err != nil {
		fmt.Fprintf(w, "note: auto-sync failed (%v); change will upload on next sync\n", err)
		return
	}
	if result.Pushed == 0 && result.Deleted == 0 && len(result.Errors) == 0 {
		return
	}
	if len(result.Errors) > 0 {
		fmt.Fprintf(w, "note: auto-sync partial (%d error(s)); change will retry on next sync\n", len(result.Errors))
		return
	}
	fmt.Fprintf(w, "Synced to %s · pushed %d · deleted %d\n", cal.Name, result.Pushed, result.Deleted)
}
