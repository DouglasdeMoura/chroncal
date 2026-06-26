// Package softdelete holds reversible-delete logic shared by the event, todo,
// and journal services. Centralizing it keeps the subtle EXDATE-provenance
// restore contract (issue #86) encoded in exactly one place, so a correctness
// fix can no longer drift between the three domains.
package softdelete

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ExdateProvenance adapts a single domain's generated queries (bound to the
// active transaction, and to the uid/recurrenceID under restore) to the
// operations ClearMasterEXDATE needs. Each domain wires its sqlc methods into
// these closures.
type ExdateProvenance struct {
	// GetDeleteLog returns the provenance row id recorded when a delete added
	// the EXDATE for the override under restore. found is false (with a nil
	// error) when no provenance row exists — meaning the EXDATE arrived via
	// import (or a series delete) and must survive restore.
	GetDeleteLog func(ctx context.Context) (logID int64, found bool, err error)
	// GetMaster returns the master row's id and its serialized EXDATE list.
	// found is false (with a nil error) when the master row is gone.
	GetMaster func(ctx context.Context) (masterID int64, exdates string, found bool, err error)
	// UpdateExdates writes the filtered EXDATE list back to the master.
	UpdateExdates func(ctx context.Context, masterID int64, exdates string) error
	// DeleteDeleteLog removes the provenance row once its EXDATE is reversed
	// (or once the master it pointed at is gone).
	DeleteDeleteLog func(ctx context.Context, logID int64) error
}

// ClearMasterEXDATE removes the EXDATE entry for recurrenceID from the master
// identified by uid, reversing the exclusion an instance-delete added. It only
// strips EXDATEs that a delete recorded in the *_exdate_deletes provenance
// table; EXDATEs that arrived via import (or a series delete, which never adds
// one) have no provenance row and survive restore — otherwise a UID-wide
// restore would silently drop a legitimate imported EXDATE whose slot happens
// to match an override's recurrence_id (issue #86). A malformed recurrence_id
// is a data-integrity error and is propagated rather than swallowed. Callers
// must wire p to the same transaction that un-hides the override so the row is
// never visible-but-excluded.
func ClearMasterEXDATE(ctx context.Context, p ExdateProvenance, recurrenceID string) error {
	logID, found, err := p.GetDeleteLog(ctx)
	if err != nil {
		return fmt.Errorf("get exdate log: %w", err)
	}
	if !found {
		return nil
	}

	masterID, exdates, found, err := p.GetMaster(ctx)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}
	if !found {
		// Master gone; drop the now-orphaned provenance row.
		return p.DeleteDeleteLog(ctx, logID)
	}

	target, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return fmt.Errorf("parse recurrence_id %q: %w", recurrenceID, err)
	}
	existing := timeutil.ParseTimeList(exdates)
	filtered := timeutil.RemoveTimeFromList(existing, target)
	if len(filtered) != len(existing) {
		if err := p.UpdateExdates(ctx, masterID, timeutil.SerializeTimeList(filtered)); err != nil {
			return fmt.Errorf("update exdates: %w", err)
		}
	}
	if err := p.DeleteDeleteLog(ctx, logID); err != nil {
		return fmt.Errorf("delete exdate log: %w", err)
	}
	return nil
}
