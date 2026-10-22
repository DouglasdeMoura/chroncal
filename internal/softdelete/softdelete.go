// Package softdelete holds reversible-delete logic that the event, todo,
// and journal services share. The package keeps the EXDATE-provenance
// restore contract (issue #86) in one place. A correctness fix then cannot
// drift across the three domains.
package softdelete

import (
	"context"
	"fmt"
	"time"

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

	return stripAndDrop(ctx, p, logID, recurrenceID)
}

// stripAndDrop removes one copy of the EXDATE of recurrenceID from the master
// of p and drops the log logID. With no master, it drops the log only.
func stripAndDrop(ctx context.Context, p ExdateProvenance, logID int64, recurrenceID string) error {
	masterID, exdates, found, err := p.GetMaster(ctx)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}
	if !found {
		// Master gone; drop the now-orphaned provenance row.
		return p.DeleteDeleteLog(ctx, logID)
	}
	filtered, changed, err := removeExdate(exdates, recurrenceID)
	if err != nil {
		return err
	}
	if changed {
		if err := p.UpdateExdates(ctx, masterID, filtered); err != nil {
			return fmt.Errorf("update exdates: %w", err)
		}
	}
	if err := p.DeleteDeleteLog(ctx, logID); err != nil {
		return fmt.Errorf("delete exdate log: %w", err)
	}
	return nil
}

// removeExdate removes one copy of the EXDATE of recurrenceID from exdates.
// changed is false when exdates holds no copy.
func removeExdate(exdates, recurrenceID string) (filtered string, changed bool, err error) {
	target, err := timeutil.ParseRecurrenceID(recurrenceID)
	if err != nil {
		return "", false, fmt.Errorf("parse recurrence_id %q: %w", recurrenceID, err)
	}
	existing := timeutil.ParseTimeList(exdates)
	kept := timeutil.RemoveTimeFromList(existing, target)
	return timeutil.SerializeTimeList(kept), len(kept) != len(existing), nil
}

// The occurrence rule of the event service follows. An occurrence log of
// the event service records each delete of one occurrence X. The invariant:
//
//	An EXDATE that a delete recorded exists at X only while no live override
//	of the series sits at X.
//
// Locally, an EXDATE never hides an override. But Google and iCloud drop an
// override whose RECURRENCE-ID has an EXDATE, so the export must never
// carry both. An imported EXDATE has no log, and the rule never touches it
// (issue #86). Two operations hold the invariant:
//   - RecordOccurrenceDelete: a delete adds the EXDATE only when no live
//     override of the series remains at X.
//   - SettleOccurrence: a path that makes an override live at X again strips
//     the EXDATE that the log owns. The log keeps the rows that stay hidden.

// OccurrenceEntry is the record of one delete in an occurrence log. Seq names
// the delete in its log. Rows lists the override rows that the delete hid
// and that stay hidden. A delete with no stored override has no row.
type OccurrenceEntry struct {
	Seq  int64
	Rows []int64
}

// OccurrenceLog is one occurrence log, as the caller read it.
type OccurrenceLog struct {
	ID      int64
	Entries []OccurrenceEntry
	// OwnsExdate is true while the log owns a copy of the EXDATE of X.
	OwnsExdate bool
	// LastSeq is the Seq of the last delete in the log.
	LastSeq int64
}

// HasRows reports whether an entry of the log names a hidden row.
func (l OccurrenceLog) HasRows() bool {
	for _, e := range l.Entries {
		if len(e.Rows) > 0 {
			return true
		}
	}
	return false
}

// Entry returns the entry of the delete seq. found is false with no entry.
func (l OccurrenceLog) Entry(seq int64) (entry OccurrenceEntry, found bool) {
	for _, e := range l.Entries {
		if e.Seq == seq {
			return e, true
		}
	}
	return OccurrenceEntry{}, false
}

