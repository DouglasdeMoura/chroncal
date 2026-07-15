package auth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// Credential holds authentication secrets for an account.
type Credential struct {
	AccountID          int64  `json:"account_id"`
	Username           string `json:"username,omitempty"`
	Password           string `json:"password,omitempty"`
	AccessToken        string `json:"access_token,omitempty"`
	RefreshToken       string `json:"refresh_token,omitempty"`
	TokenExpiry        string `json:"token_expiry,omitempty"` // RFC 3339
	AccountFingerprint string `json:"account_fingerprint,omitempty"`
	// OAuth client config (stored with credential, not in DB)
	OAuthClientID     string `json:"oauth_client_id,omitempty"`
	OAuthClientSecret string `json:"oauth_client_secret,omitempty"`
}

// CredentialStore provides read/write access to account credentials.
type CredentialStore interface {
	Get(accountID int64, accountFingerprint string) (Credential, error)
	Set(cred Credential) error
	Delete(accountID int64) error
}

// PreviousCredentialScope is a non-destructive migration source recorded when
// a database is moved or copied. MaxAccountID is the highest account ID that
// existed at that location; bounding fallback prevents independently-created
// post-copy accounts with the same numeric ID from sharing credentials.
type PreviousCredentialScope struct {
	Namespace    string
	MaxAccountID int64
}

const keyringService = "chroncal"

var errCredentialNotFound = keyring.ErrNotFound

// IsCredentialNotFound reports whether a credential lookup found no entry.
// Lifecycle code uses it to distinguish an absent credential from a transient
// backend failure that must abort destructive changes.
func IsCredentialNotFound(err error) bool {
	return errors.Is(err, errCredentialNotFound)
}

// AccountFingerprint binds a credential to the connection identity it was
// issued for. It intentionally excludes display names and numeric IDs.
func AccountFingerprint(serverURL, authType, username string) string {
	identity := strings.TrimSpace(serverURL) + "\x00" +
		strings.ToLower(strings.TrimSpace(authType)) + "\x00" +
		strings.TrimSpace(username)
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", sum)
}

var ErrCredentialIdentityMismatch = errors.New("credential belongs to a different account connection")

var (
	keyringUnavailableReasonFn = newKeyringAvailabilityProbe()
	keyringGetFn               = keyring.Get
	keyringSetFn               = keyring.Set
	keyringDeleteFn            = keyring.Delete
)

// NewCredentialStore returns the best available credential store scoped to a
// database namespace. It tries strategies in order: OS keyring, encrypted
// file, plaintext. Plaintext is only used if allowPlaintext is true.
//
// previousNamespaces are read-only migration sources recorded when the same
// database was opened under an older file identity. They are copied, never
// deleted: the older path may be a still-active source database from which this
// one was cloned. migrateLegacy controls one-way cleanup of the pre-namespace
// global account_<id> keys and is safe only for the default database.
func NewCredentialStore(namespace string, previousNamespaces []PreviousCredentialScope, migrateLegacy, allowPlaintext bool) (CredentialStore, error) {
	return NewCredentialStoreWithWarnings(namespace, previousNamespaces, migrateLegacy, allowPlaintext, os.Stderr)
}

