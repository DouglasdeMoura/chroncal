package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
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

func writeMissedAlarmLine(w io.Writer, triggerAt time.Time, title string, isTodo bool, age time.Duration) {
	prefix := ""
	if isTodo {
		prefix = "[todo] "
	}
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
	fmt.Fprintf(w, "  %-20s  account=%-15s  last_sync=%s  last_attempt=%s  pending=%d  conflicts=%d  last_error=%s\n",
		safeText(status.CalendarName),
		safeText(status.AccountName),
		lastSync,
		lastAttempt,
		status.PendingPush,
		status.Conflicts,
		lastError,
	)
}

func writeSyncConflictLine(w io.Writer, conflict syncPkg.Conflict) {
	fmt.Fprintf(w, "  #%d  type=%s  uid=%s  detected=%s\n",
		conflict.ID,
		safeText(conflict.OwnerType),
		safeText(conflict.UID),
		conflict.DetectedAt.Format("2006-01-02 15:04"),
	)
}

func writeSyncResult(outW, errW io.Writer, r *syncPkg.SyncResult) {
	fmt.Fprintf(outW, "  Calendar %d: pushed=%d pulled=%d deleted=%d conflicts=%d errors=%d\n",
		r.CalendarID, r.Pushed, r.Pulled, r.Deleted, r.Conflicts, len(r.Errors))
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
