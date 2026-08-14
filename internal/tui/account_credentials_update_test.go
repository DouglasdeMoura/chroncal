package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/zalando/go-keyring"
)

// basicAccountModel seeds a Model with one basic-auth account and opens its
// Account Settings panel, the state from which Update Credentials is launched.
func basicAccountModel(t *testing.T, authType string) Model {
	t.Helper()
	m := NewModel(nil, "")
	m.accounts = map[int64]account.Account{
		7: {
			ID:          7,
			DisplayName: "Work",
			AuthType:    authType,
			Username:    "alice@example.com",
			ServerURL:   "https://cal.example.com/dav/",
		},
	}
	m = updateModel(t, m, AccountSettingsRequestedMsg{AccountID: 7})
	if !accountManagerOpen(m) {
		t.Fatalf("setup: account settings did not open")
	}
	return m
}

func TestAccountSettingsUpdateCredentialsOpensDialogBasic(t *testing.T) {
	m := basicAccountModel(t, "basic")
	m = updateModel(t, m, AccountSettingsUpdateCredentialsRequestedMsg{AccountID: 7})

	if !m.accountCredentialsOpen {
		t.Fatal("Update Credentials request did not open the credentials dialog")
	}
	if accountManagerOpen(m) {
		t.Fatal("Account Settings stayed open over the credentials dialog")
	}
	if m.accountCredentials.accountID != 7 || m.accountCredentials.authType != "basic" {
		t.Fatalf("credentials dialog = %+v, want account 7 basic", m.accountCredentials)
	}
}

func TestAccountSettingsUpdateCredentialsOpensDialogBearer(t *testing.T) {
	m := basicAccountModel(t, "bearer")
	m = updateModel(t, m, AccountSettingsUpdateCredentialsRequestedMsg{AccountID: 7})

	if !m.accountCredentialsOpen || m.accountCredentials.authType != "bearer" {
		t.Fatalf("bearer Update Credentials: open=%v dialog=%+v",
			m.accountCredentialsOpen, m.accountCredentials)
	}
}

func TestAccountSettingsUpdateCredentialsIgnoredForOAuth(t *testing.T) {
	m := NewModel(nil, "")
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
	}
	m = updateModel(t, m, AccountSettingsRequestedMsg{AccountID: 7})

	m = updateModel(t, m, AccountSettingsUpdateCredentialsRequestedMsg{AccountID: 7})
	if m.accountCredentialsOpen {
		t.Fatal("Update Credentials opened for an OAuth account; OAuth must stay on Sign In Again")
	}
	if !accountManagerOpen(m) {
		t.Fatal("OAuth Account Settings should remain open after the ignored request")
	}
}

func TestAccountSettingsUpdateCredentialsIgnoredWhileSyncing(t *testing.T) {
	m := basicAccountModel(t, "basic")
	m.syncing = true
	m = updateModel(t, m, AccountSettingsUpdateCredentialsRequestedMsg{AccountID: 7})
	if m.accountCredentialsOpen {
		t.Fatal("Update Credentials opened while syncing; it must be blocked mid-sync")
	}
}

func TestAccountCredentialsUpdateCancelReturnsToSettings(t *testing.T) {
	m := basicAccountModel(t, "basic")
	m = updateModel(t, m, AccountSettingsUpdateCredentialsRequestedMsg{AccountID: 7})
	m = updateModel(t, m, AccountCredentialsUpdateClosedMsg{AccountID: 7})

	if m.accountCredentialsOpen {
		t.Fatal("credentials dialog stayed open after cancel")
	}
	if !accountManagerOpen(m) {
		t.Fatal("cancel did not return to Account Settings")
	}
	if !strings.Contains(m.syncStatus, "cancelled") {
		t.Fatalf("syncStatus = %q, want cancelled notice", m.syncStatus)
	}
}

func TestAccountCredentialsUpdateSubmitStartsStore(t *testing.T) {
	m := basicAccountModel(t, "basic")
	m = updateModel(t, m, AccountSettingsUpdateCredentialsRequestedMsg{AccountID: 7})
	// Mirror the dialog's own submit: the secret the user typed.
	m = updateModel(t, m, AccountCredentialsUpdateSubmittedMsg{
		AccountID: 7, Secret: "rotated-pw",
	})

	if m.accountCredentialsOpen {
		t.Fatal("credentials dialog stayed open after submit")
	}
	if !m.syncing {
		t.Fatal("submit did not enter the syncing state to store the credential")
	}
}

