package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// NewCredentialStore returns the best available credential store.
// It tries strategies in order: OS keyring, encrypted file, plaintext.
// Plaintext is only used if allowPlaintext is true.
func NewCredentialStore(allowPlaintext bool) (CredentialStore, error) {
	if keyringAvailable() {
		return &KeyringStore{}, nil
	}
	// Encrypted file store needs a passphrase prompt; skip for now.
	// TODO: implement EncryptedFileStore with argon2id + AES-256-GCM
	if allowPlaintext {
		dir, err := credentialDir()
		if err != nil {
			return nil, err
		}
		return &PlaintextFileStore{dir: dir}, nil
	}
	return nil, fmt.Errorf("no secure credential store available; use --allow-plaintext to store credentials in plaintext, or install a keyring provider")
}

// keyringAvailable returns true if the OS keyring is usable.
func keyringAvailable() bool {
	// Keyring support requires zalando/go-keyring dependency.
	// For now, return false — will be implemented when go-keyring is added.
	return false
}

// KeyringStore stores credentials in the OS keyring (GNOME Keyring, KWallet, macOS Keychain).
type KeyringStore struct{}

func (s *KeyringStore) Get(accountID int64) (Credential, error) {
	return Credential{}, errors.New("keyring store not yet implemented")
}

func (s *KeyringStore) Set(cred Credential) error {
	return errors.New("keyring store not yet implemented")
}

func (s *KeyringStore) Delete(accountID int64) error {
	return errors.New("keyring store not yet implemented")
}

// PlaintextFileStore stores credentials as JSON files with 0600 permissions.
// Requires explicit --allow-plaintext flag.
type PlaintextFileStore struct {
	dir string
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
	fmt.Fprintf(os.Stderr, "Warning: credentials stored in plaintext at %s\n", path)
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

func credentialDir() (string, error) {
	var configDir string
	if runtime.GOOS == "linux" {
		if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
			configDir = dir
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			configDir = filepath.Join(home, ".config")
		}
	} else {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		configDir = dir
	}
	return filepath.Join(configDir, "chroncal", "credentials"), nil
}