// WithoutRows returns the log with the rows of ids removed from each entry.
// The entries stay: each one still names its delete.
func (l OccurrenceLog) WithoutRows(ids []int64) OccurrenceLog {
	drop := make(map[int64]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	out := l
	out.Entries = make([]OccurrenceEntry, 0, len(l.Entries))
	for _, e := range l.Entries {
		rows := make([]int64, 0, len(e.Rows))
		for _, r := range e.Rows {
			if !drop[r] {
				rows = append(rows, r)
			}
		}
		out.Entries = append(out.Entries, OccurrenceEntry{Seq: e.Seq, Rows: rows})
	}
	return out
}

// WithoutEntry returns the log with the entry of the delete seq removed.
func (l OccurrenceLog) WithoutEntry(seq int64) OccurrenceLog {
	out := l
	out.Entries = make([]OccurrenceEntry, 0, len(l.Entries))
	for _, e := range l.Entries {
		if e.Seq != seq {
			out.Entries = append(out.Entries, e)
		}
	}
	return out
}

// Occurrence is the state of one occurrence X of a series, as the caller
// holds it in the transaction.
type Occurrence struct {
	RecurrenceID string
	// Exdate is the EXDATE value of X that a delete adds. The caller tags it
	// as date-only for an all-day master (timeutil.AsDateOnly).
	Exdate time.Time
	// MasterID and MasterExdates are the master of the series.
	MasterID      int64
	MasterExdates string
	// LiveOverride is true when a live override of the series sits at X,
	// after the write of the caller.
	LiveOverride bool
}

// OccurrenceStore adapts the queries of the event service. The closures bind
// to the active transaction.
type OccurrenceStore struct {
	// UpdateExdates writes the EXDATE list of the master.
	UpdateExdates func(ctx context.Context, masterID int64, exdates string) error
	// SaveLog writes the entries and the EXDATE flag of the log. A log with
	// ID 0 is new: the delete writes it.
	SaveLog func(ctx context.Context, log OccurrenceLog) (logID int64, err error)
	// DeleteLog drops the log.
	DeleteLog func(ctx context.Context, logID int64) error
}

// RecordOccurrenceDelete records a delete that hid rows at X. log is the log
// of X, with ID 0 when X has no log. The delete appends the EXDATE only when
// no live override of the series remains at X. The list of the master must
// not hold X yet. The log owns the EXDATE only when this delete appends it.
// An EXDATE that is in the list already stays the EXDATE of its source: an
// import, the server, or an earlier delete. It returns the log ID and the Seq
// of the delete.
func RecordOccurrenceDelete(ctx context.Context, s OccurrenceStore, o Occurrence, log OccurrenceLog, rows []int64) (logID, seq int64, err error) {
	existing := timeutil.ParseTimeList(o.MasterExdates)
	if !o.LiveOverride && !timeutil.TimeListHas(existing, o.Exdate) {
		exdates := timeutil.SerializeTimeList(append(existing, o.Exdate))
		if err := s.UpdateExdates(ctx, o.MasterID, exdates); err != nil {
			return 0, 0, fmt.Errorf("update exdates: %w", err)
		}
		log.OwnsExdate = true
	}
	log.LastSeq++
	log.Entries = append(log.Entries, OccurrenceEntry{Seq: log.LastSeq, Rows: rows})
	id, err := s.SaveLog(ctx, log)
	if err != nil {
		return 0, 0, fmt.Errorf("record exdate delete: %w", err)
	}
	return id, log.LastSeq, nil
}

// SettleOccurrence writes log after a restore at X. The caller removes the
// restored rows or entries from log first (WithoutRows, WithoutEntry). The
// rules:
//  1. With a live override at X, the log gives up its copy of the EXDATE.
//  2. With no entry left, the log gives up its copy of the EXDATE, and the
//     log goes.
//  3. With no hidden row left and no copy of the EXDATE, the log goes: it
//     hides nothing.
//  4. Else the log stays with its entries.
//
// An entry with no row keeps its EXDATE while no override is live at X: the
// delete of an occurrence with no stored override.
func SettleOccurrence(ctx context.Context, s OccurrenceStore, o Occurrence, log OccurrenceLog) error {
	// The strip removes one copy of X only while the list still holds X
	// (removeExdate). A pull that rewrites the list of the master clears
	// the flag of each log of that master, so a server EXDATE never goes.
	if log.OwnsExdate && (o.LiveOverride || len(log.Entries) == 0) {
		filtered, changed, err := removeExdate(o.MasterExdates, o.RecurrenceID)
		if err != nil {
			return err
		}
		if changed {
			if err := s.UpdateExdates(ctx, o.MasterID, filtered); err != nil {
				return fmt.Errorf("update exdates: %w", err)
			}
		}
		log.OwnsExdate = false
	}
	if len(log.Entries) == 0 || (!log.HasRows() && !log.OwnsExdate) {
		if err := s.DeleteLog(ctx, log.ID); err != nil {
			return fmt.Errorf("delete exdate log: %w", err)
		}
		return nil
	}
	if _, err := s.SaveLog(ctx, log); err != nil {
		return fmt.Errorf("update exdate log: %w", err)
	}
	return nil
}
