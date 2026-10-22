// Package softdelete holds reversible-delete logic that the event, todo,
// and journal services share. The package keeps the EXDATE-provenance
// restore contract (issue #86) in one place. A correctness fix then cannot
// drift across the three domains.
package softdelete

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ExdateProvenance adapts one domain's generated queries to the operations
// ClearMasterEXDATE needs. The queries bind to the active transaction and
// to the uid/recurrenceID under restore. Each domain wires its sqlc methods
// into these closures.
type ExdateProvenance struct {
	// GetDeleteLog returns the provenance row id recorded when a delete added
	// the EXDATE for the override under restore. found is false (with a nil
	// error) when no provenance row exists. That means the EXDATE arrived via
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
// identified by uid. This reverses the exclusion that an instance-delete added.
// It only strips EXDATEs that a delete recorded in the *_exdate_deletes
// provenance table. EXDATEs that arrived via import (or a series delete, which
// never adds one) have no provenance row and survive restore.
//
// Otherwise a UID-wide restore would drop a legitimate imported EXDATE whose
// slot matches an override's recurrence_id (issue #86). A malformed
// recurrence_id is a data-integrity error. The function returns that error.
// Callers must wire p to the same transaction that un-hides the override so
// the row is never visible-but-excluded.
func ClearMasterEXDATE(ctx context.Context, p ExdateProvenance, recurrenceID string) error {
	logID, found, err := p.GetDeleteLog(ctx)
	if err != nil {
		return fmt.Errorf("get exdate log: %w", err)
	}
	if !found {
		return nil
	}

	return stripAndDrop(ctx, p.GetMaster, p.UpdateExdates, p.DeleteDeleteLog, logID, recurrenceID)
}

// stripAndDrop removes one copy of the EXDATE of recurrenceID from the master
// and drops the log logID. With no master, it drops the log only.
func stripAndDrop(ctx context.Context,
	getMaster func(ctx context.Context) (int64, string, bool, error),
	updateExdates func(ctx context.Context, masterID int64, exdates string) error,
	deleteLog func(ctx context.Context, logID int64) error,
	logID int64, recurrenceID string,
) error {
	masterID, exdates, found, err := getMaster(ctx)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}
	if !found {
		// Master gone; drop the now-orphaned provenance row.
		return deleteLog(ctx, logID)
	}
	target, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return fmt.Errorf("parse recurrence_id %q: %w", recurrenceID, err)
	}
	existing := timeutil.ParseTimeList(exdates)
	filtered := timeutil.RemoveTimeFromList(existing, target)
	if len(filtered) != len(existing) {
		if err := updateExdates(ctx, masterID, timeutil.SerializeTimeList(filtered)); err != nil {
			return fmt.Errorf("update exdates: %w", err)
		}
	}
	if err := deleteLog(ctx, logID); err != nil {
		return fmt.Errorf("delete exdate log: %w", err)
	}
	return nil
}

// OccurrenceLog is one occurrence log, as the caller read it before the
// restore of its rows.
type OccurrenceLog struct {
	ID int64
	// HiddenIDs lists the override rows that the deletes of the occurrence
	// hid. The list can be empty: a delete of an occurrence with no stored
	// override.
	HiddenIDs []int64
}

// OccurrenceRelease adapts the queries of one domain to the operations of
// ReleaseOccurrenceRows. The closures bind to the active transaction. The
// fields of ExdateProvenance do the same jobs as there. GetDeleteLog is not
// used: the caller passes the log.
type OccurrenceRelease struct {
	ExdateProvenance
	// SetHiddenIDs writes the shorter row list of the log logID.
	SetHiddenIDs func(ctx context.Context, logID int64, ids []int64) error
}

// ReleaseOccurrenceRows releases the rows of rowIDs from log, the occurrence
// log of recurrenceID. Four restore paths call it. These are the undo, the
// restore by ID of an override, the edit that revives an override, and the
// trash restore. The rules:
//  1. rowIDs that the log does not name give no change, when the log names
//     rows. The log records another delete of the occurrence. A log with an
//     empty list names no row: a delete with no stored override, or a log
//     from before the list (migration 057). Its release goes on to rule 3.
//  2. With rows of the log left, the log keeps them, and the EXDATE stays.
//     The rows of another delete stay hidden.
//  3. With no row left, the release removes one copy of the EXDATE that the
//     log recorded, and it drops the log. An imported EXDATE has no log, so
//     it stays (issue #86).
//
// Empty rowIDs release a delete that hid no row. The caller then must know
// that log is the log of that delete.
func ReleaseOccurrenceRows(ctx context.Context, p OccurrenceRelease, log OccurrenceLog, rowIDs []int64, recurrenceID string) error {
	release := make(map[int64]bool, len(rowIDs))
	for _, id := range rowIDs {
		release[id] = true
	}
	rest := make([]int64, 0, len(log.HiddenIDs))
	named := false
	for _, id := range log.HiddenIDs {
		if release[id] {
			named = true
			continue
		}
		rest = append(rest, id)
	}
	if len(rowIDs) > 0 && len(log.HiddenIDs) > 0 && !named {
		return nil
	}
	if len(rest) > 0 {
		if err := p.SetHiddenIDs(ctx, log.ID, rest); err != nil {
			return fmt.Errorf("update exdate log: %w", err)
		}
		return nil
	}
	return stripAndDrop(ctx, p.GetMaster, p.UpdateExdates, p.DeleteDeleteLog, log.ID, recurrenceID)
}
