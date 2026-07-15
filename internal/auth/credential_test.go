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

// TestAppConfigBaseDir_HonorsXDGOnNonLinux reproduces issue #372: before the
// fix, XDG_CONFIG_HOME was only consulted when GOOS == "linux", so macOS/Windows
// users who set XDG_CONFIG_HOME got credentials written under os.UserConfigDir()
// while the config loader read from XDG — two different roots.
func TestAppConfigBaseDir_HonorsXDGOnNonLinux(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Simulate Darwin — the platform where the bug manifested.
	got, err := appConfigBaseDir("darwin")
	if err != nil {
		t.Fatalf("appConfigBaseDir: %v", err)
	}
	if got != dir {
		t.Errorf("appConfigBaseDir(darwin) with XDG_CONFIG_HOME=%q returned %q; want XDG_CONFIG_HOME to be honoured on all platforms", dir, got)
	}
}

func TestAppConfigBaseDir_HonorsXDGOnLinux(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got, err := appConfigBaseDir("linux")
	if err != nil {
		t.Fatalf("appConfigBaseDir: %v", err)
	}
	if got != dir {
		t.Errorf("appConfigBaseDir(linux) with XDG_CONFIG_HOME=%q returned %q; want %q", dir, got, dir)
	}
}

func TestAppConfigBaseDir_LinuxFallsBackToHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "") // explicitly unset
	t.Setenv("HOME", home)

	got, err := appConfigBaseDir("linux")
	if err != nil {
		t.Fatalf("appConfigBaseDir: %v", err)
	}
	want := filepath.Join(home, ".config")
	if got != want {
		t.Errorf("appConfigBaseDir(linux) without XDG = %q, want %q", got, want)
	}
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

	got, err := store.Get(42, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != "alice" || got.Password != "secret123" {
		t.Errorf("Get returned %+v, want username=alice password=secret123", got)
	}

	if err := store.Delete(42); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Get(42, "")
	if err == nil {
		t.Error("Get after Delete should return error")
	}
}

func TestPlaintextFileStores_IsolateEqualAccountIDsByDatabaseNamespace(t *testing.T) {
	dir := t.TempDir()
	first := &PlaintextFileStore{dir: dir, namespace: "database-a", warn: io.Discard}
	second := &PlaintextFileStore{dir: dir, namespace: "database-b", warn: io.Discard}
	if err := first.Set(Credential{AccountID: 1, Password: "first"}); err != nil {
		t.Fatalf("set first: %v", err)
	}
	if err := second.Set(Credential{AccountID: 1, Password: "second"}); err != nil {
		t.Fatalf("set second: %v", err)
	}
	firstCred, err := first.Get(1, "")
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	secondCred, err := second.Get(1, "")
	if err != nil {
		t.Fatalf("get second: %v", err)
	}
	if firstCred.Password != "first" || secondCred.Password != "second" {
		t.Fatalf("namespaced plaintext credentials collided: first=%+v second=%+v", firstCred, secondCred)
	}
}

func TestPlaintextFileStore_GetMissing(t *testing.T) {
	store := &PlaintextFileStore{dir: t.TempDir()}
	_, err := store.Get(999, "")
	if err == nil {
		t.Error("Get for non-existent account should return error")
	}
}

func TestPlaintextFileStore_GetMissingReturnsErrNotFound(t *testing.T) {
	store := &PlaintextFileStore{dir: t.TempDir()}
	_, err := store.Get(999, "")
	if !errors.Is(err, errCredentialNotFound) {
		t.Errorf("Get for non-existent account should satisfy errors.Is(err, errCredentialNotFound), got %v", err)
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

	got, err := store.Get(1, "")
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

	store, err := NewCredentialStore("test", nil, true, false)
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

	_, err := NewCredentialStore("test", nil, true, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), probeErr.Error()) {
		t.Fatalf("expected error to include probe failure %q, got %q", probeErr.Error(), err.Error())
	}
}

func TestNewCredentialStore_AllowPlaintext(t *testing.T) {
	overrideKeyringForTest(t, false, map[string]string{})

	store, err := NewCredentialStore("test", nil, false, true)
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

	store, err := NewCredentialStore("test", nil, false, false)
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

	got, err := store.Get(7, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != "alice" || got.Password != "secret123" {
		t.Fatalf("Get returned %+v", got)
	}
}

func TestCredentialStores_IsolateEqualAccountIDsByDatabaseNamespace(t *testing.T) {
	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)

	first, err := NewCredentialStore("database-a", nil, false, false)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	second, err := NewCredentialStore("database-b", nil, false, false)
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	if err := first.Set(Credential{AccountID: 1, Username: "alice", Password: "first"}); err != nil {
		t.Fatalf("set first: %v", err)
	}
	if err := second.Set(Credential{AccountID: 1, Username: "bob", Password: "second"}); err != nil {
		t.Fatalf("set second: %v", err)
	}

	firstCred, err := first.Get(1, "")
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	secondCred, err := second.Get(1, "")
	if err != nil {
		t.Fatalf("get second: %v", err)
	}
	if firstCred.Password != "first" || secondCred.Password != "second" {
		t.Fatalf("namespaced credentials collided: first=%+v second=%+v", firstCred, secondCred)
	}
	if _, ok := backing["db_database-a_account_1"]; !ok {
		t.Fatal("first namespaced key missing")
	}
	if _, ok := backing["db_database-b_account_1"]; !ok {
		t.Fatal("second namespaced key missing")
	}
}

