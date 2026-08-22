package journal

import (
	"context"

	"github.com/douglasdemoura/chroncal/internal/hydrate"
)

// Hydrate loads the transient relation slices onto j. See event.Service.Hydrate
// for the contract. VJOURNAL has no alarms or resources, so the set is smaller
// than the event and todo ones.
func (s *Service) Hydrate(ctx context.Context, j *Journal) error {
	return s.hydrate(ctx, j, true)
}

// HydrateBestEffort populates every relation it can and returns the joined
// errors for the ones it could not. It does not stop at the first failure.
//
// This is for read-only display paths. One unreadable relation should degrade
// that field alone. An early stop would leave every relation after it nil.
// A caller that renders JSON cannot tell that apart from "there are none".
// Anything that writes iCal must use Hydrate. A partial record pushed to a
// server overwrites the complete copy there.
func (s *Service) HydrateBestEffort(ctx context.Context, j *Journal) error {
	return s.hydrate(ctx, j, false)
}

// HydrateSkipUnreadable populates every relation it can and returns the names
// of the relations it could not load. It never fails. See
// event.Service.HydrateSkipUnreadable for the contract.
func (s *Service) HydrateSkipUnreadable(ctx context.Context, j *Journal) []string {
	return s.collect(ctx, j, false).Failed()
}

// collect loads every relation onto j through one Collector. Hydrate,
// HydrateBestEffort, and HydrateSkipUnreadable share it, so the relation set
// stays a single definition.
func (s *Service) collect(ctx context.Context, j *Journal, failFast bool) *hydrate.Collector {
	c := &hydrate.Collector{Kind: "journal", ID: j.ID, FailFast: failFast}
	hydrate.Rel(ctx, c, &j.Attendees, "attendees", s.ListAttendees)
	hydrate.Rel(ctx, c, &j.Attachments, "attachments", s.ListAttachments)
	hydrate.Rel(ctx, c, &j.Comments, "comments", s.ListComments)
	hydrate.Rel(ctx, c, &j.Contacts, "contacts", s.ListContacts)
	hydrate.Rel(ctx, c, &j.Relations, "relations", s.ListRelations)
	hydrate.Rel(ctx, c, &j.XProperties, "x-properties", s.ListXProperties)
	return c
}

func (s *Service) hydrate(ctx context.Context, j *Journal, failFast bool) error {
	return s.collect(ctx, j, failFast).Err()
}
