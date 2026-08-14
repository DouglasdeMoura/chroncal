package auth

import (
	"database/sql"
	"errors"
	"fmt"
)

// PriorCredential captures the credential-store entry that existed before a
// lifecycle operation mutated it. A failed transaction uses it to roll the
// keyring back to a state consistent with the rolled-back database row. The
// two then never diverge in silence (issue #300 family).
//
// The zero value means "no prior credential": either the lookup found no entry
// or the operation stores a brand-new credential with nothing to restore. It is
// the value callers pass when they know there is no prior state to capture.
type PriorCredential struct {
	cred        Credential
	hasPrevious bool
}

// CapturePriorCredential reads the current credential for accountID. It
// accepts only the established not-found and identity-mismatch outcomes a
// destructive lifecycle operation must proceed past. Any other (typically
// transient backend) error is returned. The caller then aborts instead of an
// orphan credential or a clobber with a mismatched replacement.
func CapturePriorCredential(store CredentialStore, accountID int64, fingerprint string) (PriorCredential, error) {
	prev, err := store.Get(accountID, fingerprint)
	if err != nil && !IsCredentialNotFound(err) && !errors.Is(err, ErrCredentialIdentityMismatch) {
		return PriorCredential{}, err
	}
	return PriorCredential{cred: prev, hasPrevious: err == nil}, nil
}

// Restore rolls the credential store back to the captured prior state after a
// failure. It returns an error that surfaces both the original cause and any
// compensation failure rather than a hide of either.
//
// wroteNew reports whether the failed operation stored a brand-new credential
// for accountID (a Set on an account whose prior lookup found no entry). Such
// an entry cannot match the rolled-back row, so it is deleted. When wroteNew is
// false the prior lookup either found an entry to restore or found nothing to
// undo, so no delete is needed.
//
// This is the single canonical compensation implementation shared by the
// account lifecycle methods and calendar Connect (issue #545): every
// commit-then-rollback-credential path funnels through it.
func (p PriorCredential) Restore(store CredentialStore, accountID int64, wroteNew bool, operation string, cause error) error {
	if p.hasPrevious {
		if restoreErr := store.Set(p.cred); restoreErr != nil {
			return fmt.Errorf("%s: %w (restore credentials: %w)", operation, cause, restoreErr)
		}
		return fmt.Errorf("%s: %w", operation, cause)
	}
	if wroteNew {
		if deleteErr := store.Delete(accountID); deleteErr != nil {
			return fmt.Errorf("%s: %w (delete credentials: %w)", operation, cause, deleteErr)
		}
	}
	return fmt.Errorf("%s: %w", operation, cause)
}

// CommitWithCredentialCompensation commits tx and, on failure, restores the
// credential store to the prior state captured before the in-transaction
// credential mutation. It owns the commit-then-rollback-credential invariant so
// the account lifecycle methods and calendar Connect share one implementation.
//
// Pass wroteNew true when the transaction stored a brand-new credential for
// accountID on an account whose prior lookup found no entry. The rollback must
// delete it. Pass false for destructive operations that deleted the credential.
// A gone prior then has nothing to undo.
//
// On a successful commit no credential store is touched.
func CommitWithCredentialCompensation(tx *sql.Tx, store CredentialStore, accountID int64, prior PriorCredential, wroteNew bool, operation string) error {
	if err := tx.Commit(); err != nil {
		return prior.Restore(store, accountID, wroteNew, operation, err)
	}
	return nil
}