// NewCredentialStoreWithWarnings is NewCredentialStore with plaintext-storage
// warnings routed to warn instead of stderr. The TUI passes a collector and
// surfaces the warnings in its own chrome — raw stderr writes would corrupt
// the bubbletea renderer mid-frame.
func NewCredentialStoreWithWarnings(namespace string, previousNamespaces []PreviousCredentialScope, migrateLegacy, allowPlaintext bool, warn io.Writer) (CredentialStore, error) {
	if !validCredentialNamespace(namespace) {
		return nil, fmt.Errorf("invalid credential namespace %q", namespace)
	}
	dir, err := credentialDir()
	if err != nil {
		return nil, err
	}
	plaintext := &PlaintextFileStore{dir: dir, namespace: namespace, warn: warn}

	var primary CredentialStore
	var legacy []legacyCredentialStore
	if probeErr := keyringUnavailableReason(); probeErr == nil {
		primary = &KeyringStore{namespace: namespace}
		for _, previous := range previousNamespaces {
			if previous.Namespace == namespace || !validCredentialNamespace(previous.Namespace) {
				continue
			}
			legacy = append(legacy,
				legacyCredentialStore{store: &KeyringStore{namespace: previous.Namespace}, maxAccountID: previous.MaxAccountID, limited: true},
				legacyCredentialStore{store: &PlaintextFileStore{dir: dir, namespace: previous.Namespace, warn: warn}, maxAccountID: previous.MaxAccountID, limited: true},
			)
		}
		if migrateLegacy {
			legacy = append(legacy,
				legacyCredentialStore{store: &KeyringStore{}, cleanup: true},
				legacyCredentialStore{store: &PlaintextFileStore{dir: dir, warn: warn}, cleanup: true},
			)
		}
	} else {
		// Encrypted file store needs a passphrase prompt; skip for now.
		// TODO: implement EncryptedFileStore with argon2id + AES-256-GCM
		if !allowPlaintext {
			return nil, fmt.Errorf("no secure credential store available: %w; use --allow-plaintext to store credentials in plaintext, or install a keyring provider", keyringUnavailableReason())
		}
		primary = plaintext
		for _, previous := range previousNamespaces {
			if previous.Namespace != namespace && validCredentialNamespace(previous.Namespace) {
				legacy = append(legacy, legacyCredentialStore{
					store:        &PlaintextFileStore{dir: dir, namespace: previous.Namespace, warn: warn},
					maxAccountID: previous.MaxAccountID,
					limited:      true,
				})
			}
		}
		if migrateLegacy {
			legacy = append(legacy, legacyCredentialStore{
				store: &PlaintextFileStore{dir: dir, warn: warn}, cleanup: true,
			})
		}
	}
	if len(legacy) == 0 {
		return primary, nil
	}
	return &migratingCredentialStore{primary: primary, legacy: legacy}, nil
}

func keyringUnavailableReason() error {
	return keyringUnavailableReasonFn()
}

