package main

import (
	"context"
	"log"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

func populateEventFields(ctx context.Context, svc *event.Service, e *event.Event) {
	var err error
	if e.Alarms, err = svc.ListAlarms(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list alarms: %v", e.ID, err)
	}
	if e.Attendees, err = svc.ListAttendees(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list attendees: %v", e.ID, err)
	}
	if e.Attachments, err = svc.ListAttachments(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list attachments: %v", e.ID, err)
	}
	if e.Comments, err = svc.ListComments(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list comments: %v", e.ID, err)
	}
	if e.Contacts, err = svc.ListContacts(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list contacts: %v", e.ID, err)
	}
	if e.Resources, err = svc.ListResources(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list resources: %v", e.ID, err)
	}
	if e.Relations, err = svc.ListRelations(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list relations: %v", e.ID, err)
	}
	if e.XProperties, err = svc.ListXProperties(ctx, e.ID); err != nil {
		log.Printf("warning: event %d: list x-properties: %v", e.ID, err)
	}
}

func populateTodoFields(ctx context.Context, svc *todo.Service, t *todo.Todo) {
	var err error
	if t.Alarms, err = svc.ListAlarms(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list alarms: %v", t.ID, err)
	}
	if t.Attendees, err = svc.ListAttendees(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list attendees: %v", t.ID, err)
	}
	if t.Attachments, err = svc.ListAttachments(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list attachments: %v", t.ID, err)
	}
	if t.Comments, err = svc.ListComments(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list comments: %v", t.ID, err)
	}
	if t.Contacts, err = svc.ListContacts(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list contacts: %v", t.ID, err)
	}
	if t.Resources, err = svc.ListResources(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list resources: %v", t.ID, err)
	}
	if t.Relations, err = svc.ListRelations(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list relations: %v", t.ID, err)
	}
	if t.XProperties, err = svc.ListXProperties(ctx, t.ID); err != nil {
		log.Printf("warning: todo %d: list x-properties: %v", t.ID, err)
	}
}

func populateJournalFields(ctx context.Context, svc *journal.Service, j *journal.Journal) {
	var err error
	if j.Attendees, err = svc.ListAttendees(ctx, j.ID); err != nil {
		log.Printf("warning: journal %d: list attendees: %v", j.ID, err)
	}
	if j.Attachments, err = svc.ListAttachments(ctx, j.ID); err != nil {
		log.Printf("warning: journal %d: list attachments: %v", j.ID, err)
	}
	if j.Comments, err = svc.ListComments(ctx, j.ID); err != nil {
		log.Printf("warning: journal %d: list comments: %v", j.ID, err)
	}
	if j.Contacts, err = svc.ListContacts(ctx, j.ID); err != nil {
		log.Printf("warning: journal %d: list contacts: %v", j.ID, err)
	}
	if j.Relations, err = svc.ListRelations(ctx, j.ID); err != nil {
		log.Printf("warning: journal %d: list relations: %v", j.ID, err)
	}
	if j.XProperties, err = svc.ListXProperties(ctx, j.ID); err != nil {
		log.Printf("warning: journal %d: list x-properties: %v", j.ID, err)
	}
}