func TestCredentialStore_MigratesLegacyKeyringCredential(t *testing.T) {
	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)
	legacy := &KeyringStore{}
	if err := legacy.Set(Credential{AccountID: 3, Username: "legacy", Password: "secret"}); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}

	store, err := NewCredentialStore("database-new", nil, true, false)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	got, err := store.Get(3, "")
	if err != nil {
		t.Fatalf("migrating Get: %v", err)
	}
	if got.Username != "legacy" || got.Password != "secret" {
		t.Fatalf("migrated credential = %+v", got)
	}
	if _, exists := backing["account_3"]; exists {
		t.Fatal("legacy unscoped key survived migration")
	}
	if _, exists := backing["db_database-new_account_3"]; !exists {
		t.Fatal("namespaced key missing after migration")
	}
}

func TestCredentialStore_CopiesPriorScopeWithoutDeletingSource(t *testing.T) {
	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)
	source := &KeyringStore{namespace: "database-source"}
	if err := source.Set(Credential{AccountID: 1, Username: "alice", Password: "source"}); err != nil {
		t.Fatalf("seed source credential: %v", err)
	}

	copyStore, err := NewCredentialStore("database-copy", []PreviousCredentialScope{{
		Namespace: "database-source", MaxAccountID: 1,
	}}, false, false)
	if err != nil {
		t.Fatalf("copy store: %v", err)
	}
	got, err := copyStore.Get(1, "")
	if err != nil {
		t.Fatalf("copy prior credential: %v", err)
	}
	if got.Password != "source" {
		t.Fatalf("copied credential = %+v", got)
	}
	if _, exists := backing["db_database-source_account_1"]; !exists {
		t.Fatal("reading a copied database deleted the source credential")
	}
	if err := copyStore.Set(Credential{AccountID: 1, Username: "alice", Password: "copy"}); err != nil {
		t.Fatalf("update copied credential: %v", err)
	}
	sourceCred, err := source.Get(1, "")
	if err != nil {
		t.Fatalf("source credential after copied update: %v", err)
	}
	if sourceCred.Password != "source" {
		t.Fatalf("copied database overwrote source credential: %+v", sourceCred)
	}
	if err := source.Set(Credential{AccountID: 2, Username: "source-new", Password: "must-not-leak"}); err != nil {
		t.Fatalf("seed divergent source account: %v", err)
	}
	if _, err := copyStore.Get(2, ""); !errors.Is(err, errCredentialNotFound) {
		t.Fatalf("copied database read post-copy source account: %v", err)
	}
}

func TestCredentialStore_RejectsPriorScopeCredentialAfterSourceRelink(t *testing.T) {
	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)
	source := &KeyringStore{namespace: "database-source"}
	oldFingerprint := AccountFingerprint("https://old.example.test/dav", "basic", "alice")
	newFingerprint := AccountFingerprint("https://new.example.test/dav", "basic", "alice")
	if err := source.Set(Credential{
		AccountID: 1, AccountFingerprint: oldFingerprint, Username: "alice", Password: "old",
	}); err != nil {
		t.Fatalf("seed source credential: %v", err)
	}

	copyStore, err := NewCredentialStore("database-copy", []PreviousCredentialScope{{
		Namespace: "database-source", MaxAccountID: 1,
	}}, false, false)
	if err != nil {
		t.Fatalf("copy store: %v", err)
	}
	if err := source.Set(Credential{
		AccountID: 1, AccountFingerprint: newFingerprint, Username: "alice", Password: "new",
	}); err != nil {
		t.Fatalf("relink source credential: %v", err)
	}

	if _, err := copyStore.Get(1, oldFingerprint); !errors.Is(err, ErrCredentialIdentityMismatch) {
		t.Fatalf("copied database credential read error = %v, want identity mismatch", err)
	}
	if _, exists := backing["db_database-copy_account_1"]; exists {
		t.Fatal("identity-mismatched prior credential was copied into the current scope")
	}
}

