package event

import (
	"context"

	"github.com/douglasdemoura/chroncal/internal/hydrate"
)

// Hydrate loads the transient relation slices — alarms, attendees, attachments,
// comments, contacts, resources, relations, x-properties — onto e. They live in
// side tables rather than the events row. A value straight out of Get/List
// carries none of them. Any iCal built from it would omit them in silence.
//
// This is the single definition of "what a fully populated event is". Export
// (CLI and file), CalDAV push, and the display paths all call it. A relation
// added later then reaches every consumer at once. It fails on the first error
// instead of a partial value. A caller that pushes an amputated record
// overwrites the server copy with it.
func (s *Service) Hydrate(ctx context.Context, e *Event) error {
	return s.hydrate(ctx, e, true)
}

// HydrateBestEffort populates every relation it can and returns the joined
// errors for the ones it could not. It does not stop at the first failure.
//
// This is for read-only display paths. One unreadable relation should degrade
// that field alone. An early stop would leave every relation after it nil.
// A caller that renders JSON cannot tell that apart from "there are none".
// Any path that writes iCal must use Hydrate. A partial record pushed to a
// server overwrites the complete copy there.
func (s *Service) HydrateBestEffort(ctx context.Context, e *Event) error {
	return s.hydrate(ctx, e, false)
}

// HydrateSkipUnreadable populates every relation it can and returns the names
// of the relations it could not load. It never fails.
//
// This serves the CLI's ical export --skip-unreadable path. The user chose to
// keep a backup that misses unreadable relations, so the export names what
// each record lost. Any path that writes iCal for a push must use Hydrate.
// A partial record pushed to a server overwrites the complete copy there.
func (s *Service) HydrateSkipUnreadable(ctx context.Context, e *Event) []string {
	return s.collect(ctx, e, false).Failed()
}

// collect loads every relation onto e through one Collector. Hydrate,
// HydrateBestEffort, and HydrateSkipUnreadable share it, so the relation set
// stays a single definition.
func (s *Service) collect(ctx context.Context, e *Event, failFast bool) *hydrate.Collector {
	c := &hydrate.Collector{Kind: "event", ID: e.ID, FailFast: failFast}
	hydrate.Rel(ctx, c, &e.Alarms, "alarms", s.ListAlarms)
	hydrate.Rel(ctx, c, &e.Attendees, "attendees", s.ListAttendees)
	hydrate.Rel(ctx, c, &e.Attachments, "attachments", s.ListAttachments)
	hydrate.Rel(ctx, c, &e.Comments, "comments", s.ListComments)
	hydrate.Rel(ctx, c, &e.Contacts, "contacts", s.ListContacts)
	hydrate.Rel(ctx, c, &e.Resources, "resources", s.ListResources)
	hydrate.Rel(ctx, c, &e.Relations, "relations", s.ListRelations)
	hydrate.Rel(ctx, c, &e.XProperties, "x-properties", s.ListXProperties)
	return c
}

func (s *Service) hydrate(ctx context.Context, e *Event, failFast bool) error {
	return s.collect(ctx, e, failFast).Err()
}
