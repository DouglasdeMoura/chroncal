// Package hydrate collapses the per-relation "load, assign, record error"
// blocks that the event, todo, and journal Hydrate and HydrateBestEffort
// methods share.
//
// Every relation loader has the same shape: func(ctx, id) ([]T, error).
// Before this package, each service repeated a 6-line if/else per relation.
// A copied block could assign the wrong field or drop the fail-fast return.
// Tests could still pass. Rel makes each relation a single line. It keeps
// the three services in parallel form.
package hydrate

import (
	"context"
	"errors"
	"fmt"
)

// RelFailure identifies one relation that failed to load.
type RelFailure struct {
	Kind     string // record kind: "event", "todo", "journal"
	ID       int64  // record row ID
	Relation string // relation name as passed to Rel
	Cause    error  // loader error
}

// HydrationError reports one or more failed relation loads. It wraps the
// joined causes and also carries each failure in structured form, so a
// caller can name the broken relation without parsing text.
type HydrationError struct {
	Err      error
	Failures []RelFailure
}

func (h *HydrationError) Error() string { return h.Err.Error() }

func (h *HydrationError) Unwrap() error { return h.Err }

// Collector tracks relation-load errors for a single record.
type Collector struct {
	Kind     string // record kind for error messages: "event", "todo", "journal"
	ID       int64  // record row ID, passed to every loader
	FailFast bool   // stop at the first error instead of loading the rest

	failures []RelFailure
}

// Rel loads one relation via load(ctx, c.ID) and assigns the result to *dst.
// On failure it records "<kind> <id> <relation>: <cause>" and leaves *dst
// unchanged. In fail-fast mode every later Rel call is a no-op. A record
// that failed to hydrate is then not completed by later loaders.
//
// It is a free function because Go methods cannot have type parameters.
func Rel[T any](ctx context.Context, c *Collector, dst *[]T, rel string, load func(context.Context, int64) ([]T, error)) {
	if c.FailFast && len(c.failures) > 0 {
		return
	}
	v, err := load(ctx, c.ID)
	if err != nil {
		c.failures = append(c.failures, RelFailure{Kind: c.Kind, ID: c.ID, Relation: rel, Cause: err})
		return
	}
	*dst = v
}

// Failures returns one entry per failed relation load.
func (c *Collector) Failures() []RelFailure {
	return c.failures
}

// Err returns the recorded errors joined via errors.Join, or nil when every
// relation loaded. A non-nil result is always a *HydrationError, so callers
// can read the failed relations in structured form via errors.As.
func (c *Collector) Err() error {
	if len(c.failures) == 0 {
		return nil
	}
	errs := make([]error, len(c.failures))
	for i, f := range c.failures {
		errs[i] = fmt.Errorf("%s %d %s: %w", f.Kind, f.ID, f.Relation, f.Cause)
	}
	return &HydrationError{Err: errors.Join(errs...), Failures: c.failures}
}
