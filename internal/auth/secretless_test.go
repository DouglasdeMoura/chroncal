package auth

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestHasStoredSecret pins what counts as secret material. A password
// command is a command, not a secret, so it must not count.
func TestHasStoredSecret(t *testing.T) {
	cases := []struct {
		name string
		cred Credential
		want bool
	}{
		{name: "empty", cred: Credential{}, want: false},
		{name: "username only", cred: Credential{Username: "scott"}, want: false},
		{name: "password command", cred: Credential{PasswordCommand: "pass show caldav"}, want: false},
		{name: "oauth client id", cred: Credential{OAuthClientID: "cid.apps.googleusercontent.com"}, want: false},
		{name: "password", cred: Credential{Password: "hunter2"}, want: true},
		{name: "whitespace password", cred: Credential{Password: "   "}, want: true},
		{name: "access token", cred: Credential{AccessToken: "ya29."}, want: true},
		{name: "refresh token", cred: Credential{RefreshToken: "1//0xyz"}, want: true},
		{name: "oauth client secret", cred: Credential{OAuthClientSecret: "GOCSPX-x"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cred.HasStoredSecret(); got != tc.want {
				t.Fatalf("HasStoredSecret() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEnsureCanStoreSecret confirms the pre-flight check reads through the
// migrating wrapper. The CLI calls it before a password prompt and before an
// OAuth browser flow, so a wrapped store must report the same answer.
func TestEnsureCanStoreSecret(t *testing.T) {
	dir := t.TempDir()
	plaintext := &PlaintextFileStore{dir: dir, namespace: "test"}
	secretless := &secretlessFileStore{inner: plaintext, reason: errors.New("keyring unavailable")}

	if err := EnsureCanStoreSecret(plaintext, 1); err != nil {
		t.Fatalf("plaintext store: %v", err)
	}
	if err := EnsureCanStoreSecret(&KeyringStore{namespace: "test"}, 1); err != nil {
		t.Fatalf("keyring store: %v", err)
	}
	if err := EnsureCanStoreSecret(secretless, 1); !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("secretless store: err = %v, want ErrPlaintextRequired", err)
	}
	wrapped := &migratingCredentialStore{primary: secretless}
	if err := EnsureCanStoreSecret(wrapped, 1); !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("wrapped secretless store: err = %v, want ErrPlaintextRequired", err)
	}
}

// TestEnsureCanStoreSecretMatchesTheWrite keeps the pre-flight check level
// with secretlessFileStore.Set. The write permits a rewrite of a file that
// already holds a secret, so "chroncal account reauth" on such an account must
// not stop before the browser flow it would then complete.
func TestEnsureCanStoreSecretMatchesTheWrite(t *testing.T) {
	plaintext := &PlaintextFileStore{dir: t.TempDir(), namespace: "test"}
	secretless := &secretlessFileStore{inner: plaintext, reason: errors.New("keyring unavailable")}

	// An earlier run with the opt-in wrote the tokens.
	opted := Credential{AccountID: 4, Username: "me@example.com", RefreshToken: "1//0xyz"}
	if err := plaintext.Set(opted); err != nil {
		t.Fatalf("seed the opted-in credential: %v", err)
	}

	if err := EnsureCanStoreSecret(secretless, 4); err != nil {
		t.Fatalf("account with a secret on disk: err = %v, want nil", err)
	}
	if err := secretless.Set(opted); err != nil {
		t.Fatalf("Set for the same account: %v", err)
	}
	// Every other account still stops before the work.
	if err := EnsureCanStoreSecret(secretless, 5); !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("account without a secret: err = %v, want ErrPlaintextRequired", err)
	}
}

// TestSecretlessStoreMigratesALegacySecret covers the credential that only a
// previous namespace holds. The first read copies it into the primary file,
// and a refusal of that copy would leave the secret on disk and break every
// later OAuth token refresh.
func TestSecretlessStoreMigratesALegacySecret(t *testing.T) {
	dir := t.TempDir()
	legacy := &PlaintextFileStore{dir: dir, namespace: "old"}
	primary := &PlaintextFileStore{dir: dir, namespace: "new"}

	const fingerprint = "fp"
	seeded := Credential{
		AccountID: 3, AccountFingerprint: fingerprint, Username: "me@example.com",
		AccessToken: "ya29.expired", RefreshToken: "1//0xyz",
	}
	if err := legacy.Set(seeded); err != nil {
		t.Fatalf("seed the legacy credential: %v", err)
	}

	sources := []legacyCredentialStore{{store: legacy, maxAccountID: 3, limited: true}}
	store := &migratingCredentialStore{
		primary: &secretlessFileStore{
			inner:  primary,
			legacy: sources,
			reason: errors.New("keyring unavailable"),
		},
		legacy: sources,
	}

	if _, err := store.Get(3, fingerprint); err != nil {
		t.Fatalf("read the legacy credential: %v", err)
	}
	// The migration must have landed in the primary namespace.
	migrated, err := primary.Get(3, fingerprint)
	if err != nil {
		t.Fatalf("read the migrated credential: %v", err)
	}
	if migrated.RefreshToken != seeded.RefreshToken {
		t.Fatalf("RefreshToken = %q, want %q", migrated.RefreshToken, seeded.RefreshToken)
	}

	// A token refresh writes the new access token back through the store.
	refreshed := migrated
	refreshed.AccessToken = "ya29.fresh"
	if err := store.Set(refreshed); err != nil {
		t.Fatalf("persist the refreshed token: %v", err)
	}

	// A different account still has no secret on disk, so it stays refused.
	other := Credential{AccountID: 2, Username: "other@example.com", Password: "hunter2"}
	if err := store.Set(other); !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("Set for an account without a secret: err = %v, want ErrPlaintextRequired", err)
	}
}

// TestSecretlessStoreRefusesABeyondRangeLegacySecret keeps the maxAccountID
// bound of a limited legacy source. An account the previous namespace never
// held must not borrow its permission.
func TestSecretlessStoreRefusesABeyondRangeLegacySecret(t *testing.T) {
	dir := t.TempDir()
	legacy := &PlaintextFileStore{dir: dir, namespace: "old"}
	if err := legacy.Set(Credential{AccountID: 9, Username: "me", Password: "hunter2"}); err != nil {
		t.Fatalf("seed the legacy credential: %v", err)
	}
	store := &secretlessFileStore{
		inner:  &PlaintextFileStore{dir: dir, namespace: "new"},
		legacy: []legacyCredentialStore{{store: legacy, maxAccountID: 3, limited: true}},
		reason: errors.New("keyring unavailable"),
	}
	if store.holdsSecret(9) {
		t.Fatal("a limited legacy source answered beyond its maxAccountID")
	}
}

// TestPlaintextStoreWarnsOnlyForASecret confirms the plaintext warning
// reports what actually landed. A credential with a password command keeps no
// secret in the file, so the "stored in plaintext" line would misreport it.
func TestPlaintextStoreWarnsOnlyForASecret(t *testing.T) {
	var warn bytes.Buffer
	store := &PlaintextFileStore{dir: t.TempDir(), namespace: "test", warn: &warn}

	if err := store.Set(Credential{AccountID: 1, Username: "scott", PasswordCommand: "pass show caldav"}); err != nil {
		t.Fatalf("Set with a password command: %v", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("warning for a secretless credential: %q", warn.String())
	}

	if err := store.Set(Credential{AccountID: 2, Username: "scott", Password: "hunter2"}); err != nil {
		t.Fatalf("Set with a password: %v", err)
	}
	if !strings.Contains(warn.String(), "plaintext") {
		t.Fatalf("no plaintext warning for a stored password: %q", warn.String())
	}
}

// TestSecretlessStoreRefreshesAnExistingSecret covers the OAuth refresh path.
// A Google access token expires after about an hour. The refresh writes the
// new token back through the store, so a refusal there would break an account
// that an earlier --allow-plaintext run set up. The file already holds the
// secret, so the rewrite protects nothing and must go through.
func TestSecretlessStoreRefreshesAnExistingSecret(t *testing.T) {
	var warn bytes.Buffer
	plaintext := &PlaintextFileStore{dir: t.TempDir(), namespace: "test", warn: &warn}
	stored := Credential{
		AccountID:         1,
		Username:          "scott",
		AccessToken:       "old-token",
		RefreshToken:      "1//0xyz",
		OAuthClientID:     "cid.apps.googleusercontent.com",
		OAuthClientSecret: "GOCSPX-x",
	}
	if err := plaintext.Set(stored); err != nil {
		t.Fatalf("seed the opt-in write: %v", err)
	}
	warn.Reset()

	secretless := &secretlessFileStore{inner: plaintext, reason: errors.New("no keyring")}
	refreshed := stored
	refreshed.AccessToken = "new-token"
	if err := secretless.Set(refreshed); err != nil {
		t.Fatalf("refresh of an existing secret: %v", err)
	}
	got, err := secretless.Get(1, "")
	if err != nil {
		t.Fatalf("Get after the refresh: %v", err)
	}
	if got.AccessToken != "new-token" {
		t.Fatalf("AccessToken = %q, want the refreshed token", got.AccessToken)
	}
	// A repeat warning says nothing new, and a TUI sync refreshes tokens
	// while Bubble Tea owns the terminal.
	if warn.Len() != 0 {
		t.Fatalf("warning repeated on a refresh: %q", warn.String())
	}
}

// TestSecretlessStoreRefusesASecretForAFreshAccount keeps the other half of
// the contract. A file that holds no secret must not gain one without the
// opt-in, even when a neighbouring account has one.
func TestSecretlessStoreRefusesASecretForAFreshAccount(t *testing.T) {
	plaintext := &PlaintextFileStore{dir: t.TempDir(), namespace: "test", warn: &bytes.Buffer{}}
	if err := plaintext.Set(Credential{AccountID: 1, Username: "scott", Password: "hunter2"}); err != nil {
		t.Fatalf("seed the opt-in write: %v", err)
	}
	secretless := &secretlessFileStore{inner: plaintext, reason: errors.New("no keyring")}

	err := secretless.Set(Credential{AccountID: 2, Username: "ripley", Password: "hunter2"})
	if !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("Set for a fresh account: err = %v, want ErrPlaintextRequired", err)
	}
	// A password command still needs no opt-in on that same fresh account.
	if err := secretless.Set(Credential{AccountID: 2, Username: "ripley", PasswordCommand: "pass show caldav"}); err != nil {
		t.Fatalf("Set with a password command: %v", err)
	}
}

// TestSecretlessStoreRefusesASecretOverAPasswordCommand confirms the check
// reads the stored file, not the incoming credential. An account that holds a
// password command keeps no secret, so a password must still need the opt-in.
func TestSecretlessStoreRefusesASecretOverAPasswordCommand(t *testing.T) {
	plaintext := &PlaintextFileStore{dir: t.TempDir(), namespace: "test", warn: &bytes.Buffer{}}
	secretless := &secretlessFileStore{inner: plaintext, reason: errors.New("no keyring")}
	if err := secretless.Set(Credential{AccountID: 1, Username: "scott", PasswordCommand: "pass show caldav"}); err != nil {
		t.Fatalf("Set with a password command: %v", err)
	}

	err := secretless.Set(Credential{AccountID: 1, Username: "scott", Password: "hunter2"})
	if !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("Set a password over a command: err = %v, want ErrPlaintextRequired", err)
	}
}

// TestRestoreCredentialPutsBackARefusedSecret pins the compensation contract:
// a store must accept back what it gave out. A relink that replaces a password
// with a password command empties the secret from the file. If the commit then
// fails, the restore of the old password must land, or the credential file and
// the database row diverge.
func TestRestoreCredentialPutsBackARefusedSecret(t *testing.T) {
	dir := t.TempDir()
	plaintext := &PlaintextFileStore{dir: dir, namespace: "test", warn: io.Discard}
	store := &secretlessFileStore{inner: plaintext, reason: errors.New("keyring unavailable")}

	opted := Credential{AccountID: 6, Username: "scott", Password: "hunter2"}
	if err := plaintext.Set(opted); err != nil {
		t.Fatalf("seed the opted-in credential: %v", err)
	}
	prior, err := CapturePriorCredential(store, 6, "")
	if err != nil {
		t.Fatalf("capture the prior credential: %v", err)
	}

	// The relink writes a command-only credential, which carries no secret.
	if err := store.Set(Credential{AccountID: 6, Username: "scott", PasswordCommand: "pass show caldav"}); err != nil {
		t.Fatalf("store the command-only credential: %v", err)
	}
	// A plain Set can no longer put the password back: the file holds none.
	if err := store.Set(opted); !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("Set of the prior credential: err = %v, want ErrPlaintextRequired", err)
	}

	cause := errors.New("commit failed")
	err = prior.Restore(store, 6, true, "commit remote calendar link", cause)
	if !errors.Is(err, cause) {
		t.Fatalf("Restore err = %v, want it to wrap the cause", err)
	}
	if errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("Restore refused to put back a credential the store gave out: %v", err)
	}
	restored, err := plaintext.Get(6, "")
	if err != nil {
		t.Fatalf("read the restored credential: %v", err)
	}
	if restored.Password != "hunter2" || restored.PasswordCommand != "" {
		t.Fatalf("restored credential = %+v, want the prior password", restored)
	}
}
