package auth

import (
	"errors"
	"fmt"
)

// ErrPlaintextRequired reports a credential that carries a secret on a host
// with no OS keyring and no plaintext opt-in. chroncal refuses to write the
// secret to disk without consent.
var ErrPlaintextRequired = errors.New("no secure credential store is available")

// HasStoredSecret reports whether the credential carries secret material that
// the store must keep. A password command is not secret material: the store
// keeps the command, and chroncal runs it for each connection. Every non-empty
// secret counts, even a whitespace-only password: the store would still write
// it to disk.
func (c Credential) HasStoredSecret() bool {
	for _, secret := range []string{
		c.Password, c.AccessToken, c.RefreshToken, c.OAuthClientSecret,
	} {
		if secret != "" {
			return true
		}
	}
	return false
}

// secretlessFileStore is the credential store for a host with no OS keyring
// and no plaintext opt-in. It reads and writes the same 0600-mode files as
// PlaintextFileStore, but it refuses to put a new secret in a file that holds
// none.
//
// A basic-auth account with a password command needs no keyring and no
// --allow-plaintext flag: the file keeps the command, never the password
// (issue #777). An account with a password or an OAuth token still needs an
// explicit opt-in.
//
// A read is always permitted, and so is a rewrite of a file that already holds
// a secret. Such a file exists only because the user opted in earlier, so a
// later run without the flag can still use the account and refresh its OAuth
// token.
type secretlessFileStore struct {
	inner *PlaintextFileStore
	// legacy lists the read-only migration sources that the enclosing
	// migratingCredentialStore reads. A secret in one of them is already on
	// disk, so the copy into the primary file must not be refused.
	legacy []legacyCredentialStore
	// reason records why the OS keyring is unavailable. The refusal message
	// repeats it, so the user learns what to install.
	reason error
}

// Get reads an existing credential and checks its account identity.
func (s *secretlessFileStore) Get(accountID int64, accountFingerprint string) (Credential, error) {
	return s.inner.Get(accountID, accountFingerprint)
}

// Set refuses a new secret before it changes the credential file. It permits a
// credential with no secret, and it permits a rewrite of a file that already
// holds one.
//
// The rewrite matters for OAuth. An access token expires after about an hour.
// The refresh writes the new token back through this store, so a refusal would
// break an account that the user set up with an earlier opt-in. The refusal
// also cannot un-write the secret that is already on disk, so it protects
// nothing. The same reasoning permits the read.
//
// This is the write-side safety net, not the place that reports the remedy to
// a person. Every interactive path calls EnsureCanStoreSecret first, which
// applies the same rule for the same account. A password prompt and an OAuth
// sign-in therefore stop up front on the account that the store refuses.
func (s *secretlessFileStore) Set(cred Credential) error {
	if cred.HasStoredSecret() && !s.holdsSecret(cred.AccountID) {
		return s.refusal()
	}
	if cred.HasStoredSecret() {
		// Write without the plaintext warning. The first write printed it,
		// and a repeat says nothing new. A token refresh also runs under a
		// TUI sync, where a write to stderr prints over the alternate
		// screen.
		_, err := s.inner.write(cred)
		return err
	}
	return s.inner.Set(cred)
}

// holdsSecret reports whether a credential for accountID already keeps secret
// material. It reads the primary namespace file first, then each legacy
// source.
//
// The legacy sources matter for a user who opted in before the namespace
// change, or who copied the database. The credential then lives under an older
// namespace, and the first read copies it into the primary file. A refusal of
// that copy leaves the secret on disk and breaks every later OAuth token
// refresh, so the copy must pass.
func (s *secretlessFileStore) holdsSecret(accountID int64) bool {
	if storedSecret(s.inner, accountID) {
		return true
	}
	for _, source := range s.legacy {
		if source.limited && accountID > source.maxAccountID {
			continue
		}
		if storedSecret(source.store, accountID) {
			return true
		}
	}
	return false
}

// storedSecret reports whether store keeps secret material for accountID. The
// empty fingerprint skips the identity check on purpose: the question is about
// the file, not about the account that owns it now.
func storedSecret(store CredentialStore, accountID int64) bool {
	stored, err := store.Get(accountID, "")
	if err != nil {
		return false
	}
	return stored.HasStoredSecret()
}

// Delete removes the credential file, even if it contains a secret.
func (s *secretlessFileStore) Delete(accountID int64) error {
	return s.inner.Delete(accountID)
}

// plaintextRemedy lists every way to store a secret on a host with no OS
// keyring. Every credential-write error on such a host ends with it.
const plaintextRemedy = "Install a keyring provider (libsecret with gnome-keyring on Linux), " +
	"or set security.allow_plaintext in the config file, " +
	"or pass --allow-plaintext. " +
	"A password command needs none of these: chroncal stores the command, not the password"

// refusal returns the error that Set returns for a credential with a secret.
func (s *secretlessFileStore) refusal() error {
	return fmt.Errorf("%w: %w. %s", ErrPlaintextRequired, s.reason, plaintextRemedy)
}

// EnsureCanStoreSecret reports whether store accepts a secret for accountID.
// Call it before an interactive password prompt or an OAuth browser flow. The
// user then learns the remedy before the work, not after it.
//
// It returns nil for every store that keeps secrets: the OS keyring, and the
// plaintext file store that an explicit opt-in enables.
//
// accountID keeps this check and the write in agreement. The secretless store
// permits a rewrite of a credential that already holds a secret, so the same
// account passes here. Pass 0 for an account that does not exist yet: no
// credential holds a secret for it, and the store refuses.
func EnsureCanStoreSecret(store CredentialStore, accountID int64) error {
	switch typed := store.(type) {
	case *secretlessFileStore:
		if typed.holdsSecret(accountID) {
			return nil
		}
		return typed.refusal()
	case *migratingCredentialStore:
		return EnsureCanStoreSecret(typed.primary, accountID)
	default:
		return nil
	}
}
