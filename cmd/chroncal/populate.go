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

func importEventFields(ctx context.Context, svc *event.Service, id int64, e event.Event) {
	if len(e.Alarms) > 0 {
		if err := svc.ReplaceAlarms(ctx, id, e.Alarms); err != nil {
			log.Printf("warning: import event %d: replace alarms: %v", id, err)
		}
	}
	if len(e.Attendees) > 0 {
		if err := svc.ReplaceAttendees(ctx, id, e.Attendees); err != nil {
			log.Printf("warning: import event %d: replace attendees: %v", id, err)
		}
	}
	if len(e.Attachments) > 0 {
		if err := svc.ReplaceAttachments(ctx, id, e.Attachments); err != nil {
			log.Printf("warning: import event %d: replace attachments: %v", id, err)
		}
	}
	if len(e.Comments) > 0 {
		if err := svc.ReplaceComments(ctx, id, e.Comments); err != nil {
			log.Printf("warning: import event %d: replace comments: %v", id, err)
		}
	}
	if len(e.Contacts) > 0 {
		if err := svc.ReplaceContacts(ctx, id, e.Contacts); err != nil {
			log.Printf("warning: import event %d: replace contacts: %v", id, err)
		}
	}
	if len(e.Resources) > 0 {
		if err := svc.ReplaceResources(ctx, id, e.Resources); err != nil {
			log.Printf("warning: import event %d: replace resources: %v", id, err)
		}
	}
	if len(e.Relations) > 0 {
		if err := svc.ReplaceRelations(ctx, id, e.Relations); err != nil {
			log.Printf("warning: import event %d: replace relations: %v", id, err)
		}
	}
	if len(e.XProperties) > 0 {
		if err := svc.ReplaceXProperties(ctx, id, e.XProperties); err != nil {
			log.Printf("warning: import event %d: replace x-properties: %v", id, err)
		}
	}
}

func importTodoFields(ctx context.Context, svc *todo.Service, id int64, t todo.Todo) {
	if len(t.Alarms) > 0 {
		if err := svc.ReplaceAlarms(ctx, id, t.Alarms); err != nil {
			log.Printf("warning: import todo %d: replace alarms: %v", id, err)
		}
	}
	if len(t.Attendees) > 0 {
		if err := svc.ReplaceAttendees(ctx, id, t.Attendees); err != nil {
			log.Printf("warning: import todo %d: replace attendees: %v", id, err)
		}
	}
	if len(t.Attachments) > 0 {
		if err := svc.ReplaceAttachments(ctx, id, t.Attachments); err != nil {
			log.Printf("warning: import todo %d: replace attachments: %v", id, err)
		}
	}
	if len(t.Comments) > 0 {
		if err := svc.ReplaceComments(ctx, id, t.Comments); err != nil {
			log.Printf("warning: import todo %d: replace comments: %v", id, err)
		}
	}
	if len(t.Contacts) > 0 {
		if err := svc.ReplaceContacts(ctx, id, t.Contacts); err != nil {
			log.Printf("warning: import todo %d: replace contacts: %v", id, err)
		}
	}
	if len(t.Resources) > 0 {
		if err := svc.ReplaceResources(ctx, id, t.Resources); err != nil {
			log.Printf("warning: import todo %d: replace resources: %v", id, err)
		}
	}
	if len(t.Relations) > 0 {
		if err := svc.ReplaceRelations(ctx, id, t.Relations); err != nil {
			log.Printf("warning: import todo %d: replace relations: %v", id, err)
		}
	}
	if len(t.XProperties) > 0 {
		if err := svc.ReplaceXProperties(ctx, id, t.XProperties); err != nil {
			log.Printf("warning: import todo %d: replace x-properties: %v", id, err)
		}
	}
}

func importJournalFields(ctx context.Context, svc *journal.Service, id int64, j journal.Journal) {
	if len(j.Attendees) > 0 {
		if err := svc.ReplaceAttendees(ctx, id, j.Attendees); err != nil {
			log.Printf("warning: import journal %d: replace attendees: %v", id, err)
		}
	}
	if len(j.Attachments) > 0 {
		if err := svc.ReplaceAttachments(ctx, id, j.Attachments); err != nil {
			log.Printf("warning: import journal %d: replace attachments: %v", id, err)
		}
	}
	if len(j.Comments) > 0 {
		if err := svc.ReplaceComments(ctx, id, j.Comments); err != nil {
			log.Printf("warning: import journal %d: replace comments: %v", id, err)
		}
	}
	if len(j.Contacts) > 0 {
		if err := svc.ReplaceContacts(ctx, id, j.Contacts); err != nil {
			log.Printf("warning: import journal %d: replace contacts: %v", id, err)
		}
	}
	if len(j.Relations) > 0 {
		if err := svc.ReplaceRelations(ctx, id, j.Relations); err != nil {
			log.Printf("warning: import journal %d: replace relations: %v", id, err)
		}
	}
	if len(j.XProperties) > 0 {
		if err := svc.ReplaceXProperties(ctx, id, j.XProperties); err != nil {
			log.Printf("warning: import journal %d: replace x-properties: %v", id, err)
		}
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