func validCredentialNamespace(namespace string) bool {
	if namespace == "" {
		return false
	}
	for _, r := range namespace {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// KeyringStore stores credentials in the OS keyring (GNOME Keyring, KWallet, macOS Keychain).
type KeyringStore struct {
	namespace string
}

func (s *KeyringStore) Get(accountID int64, accountFingerprint string) (Credential, error) {
	secret, err := keyringGetFn(keyringService, keyringAccountName(s.namespace, accountID))
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
	if err := validateCredentialFingerprint(cred, accountFingerprint); err != nil {
		return Credential{}, err
	}
	return cred, nil
}

func (s *KeyringStore) Set(cred Credential) error {
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}
	if err := keyringSetFn(keyringService, keyringAccountName(s.namespace, cred.AccountID), string(data)); err != nil {
		return fmt.Errorf("write credential to keyring: %w", err)
	}
	return nil
}

func (s *KeyringStore) Delete(accountID int64) error {
	err := keyringDeleteFn(keyringService, keyringAccountName(s.namespace, accountID))
	if err != nil && !errors.Is(err, errCredentialNotFound) {
		return fmt.Errorf("delete credential from keyring: %w", err)
	}
	return nil
}

// PlaintextFileStore stores credentials as JSON files with 0600 permissions.
// Requires explicit --allow-plaintext flag.
type PlaintextFileStore struct {
	dir       string
	namespace string
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

func (s *PlaintextFileStore) Get(accountID int64, accountFingerprint string) (Credential, error) {
	path := s.path(accountID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credential{}, errCredentialNotFound
		}
		return Credential{}, fmt.Errorf("read credential: %w", err)
	}
	var cred Credential
	if err := json.Unmarshal(data, &cred); err != nil {
		return Credential{}, fmt.Errorf("parse credential: %w", err)
	}
	if err := validateCredentialFingerprint(cred, accountFingerprint); err != nil {
		return Credential{}, err
	}
	return cred, nil
}

func (s *PlaintextFileStore) Set(cred Credential) error {
	if err := os.MkdirAll(filepath.Dir(s.path(cred.AccountID)), 0o700); err != nil {
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
	if s.namespace == "" {
		return filepath.Join(s.dir, fmt.Sprintf("account_%d.json", accountID))
	}
	return filepath.Join(s.dir, "db_"+s.namespace, fmt.Sprintf("account_%d.json", accountID))
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
	switch typed := store.(type) {
	case *PlaintextFileStore:
		return "plaintext files"
	case *migratingCredentialStore:
		return StoreDescription(typed.primary)
	default:
		return "OS keyring"
	}
}

type legacyCredentialStore struct {
	store        CredentialStore
	cleanup      bool
	maxAccountID int64
	limited      bool
}

type migratingCredentialStore struct {
	primary CredentialStore
	legacy  []legacyCredentialStore
}

func (s *migratingCredentialStore) Get(accountID int64, accountFingerprint string) (Credential, error) {
	cred, err := s.primary.Get(accountID, accountFingerprint)
	if err == nil {
		if cred.AccountFingerprint == "" {
			cred.AccountFingerprint = accountFingerprint
			if setErr := s.primary.Set(cred); setErr != nil {
				return cred, nil //nolint:nilerr // a successful read remains usable
			}
		}
		return cred, nil
	}
	if errors.Is(err, ErrCredentialIdentityMismatch) {
		return Credential{}, err
	}
	if !IsCredentialNotFound(err) {
		return Credential{}, err
	}

	var identityErr error
	for _, legacy := range s.legacy {
		if legacy.limited && accountID > legacy.maxAccountID {
			continue
		}
		legacyCred, legacyErr := legacy.store.Get(accountID, accountFingerprint)
		if errors.Is(legacyErr, ErrCredentialIdentityMismatch) {
			identityErr = legacyErr
			continue
		}
		if legacyErr != nil {
			if IsCredentialNotFound(legacyErr) {
				continue
			}
			return Credential{}, legacyErr
		}
		if legacyCred.AccountFingerprint == "" {
			legacyCred.AccountFingerprint = accountFingerprint
		}
		// Migrate into the primary store best-effort. A transient write failure
		// must not turn a successful legacy read into an error.
		if setErr := s.primary.Set(legacyCred); setErr != nil {
			return legacyCred, nil //nolint:nilerr // successful legacy read remains usable
		}
		for _, source := range s.legacy {
			if source.cleanup {
				_ = source.store.Delete(accountID)
			}
		}
		return legacyCred, nil
	}
	if identityErr != nil {
		return Credential{}, identityErr
	}
	return Credential{}, err
}

func (s *migratingCredentialStore) Set(cred Credential) error {
	if err := s.primary.Set(cred); err != nil {
		return err
	}
	for _, legacy := range s.legacy {
		if legacy.cleanup {
			_ = legacy.store.Delete(cred.AccountID)
		}
	}
	return nil
}

func (s *migratingCredentialStore) Delete(accountID int64) error {
	if err := s.primary.Delete(accountID); err != nil {
		return err
	}
	for _, legacy := range s.legacy {
		if !legacy.cleanup {
			continue
		}
		// Surface a real legacy-delete failure: discarding it would report
		// success while the credential survives. Store Delete methods already
		// treat a missing credential as success.
		if err := legacy.store.Delete(accountID); err != nil {
			return err
		}
	}
	return nil
}

func validateCredentialFingerprint(cred Credential, expected string) error {
	if expected != "" && cred.AccountFingerprint != "" && cred.AccountFingerprint != expected {
		return ErrCredentialIdentityMismatch
	}
	return nil
}

func keyringAccountName(namespace string, accountID int64) string {
	if namespace == "" {
		return fmt.Sprintf("account_%d", accountID)
	}
	return fmt.Sprintf("db_%s_account_%d", namespace, accountID)
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
