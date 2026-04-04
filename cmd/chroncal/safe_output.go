package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/caldav"
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
