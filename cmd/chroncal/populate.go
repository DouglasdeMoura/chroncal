package main

import (
	"context"
	"log"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// The populate*Fields helpers wrap HydrateBestEffort for the CLI's read-only
// display paths. One unreadable relation degrades that field and warns. Every
// other relation is still populated. A stop at the first failure would
// print "attendees": null for attendees that exist. A script that reads
// the JSON cannot tell that apart from "this event has none".
//
// Anything that writes iCal — the export commands, CalDAV push — must call
// Hydrate instead and propagate the error. An amputated payload drops
// alarms and attendees from the file or from the server copy in silence.

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
