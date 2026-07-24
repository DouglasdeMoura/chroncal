package auth

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// fakeCredStore is a minimal CredentialStore for compensation tests: it records
// Set/Delete calls and their targets and can inject failures. Get returns a
// configured error (used to exercise not-found / identity-mismatch / transient
// outcomes) or the stored credential.
type fakeCredStore struct {
	creds       map[int64]Credential
	getErr      error
	setErr      error
	deleteErr   error
	setCalls    int
	deleteCalls int
	setIDs      []int64
	deleteIDs   []int64
}

func (s *fakeCredStore) Get(accountID int64, _ string) (Credential, error) {
	if s.getErr != nil {
		return Credential{}, s.getErr
	}
	if c, ok := s.creds[accountID]; ok {
		return c, nil
	}
	return Credential{}, nil
}

func (s *fakeCredStore) Set(c Credential) error {
	s.setCalls++
	s.setIDs = append(s.setIDs, c.AccountID)
	if s.setErr != nil {
		return s.setErr
	}
	if s.creds == nil {
		s.creds = make(map[int64]Credential)
	}
	s.creds[c.AccountID] = c
	return nil
}

func (s *fakeCredStore) Delete(accountID int64) error {
	s.deleteCalls++
	s.deleteIDs = append(s.deleteIDs, accountID)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.creds, accountID)
	return nil
}

func TestCapturePriorCredential(t *testing.T) {
	accountID := int64(7)
	stored := Credential{AccountID: accountID, Password: "secret"}

	t.Run("found", func(t *testing.T) {
		store := &fakeCredStore{creds: map[int64]Credential{accountID: stored}}
		prior, err := CapturePriorCredential(store, accountID, "fp")
		if err != nil {
			t.Fatalf("CapturePriorCredential error = %v", err)
		}
		if !prior.hasPrevious {
			t.Fatal("hasPrevious = false, want true")
		}
		if prior.cred.Password != "secret" {
			t.Fatalf("captured credential = %+v, want the stored one", prior.cred)
		}
	})

	t.Run("not found is tolerated", func(t *testing.T) {
		store := &fakeCredStore{getErr: errCredentialNotFound}
		prior, err := CapturePriorCredential(store, accountID, "fp")
		if err != nil {
			t.Fatalf("CapturePriorCredential error = %v, want nil for not-found", err)
		}
		if prior.hasPrevious {
			t.Fatal("hasPrevious = true, want false for not-found")
		}
	})

	t.Run("identity mismatch is tolerated", func(t *testing.T) {
		store := &fakeCredStore{getErr: ErrCredentialIdentityMismatch}
		prior, err := CapturePriorCredential(store, accountID, "fp")
		if err != nil {
			t.Fatalf("CapturePriorCredential error = %v, want nil for identity mismatch", err)
		}
		if prior.hasPrevious {
			t.Fatal("hasPrevious = true, want false for identity mismatch")
		}
	})

	t.Run("transient error aborts", func(t *testing.T) {
		transient := errors.New("keyring backend down")
		store := &fakeCredStore{getErr: transient}
		if _, err := CapturePriorCredential(store, accountID, "fp"); !errors.Is(err, transient) {
			t.Fatalf("CapturePriorCredential error = %v, want transient error", err)
		}
	})
}

