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

// CaptureReplacedCredential reads the credential that a replacement
// overwrites. It accepts a missing entry, which the replacement repairs, and
// it returns every other read failure.
//
// An entry of another connection is not a missing entry, so this function
// returns that error. The store holds a credential file in that case, and the
// rollback cannot put its value back: a store reports the mismatch in place of
// the value. The rollback would delete the file instead, which removes a
// secret the user consented to. On a host with no keyring that delete also
// takes away the permission to write the next secret, so the user could not
// try again (issue #777).
func CaptureReplacedCredential(store CredentialStore, accountID int64, fingerprint string) (PriorCredential, error) {
	cred, err := store.Get(accountID, fingerprint)
	if err == nil {
		return PriorCredential{cred: cred, hasPrevious: true}, nil
	}
	if IsCredentialNotFound(err) {
		return PriorCredential{}, nil
	}
	return PriorCredential{}, err
}

// Credential returns the captured credential, or the zero Credential when the
// capture found no entry.
//
// A caller reads it to carry a field forward into the replacement, for example
// an OAuth refresh token that the new value does not provide.
//
// The hasPrevious guard does not trust a store to leave its result empty on an
// error. CapturePriorCredential keeps whatever Get returned beside a tolerated
// error, and the CredentialStore contract does not say that value is empty. A
// store that returns a mismatched credential for diagnostics would otherwise
// hand another connection's secret to a caller that carries fields forward.
func (p PriorCredential) Credential() Credential {
	if !p.hasPrevious {
		return Credential{}
	}
	return p.cred
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
		return restoreWritten(store, p.cred, operation, cause)
	}
	if wroteNew {
		if deleteErr := store.Delete(accountID); deleteErr != nil {
			return fmt.Errorf("%s: %w (delete credentials: %w)", operation, cause, deleteErr)
		}
	}
	return fmt.Errorf("%s: %w", operation, cause)
}

// Replacement describes the credential write that a rollback undoes.
type Replacement struct {
	// AccountID is the account whose credential the operation replaced.
	AccountID int64
	// Fingerprint is the connection identity of that credential.
	Fingerprint string
	// RefreshToken is the refresh token that the replacement credential
	// carried. The rollback compares it with the captured one to decide
	// whether the failed operation ran on the captured OAuth identity.
	RefreshToken string
}

// RestoreReplacement rolls back an operation that replaced the credential of
// one account. It puts the captured credential back, or it removes the
// replacement when the capture found no entry. It returns an error that
// carries the original cause.
//
// A capture with no entry is a normal state here. A credential goes missing
// after a keyring reset, and after a copy of the database to another host. The
// account then held no credential, which is the state before the operation, so
// the rollback removes what the operation wrote. Pair this method with
// CaptureReplacedCredential, which reports every other read failure instead of
// capturing nothing.
//
// The rollback keeps the OAuth token triple that the store holds now, when the
// operation rotated the captured refresh token. An operation that refreshes an
// expired access token persists a rotated refresh token, and the provider can
// already have invalidated the captured one. A rollback to the captured triple
// would end the ability of the account to refresh. The kept values come from
// the store, so this is not a way to write a secret of the caller.
func (p PriorCredential) RestoreReplacement(
	store CredentialStore, replaced Replacement, operation string, cause error,
) error {
	if !p.hasPrevious {
		if deleteErr := store.Delete(replaced.AccountID); deleteErr != nil {
			return fmt.Errorf("%s: %w (delete credentials: %w)", operation, cause, deleteErr)
		}
		return fmt.Errorf("%s: %w", operation, cause)
	}
	return restoreWritten(store, withRefreshedTokens(store, p.cred, replaced), operation, cause)
}

// withRefreshedTokens returns cred with the OAuth token triple that store
// holds now, when the operation rotated the refresh token of cred.
//
// The replacement must have carried the refresh token of cred. The operation
// then ran on the OAuth identity of cred, so a store value that differs from
// it is a rotation of it. A replacement that carried its own refresh token
// belongs to another identity, and its tokens belong to the value that failed,
// so this function keeps none of them.
//
// The caller passes the refresh token that it wrote. Replacement.RefreshToken
// is a value, not a claim about one, so a caller that passes what it wrote
// gets the right answer without stating an intent.
//
// A read failure returns cred unchanged: the rollback matters more than the
// newer token.
func withRefreshedTokens(store CredentialStore, cred Credential, replaced Replacement) Credential {
	if replaced.RefreshToken == "" || replaced.RefreshToken != cred.RefreshToken {
		return cred
	}
	current, err := store.Get(replaced.AccountID, replaced.Fingerprint)
	if err != nil || current.RefreshToken == "" || current.RefreshToken == cred.RefreshToken {
		return cred
	}
	cred.AccessToken = current.AccessToken
	cred.RefreshToken = current.RefreshToken
	cred.TokenExpiry = current.TokenExpiry
	return cred
}

// restoreWritten writes cred back. Every rollback shares it, so every one of
// them reports a restore failure the same way.
func restoreWritten(store CredentialStore, cred Credential, operation string, cause error) error {
	if restoreErr := restoreCredential(store, cred); restoreErr != nil {
		return fmt.Errorf("%s: %w (restore credentials: %w)", operation, cause, restoreErr)
	}
	return fmt.Errorf("%s: %w", operation, cause)
}

// restoreCredential puts back a credential that store gave out earlier.
//
// It is Set for every store but the secretless one. That store refuses a new
// secret, and a restore carries no new secret: the value comes from the store
// itself, so it was already on disk before the failed change. A refusal would
// leave the credential file in the state that the failed change put it in,
// which is the divergence this compensation exists to prevent.
func restoreCredential(store CredentialStore, cred Credential) error {
	switch typed := store.(type) {
	case *secretlessFileStore:
		return typed.restore(cred)
	case *migratingCredentialStore:
		if err := restoreCredential(typed.primary, cred); err != nil {
			return err
		}
		// Mirror migratingCredentialStore.Set: a successful primary write
		// removes the credential from every cleanup source.
		for _, legacy := range typed.legacy {
			if legacy.cleanup {
				_ = legacy.store.Delete(cred.AccountID)
			}
		}
		return nil
	default:
		return store.Set(cred)
	}
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
