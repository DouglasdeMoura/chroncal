package auth

import (
	"errors"
	"fmt"
	"strings"
)

// ErrPlaintextRequired reports a credential that carries a secret on a host
// with no OS keyring and no plaintext opt-in. chroncal refuses to write the
// secret to disk without consent.
var ErrPlaintextRequired = errors.New("no secure credential store is available")

// HasStoredSecret reports whether the credential carries secret material that
// the store must keep. A password command is not secret material: the store
// keeps the command, and chroncal runs it for each connection.
func (c Credential) HasStoredSecret() bool {
	for _, secret := range []string{
		c.Password, c.AccessToken, c.RefreshToken, c.OAuthClientSecret,
	} {
		if strings.TrimSpace(secret) != "" {
			return true
		}
	}
	return false
}

// secretlessFileStore is the credential store for a host with no OS keyring
// and no plaintext opt-in. It reads and writes the same 0600-mode files as
// PlaintextFileStore, but it refuses to write a credential that carries a
// secret.
//
// A basic-auth account with a password command needs no keyring and no
// --allow-plaintext flag: the file keeps the command, never the password
// (issue #777). An account with a password or an OAuth token still needs an
// explicit opt-in.
//
// A read is always permitted. A file that already exists holds a secret the
// user chose to write earlier, so a later run without the flag can still use
// the account.
type secretlessFileStore struct {
	inner *PlaintextFileStore
	// reason records why the OS keyring is unavailable. The refusal message
	// repeats it, so the user learns what to install.
	reason error
}

func (s *secretlessFileStore) Get(accountID int64, accountFingerprint string) (Credential, error) {
	return s.inner.Get(accountID, accountFingerprint)
}

func (s *secretlessFileStore) Set(cred Credential) error {
	if cred.HasStoredSecret() {
		return s.refusal()
	}
	return s.inner.Set(cred)
}

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

// EnsureCanStoreSecret reports whether store accepts a credential that
// carries a secret. Call it before an interactive password prompt or an
// OAuth browser flow. The user then learns the remedy before the work, not
// after it.
//
// It returns nil for every store that keeps secrets: the OS keyring, and the
// plaintext file store that an explicit opt-in enables.
func EnsureCanStoreSecret(store CredentialStore) error {
	switch typed := store.(type) {
	case *secretlessFileStore:
		return typed.refusal()
	case *migratingCredentialStore:
		return EnsureCanStoreSecret(typed.primary)
	default:
		return nil
	}
}
