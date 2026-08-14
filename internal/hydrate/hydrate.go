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

// Collector tracks relation-load errors for a single record.
type Collector struct {
	Kind     string // record kind for error messages: "event", "todo", "journal"
	ID       int64  // record row ID, passed to every loader
	FailFast bool   // stop at the first error instead of loading the rest

	errs []error
}

// Rel loads one relation via load(ctx, c.ID) and assigns the result to *dst.
// On failure it records "<kind> <id> <relation>: <cause>" and leaves *dst
// unchanged. In fail-fast mode every later Rel call is a no-op. A record
// that failed to hydrate is then not completed by later loaders.
//
// It is a free function because Go methods cannot have type parameters.
func Rel[T any](ctx context.Context, c *Collector, dst *[]T, rel string, load func(context.Context, int64) ([]T, error)) {
	if c.FailFast && len(c.errs) > 0 {
		return
	}
	v, err := load(ctx, c.ID)
	if err != nil {
		c.errs = append(c.errs, fmt.Errorf("%s %d %s: %w", c.Kind, c.ID, rel, err))
		return
	}
	*dst = v
}

// Err returns the recorded errors joined via errors.Join, or nil when every
// relation loaded.
func (c *Collector) Err() error {
	return errors.Join(c.errs...)
}
