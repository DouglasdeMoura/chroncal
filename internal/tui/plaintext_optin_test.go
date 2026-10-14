package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/config"
)

// optInConfigPath returns the config file that the plaintext opt-in writes.
// secretlessEnv points XDG_CONFIG_HOME at a temporary directory, so the
// tests never touch the real config file.
func optInConfigPath(t *testing.T) string {
	t.Helper()
	t.Setenv("CHRONCAL_SECURITY_ALLOW_PLAINTEXT", "")
	os.Unsetenv("CHRONCAL_SECURITY_ALLOW_PLAINTEXT")
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chroncal", "config.toml")
}

// refusedAddAccount runs a basic-auth Add Account request with a password
// against the secretless store and feeds the refusal back to the model.
func refusedAddAccount(t *testing.T) (Model, CalendarDiscoveryRequestedMsg) {
	t.Helper()
	secretlessEnv(t)
	m, _ := newDBBackedModel(t)
	m.calendarManagerOpen = true

	req := CalendarDiscoveryRequestedMsg{
		ServerURL: "https://cloud.example.com/remote.php/dav/",
		Username:  "scott",
		AuthType:  "basic",
		Secret:    "hunter2",
	}
	cred := auth.Credential{Username: req.Username, Password: req.Secret}
	msg := m.connectAndDiscoverCalendar(context.Background(), req, cred)()
	ready, ok := msg.(accountDiscoveryReadyMsg)
	if !ok {
		t.Fatalf("connectAndDiscoverCalendar cmd = %T, want accountDiscoveryReadyMsg", msg)
	}
	if !errors.Is(ready.err, auth.ErrPlaintextRequired) {
		t.Fatalf("connectAndDiscoverCalendar err = %v, want ErrPlaintextRequired", ready.err)
	}
	updated, _ := m.Update(ready)
	return updated.(Model), req
}

func TestAddAccountRefusalOffersPlaintextOptIn(t *testing.T) {
	m, _ := refusedAddAccount(t)
	path := optInConfigPath(t)

	if !m.confirmOpen || m.pending.kind != pendingActionPlaintextOptIn {
		t.Fatalf("confirmOpen = %v, pending = %v; want the plaintext opt-in prompt",
			m.confirmOpen, m.pending.kind)
	}
	if !m.calendarManagerOpen {
		t.Fatal("the Add Account form closed behind the prompt")
	}
	view := m.confirmDialog.View()
	for _, want := range []string{"0600", "backups", "snapshots", "password command"} {
		if !strings.Contains(view, want) {
			t.Errorf("the prompt does not mention %q:\n%s", want, view)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the prompt wrote the config file before the user agreed: %v", err)
	}
}

// TestPlaintextOptInFocusesCancel keeps a quick Enter safe: the prompt
// focuses Cancel, so Enter cancels and writes nothing.
func TestPlaintextOptInFocusesCancel(t *testing.T) {
	m, _ := refusedAddAccount(t)
	path := optInConfigPath(t)

	var cmd tea.Cmd
	m.confirmDialog, cmd = m.confirmDialog.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter produced no result")
	}
	result, ok := cmd().(ConfirmDialogResultMsg)
	if !ok || result.Confirmed {
		t.Fatalf("Enter result = %#v, want a cancel", result)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cancel wrote the config file: %v", err)
	}
}

func TestPlaintextOptInCancelKeepsTheFormAndWritesNothing(t *testing.T) {
	m, _ := refusedAddAccount(t)
	path := optInConfigPath(t)

	updated, cmd := m.Update(ConfirmDialogResultMsg{Confirmed: false})
	m = updated.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, saved := msg.(plaintextOptInSavedMsg); saved {
				t.Fatal("the cancel started the config write")
			}
		}
	}
	if m.confirmOpen {
		t.Fatal("the prompt stayed open after the cancel")
	}
	if !m.calendarManagerOpen {
		t.Fatal("the cancel closed the Add Account form")
	}
	if !strings.Contains(m.syncStatus, "no secure credential store") {
		t.Fatalf("status = %q, want the refusal", m.syncStatus)
	}
	if m.app.AllowPlaintext {
		t.Fatal("the cancel turned on plaintext storage")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cancel wrote the config file: %v", err)
	}
}