func TestPlaintextFileStore_RejectsCredentialIdentityMismatch(t *testing.T) {
	store := &PlaintextFileStore{dir: t.TempDir(), warn: io.Discard}
	if err := store.Set(Credential{
		AccountID: 7, AccountFingerprint: "fingerprint-a", Password: "secret",
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := store.Get(7, "fingerprint-b"); !errors.Is(err, ErrCredentialIdentityMismatch) {
		t.Fatalf("Get error = %v, want identity mismatch", err)
	}
}

func TestNewCredentialStore_MigratesLegacyPlaintextCredentials(t *testing.T) {
	dir := t.TempDir()
	// credentialDir honours XDG_CONFIG_HOME on every platform (issue #372).
	// Setting XDG_CONFIG_HOME is sufficient to pin the credential directory
	// on all OSes; HOME is also set so the Linux fallback path is stable if
	// XDG_CONFIG_HOME happens to be cleared elsewhere in the process.
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

	store, err := NewCredentialStore("test", nil, true, false)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}

	got, err := store.Get(99, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Username != "legacy" || got.Password != "plaintext-secret" {
		t.Fatalf("Get returned %+v", got)
	}

	if _, err := legacyStore.Get(99, ""); !errors.Is(err, errCredentialNotFound) {
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
		legacy:  []legacyCredentialStore{{store: legacy, cleanup: true}},
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
		legacy:  []legacyCredentialStore{{store: legacy, cleanup: true}},
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
		legacy:  []legacyCredentialStore{{store: legacy, cleanup: true}},
	}

	// Migration succeeds (primary.Set writes to the keyring) but cleaning up
	// the legacy copy fails. Get must still return the migrated credential
	// rather than a spurious cleanup error.
	got, err := store.Get(42, "")
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

// TestMigratingCredentialStore_GetReturnsLegacyWhenMigrationWriteFails
// reproduces issue #423: when the primary keyring is transiently
// unavailable for writes, the migration's primary.Set fails. Get must
// still return the legacy credential (a successful read) rather than
// turning the read into an error and locking the user out of sync for
// the whole process lifetime.
func TestMigratingCredentialStore_GetReturnsLegacyWhenMigrationWriteFails(t *testing.T) {
	legacy := &PlaintextFileStore{dir: t.TempDir(), warn: io.Discard}

	legacyCred := Credential{
		AccountID: 42,
		Username:  "legacy",
		Password:  "plaintext-secret",
	}
	if err := legacy.Set(legacyCred); err != nil {
		t.Fatalf("legacy Set: %v", err)
	}

	// Keyring reads miss (no migrated copy yet) and keyring writes fail,
	// simulating a transient backend that can be read-probed but not written.
	backing := map[string]string{}
	overrideKeyringForTest(t, true, backing)
	keyringSetFn = func(service, user, value string) error {
		return errors.New("keyring temporarily unavailable")
	}

	store := &migratingCredentialStore{
		primary: &KeyringStore{},
		legacy:  []legacyCredentialStore{{store: legacy, cleanup: true}},
	}

	got, err := store.Get(42, "")
	if err != nil {
		t.Fatalf("Get should return the legacy credential when migration write fails, got %v", err)
	}
	if got.Username != "legacy" || got.Password != "plaintext-secret" {
		t.Fatalf("Get returned %+v, want the legacy credential", got)
	}
	// The migration write failed, so the legacy copy must survive for a
	// later retry rather than being deleted.
	if _, err := legacy.Get(42, ""); err != nil {
		t.Fatalf("legacy credential should survive a failed migration write, got %v", err)
	}
}

type getStubCredentialStore struct {
	cred     Credential
	getErr   error
	getCalls int
}

func (s *getStubCredentialStore) Get(int64, string) (Credential, error) {
	s.getCalls++
	return s.cred, s.getErr
}

func (s *getStubCredentialStore) Set(Credential) error { return nil }
func (s *getStubCredentialStore) Delete(int64) error   { return nil }

func TestMigratingCredentialStore_PrimaryOperationalReadFailureDoesNotFallback(t *testing.T) {
	primaryErr := errors.New("keyring temporarily unavailable")
	primary := &getStubCredentialStore{getErr: primaryErr}
	legacy := &getStubCredentialStore{cred: Credential{AccountID: 1, Password: "stale"}}
	store := &migratingCredentialStore{
		primary: primary,
		legacy:  []legacyCredentialStore{{store: legacy}},
	}

	if _, err := store.Get(1, "fingerprint"); !errors.Is(err, primaryErr) {
		t.Fatalf("Get error = %v, want primary backend failure", err)
	}
	if legacy.getCalls != 0 {
		t.Fatalf("legacy Get calls = %d, want 0 after primary operational failure", legacy.getCalls)
	}
}

func TestMigratingCredentialStore_LegacyOperationalReadFailureDoesNotTryOlderSource(t *testing.T) {
	legacyErr := errors.New("prior keyring unavailable")
	primary := &getStubCredentialStore{getErr: errCredentialNotFound}
	higherPriority := &getStubCredentialStore{getErr: legacyErr}
	older := &getStubCredentialStore{cred: Credential{AccountID: 1, Password: "stale"}}
	store := &migratingCredentialStore{
		primary: primary,
		legacy: []legacyCredentialStore{
			{store: higherPriority},
			{store: older},
		},
	}

	if _, err := store.Get(1, "fingerprint"); !errors.Is(err, legacyErr) {
		t.Fatalf("Get error = %v, want higher-priority backend failure", err)
	}
	if older.getCalls != 0 {
		t.Fatalf("older source Get calls = %d, want 0 after operational failure", older.getCalls)
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
