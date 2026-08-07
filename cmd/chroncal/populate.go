package main

import (
	"context"
	"log"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// The populate*Fields helpers wrap the domain services' Hydrate methods for the
// CLI's read-only display paths, where a missing relation degrades the output
// but is not worth aborting the command over. Anything that writes iCal — the
// export commands, CalDAV push — must call Hydrate directly and propagate the
// error instead: an amputated payload silently drops alarms and attendees from
// the file or the server copy.

func populateEventFields(ctx context.Context, svc *event.Service, e *event.Event) {
	if err := svc.Hydrate(ctx, e); err != nil {
		log.Printf("warning: %v", err)
	}
}

func populateTodoFields(ctx context.Context, svc *todo.Service, t *todo.Todo) {
	if err := svc.Hydrate(ctx, t); err != nil {
		log.Printf("warning: %v", err)
	}
}

func populateJournalFields(ctx context.Context, svc *journal.Service, j *journal.Journal) {
	if err := svc.Hydrate(ctx, j); err != nil {
		log.Printf("warning: %v", err)
	}
}
