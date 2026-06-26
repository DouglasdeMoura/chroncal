package auth

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func overrideKeyringForTest(t *testing.T, available bool, values map[string]string) {
	t.Helper()

	prevUnavailableReason := keyringUnavailableReasonFn
	prevGet := keyringGetFn
	prevSet := keyringSetFn
	prevDelete := keyringDeleteFn

	keyringUnavailableReasonFn = func() error {
		if available {
			return nil
		}
		return errors.New("keyring unavailable")
	}
	keyringGetFn = func(service, user string) (string, error) {
		value, ok := values[user]
		if !ok {
			return "", errCredentialNotFound
		}
		return value, nil
	}
	keyringSetFn = func(service, user, value string) error {
		values[user] = value
		return nil
	}
	keyringDeleteFn = func(service, user string) error {
		delete(values, user)
		return nil
	}

	t.Cleanup(func() {
		keyringUnavailableReasonFn = prevUnavailableReason
		keyringGetFn = prevGet
		keyringSetFn = prevSet
		keyringDeleteFn = prevDelete
	})
}

func TestPlaintextFileStore_SetGetDelete(t *testing.T) {
	dir := t.TempDir()
	store := &PlaintextFileStore{dir: dir}

	cred := Credential{
		AccountID: 42,
		Username:  "alice",
		Password:  "secret123",
	}

	if err := store.Set(cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// File should exist with 0600 permissions
	path := filepath.Join(dir, "account_42.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}

	got, err := store.Get(42)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != "alice" || got.Password != "secret123" {
		t.Errorf("Get returned %+v, want username=alice password=secret123", got)
	}

	if err := store.Delete(42); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Get(42)
	if err == nil {
		t.Error("Get after Delete should return error")
	}
}

func TestPlaintextFileStore_GetMissing(t *testing.T) {
	store := &PlaintextFileStore{dir: t.TempDir()}
	_, err := store.Get(999)
	if err == nil {
		t.Error("Get for non-existent account should return error")
	}
}

func TestPlaintextFileStore_DeleteMissing(t *testing.T) {
	store := &PlaintextFileStore{dir: t.TempDir()}
	// Deleting a non-existent credential should not error
	if err := store.Delete(999); err != nil {
		t.Errorf("Delete non-existent should not error, got: %v", err)
	}
}

func TestPlaintextFileStore_OAuthCredentials(t *testing.T) {
	store := &PlaintextFileStore{dir: t.TempDir()}

	cred := Credential{
		AccountID:         1,
		AccessToken:       "ya29.abc",
		RefreshToken:      "1//0xyz",
		TokenExpiry:       "2026-04-03T12:00:00Z",
		OAuthClientID:     "client-id.apps.googleusercontent.com",
		OAuthClientSecret: "GOCSPX-test-secret",
	}

	if err := store.Set(cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken != "ya29.abc" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "ya29.abc")
	}
	if got.RefreshToken != "1//0xyz" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "1//0xyz")
	}
	if got.OAuthClientID != "client-id.apps.googleusercontent.com" {
		t.Errorf("OAuthClientID = %q, want %q", got.OAuthClientID, "client-id.apps.googleusercontent.com")
	}
	if got.OAuthClientSecret != "GOCSPX-test-secret" {
		t.Errorf("OAuthClientSecret = %q, want round-trip persistence", got.OAuthClientSecret)
	}

	data, err := os.ReadFile(filepath.Join(store.dir, "account_1.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "oauth_client_secret") {
		t.Fatal("plaintext store must persist oauth_client_secret so unattended refresh works; see README plaintext caveat")
	}
}

func TestNewCredentialStore_NoKeyring_NoPlaintext(t *testing.T) {
	overrideKeyringForTest(t, false, map[string]string{})

	store, err := NewCredentialStore(false)
	if err == nil {
		t.Errorf("expected error when no keyring and plaintext disabled, got store: %v", store)
	}
}

func TestNewCredentialStore_IncludesKeyringProbeError(t *testing.T) {
	prevUnavailableReason := keyringUnavailableReasonFn
	prevSet := keyringSetFn
	prevDelete := keyringDeleteFn

	probeErr := errors.New("dbus: org.freedesktop.secrets unavailable")
	keyringSetFn = func(service, user, value string) error {
		return probeErr
	}
	keyringDeleteFn = func(service, user string) error {
		return nil
	}
	keyringUnavailableReasonFn = newKeyringAvailabilityProbe()

	t.Cleanup(func() {
		keyringUnavailableReasonFn = prevUnavailableReason
		keyringSetFn = prevSet
		keyringDeleteFn = prevDelete
	})

	_, err := NewCredentialStore(false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), probeErr.Error()) {
		t.Fatalf("expected error to include probe failure %q, got %q", probeErr.Error(), err.Error())
	}
}

func TestNewCredentialStore_AllowPlaintext(t *testing.T) {
	overrideKeyringForTest(t, false, map[string]string{})

	store, err := NewCredentialStore(true)
	if err != nil {
		t.Fatalf("expected no error with plaintext allowed, got: %v", err)
	}
	if store == nil {
		t.Error("store should not be nil")
	}
	if _, ok := store.(*PlaintextFileStore); !ok {
		t.Errorf("expected PlaintextFileStore, got %T", store)
	}
}

