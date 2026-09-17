package tui

import (
	"context"
	"errors"
	"runtime"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
)

// secretlessEnv points the credential store at a temporary config directory
// and a dead session bus. The keyring probe answers once for the whole test
// binary, so the reset runs before and after: an earlier test must not leak a
// cached answer into this one, and this one must not leak into a later test.
// Linux-only, like the CLI secretless tests: the probe only applies there.
func secretlessEnv(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the session bus probe only applies on Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/chroncal-test-bus")
	auth.ResetKeyringProbe()
	t.Cleanup(auth.ResetKeyringProbe)
}

// secretlessTUIModel builds a Model whose credential store opens secretless:
// the session bus is dead and plaintext is not allowed.
func secretlessTUIModel(t *testing.T) Model {
	t.Helper()
	secretlessEnv(t)
	return NewModel(&app.App{CredentialNamespace: "test"}, "")
}

// TestPrepareAccountReauthRefusesSecretlessStore is the TUI half of issue
// #777: reauth always stores OAuth secrets, so the secretless store must
// refuse before the browser opens, not after the consent is spent.
func TestPrepareAccountReauthRefusesSecretlessStore(t *testing.T) {
	m := secretlessTUIModel(t)
	configured := account.Account{
		ID: 7, DisplayName: "Personal Google", AuthType: "oauth2",
		Username: "me@example.com", ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
	}
	msg := m.prepareAccountReauth(configured, "", "")()
	ready, ok := msg.(accountReauthReadyMsg)
	if !ok {
		t.Fatalf("prepareAccountReauth cmd = %T, want accountReauthReadyMsg", msg)
	}
	if !errors.Is(ready.err, auth.ErrPlaintextRequired) {
		t.Fatalf("prepareAccountReauth err = %v, want ErrPlaintextRequired", ready.err)
	}
}

// TestStartOAuthFlowRefusesSecretlessStore covers the discovery path: a new
// OAuth account would store tokens, so the flow must not bind the listener
// or open the browser when the store cannot keep them.
func TestStartOAuthFlowRefusesSecretlessStore(t *testing.T) {
	m := secretlessTUIModel(t)
	m.width, m.height = 120, 40
	started := false
	prev := oauthStartFn
	oauthStartFn = func(ctx context.Context, clientID, clientSecret string) (*auth.PendingOAuthFlow, error) {
		started = true
		return nil, errors.New("OAuth must not start on a secretless store")
	}
	t.Cleanup(func() { oauthStartFn = prev })
	m.oauthPurpose = oauthFlowPurpose{accountID: 7, accountName: "Personal Google"}

	_, cmd := m.startOAuthFlow("cid", "secret")
	msg := cmd()
	startedMsg, ok := msg.(oauthFlowStartedMsg)
	if !ok {
		t.Fatalf("startOAuthFlow cmd = %T, want oauthFlowStartedMsg refusal", msg)
	}
	if !errors.Is(startedMsg.err, auth.ErrPlaintextRequired) {
		t.Fatalf("startOAuthFlow err = %v, want ErrPlaintextRequired", startedMsg.err)
	}
	if started {
		t.Fatal("OAuth flow started despite a secretless store")
	}
}

// TestUpdateAccountCredentialsRefusesSecretOnSecretlessStore pins the
// rotation preflight: a submitted password must fail fast with the remedy,
// before the load and the write.
func TestUpdateAccountCredentialsRefusesSecretOnSecretlessStore(t *testing.T) {
	m := secretlessTUIModel(t)
	configured := account.Account{
		ID: 7, DisplayName: "Work", AuthType: "basic",
		Username: "alice@example.com", ServerURL: "https://cal.example.com/dav/",
	}
	msg := m.updateAccountCredentials(configured, "hunter2", "")()
	stored, ok := msg.(accountCredentialStoredMsg)
	if !ok {
		t.Fatalf("updateAccountCredentials cmd = %T, want accountCredentialStoredMsg", msg)
	}
	if !errors.Is(stored.err, auth.ErrPlaintextRequired) {
		t.Fatalf("updateAccountCredentials err = %v, want ErrPlaintextRequired", stored.err)
	}
}

// TestUpdateAccountCredentialsStoresAPasswordCommandOnSecretlessStore covers
// the second comment on issue #777: the reporter could not use a password
// command from the TUI. A command is not a secret, so the rotation must store
// it with no keyring and no opt-in. The other tests in this file cover the
// refusals only, so this one guards the path that must work.
func TestUpdateAccountCredentialsStoresAPasswordCommandOnSecretlessStore(t *testing.T) {
	secretlessEnv(t)
	m, a := newDBBackedModel(t)

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
		t.Fatalf("create the account with a password command: %v", err)
	}

	const command = "pass show caldav/nextcloud"
	msg := m.updateAccountCredentials(created, "", command)()
	stored, ok := msg.(accountCredentialStoredMsg)
	if !ok {
		t.Fatalf("updateAccountCredentials cmd = %T, want accountCredentialStoredMsg", msg)
	}
	if stored.err != nil {
		t.Fatalf("rotation with a password command: %v", stored.err)
	}

	cred, err := store.Get(created.ID, created.CredentialFingerprint())
	if err != nil {
		t.Fatalf("read the rotated credential: %v", err)
	}
	if cred.PasswordCommand != command {
		t.Fatalf("PasswordCommand = %q, want %q", cred.PasswordCommand, command)
	}
	if cred.HasStoredSecret() {
		t.Fatal("a secret reached the disk for a command-only rotation")
	}

	// The same account must still refuse a password. The relaxed write rule
	// keys on the stored file, which holds a command and therefore no secret.
	refused := m.updateAccountCredentials(created, "hunter2", "")()
	storedSecret, ok := refused.(accountCredentialStoredMsg)
	if !ok {
		t.Fatalf("password rotation cmd = %T, want accountCredentialStoredMsg", refused)
	}
	if !errors.Is(storedSecret.err, auth.ErrPlaintextRequired) {
		t.Fatalf("password rotation err = %v, want ErrPlaintextRequired", storedSecret.err)
	}
}

