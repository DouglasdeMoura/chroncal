package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

func safeText(s string) string {
	return textsafe.Display(s)
}

func writeAlarmCheckLine(w io.Writer, triggerAt time.Time, action, label string, isTodo bool) {
	suffix := ""
	if isTodo {
		suffix = " (todo)"
	}
	fmt.Fprintf(w, "%s\t%s\t%s%s\n", triggerAt.Local().Format("15:04"), action, safeText(label), suffix)
}

func writePendingAlarmLine(w io.Writer, id, triggerLocal, action, title string, isTodo bool, snoozed string) {
	suffix := snoozed
	if isTodo {
		suffix = " (todo)" + snoozed
	}
	fmt.Fprintf(w, "  [%s] %s\t%s\t%s%s\n", id, triggerLocal, action, safeText(title), suffix)
}

// writeMissedAlarmLine prints one missed alarm. The prefix tags the alarm
// kind: "" for a stored event alarm, "[todo] " for a todo, and "[default] "
// for a default alarm.
func writeMissedAlarmLine(w io.Writer, triggerAt time.Time, title, prefix string, age time.Duration) {
	fmt.Fprintf(w, "  %s  %s%s (%s ago)\n",
		triggerAt.Local().Format("2006-01-02 15:04"),
		prefix,
		safeText(title),
		age.Round(time.Minute),
	)
}

func writeSyncStatusLine(w io.Writer, status syncPkg.SyncStatus) {
	lastSync := "never"
	if status.LastSyncAt != "" {
		lastSync = safeText(status.LastSyncAt)
	}
	lastAttempt := "never"
	if status.LastSyncAttemptedAt != "" {
		lastAttempt = safeText(status.LastSyncAttemptedAt)
	}
	lastError := "-"
	if status.LastSyncError != "" {
		lastError = safeText(status.LastSyncError)
	}
	fmt.Fprintf(w, "  %-20s  last_sync=%s  last_attempt=%s  pending=%d  conflicts=%d  last_error=%s\n",
		safeText(status.CalendarName),
		lastSync,
		lastAttempt,
		status.PendingPush,
		status.Conflicts,
		lastError,
	)
	// A stuck href does not give the calendar a sync error, so this line is
	// the persistent signal for it. See issue #816.
	for _, h := range status.StuckHrefs {
		fmt.Fprintf(w, "    stuck href=%s  uid=%s  reason=%s  since=%s  (local copy kept; server changes not imported)\n",
			safeText(h.Href),
			safeText(h.UID),
			safeText(h.Reason),
			safeText(h.GaveUpAt),
		)
	}
	// A wait is no failure: the push of the row waits for another step
	// (issue #841).
	for _, wt := range status.Waiting {
		fmt.Fprintf(w, "    waiting uid=%s  on %s\n", safeText(wt.UID), safeText(wt.Reason))
	}
	// An edit that no series owns stays local. This line is its signal.
	for _, uid := range status.UnownedEdits {
		fmt.Fprintf(w, "    edit not pushed uid=%s  reason=%s  (local copy kept)\n",
			safeText(uid),
			storage.NoSeriesOwnerMsg,
		)
	}
	// A push failure keeps an edit local. This line is its signal.
	for _, f := range status.PushFailures {
		fmt.Fprintf(w, "    push failed uid=%s  type=%s  attempts=%d  error=%s  (local copy kept)\n",
			safeText(f.UID),
			safeText(f.OwnerType),
			f.Count,
			safeText(f.Error),
		)
	}
}

func writeSyncConflictLine(w io.Writer, conflict syncPkg.Conflict) {
	// A move conflict names the source calendar of the move, so the user
	// can tell it from an edit conflict (issue #841).
	moved := ""
	if conflict.MoveSourceCalendarID != 0 {
		moved = fmt.Sprintf("  moved-from-calendar=%d  move=%s", conflict.MoveSourceCalendarID, safeText(conflict.MoveState))
	}
	if conflict.Resolution != "" {
		fmt.Fprintf(w, "  #%d  type=%s  uid=%s  detected=%s  resolved=%s  resolution=%s%s\n",
			conflict.ID,
			safeText(conflict.OwnerType),
			safeText(conflict.UID),
			conflict.DetectedAt.Format("2006-01-02 15:04"),
			conflict.ResolvedAt.Format("2006-01-02 15:04"),
			safeText(conflict.Resolution),
			moved,
		)
		return
	}
	fmt.Fprintf(w, "  #%d  type=%s  uid=%s  detected=%s%s\n",
		conflict.ID,
		safeText(conflict.OwnerType),
		safeText(conflict.UID),
		conflict.DetectedAt.Format("2006-01-02 15:04"),
		moved,
	)
}

func writeSyncResult(outW, errW io.Writer, r *syncPkg.SyncResult) {
	fmt.Fprintf(outW, "  Calendar %d: pushed=%d pulled=%d deleted=%d conflicts=%d auto-resolved=%d skipped-conflicts=%d errors=%d warnings=%d\n",
		r.CalendarID, r.Pushed, r.Pulled, r.Deleted, r.Conflicts, r.AutoResolved, r.SkippedConflicts, len(r.Errors), len(r.Warnings))
	if r.SkippedPendingDeletes > 0 {
		fmt.Fprintf(outW, "    skipped-pending-deletes=%d\n", r.SkippedPendingDeletes)
	}
	for _, err := range r.Errors {
		fmt.Fprintf(errW, "    error: %s\n", safeText(err.Error()))
	}
}

func printDiscoveredCalendars(w io.Writer, accountName string, calendars []caldav.RemoteCalendar) {
	fmt.Fprintf(w, "Found %d calendar(s) on %s:\n\n", len(calendars), safeText(accountName))
	for i, cal := range calendars {
		components := "none"
		if len(cal.SupportedComponentSet) > 0 {
			components = strings.Join(cal.SupportedComponentSet, ", ")
		}
		fmt.Fprintf(w, "  %d. %s\n     Path: %s\n     Components: %s\n",
			i+1, safeText(cal.Name), safeText(cal.Path), safeText(components))
		if cal.Description != "" {
			fmt.Fprintf(w, "     Description: %s\n", safeText(cal.Description))
		}
		fmt.Fprintln(w)
	}
}
