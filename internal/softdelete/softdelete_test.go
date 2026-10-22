package softdelete

import (
	"context"
	"errors"
	"testing"
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

// release wires f into an OccurrenceRelease. setHidden receives the shorter
// list of the log.
func (f *fakeStore) release(setHidden *[]int64) OccurrenceRelease {
	return OccurrenceRelease{
		ExdateProvenance: f.provenance(),
		SetHiddenIDs: func(ctx context.Context, logID int64, ids []int64) error {
			*setHidden = ids
			return nil
		},
	}
}

// TestReleaseOccurrenceRows covers the three rules of the release.
func TestReleaseOccurrenceRows(t *testing.T) {
	log := OccurrenceLog{ID: 7, HiddenIDs: []int64{11, 12}}
	t.Run("rows of another delete", func(t *testing.T) {
		f := &fakeStore{masterFound: true, masterID: 3, exdates: testSlot}
		var hidden []int64
		if err := ReleaseOccurrenceRows(context.Background(), f.release(&hidden), log, []int64{99}, testSlot); err != nil {
			t.Fatal(err)
		}
		if hidden != nil || f.updatedExdates != nil || f.deletedLogID != nil {
			t.Errorf("release of a row the log does not name wrote %v, %v, %v; want nothing", hidden, f.updatedExdates, f.deletedLogID)
		}
	})
	t.Run("rows left", func(t *testing.T) {
		f := &fakeStore{masterFound: true, masterID: 3, exdates: testSlot}
		var hidden []int64
		if err := ReleaseOccurrenceRows(context.Background(), f.release(&hidden), log, []int64{11}, testSlot); err != nil {
			t.Fatal(err)
		}
		if len(hidden) != 1 || hidden[0] != 12 || f.updatedExdates != nil || f.deletedLogID != nil {
			t.Errorf("release of one of two rows = list %v, exdates %v, log %v; want [12] and the EXDATE kept", hidden, f.updatedExdates, f.deletedLogID)
		}
	})
	t.Run("no row left", func(t *testing.T) {
		f := &fakeStore{masterFound: true, masterID: 3, exdates: testSlot}
		var hidden []int64
		if err := ReleaseOccurrenceRows(context.Background(), f.release(&hidden), log, []int64{11, 12}, testSlot); err != nil {
			t.Fatal(err)
		}
		if f.updatedExdates == nil || *f.updatedExdates != "" || f.deletedLogID == nil || *f.deletedLogID != 7 {
			t.Errorf("release of every row = exdates %v, log %v; want the EXDATE and the log gone", f.updatedExdates, f.deletedLogID)
		}
	})
	t.Run("log with an empty list", func(t *testing.T) {
		f := &fakeStore{masterFound: true, masterID: 3, exdates: testSlot}
		var hidden []int64
		if err := ReleaseOccurrenceRows(context.Background(), f.release(&hidden), OccurrenceLog{ID: 7}, []int64{11}, testSlot); err != nil {
			t.Fatal(err)
		}
		if f.updatedExdates == nil || *f.updatedExdates != "" || f.deletedLogID == nil {
			t.Errorf("release of a row from a log with no list = exdates %v, log %v; want the EXDATE and the log gone", f.updatedExdates, f.deletedLogID)
		}
	})
	t.Run("delete with no row", func(t *testing.T) {
		f := &fakeStore{masterFound: true, masterID: 3, exdates: testSlot}
		var hidden []int64
		if err := ReleaseOccurrenceRows(context.Background(), f.release(&hidden), OccurrenceLog{ID: 7}, nil, testSlot); err != nil {
			t.Fatal(err)
		}
		if f.deletedLogID == nil || *f.deletedLogID != 7 {
			t.Errorf("release of an empty log = log %v; want it gone", f.deletedLogID)
		}
	})
}
