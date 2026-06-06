package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/auth"
)

func keyEsc() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEscape} }

func startedFlow(t *testing.T, browserOpened bool) OAuthFlowModel {
	t.Helper()
	m := NewOAuthFlowModel(Theme{})
	m, _ = m.Start("cid", "secret")
	if m.State() != OAuthFlowStarting {
		t.Fatalf("after Start: state = %v, want Starting", m.State())
	}
	m, cmd := m.Update(oauthFlowStartedMsg{
		flow: &auth.PendingOAuthFlow{AuthURL: "https://accounts.google.com/auth?x=1", BrowserOpened: browserOpened},
	})
	if m.State() != OAuthFlowWaiting {
		t.Fatalf("after started msg: state = %v, want Waiting", m.State())
	}
	if cmd == nil {
		t.Fatal("waiting state should have issued the Wait cmd")
	}
	return m
}

func TestOAuthFlow_StartFailureLandsInFailed(t *testing.T) {
	m := NewOAuthFlowModel(Theme{})
	m, _ = m.Start("cid", "secret")
	m, _ = m.Update(oauthFlowStartedMsg{err: errors.New("bind: address in use")})
	if m.State() != OAuthFlowFailed {
		t.Fatalf("state = %v, want Failed", m.State())
	}
	if !strings.Contains(m.Err(), "address in use") {
		t.Errorf("Err() = %q, want bind error", m.Err())
	}
}

func TestOAuthFlow_SuccessPath(t *testing.T) {
	m := startedFlow(t, true)
	if m.AuthURL() == "" {
		t.Error("AuthURL should be populated in Waiting")
	}
	m, _ = m.Update(oauthFlowDoneMsg{result: &auth.GoogleOAuthResult{AccessToken: "tok"}})
	if m.State() != OAuthFlowDone {
		t.Fatalf("state = %v, want Done", m.State())
	}
}

func TestOAuthFlow_WaitErrorLandsInFailed(t *testing.T) {
	m := startedFlow(t, true)
	m, _ = m.Update(oauthFlowDoneMsg{err: errors.New("authorization timed out after 5 minutes")})
	if m.State() != OAuthFlowFailed {
		t.Fatalf("state = %v, want Failed", m.State())
	}
	if !strings.Contains(m.Err(), "timed out") {
		t.Errorf("Err() = %q, want timeout text", m.Err())
	}
}

func TestOAuthFlow_EscCancelsActiveFlow(t *testing.T) {
	m := startedFlow(t, true)
	m, _ = m.Update(keyEsc())
	if m.State() != OAuthFlowCancelled {
		t.Fatalf("state after esc = %v, want Cancelled", m.State())
	}
	// The late Wait return (ctx.Canceled) must not flip the state again.
	m, _ = m.Update(oauthFlowDoneMsg{err: context.Canceled})
	if m.State() != OAuthFlowCancelled {
		t.Fatalf("state after late done = %v, want Cancelled", m.State())
	}
}

func TestOAuthFlow_CtxCanceledMapsToCancelled(t *testing.T) {
	m := startedFlow(t, true)
	m, _ = m.Update(oauthFlowDoneMsg{err: context.Canceled})
	if m.State() != OAuthFlowCancelled {
		t.Fatalf("state = %v, want Cancelled", m.State())
	}
}

func TestOAuthFlow_EscOnTerminalStateEmitsClosed(t *testing.T) {
	m := startedFlow(t, true)
	m, _ = m.Update(oauthFlowDoneMsg{err: errors.New("boom")})
	m, cmd := m.Update(keyEsc())
	if cmd == nil {
		t.Fatal("esc on Failed should emit a command")
	}
	if _, ok := cmd().(oauthFlowClosedMsg); !ok {
		t.Fatalf("expected oauthFlowClosedMsg, got %T", cmd())
	}
	_ = m
}

func TestOAuthFlow_StaleStartedAfterCancelIgnored(t *testing.T) {
	m := NewOAuthFlowModel(Theme{})
	m, _ = m.Start("cid", "secret")
	m, _ = m.Update(keyEsc()) // cancel before the listener came up
	if m.State() != OAuthFlowCancelled {
		t.Fatalf("state = %v, want Cancelled", m.State())
	}
	m, cmd := m.Update(oauthFlowStartedMsg{flow: &auth.PendingOAuthFlow{AuthURL: "x"}})
	if m.State() != OAuthFlowCancelled {
		t.Fatalf("stale started msg moved state to %v", m.State())
	}
	if cmd != nil {
		t.Error("stale started msg should not issue a Wait cmd")
	}
}

func TestOAuthFlow_ViewShowsURLWhenBrowserFailed(t *testing.T) {
	m := startedFlow(t, false).SetSize(100, 40)
	view := m.View()
	if !strings.Contains(view, "accounts.google.com") {
		t.Errorf("view should show the auth URL when the browser failed; got %q", view)
	}
	if !strings.Contains(view, "open browser") {
		t.Errorf("view should hint the o keybinding; got %q", view)
	}
}

func TestOAuthFlow_ViewBrowserOpenedHidesURL(t *testing.T) {
	m := startedFlow(t, true).SetSize(100, 40)
	view := m.View()
	if !strings.Contains(view, "Browser opened") {
		t.Errorf("view should say the browser opened; got %q", view)
	}
	if strings.Contains(view, "accounts.google.com") {
		t.Errorf("view should not dump the URL when the browser opened; got %q", view)
	}
}
