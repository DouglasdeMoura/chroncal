package tui

import (
	"strings"
	"testing"
)

func TestAccountDialog_PasswordCmdSubmit(t *testing.T) {
	m := oauthDialogFixture(t, "basic")
	m.form.Field(calDAVIdxServer).(*TextField).SetValue("https://example.com/dav/")
	m.form.Field(calDAVIdxUsername).(*TextField).SetValue("alice")
	m.form.Field(calDAVIdxPasswordCmd).(*TextField).SetValue("pass show caldav_password")
	m.form.onRebuild(&m.form)

	cmd := m.form.onSubmit(&m.form)
	if cmd == nil {
		t.Fatal("submit returned nil; password cmd should satisfy the credential")
	}
	discovery, ok := cmd().(CalendarDiscoveryRequestedMsg)
	if !ok {
		t.Fatalf("expected CalendarDiscoveryRequestedMsg, got %T", cmd())
	}
	if discovery.PasswordCommand != "pass show caldav_password" {
		t.Errorf("PasswordCommand = %q, want pass show caldav_password", discovery.PasswordCommand)
	}
	if strings.TrimSpace(discovery.Secret) != "" {
		t.Errorf("Secret = %q, want empty when password cmd is set", discovery.Secret)
	}
}

func TestAccountDialog_PasswordAndCmdConflict(t *testing.T) {
	m := oauthDialogFixture(t, "basic")
	m.form.Field(calDAVIdxServer).(*TextField).SetValue("https://example.com/dav/")
	m.form.Field(calDAVIdxUsername).(*TextField).SetValue("alice")
	m.form.Field(calDAVIdxSecret).(*TextField).SetValue("hunter2")
	m.form.Field(calDAVIdxPasswordCmd).(*TextField).SetValue("pass show caldav_password")

	if cmd := m.form.onSubmit(&m.form); cmd != nil {
		t.Fatalf("submit should reject password and password cmd together, got %T", cmd())
	}
}

func TestAccountDialog_BearerIgnoresPasswordCmd(t *testing.T) {
	m := oauthDialogFixture(t, "bearer")
	m.form.Field(calDAVIdxServer).(*TextField).SetValue("https://example.com/dav/")
	m.form.Field(calDAVIdxUsername).(*TextField).SetValue("alice")
	m.form.Field(calDAVIdxSecret).(*TextField).SetValue("token-value")
	m.form.Field(calDAVIdxPasswordCmd).(*TextField).SetValue("pass show caldav_password")

	cmd := m.form.onSubmit(&m.form)
	if cmd == nil {
		t.Fatal("bearer submit returned nil")
	}
	discovery, ok := cmd().(CalendarDiscoveryRequestedMsg)
	if !ok {
		t.Fatalf("expected CalendarDiscoveryRequestedMsg, got %T", cmd())
	}
	if discovery.Secret != "token-value" {
		t.Errorf("Secret = %q, want token-value", discovery.Secret)
	}
	if discovery.PasswordCommand != "" {
		t.Errorf("PasswordCommand = %q, want empty for bearer", discovery.PasswordCommand)
	}
}