func TestPlaintextOptInConfirmWritesConfigAndRetriesAddAccount(t *testing.T) {
	m, req := refusedAddAccount(t)
	path := optInConfigPath(t)

	updated, cmd := m.Update(ConfirmDialogResultMsg{Confirmed: true})
	m = updated.(Model)
	if m.syncing || !m.plaintextSaving {
		t.Fatalf("syncing = %v, plaintextSaving = %v; want the dedicated flag and no sync spinner",
			m.syncing, m.plaintextSaving)
	}
	saved := findMsg[plaintextOptInSavedMsg](t, cmd)
	if saved.err != nil {
		t.Fatalf("config write: %v", saved.err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load the written config: %v", err)
	}
	if !cfg.Security.AllowPlaintext {
		t.Fatalf("%s does not set security.allow_plaintext", path)
	}

	updated, _ = m.Update(saved)
	m = updated.(Model)
	if !m.app.AllowPlaintext {
		t.Fatal("app.AllowPlaintext stayed false after the opt-in")
	}
	if !m.syncing || m.syncStatus != "Adding account…" {
		t.Fatalf("syncing = %v, status = %q; want the Add Account retry", m.syncing, m.syncStatus)
	}
	store, err := m.openCredentialStore()
	if err != nil {
		t.Fatalf("open the credential store: %v", err)
	}
	if err := auth.EnsureCanStoreSecret(store, 0, discoveryFingerprint(req)); err != nil {
		t.Fatalf("the store still refuses the password after the opt-in: %v", err)
	}
}

// TestPlaintextOptInConfirmSavesTheRotatedPassword covers the account
// settings path: the refused password rotation continues after the opt-in,
// and the store keeps the password the user typed.
func TestPlaintextOptInConfirmSavesTheRotatedPassword(t *testing.T) {
	secretlessEnv(t)
	m, a := newDBBackedModel(t)
	path := optInConfigPath(t)
	ctx := context.Background()

	store, err := auth.NewCredentialStore(a.CredentialNamespace, nil, false, false)
	if err != nil {
		t.Fatalf("open the secretless store: %v", err)
	}
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name:      "Nextcloud",
		ServerURL: "https://cloud.example.com/remote.php/dav/",
		Username:  "scott",
		AuthType:  "basic",
	}, auth.Credential{Username: "scott", PasswordCommand: "pass show caldav/seed"}, store)
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	m.accounts = map[int64]account.Account{created.ID: created}
	m.accountCredentials = NewAccountCredentialsDialogModel(
		created.ID, created.DisplayName, created.AuthType, created.Username, m.theme)
	m.accountCredentialsOpen = true

	submit := AccountCredentialsUpdateSubmittedMsg{AccountID: created.ID, Secret: "hunter2"}
	updated, cmd := m.Update(submit)
	m = updated.(Model)
	stored := findMsg[accountCredentialStoredMsg](t, cmd)
	if !errors.Is(stored.err, auth.ErrPlaintextRequired) {
		t.Fatalf("rotation err = %v, want ErrPlaintextRequired", stored.err)
	}
	updated, _ = m.Update(stored)
	m = updated.(Model)
	if !m.confirmOpen || m.pending.kind != pendingActionPlaintextOptIn {
		t.Fatal("the refused rotation did not offer the plaintext opt-in")
	}
	if !m.accountCredentialsOpen {
		t.Fatal("the credentials form closed behind the prompt")
	}

	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: true})
	m = updated.(Model)
	saved := findMsg[plaintextOptInSavedMsg](t, cmd)
	if saved.err != nil {
		t.Fatalf("config write: %v", saved.err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the opt-in did not write the config file: %v", err)
	}
	updated, cmd = m.Update(saved)
	m = updated.(Model)
	if !m.app.AllowPlaintext || m.accountCredentialsOpen || !m.syncing {
		t.Fatalf("AllowPlaintext = %v, form open = %v, syncing = %v; want the rotation retry",
			m.app.AllowPlaintext, m.accountCredentialsOpen, m.syncing)
	}
	stored = findMsg[accountCredentialStoredMsg](t, cmd)
	if stored.err != nil {
		t.Fatalf("the retried rotation failed: %v", stored.err)
	}

	opted, err := auth.NewCredentialStore(a.CredentialNamespace, nil, false, true)
	if err != nil {
		t.Fatalf("open the plaintext store: %v", err)
	}
	cred, err := opted.Get(created.ID, created.CredentialFingerprint())
	if err != nil {
		t.Fatalf("read the rotated credential: %v", err)
	}
	if cred.Password != "hunter2" {
		t.Fatalf("Password = %q, want the typed password", cred.Password)
	}
}

// findMsg runs cmd and returns the first message of type T. It uses
// batchEmits, so it descends into a tea.BatchMsg, and a tea.Tick in the
// batch (the spinner tick) runs on the fake clock.
func findMsg[T any](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	var got T
	if !batchEmits(t, cmd, func(msg tea.Msg) bool {
		v, ok := msg.(T)
		if ok {
			got = v
		}
		return ok
	}) {
		t.Fatalf("the command produced no %T", got)
	}
	return got
}
