package tui

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
)

// secretlessTUIModel builds a Model whose credential store opens secretless:
// the session bus is dead and plaintext is not allowed. Linux-only, like the
// CLI secretless tests: the keyring probe only applies there.
func secretlessTUIModel(t *testing.T) Model {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the session bus probe only applies on Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/chroncal-test-bus")
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

// TestRotationCredential pins the rotation mapping. The unused source is
// cleared so a stale password cannot conflict with a new password command
// and back. A command-only rotation keeps no secret, so it stays available
// with no keyring.
func TestRotationCredential(t *testing.T) {
	cases := []struct {
		name       string
		base       auth.Credential
		secret     string
		command    string
		bearer     bool
		check      func(auth.Credential) bool
		wantSecret bool
	}{
		{
			name:   "bearer clears a password",
			base:   auth.Credential{Password: "old"},
			secret: "new-token", bearer: true,
			check: func(c auth.Credential) bool {
				return c.AccessToken == "new-token" && c.Password == "" && c.PasswordCommand == ""
			},
			wantSecret: true,
		},
		{
			name:   "bearer clears a password command",
			base:   auth.Credential{PasswordCommand: "pass show old"},
			secret: "new-token", bearer: true,
			check: func(c auth.Credential) bool {
				return c.AccessToken == "new-token" && c.Password == "" && c.PasswordCommand == ""
			},
			wantSecret: true,
		},
		{
			name:   "password clears a stale command",
			base:   auth.Credential{PasswordCommand: "pass show old"},
			secret: "new-pw",
			check: func(c auth.Credential) bool {
				return c.Password == "new-pw" && c.PasswordCommand == ""
			},
			wantSecret: true,
		},
		{
			name:    "password command clears a stale password",
			base:    auth.Credential{Password: "old"},
			command: "pass show caldav",
			check: func(c auth.Credential) bool {
				return c.Password == "" && c.PasswordCommand == "pass show caldav"
			},
			wantSecret: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.base.AccountID, tc.base.Username = 7, "alice"
			if err := tc.base.ValidatePasswordSources(); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			got := rotationCredential(tc.base, tc.secret, tc.command, tc.bearer)
			if got.AccountID != 7 || got.Username != "alice" {
				t.Fatal("rotation changed the credential identity")
			}
			if !tc.check(got) {
				t.Fatalf("rotationCredential() = %+v", got)
			}
			if got.HasStoredSecret() != tc.wantSecret {
				t.Fatalf("HasStoredSecret() = %v, want %v", got.HasStoredSecret(), tc.wantSecret)
			}
		})
	}
}

// TestUpdateAccountCredentialsStoresAPasswordCommandOnSecretlessStore covers
// the second comment on issue #777: the reporter could not use a password
// command from the TUI. A command is not a secret, so the rotation must store
// it with no keyring and no opt-in. The other tests in this file cover the
// refusals only, so this one guards the path that must work.
func TestUpdateAccountCredentialsStoresAPasswordCommandOnSecretlessStore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the session bus probe only applies on Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/chroncal-test-bus")
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
	}, auth.Credential{Username: "scott", PasswordCommand: "printf hunter2"}, store)
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
