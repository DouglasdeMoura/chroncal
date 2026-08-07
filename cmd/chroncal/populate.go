package main

import (
	"context"
	"log"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// The populate*Fields helpers wrap HydrateBestEffort for the CLI's read-only
// display paths: one unreadable relation degrades that field and warns, while
// every other relation is still populated. Stopping at the first failure would
// print "attendees": null for attendees that exist, which a script consuming
// the JSON cannot tell apart from "this event has none".
//
// Anything that writes iCal — the export commands, CalDAV push — must call
// Hydrate instead and propagate the error: an amputated payload silently drops
// alarms and attendees from the file or from the server copy.

func populateEventFields(ctx context.Context, svc *event.Service, e *event.Event) {
	if err := svc.HydrateBestEffort(ctx, e); err != nil {
		log.Printf("warning: %v", err)
	}
}

func populateTodoFields(ctx context.Context, svc *todo.Service, t *todo.Todo) {
	if err := svc.HydrateBestEffort(ctx, t); err != nil {
		log.Printf("warning: %v", err)
	}
}

func populateJournalFields(ctx context.Context, svc *journal.Service, j *journal.Journal) {
	if err := svc.HydrateBestEffort(ctx, j); err != nil {
		log.Printf("warning: %v", err)
	}
}
