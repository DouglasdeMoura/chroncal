package auth

import (
	"bytes"
	"errors"
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

	if err := EnsureCanStoreSecret(plaintext); err != nil {
		t.Fatalf("plaintext store: %v", err)
	}
	if err := EnsureCanStoreSecret(&KeyringStore{namespace: "test"}); err != nil {
		t.Fatalf("keyring store: %v", err)
	}
	if err := EnsureCanStoreSecret(secretless); !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("secretless store: err = %v, want ErrPlaintextRequired", err)
	}
	wrapped := &migratingCredentialStore{primary: secretless}
	if err := EnsureCanStoreSecret(wrapped); !errors.Is(err, ErrPlaintextRequired) {
		t.Fatalf("wrapped secretless store: err = %v, want ErrPlaintextRequired", err)
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