// TestConnectAndDiscoverCalendarRefusesSecretlessStore covers the Add Account
// path of issue #777. The reporter used it. A submitted password must stop
// with the remedy before discovery and before the account row, not with a
// store error from deep inside Create.
func TestConnectAndDiscoverCalendarRefusesSecretlessStore(t *testing.T) {
	secretlessEnv(t)
	m, a := newDBBackedModel(t)

	req := CalendarDiscoveryRequestedMsg{
		ServerURL: "https://cloud.example.com/remote.php/dav/",
		Username:  "scott",
		AuthType:  "basic",
		Secret:    "hunter2",
	}
	cred := auth.Credential{Username: req.Username, Password: req.Secret}
	msg := m.connectAndDiscoverCalendar(req, cred)()
	ready, ok := msg.(accountDiscoveryReadyMsg)
	if !ok {
		t.Fatalf("connectAndDiscoverCalendar cmd = %T, want accountDiscoveryReadyMsg", msg)
	}
	if !errors.Is(ready.err, auth.ErrPlaintextRequired) {
		t.Fatalf("connectAndDiscoverCalendar err = %v, want ErrPlaintextRequired", ready.err)
	}
	if ready.createdAccount {
		t.Fatal("the refused connection created an account")
	}
	accounts, err := a.Accounts.List(context.Background())
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after the refusal = %d, want 0", len(accounts))
	}
}

// TestEnsureDiscoveryCanStoreSecretPassesAPasswordCommand keeps the Add
// Account path open for a password command. The file holds the command, not
// the password, so no keyring and no opt-in are needed.
func TestEnsureDiscoveryCanStoreSecretPassesAPasswordCommand(t *testing.T) {
	secretlessEnv(t)
	store, err := auth.NewCredentialStore("test", nil, false, false)
	if err != nil {
		t.Fatalf("open the secretless store: %v", err)
	}
	cred := auth.Credential{Username: "scott", PasswordCommand: "pass show caldav/nextcloud"}
	if err := ensureDiscoveryCanStoreSecret(store, cred, 0, "fp"); err != nil {
		t.Fatalf("password command refused: %v", err)
	}
	withSecret := auth.Credential{Username: "scott", Password: "hunter2"}
	if err := ensureDiscoveryCanStoreSecret(store, withSecret, 0, "fp"); !errors.Is(err, auth.ErrPlaintextRequired) {
		t.Fatalf("password err = %v, want ErrPlaintextRequired", err)
	}
}

// TestStartOAuthFlowAllowsAnAccountThatHoldsTokens keeps the OAuth preflight
// level with the write. Add Account resolves an existing account by its
// connection identity, so re-running it for an account whose file already
// holds the tokens must open the browser. The store would accept the rewrite.
func TestStartOAuthFlowAllowsAnAccountThatHoldsTokens(t *testing.T) {
	secretlessEnv(t)
	m, a := newDBBackedModel(t)
	ctx := context.Background()

	// Seed the account the way an earlier --allow-plaintext run would.
	opted, err := auth.NewCredentialStore(a.CredentialNamespace, nil, false, true)
	if err != nil {
		t.Fatalf("open the plaintext store: %v", err)
	}
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name:      "Personal Google",
		ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
		Username:  "me@example.com",
		AuthType:  "oauth2",
	}, auth.Credential{
		Username: "me@example.com", AccessToken: "ya29.stale", RefreshToken: "1//0xyz",
		OAuthClientID: "cid.apps.googleusercontent.com", OAuthClientSecret: "GOCSPX-x",
	}, opted)
	if err != nil {
		t.Fatalf("create the opted-in account: %v", err)
	}

	// Guard the flow in case the batch below ever runs: no listener binds and
	// no browser opens in a test.
	prev := oauthStartFn
	oauthStartFn = func(ctx context.Context, clientID, clientSecret string) (*auth.PendingOAuthFlow, error) {
		return nil, errors.New("the test does not run the OAuth flow")
	}
	t.Cleanup(func() { oauthStartFn = prev })

	m.width, m.height = 120, 40
	m.oauthPurpose = oauthFlowPurpose{
		calendarDiscovery: true,
		calendarDiscoveryMsg: CalendarDiscoveryRequestedMsg{
			ServerURL: created.ServerURL,
			Username:  created.Username,
			AuthType:  "oauth2",
		},
	}
	_, cmd := m.startOAuthFlow("cid.apps.googleusercontent.com", "GOCSPX-x")
	msg := cmd()
	if started, refused := msg.(oauthFlowStartedMsg); refused {
		t.Fatalf("the preflight stopped an account whose file holds the tokens: %v", started.err)
	}
	// The preflight passed, so the command returns the batch that starts the
	// flow. The test stops here: it does not run the flow.
	if _, ok := msg.(tea.BatchMsg); !ok {
		t.Fatalf("startOAuthFlow cmd = %T, want a tea.BatchMsg", msg)
	}
}
