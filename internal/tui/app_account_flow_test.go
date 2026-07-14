package tui

import "testing"

func TestAppAccountSetupOpensDiscoveryPicker(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40

	updated, cmd := m.Update(AccountDialogRequestedMsg{})
	m = updated.(Model)
	if cmd != nil || !m.accountDialogOpen {
		t.Fatalf("account dialog open=%v cmd=%v", m.accountDialogOpen, cmd)
	}

	m.syncing = true
	updated, cmd = m.Update(accountDiscoveryReadyMsg{discovery: pickerDiscovery()})
	m = updated.(Model)
	if cmd != nil || m.syncing || !m.accountPickerOpen || m.accountDialogOpen {
		t.Fatalf("discovery transition: syncing=%v picker=%v form=%v cmd=%v", m.syncing, m.accountPickerOpen, m.accountDialogOpen, cmd)
	}
}

func TestAppAccountRemoveUsesDestructiveConfirmation(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	updated, cmd := m.Update(AccountRemoveRequestedMsg{AccountID: 7, Name: "Google"})
	m = updated.(Model)
	if cmd != nil || !m.confirmOpen || m.pendingAccountDelete != 7 {
		t.Fatalf("remove confirmation: open=%v pending=%d cmd=%v", m.confirmOpen, m.pendingAccountDelete, cmd)
	}
	if m.confirmDialog.form.submitVariant != ButtonDanger {
		t.Fatal("account removal confirmation must use the danger submit variant")
	}
}

func TestAppOAuthAccountConnectRecordsAccountPurpose(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	req := AccountConnectRequestedMsg{
		Name: "Google", ServerURL: "https://example.com/caldav", Username: "me@example.com",
		AuthType: "oauth2", OAuthClientID: "client.apps", OAuthClientSecret: "secret",
	}
	updated, cmd := m.Update(req)
	m = updated.(Model)
	if cmd == nil || !m.oauthFlowOpen || !m.oauthPurpose.accountConnect {
		t.Fatalf("OAuth account state: flow=%v purpose=%v cmd=%v", m.oauthFlowOpen, m.oauthPurpose.accountConnect, cmd)
	}
	if m.oauthPurpose.accountConnectMsg.Name != "Google" {
		t.Fatalf("OAuth purpose lost account request: %+v", m.oauthPurpose.accountConnectMsg)
	}
	m.oauthFlow.Abort()
}