func TestAccountCredentialsUpdateIgnoresWrongAccountMessages(t *testing.T) {
	m := basicAccountModel(t, "basic")
	m = updateModel(t, m, AccountSettingsUpdateCredentialsRequestedMsg{AccountID: 7})

	afterSubmit := updateModel(t, m, AccountCredentialsUpdateSubmittedMsg{
		AccountID: 99, Secret: "wrong-account-secret",
	})
	if !afterSubmit.accountCredentialsOpen || afterSubmit.syncing {
		t.Fatalf("wrong-account submit changed dialog state: open=%v syncing=%v",
			afterSubmit.accountCredentialsOpen, afterSubmit.syncing)
	}
	afterClose := updateModel(t, m, AccountCredentialsUpdateClosedMsg{AccountID: 99})
	if !afterClose.accountCredentialsOpen || accountManagerOpen(afterClose) {
		t.Fatalf("wrong-account close changed dialog state: open=%v settings=%v",
			afterClose.accountCredentialsOpen, accountManagerOpen(afterClose))
	}
}

func TestAccountCredentialStoreFailureReopensSettingsAndKeepsOldCredential(t *testing.T) {
	m := basicAccountModel(t, "basic")
	m.syncing = true

	m = updateModel(t, m, accountCredentialStoredMsg{
		accountID: 7, name: "Work", err: errTestReauth,
	})
	if m.syncing {
		t.Fatal("syncing stayed true after a failed credential store")
	}
	if !accountManagerOpen(m) {
		t.Fatal("failed credential store did not reopen Account Settings")
	}
	if !strings.Contains(m.syncStatus, "Couldn't update credentials") ||
		!strings.Contains(m.syncStatus, "unchanged") {
		t.Fatalf("failure status = %q, want an unchanged-credential notice", m.syncStatus)
	}
}

// TestCredentialForRotationToleratesBrokenKeyring locks the repair contract.
// A rotation must proceed from an empty credential when the keyring entry is
// gone or belongs to a different connection. Those are exactly the broken
// states the user is trying to fix. Abort only on other (transient
// backend) errors.
func TestCredentialForRotationToleratesBrokenKeyring(t *testing.T) {
	existing := auth.Credential{AccountID: 7, Username: "alice", Password: "old"}
	if cred, err := credentialForRotation(existing, nil); err != nil || cred != existing {
		t.Fatalf("healthy Get: cred=%+v err=%v, want existing credential", cred, err)
	}
	if cred, err := credentialForRotation(auth.Credential{}, keyring.ErrNotFound); err != nil || cred != (auth.Credential{}) {
		t.Fatalf("not-found: cred=%+v err=%v, want empty credential and nil error", cred, err)
	}
	if cred, err := credentialForRotation(auth.Credential{}, auth.ErrCredentialIdentityMismatch); err != nil || cred != (auth.Credential{}) {
		t.Fatalf("identity mismatch: cred=%+v err=%v, want empty credential and nil error", cred, err)
	}
	transient := errors.New("keyring backend unavailable")
	if _, err := credentialForRotation(auth.Credential{}, transient); !errors.Is(err, transient) {
		t.Fatalf("transient error: err=%v, want wrapped %v", err, transient)
	}
}

func TestAccountCredentialStoreSuccessStartsSync(t *testing.T) {
	m := basicAccountModel(t, "basic")

	m = updateModel(t, m, accountCredentialStoredMsg{accountID: 7, name: "Work"})
	if !m.syncing {
		t.Fatal("successful credential store did not start a confirming sync")
	}
	if !accountManagerOpen(m) {
		t.Fatal("successful credential store did not reopen Account Settings")
	}
	if !strings.Contains(m.syncStatus, "Syncing") || !strings.Contains(m.syncStatus, "Work") {
		t.Fatalf("syncStatus = %q, want a Work sync notice", m.syncStatus)
	}
}
