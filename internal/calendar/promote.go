package calendar

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// PromoteDefault is the single implementation of the default-calendar
// promotion invariant: whenever the current default calendar is among
// removedIDs, target must identify a surviving calendar — one that exists and
// is not itself being removed — and becomes the new default atomically within
// qtx's transaction.
//
// Clearing the old default and setting the new one run in the caller's
// already-open transaction (qtx), so the database never observes a missing
// default. Because the survival check reads through that same transaction, a
// target removed inside this operation — or one that never existed in it —
// fails before any default is cleared; the caller then rolls back.
//
// Each caller decides whether a promotion is required (i.e. whether the
// current default is among the calendars being deleted) and resolves target to
// a concrete calendar ID — including path-based resolution for discovered
// collections in the account service — before calling. removedIDs is the set of
// calendar IDs being deleted in this transaction, so target must not be among
// them.
func PromoteDefault(ctx context.Context, qtx *storage.Queries, removedIDs map[int64]struct{}, target int64) error {
	if target == 0 {
		return ErrDefaultCalendarRequiresPromotion
	}
	if _, removed := removedIDs[target]; removed {
		return ErrInvalidPromotionTarget
	}
	if _, err := qtx.GetCalendar(ctx, target); err != nil {
		return ErrInvalidPromotionTarget
	}
	if err := qtx.ClearDefaultCalendar(ctx); err != nil {
		return fmt.Errorf("clear default calendar: %w", err)
	}
	if err := qtx.SetCalendarAsDefault(ctx, target); err != nil {
		return fmt.Errorf("promote default calendar: %w", err)
	}
	return nil
}