func TestNewCredentialStore_PrefersKeyringWhenAvailable(t *testing.T) {
	overrideKeyringForTest(t, true, map[string]string{})

	store, err := NewCredentialStore(false)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}

	cred := Credential{
		AccountID: 7,
		Username:  "alice",
		Password:  "secret123",
	}
	if err := store.Set(cred); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(7)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != "alice" || got.Password != "secret123" {
		t.Fatalf("Get returned %+v", got)
	}
}

func TestNewCredentialStore_MigratesLegacyPlaintextCredentials(t *testing.T) {
	dir := t.TempDir()
	// Redirect the config dir the way each platform actually resolves it:
	// credentialDir honors XDG_CONFIG_HOME only on Linux; macOS uses
	// os.UserConfigDir ($HOME/Library/Application Support). Set both, then
	// derive the legacy dir from credentialDir itself so the test seeds the
	// legacy credential exactly where NewCredentialStore's internal plaintext
	// store looks for it — on any platform. (Hardcoding the Linux XDG layout
	// is what made this test fail on the macOS CI runner.)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	legacyDir, err := credentialDir()
	if err != nil {
		t.Fatalf("credentialDir: %v", err)
	}
	legacyStore := &PlaintextFileStore{dir: legacyDir}
	legacyCred := Credential{
		AccountID: 99,
		Username:  "legacy",
		Password:  "plaintext-secret",
	}
	if err := legacyStore.Set(legacyCred); err != nil {
		t.Fatalf("legacy Set: %v", err)
	}

	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)

	store, err := NewCredentialStore(false)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}

	got, err := store.Get(99)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != "legacy" || got.Password != "plaintext-secret" {
		t.Fatalf("Get returned %+v", got)
	}

	if _, err := legacyStore.Get(99); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy credential should be removed after migration, got %v", err)
	}
	if len(backing) == 0 {
		t.Fatal("expected migrated credential to be stored in keyring backing store")
	}
}

func TestMigratingCredentialStore_DeleteSurfacesLegacyError(t *testing.T) {
	dir := t.TempDir()
	legacy := &PlaintextFileStore{dir: dir}

	// Force the legacy delete to fail with a real (non not-found) error by
	// planting a non-empty directory where Delete expects to remove a file.
	// os.Remove on a non-empty directory returns ENOTEMPTY, which is not
	// os.IsNotExist, so PlaintextFileStore.Delete surfaces a wrapped error.
	credPath := legacy.path(42)
	if err := os.MkdirAll(filepath.Join(credPath, "blocker"), 0o700); err != nil {
		t.Fatalf("seed blocker dir: %v", err)
	}

	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)

	store := &migratingCredentialStore{
		primary: &KeyringStore{},
		legacy:  legacy,
	}

	// The primary (keyring) delete succeeds, but the legacy delete fails.
	// Delete must surface that failure rather than reporting success while
	// the credential survives in the legacy store.
	if err := store.Delete(42); err == nil {
		t.Fatal("Delete should surface the legacy store error, got nil")
	}
}

func TestMigratingCredentialStore_DeleteIgnoresLegacyNotFound(t *testing.T) {
	// A missing legacy credential is not a failure: Delete should report
	// success when both stores have nothing left to remove.
	legacy := &PlaintextFileStore{dir: t.TempDir()}
	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)

	store := &migratingCredentialStore{
		primary: &KeyringStore{},
		legacy:  legacy,
	}

	if err := store.Delete(7); err != nil {
		t.Fatalf("Delete should ignore a missing legacy credential, got %v", err)
	}
}

func TestMigratingCredentialStore_GetIgnoresLegacyCleanupError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses the directory write permission that forces the cleanup failure")
	}
	dir := t.TempDir()
	legacy := &PlaintextFileStore{dir: dir, warn: io.Discard}

	legacyCred := Credential{
		AccountID: 42,
		Username:  "legacy",
		Password:  "plaintext-secret",
	}
	if err := legacy.Set(legacyCred); err != nil {
		t.Fatalf("legacy Set: %v", err)
	}

	// Make the legacy directory read-only so the credential file stays
	// readable but the post-migration cleanup (os.Remove needs write on the
	// parent dir) fails. Restore write permission afterwards so t.TempDir can
	// clean up.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod legacy dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)

	store := &migratingCredentialStore{
		primary: &KeyringStore{},
		legacy:  legacy,
	}

	// Migration succeeds (primary.Set writes to the keyring) but cleaning up
	// the legacy copy fails. Get must still return the migrated credential
	// rather than a spurious cleanup error.
	got, err := store.Get(42)
	if err != nil {
		t.Fatalf("Get should ignore a legacy cleanup failure after a successful migration, got %v", err)
	}
	if got.Username != "legacy" || got.Password != "plaintext-secret" {
		t.Fatalf("Get returned %+v, want the migrated legacy credential", got)
	}
	if len(backing) == 0 {
		t.Fatal("expected the credential to be migrated into the keyring backing store")
	}
}

func TestPlaintextFileStore_WarningsRouteToInjectedWriter(t *testing.T) {
	var buf strings.Builder
	store := &PlaintextFileStore{dir: t.TempDir(), warn: &buf}

	err := store.Set(Credential{
		AccountID:         7,
		OAuthClientID:     "cid",
		OAuthClientSecret: "shh",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "credentials stored in plaintext") {
		t.Errorf("missing plaintext-location warning; got %q", out)
	}
	if !strings.Contains(out, "OAuth client secret persisted to disk") {
		t.Errorf("missing OAuth-secret warning; got %q", out)
	}
}
