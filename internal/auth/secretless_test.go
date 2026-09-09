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
		{name: "blank password", cred: Credential{Password: "   "}, want: false},
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
