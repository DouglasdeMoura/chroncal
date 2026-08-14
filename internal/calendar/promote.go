package calendar

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// PromoteDefault is the single implementation of the default-calendar
// promotion invariant. When the current default calendar is among
// removedIDs, target must identify a calendar that survives. That calendar
// exists and is not itself removed. It becomes the new default atomically
// within qtx's transaction.
//
// Clear of the old default and set of the new one run in the caller's
// already-open transaction (qtx). The database then never observes a gone
// default. The survival check reads through that same transaction. A
// target removed inside this operation — or one that never existed in it —
// fails before any default is cleared. The caller then rolls back.
//
// Each caller decides whether a promotion is required (whether the
// current default is among the calendars deleted). It resolves target to
// a concrete calendar ID before the call. Path-based resolve for discovered
// collections in the account service is included. removedIDs is the set of
// calendar IDs deleted in this transaction. target must not be among
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
