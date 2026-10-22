package softdelete

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStore is an in-memory stand-in for one domain's *_exdate_deletes
// provenance table plus its master row, wired into an ExdateProvenance below.
type fakeStore struct {
	logFound    bool
	logID       int64
	masterFound bool
	masterID    int64
	exdates     string

	getLogErr error

	updatedExdates *string
	deletedLogID   *int64
}

func (f *fakeStore) provenance() ExdateProvenance {
	return ExdateProvenance{
		GetDeleteLog: func(ctx context.Context) (int64, bool, error) {
			if f.getLogErr != nil {
				return 0, false, f.getLogErr
			}
			return f.logID, f.logFound, nil
		},
		GetMaster: func(ctx context.Context) (int64, string, bool, error) {
			return f.masterID, f.exdates, f.masterFound, nil
		},
		UpdateExdates: func(ctx context.Context, masterID int64, exdates string) error {
			f.updatedExdates = &exdates
			return nil
		},
		DeleteDeleteLog: func(ctx context.Context, logID int64) error {
			f.deletedLogID = &logID
			return nil
		},
	}
}

const testSlot = "2026-06-01T10:00:00Z"

// No provenance row: the EXDATE arrived via import (or a series delete) and
// must survive — nothing is updated or deleted (issue #86).
func TestClearMasterEXDATE_NoProvenanceRowIsNoOp(t *testing.T) {
	f := &fakeStore{logFound: false}
	if err := ClearMasterEXDATE(context.Background(), f.provenance(), testSlot); err != nil {
		t.Fatalf("ClearMasterEXDATE: %v", err)
	}
	if f.updatedExdates != nil {
		t.Errorf("expected no EXDATE update, got %q", *f.updatedExdates)
	}
	if f.deletedLogID != nil {
		t.Errorf("expected no provenance delete, got log id %d", *f.deletedLogID)
	}
}

// Provenance row present: strip the EXDATE that matches and drop the log row.
func TestClearMasterEXDATE_StripsMatchingEXDATE(t *testing.T) {
	f := &fakeStore{logFound: true, logID: 7, masterFound: true, masterID: 3, exdates: testSlot}
	if err := ClearMasterEXDATE(context.Background(), f.provenance(), testSlot); err != nil {
		t.Fatalf("ClearMasterEXDATE: %v", err)
	}
	if f.updatedExdates == nil || *f.updatedExdates != "" {
		t.Errorf("expected EXDATE list emptied, got %v", f.updatedExdates)
	}
	if f.deletedLogID == nil || *f.deletedLogID != 7 {
		t.Errorf("expected provenance log 7 deleted, got %v", f.deletedLogID)
	}
}

// A duplicate exclusion at the same slot (e.g. an imported EXDATE alongside the
// delete-added one) must keep exactly one entry: undo strips one, not all.
func TestClearMasterEXDATE_PreservesDuplicateSlot(t *testing.T) {
	f := &fakeStore{logFound: true, logID: 7, masterFound: true, masterID: 3, exdates: testSlot + "," + testSlot}
	if err := ClearMasterEXDATE(context.Background(), f.provenance(), testSlot); err != nil {
		t.Fatalf("ClearMasterEXDATE: %v", err)
	}
	if f.updatedExdates == nil || *f.updatedExdates != testSlot {
		t.Errorf("expected one EXDATE preserved, got %v", f.updatedExdates)
	}
}

// Master gone: drop the orphaned provenance row, update nothing.
func TestClearMasterEXDATE_MasterGoneDropsOrphanLog(t *testing.T) {
	f := &fakeStore{logFound: true, logID: 9, masterFound: false}
	if err := ClearMasterEXDATE(context.Background(), f.provenance(), testSlot); err != nil {
		t.Fatalf("ClearMasterEXDATE: %v", err)
	}
	if f.updatedExdates != nil {
		t.Errorf("expected no EXDATE update, got %q", *f.updatedExdates)
	}
	if f.deletedLogID == nil || *f.deletedLogID != 9 {
		t.Errorf("expected orphan log 9 deleted, got %v", f.deletedLogID)
	}
}