func TestPriorCredentialRestore(t *testing.T) {
	accountID := int64(9)
	cause := errors.New("commit failed")

	t.Run("previous present restores and wraps cause", func(t *testing.T) {
		store := &fakeCredStore{}
		prior := PriorCredential{cred: Credential{AccountID: accountID, Password: "old"}, hasPrevious: true}

		err := prior.Restore(store, accountID, false, "commit op", cause)

		if !errors.Is(err, cause) {
			t.Fatalf("Restore error = %v, want it to wrap the cause", err)
		}
		if store.setCalls != 1 || store.setIDs[0] != accountID {
			t.Fatalf("Set calls = %v, want one Set of account %d", store.setIDs, accountID)
		}
		if store.deleteCalls != 0 {
			t.Fatalf("Delete calls = %d, want 0 when a previous credential exists", store.deleteCalls)
		}
		if got := store.creds[accountID].Password; got != "old" {
			t.Fatalf("restored credential password = %q, want %q", got, "old")
		}
	})

	t.Run("restore failure is surfaced alongside cause", func(t *testing.T) {
		setErr := errors.New("keyring write failed")
		store := &fakeCredStore{setErr: setErr}
		prior := PriorCredential{cred: Credential{AccountID: accountID}, hasPrevious: true}

		err := prior.Restore(store, accountID, false, "commit op", cause)

		if !errors.Is(err, cause) {
			t.Fatalf("Restore error must wrap the cause, got %v", err)
		}
		if !errors.Is(err, setErr) {
			t.Fatalf("Restore error must surface the restore failure, got %v", err)
		}
		if store.deleteCalls != 0 {
			t.Fatalf("Delete calls = %d, want 0 on restore path", store.deleteCalls)
		}
	})

	t.Run("no previous and no new credential only wraps cause", func(t *testing.T) {
		store := &fakeCredStore{}
		var prior PriorCredential // zero value: no previous, destructive op

		err := prior.Restore(store, accountID, false, "commit op", cause)

		if !errors.Is(err, cause) {
			t.Fatalf("Restore error = %v, want it to wrap the cause", err)
		}
		if store.setCalls != 0 || store.deleteCalls != 0 {
			t.Fatalf("Set/Delete calls = %d/%d, want 0/0 when there is nothing to undo", store.setCalls, store.deleteCalls)
		}
	})

	t.Run("no previous with new credential deletes replacement", func(t *testing.T) {
		store := &fakeCredStore{}
		var prior PriorCredential // no previous, but a brand-new credential was written

		err := prior.Restore(store, accountID, true, "commit op", cause)

		if !errors.Is(err, cause) {
			t.Fatalf("Restore error = %v, want it to wrap the cause", err)
		}
		if store.deleteCalls != 1 || store.deleteIDs[0] != accountID {
			t.Fatalf("Delete calls = %v, want one Delete of account %d", store.deleteIDs, accountID)
		}
		if store.setCalls != 0 {
			t.Fatalf("Set calls = %d, want 0 when deleting a replacement", store.setCalls)
		}
	})

	t.Run("replacement delete failure is surfaced alongside cause", func(t *testing.T) {
		deleteErr := errors.New("keyring delete failed")
		store := &fakeCredStore{deleteErr: deleteErr}
		var prior PriorCredential

		err := prior.Restore(store, accountID, true, "commit op", cause)

		if !errors.Is(err, cause) {
			t.Fatalf("Restore error must wrap the cause, got %v", err)
		}
		if !errors.Is(err, deleteErr) {
			t.Fatalf("Restore error must surface the delete failure, got %v", err)
		}
	})
}

func TestCommitWithCredentialCompensation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	accountID := int64(11)

	// finalizedTx returns a transaction whose Commit always fails: it has
	// already been rolled back, so a later Commit reports sql.ErrTxDone. This
	// exercises the commit-failure leg without a deferred-foreign-key fixture.
	finalizedTx := func(t *testing.T) *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback to finalize tx: %v", err)
		}
		return tx
	}

	t.Run("successful commit touches no credential", func(t *testing.T) {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		store := &fakeCredStore{}
		prior := PriorCredential{cred: Credential{AccountID: accountID}, hasPrevious: true}

		if err := CommitWithCredentialCompensation(tx, store, accountID, prior, false, "commit op"); err != nil {
			t.Fatalf("CommitWithCredentialCompensation error = %v, want nil", err)
		}
		if store.setCalls != 0 || store.deleteCalls != 0 {
			t.Fatalf("Set/Delete calls = %d/%d, want 0/0 on a successful commit", store.setCalls, store.deleteCalls)
		}
	})

	t.Run("failed commit restores previous credential", func(t *testing.T) {
		store := &fakeCredStore{}
		prior := PriorCredential{cred: Credential{AccountID: accountID, Password: "old"}, hasPrevious: true}

		err := CommitWithCredentialCompensation(finalizedTx(t), store, accountID, prior, false, "commit op")

		if err == nil {
			t.Fatal("CommitWithCredentialCompensation error = nil, want commit failure")
		}
		if store.setCalls != 1 || store.setIDs[0] != accountID {
			t.Fatalf("Set calls = %v, want one restore of account %d", store.setIDs, accountID)
		}
		if got := store.creds[accountID].Password; got != "old" {
			t.Fatalf("restored credential password = %q, want %q", got, "old")
		}
	})

	t.Run("failed commit with no previous and no new credential only wraps", func(t *testing.T) {
		store := &fakeCredStore{}
		var prior PriorCredential

		err := CommitWithCredentialCompensation(finalizedTx(t), store, accountID, prior, false, "commit op")

		if err == nil {
			t.Fatal("CommitWithCredentialCompensation error = nil, want commit failure")
		}
		if store.setCalls != 0 || store.deleteCalls != 0 {
			t.Fatalf("Set/Delete calls = %d/%d, want 0/0 when nothing was written", store.setCalls, store.deleteCalls)
		}
	})

	t.Run("failed commit with new credential deletes replacement", func(t *testing.T) {
		store := &fakeCredStore{}
		var prior PriorCredential // a brand-new credential was stored, no prior to restore

		err := CommitWithCredentialCompensation(finalizedTx(t), store, accountID, prior, true, "commit op")

		if err == nil {
			t.Fatal("CommitWithCredentialCompensation error = nil, want commit failure")
		}
		if store.deleteCalls != 1 || store.deleteIDs[0] != accountID {
			t.Fatalf("Delete calls = %v, want one Delete of account %d", store.deleteIDs, accountID)
		}
		if store.setCalls != 0 {
			t.Fatalf("Set calls = %d, want 0 when deleting a replacement", store.setCalls)
		}
	})
}
