package main

import (
	"context"
	"fmt"
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

// importEventFields attaches the transient child collections (alarms,
// attendees, ...) to a freshly imported event. Each failure is returned as a
// warning rather than only logged, so callers can surface partially-dropped
// child data in the import summary instead of silently reporting success.
func importEventFields(ctx context.Context, svc *event.Service, id int64, e event.Event) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import event %d: replace %s: %v", id, field, err))
		}
	}
	if len(e.Alarms) > 0 {
		add("alarms", svc.ReplaceAlarms(ctx, id, e.Alarms))
	}
	if len(e.Attendees) > 0 {
		add("attendees", svc.ReplaceAttendees(ctx, id, e.Attendees))
	}
	if len(e.Attachments) > 0 {
		add("attachments", svc.ReplaceAttachments(ctx, id, e.Attachments))
	}
	if len(e.Comments) > 0 {
		add("comments", svc.ReplaceComments(ctx, id, e.Comments))
	}
	if len(e.Contacts) > 0 {
		add("contacts", svc.ReplaceContacts(ctx, id, e.Contacts))
	}
	if len(e.Resources) > 0 {
		add("resources", svc.ReplaceResources(ctx, id, e.Resources))
	}
	if len(e.Relations) > 0 {
		add("relations", svc.ReplaceRelations(ctx, id, e.Relations))
	}
	if len(e.XProperties) > 0 {
		add("x-properties", svc.ReplaceXProperties(ctx, id, e.XProperties))
	}
	return warns
}

// importTodoFields mirrors importEventFields for todos.
func importTodoFields(ctx context.Context, svc *todo.Service, id int64, t todo.Todo) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import todo %d: replace %s: %v", id, field, err))
		}
	}
	if len(t.Alarms) > 0 {
		add("alarms", svc.ReplaceAlarms(ctx, id, t.Alarms))
	}
	if len(t.Attendees) > 0 {
		add("attendees", svc.ReplaceAttendees(ctx, id, t.Attendees))
	}
	if len(t.Attachments) > 0 {
		add("attachments", svc.ReplaceAttachments(ctx, id, t.Attachments))
	}
	if len(t.Comments) > 0 {
		add("comments", svc.ReplaceComments(ctx, id, t.Comments))
	}
	if len(t.Contacts) > 0 {
		add("contacts", svc.ReplaceContacts(ctx, id, t.Contacts))
	}
	if len(t.Resources) > 0 {
		add("resources", svc.ReplaceResources(ctx, id, t.Resources))
	}
	if len(t.Relations) > 0 {
		add("relations", svc.ReplaceRelations(ctx, id, t.Relations))
	}
	if len(t.XProperties) > 0 {
		add("x-properties", svc.ReplaceXProperties(ctx, id, t.XProperties))
	}
	return warns
}

// importJournalFields mirrors importEventFields for journals.
func importJournalFields(ctx context.Context, svc *journal.Service, id int64, j journal.Journal) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import journal %d: replace %s: %v", id, field, err))
		}
	}
	if len(j.Attendees) > 0 {
		add("attendees", svc.ReplaceAttendees(ctx, id, j.Attendees))
	}
	if len(j.Attachments) > 0 {
		add("attachments", svc.ReplaceAttachments(ctx, id, j.Attachments))
	}
	if len(j.Comments) > 0 {
		add("comments", svc.ReplaceComments(ctx, id, j.Comments))
	}
	if len(j.Contacts) > 0 {
		add("contacts", svc.ReplaceContacts(ctx, id, j.Contacts))
	}
	if len(j.Relations) > 0 {
		add("relations", svc.ReplaceRelations(ctx, id, j.Relations))
	}
	if len(j.XProperties) > 0 {
		add("x-properties", svc.ReplaceXProperties(ctx, id, j.XProperties))
	}
	return warns
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