// A malformed recurrence_id is a data-integrity error, propagated not swallowed.
func TestClearMasterEXDATE_MalformedRecurrenceID(t *testing.T) {
	f := &fakeStore{logFound: true, logID: 7, masterFound: true, masterID: 3, exdates: testSlot}
	err := ClearMasterEXDATE(context.Background(), f.provenance(), "not-a-timestamp")
	if err == nil {
		t.Fatal("expected error for malformed recurrence_id, got nil")
	}
	if f.deletedLogID != nil {
		t.Errorf("expected no provenance delete on error, got log id %d", *f.deletedLogID)
	}
}

// A non-ErrNoRows lookup failure surfaces to the caller.
func TestClearMasterEXDATE_GetDeleteLogError(t *testing.T) {
	sentinel := errors.New("boom")
	f := &fakeStore{getLogErr: sentinel}
	err := ClearMasterEXDATE(context.Background(), f.provenance(), testSlot)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got %v", err)
	}
}

// occStore records the writes of the occurrence rule.
type occStore struct {
	exdates *string
	saved   *OccurrenceLog
	deleted bool
}

func (o *occStore) store() OccurrenceStore {
	return OccurrenceStore{
		UpdateExdates: func(ctx context.Context, masterID int64, exdates string) error {
			o.exdates = &exdates
			return nil
		},
		SaveLog: func(ctx context.Context, log OccurrenceLog) (int64, error) {
			o.saved = &log
			if log.ID == 0 {
				return 9, nil
			}
			return log.ID, nil
		},
		DeleteLog: func(ctx context.Context, logID int64) error {
			o.deleted = true
			return nil
		},
	}
}

// TestRecordOccurrenceDelete: a delete adds the EXDATE only with no live
// override at the occurrence, and only once.
func TestRecordOccurrenceDelete(t *testing.T) {
	at, _ := time.Parse(time.RFC3339, testSlot)
	for _, tc := range []struct {
		name     string
		live     bool
		owns     bool
		wantAdd  bool
		wantOwns bool
	}{
		{"no live override", false, false, true, true},
		{"live override", true, false, false, false},
		{"log owns a copy", false, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &occStore{}
			occ := Occurrence{RecurrenceID: testSlot, Exdate: at, MasterID: 3, LiveOverride: tc.live}
			_, seq, err := RecordOccurrenceDelete(context.Background(), o.store(), occ, OccurrenceLog{OwnsExdate: tc.owns, LastSeq: 4}, []int64{11})
			if err != nil {
				t.Fatal(err)
			}
			if (o.exdates != nil) != tc.wantAdd || seq != 5 || o.saved.OwnsExdate != tc.wantOwns {
				t.Errorf("add %v, seq %d, owns %v; want add %v, seq 5, owns %v", o.exdates != nil, seq, o.saved.OwnsExdate, tc.wantAdd, tc.wantOwns)
			}
		})
	}
}

// TestSettleOccurrence covers the rules of the settle.
func TestSettleOccurrence(t *testing.T) {
	rows := OccurrenceLog{ID: 7, OwnsExdate: true, Entries: []OccurrenceEntry{{Seq: 1, Rows: []int64{11}}}}
	noRow := OccurrenceLog{ID: 7, OwnsExdate: true, Entries: []OccurrenceEntry{{Seq: 1}}}
	for _, tc := range []struct {
		name        string
		log         OccurrenceLog
		live        bool
		wantStrip   bool
		wantDeleted bool
	}{
		{"live override strips and keeps the hidden rows", rows, true, true, false},
		{"no live override keeps the EXDATE", rows, false, false, false},
		{"an entry with no row keeps the EXDATE", noRow, false, false, false},
		{"a live override beside an entry with no row strips and drops", noRow, true, true, true},
		{"no entry strips and drops", OccurrenceLog{ID: 7, OwnsExdate: true}, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &occStore{}
			occ := Occurrence{RecurrenceID: testSlot, MasterID: 3, MasterExdates: testSlot, LiveOverride: tc.live}
			if err := SettleOccurrence(context.Background(), o.store(), occ, tc.log); err != nil {
				t.Fatal(err)
			}
			if (o.exdates != nil) != tc.wantStrip || o.deleted != tc.wantDeleted {
				t.Errorf("strip %v, deleted %v; want strip %v, deleted %v", o.exdates != nil, o.deleted, tc.wantStrip, tc.wantDeleted)
			}
		})
	}
}
