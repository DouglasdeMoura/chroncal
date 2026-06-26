package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
)

// Credential holds authentication secrets for an account.
type Credential struct {
	AccountID    int64  `json:"account_id"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenExpiry  string `json:"token_expiry,omitempty"` // RFC 3339
	// OAuth client config (stored with credential, not in DB)
	OAuthClientID     string `json:"oauth_client_id,omitempty"`
	OAuthClientSecret string `json:"oauth_client_secret,omitempty"`
}

// CredentialStore provides read/write access to account credentials.
type CredentialStore interface {
	Get(accountID int64) (Credential, error)
	Set(cred Credential) error
	Delete(accountID int64) error
}

const keyringService = "chroncal"

var errCredentialNotFound = keyring.ErrNotFound

var (
	keyringUnavailableReasonFn = newKeyringAvailabilityProbe()
	keyringGetFn               = keyring.Get
	keyringSetFn               = keyring.Set
	keyringDeleteFn            = keyring.Delete
)

// NewCredentialStore returns the best available credential store.
// It tries strategies in order: OS keyring, encrypted file, plaintext.
// Plaintext is only used if allowPlaintext is true. Plaintext-storage
// warnings go to stderr; full-screen UIs that own the terminal use
// NewCredentialStoreWithWarnings to route them elsewhere.
func NewCredentialStore(allowPlaintext bool) (CredentialStore, error) {
	return NewCredentialStoreWithWarnings(allowPlaintext, os.Stderr)
}

// NewCredentialStoreWithWarnings is NewCredentialStore with the
// plaintext-storage warnings routed to warn instead of stderr. The TUI
// passes a collector and surfaces the warnings in its own chrome — raw
// stderr writes would corrupt the bubbletea renderer mid-frame.
func NewCredentialStoreWithWarnings(allowPlaintext bool, warn io.Writer) (CredentialStore, error) {
	dir, err := credentialDir()
	if err != nil {
		return nil, err
	}
	plaintext := &PlaintextFileStore{dir: dir, warn: warn}

	if probeErr := keyringUnavailableReason(); probeErr == nil {
		return &migratingCredentialStore{
			primary: &KeyringStore{},
			legacy:  plaintext,
		}, nil
	}
	// Encrypted file store needs a passphrase prompt; skip for now.
	// TODO: implement EncryptedFileStore with argon2id + AES-256-GCM
	if allowPlaintext {
		return plaintext, nil
	}
	return nil, fmt.Errorf("no secure credential store available: %w; use --allow-plaintext to store credentials in plaintext, or install a keyring provider", keyringUnavailableReason())
}

func keyringUnavailableReason() error {
	return keyringUnavailableReasonFn()
}

// KeyringStore stores credentials in the OS keyring (GNOME Keyring, KWallet, macOS Keychain).
type KeyringStore struct{}

func (s *KeyringStore) Get(accountID int64) (Credential, error) {
	secret, err := keyringGetFn(keyringService, keyringAccountName(accountID))
	if err != nil {
		if errors.Is(err, errCredentialNotFound) {
			return Credential{}, errCredentialNotFound
		}
		return Credential{}, fmt.Errorf("read credential from keyring: %w", err)
	}
	var cred Credential
	if err := json.Unmarshal([]byte(secret), &cred); err != nil {
		return Credential{}, fmt.Errorf("parse credential from keyring: %w", err)
	}
	return cred, nil
}

func (s *KeyringStore) Set(cred Credential) error {
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}
	if err := keyringSetFn(keyringService, keyringAccountName(cred.AccountID), string(data)); err != nil {
		return fmt.Errorf("write credential to keyring: %w", err)
	}
	return nil
}

func (s *KeyringStore) Delete(accountID int64) error {
	err := keyringDeleteFn(keyringService, keyringAccountName(accountID))
	if err != nil && !errors.Is(err, errCredentialNotFound) {
		return fmt.Errorf("delete credential from keyring: %w", err)
	}
	return nil
}

// PlaintextFileStore stores credentials as JSON files with 0600 permissions.
// Requires explicit --allow-plaintext flag.
type PlaintextFileStore struct {
	dir string
	// warn receives the plaintext-storage warnings emitted by Set. Nil
	// falls back to stderr so zero-value construction keeps CLI behavior.
	warn io.Writer
}

// warnWriter returns the configured warning sink, defaulting to stderr.
func (s *PlaintextFileStore) warnWriter() io.Writer {
	if s.warn != nil {
		return s.warn
	}
	return os.Stderr
}

func (s *PlaintextFileStore) Get(accountID int64) (Credential, error) {
	path := s.path(accountID)
	data, err := os.ReadFile(path)
	if err != nil {
		return Credential{}, fmt.Errorf("read credential: %w", err)
	}
	var cred Credential
	if err := json.Unmarshal(data, &cred); err != nil {
		return Credential{}, fmt.Errorf("parse credential: %w", err)
	}
	return cred, nil
}

func (s *PlaintextFileStore) Set(cred Credential) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create credential dir: %w", err)
	}
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}
	path := s.path(cred.AccountID)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}
	w := s.warnWriter()
	fmt.Fprintf(w, "Warning: credentials stored in plaintext at %s\n", path)
	if cred.OAuthClientSecret != "" {
		fmt.Fprintf(w, "Warning: OAuth client secret persisted to disk in cleartext. Backups, snapshots, and sync tools (Dropbox, iCloud, rsync) will see it. Install an OS keyring (libsecret on Linux) to avoid this.\n")
	}
	return nil
}

func (s *PlaintextFileStore) Delete(accountID int64) error {
	path := s.path(accountID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}

func (s *PlaintextFileStore) path(accountID int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("account_%d.json", accountID))
}

// appConfigBaseDir returns the OS base config directory, honouring
// XDG_CONFIG_HOME on every platform (matching the behaviour of the config
// loader's configDir). goos is a runtime.GOOS value; it is a parameter so
// tests can call it with a non-current GOOS to verify cross-platform behaviour.
func appConfigBaseDir(goos string) (string, error) {
	// XDG_CONFIG_HOME takes precedence on every OS, not just Linux.
	// Many CLI tools adopt this so users on macOS/Windows can relocate
	// config with a single env var. Checking it first here matches the
	// behaviour of the config loader (internal/config.configDir).
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir, nil
	}
	if goos == "linux" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config"), nil
	}
	return os.UserConfigDir()
}

func credentialDir() (string, error) {
	base, err := appConfigBaseDir(runtime.GOOS)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chroncal", "credentials"), nil
}

// StoreDescription returns a user-facing description of the credential backend.
func StoreDescription(store CredentialStore) string {
	switch store.(type) {
	case *PlaintextFileStore:
		return "plaintext files"
	default:
		return "OS keyring"
	}
}

type migratingCredentialStore struct {
	primary CredentialStore
	legacy  *PlaintextFileStore
}

func (s *migratingCredentialStore) Get(accountID int64) (Credential, error) {
	cred, err := s.primary.Get(accountID)
	if err == nil {
		return cred, nil
	}
	if s.legacy == nil {
		return Credential{}, err
	}

	legacyCred, legacyErr := s.legacy.Get(accountID)
	if legacyErr != nil {
		return Credential{}, err
	}
	if setErr := s.primary.Set(legacyCred); setErr != nil {
		return Credential{}, setErr
	}
	// Migration succeeded: the caller now has the credential and the primary
	// store owns it. Cleaning up the legacy copy is best-effort — a failure
	// here must not turn a successful read into an error. A surviving legacy
	// copy is reclaimed on the next Set or Delete.
	_ = s.legacy.Delete(accountID)
	return legacyCred, nil
}

func (s *migratingCredentialStore) Set(cred Credential) error {
	if err := s.primary.Set(cred); err != nil {
		return err
	}
	if s.legacy != nil {
		_ = s.legacy.Delete(cred.AccountID)
	}
	return nil
}

func (s *migratingCredentialStore) Delete(accountID int64) error {
	if err := s.primary.Delete(accountID); err != nil {
		return err
	}
	if s.legacy != nil {
		// Surface a real legacy-delete failure: discarding it would
		// report success while the credential survives in the legacy
		// store. PlaintextFileStore.Delete already treats a missing
		// file as success, so only genuine failures reach here.
		if err := s.legacy.Delete(accountID); err != nil {
			return err
		}
	}
	return nil
}

func keyringAccountName(accountID int64) string {
	return fmt.Sprintf("account_%d", accountID)
}

func newKeyringAvailabilityProbe() func() error {
	var (
		once     sync.Once
		probeErr error
	)
	return func() error {
		once.Do(func() {
			probeUser := "__chroncal_probe__"
			probeValue := "ok"
			if err := keyringSetFn(keyringService, probeUser, probeValue); err != nil {
				probeErr = err
				return
			}
			_ = keyringDeleteFn(keyringService, probeUser)
		})
		return probeErr
	}
}
