package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/auth"
)

// batchEmits executes a tea.Cmd (recursing into tea.BatchMsg) and reports
// whether any produced message satisfies pred. Commands that touch app
// services (loadCalendars etc.) panic on a bare Model — those are recovered
// and treated as non-matching, so the helper can assert on the pure
// message-constructor commands in the same batch.
func batchEmits(cmd tea.Cmd, pred func(tea.Msg) bool) (found bool) {
	if cmd == nil {
		return false
	}
	// Run with a short deadline: tea.Tick-based commands (expireStatusAfter)
	// block for their full duration, which we don't care about here.
	done := make(chan tea.Msg, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- nil
			}
		}()
		done <- cmd()
	}()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(200 * time.Millisecond):
		return false
	}
	if msg == nil {
		return false
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if batchEmits(c, pred) {
				return true
			}
		}
		return false
	}
	return pred(msg)
}

// updateModel runs one Update cycle and returns the concrete Model.
func updateModel(t *testing.T, m Model, msg interface{}) Model {
	t.Helper()
	next, _ := m.Update(msg)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return out
}

func TestAccountSettingsReauthTargetsAccountIdentity(t *testing.T) {
	m := NewModel(nil, "")
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
	}
	m = updateModel(t, m, AccountSettingsRequestedMsg{AccountID: 7})

	next, cmd := m.Update(AccountSettingsReauthRequestedMsg{AccountID: 7})
	m = next.(Model)
	if cmd == nil || !m.oauthPending {
		t.Fatalf("account reauth start: cmd=%v pending=%v", cmd == nil, m.oauthPending)
	}
	if accountManagerOpen(m) {
		t.Fatal("Account settings stayed open while credential load started")
	}
}

func TestAccountReauthPreservesUnderlyingCalendarDraft(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.accounts = map[int64]account.Account{
		7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
	}
	openCalendarManagerForTest(&m, CalendarDialogParams{
		ID: 2, AccountID: 7, AccountName: "Personal Google",
		Name: "Personal", Color: "#a6e3a1", RemoteLinked: true,
	})
	m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Unsaved personal rename")
	m.calendarManagerOpen = true
	m = updateModel(t, m, AccountSettingsRequestedMsg{AccountID: 7})

	next, cmd := m.Update(AccountSettingsReauthRequestedMsg{AccountID: 7})
	m = next.(Model)
	if cmd == nil || !m.oauthPending || m.calendarManagerOpen || m.calendarManager.LocalDraft() == nil {
		t.Fatalf("reauth start: cmd=%v pending=%v manager=%v draft=%v",
			cmd == nil, m.oauthPending, m.calendarManagerOpen, m.calendarManager.LocalDraft() != nil)
	}
	m = updateModel(t, m, accountReauthReadyMsg{
		accountID: 7,
		name:      "Personal Google",
		cred:      auth.Credential{OAuthClientID: "cid", OAuthClientSecret: "secret"},
	})
	if !m.oauthFlowOpen || m.calendarManagerOpen || m.calendarManager.LocalDraft() == nil {
		t.Fatalf("OAuth open: oauth=%v manager=%v draft=%v",
			m.oauthFlowOpen, m.calendarManagerOpen, m.calendarManager.LocalDraft() != nil)
	}

	// Model the successful browser handoff at the credential-store boundary.
	m.oauthFlowOpen = false
	next, cmd = m.Update(oauthCredentialStoredMsg{accountID: 7, name: "Personal Google"})
	m = next.(Model)
	if cmd == nil || !m.syncing || !accountManagerOpen(m) || !m.calendarManagerOpen {
		t.Fatalf("post-auth sync: cmd=%v syncing=%v settings=%v calendar=%v",
			cmd == nil, m.syncing, accountManagerOpen(m), m.calendarManagerOpen)
	}
	if got := m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved personal rename" {
		t.Fatalf("reauth changed calendar draft to %q", got)
	}

	m = updateModel(t, m, AccountSettingsClosedMsg{})
	if !accountManagerOpen(m) {
		t.Fatal("Account Settings closed while re-authentication sync was active")
	}
	m = updateModel(t, m, syncFinishedMsg{summary: "Synced Personal Google"})
	m = updateModel(t, m, AccountSettingsClosedMsg{})
	if accountManagerOpen(m) || !m.calendarManagerOpen {
		t.Fatalf("post-sync return: settings=%v calendar=%v",
			accountManagerOpen(m), m.calendarManagerOpen)
	}
	if got := m.calendarManager.calendarForm.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved personal rename" {
		t.Fatalf("post-sync return changed calendar draft to %q", got)
	}
}

func TestAccountReauthReadyOpensFlowWithoutCalendar(t *testing.T) {
	m := Model{
		oauthPending: true, width: 100, height: 40,
		accounts: map[int64]account.Account{
			7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
		},
	}
	m = updateModel(t, m, accountReauthReadyMsg{
		accountID: 7,
		name:      "Personal Google",
		cred:      auth.Credential{OAuthClientID: "cid", OAuthClientSecret: "shh"},
	})
	if m.oauthPending || !m.oauthFlowOpen {
		t.Fatalf("account reauth ready: pending=%v open=%v", m.oauthPending, m.oauthFlowOpen)
	}
	if m.oauthPurpose.accountID != 7 || m.oauthPurpose.accountName != "Personal Google" {
		t.Fatalf("OAuth purpose = %+v", m.oauthPurpose)
	}
}

func TestStoredAccountOAuthCredentialStartsWholeAccountSync(t *testing.T) {
	m := Model{oauthPending: true}
	next, cmd := m.Update(oauthCredentialStoredMsg{
		accountID: 7, name: "Personal Google",
	})
	m = next.(Model)
	if cmd == nil || !m.syncing || m.oauthPending {
		t.Fatalf("post-auth account sync: cmd=%v syncing=%v pending=%v",
			cmd == nil, m.syncing, m.oauthPending)
	}
	if !strings.Contains(m.syncStatus, "Personal Google") {
		t.Fatalf("syncStatus = %q, want account name", m.syncStatus)
	}
}

func TestAccountReauthMissingClientConfigUsesAccountDialog(t *testing.T) {
	m := Model{
		oauthPending: true, width: 100, height: 40,
		accounts: map[int64]account.Account{
			7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
		},
	}
	m = updateModel(t, m, accountReauthReadyMsg{
		accountID: 7,
		name:      "Personal Google",
		cred:      auth.Credential{OAuthClientID: "stored-client"},
	})
	if m.oauthPending || !m.accountOAuthConfigOpen || m.calendarManagerOpen {
		t.Fatalf("missing config: pending=%v accountConfig=%v calendarDialog=%v",
			m.oauthPending, m.accountOAuthConfigOpen, m.calendarManagerOpen)
	}
	if got := m.accountOAuthConfig.form.Field(0).(*TextField).Value(); got != "stored-client" {
		t.Fatalf("client ID prefill = %q, want stored-client", got)
	}

	next, cmd := m.Update(AccountOAuthConfigSubmittedMsg{
		AccountID: 7, ClientID: "new-client", ClientSecret: "new-secret",
	})
	m = next.(Model)
	if cmd == nil || !m.oauthPending || m.accountOAuthConfigOpen {
		t.Fatalf("config submit: cmd=%v pending=%v open=%v",
			cmd == nil, m.oauthPending, m.accountOAuthConfigOpen)
	}
}

func TestAccountOAuthConfigCancelReturnsToAccountSettings(t *testing.T) {
	m := Model{
		accountOAuthConfigOpen: true,
		accountOAuthConfig: NewAccountOAuthConfigDialogModel(
			7, "Personal Google", "", NewTheme(true),
		),
		accounts: map[int64]account.Account{
			7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
		},
	}
	m = updateModel(t, m, AccountOAuthConfigClosedMsg{AccountID: 7})
	if m.accountOAuthConfigOpen || !accountManagerOpen(m) {
		t.Fatalf("config cancel: config=%v settings=%v",
			m.accountOAuthConfigOpen, accountManagerOpen(m))
	}
}

func TestStoredAccountCredentialFailureUsesMessageIdentity(t *testing.T) {
	m := Model{
		oauthPending: true,
		oauthPurpose: oauthFlowPurpose{
			accountID: 99,
		},
		accounts: map[int64]account.Account{
			7: {ID: 7, DisplayName: "Personal Google", AuthType: "oauth2"},
		},
	}
	m = updateModel(t, m, oauthCredentialStoredMsg{
		accountID: 7, err: errTestReauth,
	})
	if m.oauthPending || !accountManagerOpen(m) || m.calendarManager.accountSettings.params.AccountID != 7 {
		t.Fatalf("credential failure: pending=%v settings=%v account=%d",
			m.oauthPending, accountManagerOpen(m), m.calendarManager.accountSettings.params.AccountID)
	}
}

var errTestReauth = &stringError{"load credential: keyring locked"}

type stringError struct{ s string }

func (e *stringError) Error() string { return e.s }

// TestPostReauthSyncQueuedWhileSyncing reproduces the dropped-sync gap: a sync
// request arriving mid-sync is queued, then drained when the running sync
// finishes — so the post-reauth sync (and the ⚠ clear) is never lost.
func TestPostReauthSyncQueuedWhileSyncing(t *testing.T) {
	m := Model{syncing: true}

	// Re-auth completes while a sync runs: the request is queued, not dropped.
	m = updateModel(t, m, SyncCalendarRequestedMsg{ID: 12, Name: "gmail"})
	if m.pendingSyncCalendar.ID != 12 {
		t.Fatalf("pendingSyncCalendar.ID = %d, want 12 (queued)", m.pendingSyncCalendar.ID)
	}

	// The running sync finishes: the queued sync is re-dispatched.
	next, cmd := m.Update(syncFinishedMsg{summary: "done", reload: true})
	m = next.(Model)
	if m.pendingSyncCalendar.ID != 0 {
		t.Errorf("queue should be drained, got ID %d", m.pendingSyncCalendar.ID)
	}
	if cmd == nil {
		t.Fatal("syncFinishedMsg with a queued sync should emit commands")
	}
	// The batch must contain the re-dispatched SyncCalendarRequestedMsg.
	if !batchEmits(cmd, func(msg tea.Msg) bool {
		r, ok := msg.(SyncCalendarRequestedMsg)
		return ok && r.ID == 12
	}) {
		t.Error("drained queue should re-dispatch SyncCalendarRequestedMsg{ID:12}")
	}
}

// TestNoPendingSyncNoRedispatch confirms the drain is a no-op when nothing is
// queued (the common path).
func TestNoPendingSyncNoRedispatch(t *testing.T) {
	m := Model{syncing: true}
	next, cmd := m.Update(syncFinishedMsg{summary: "done", reload: true})
	m = next.(Model)
	if m.pendingSyncCalendar.ID != 0 {
		t.Error("nothing should be queued")
	}
	if cmd != nil && batchEmits(cmd, func(msg tea.Msg) bool {
		_, ok := msg.(SyncCalendarRequestedMsg)
		return ok
	}) {
		t.Error("no SyncCalendarRequestedMsg should be re-dispatched when queue is empty")
	}
}

// TestOAuthModalBlocksWheelScroll verifies that a tea.MouseWheelMsg delivered
// while the OAuth pending modal is open does NOT scroll the background
// week/day grid (issue #355).
func TestOAuthModalBlocksWheelScroll(t *testing.T) {
	// Build a minimal week-view model with a known non-zero scrollOffset.
	// linesPerHour=4 means a WheelDown event would add 4 to scrollOffset.
	wm := WeekModel{linesPerHour: 4, scrollOffset: 8}
	m := Model{
		oauthFlowOpen: true,
		viewMode:      viewWeek,
		week:          wm,
	}

	wheel := tea.MouseWheelMsg{Button: tea.MouseWheelDown}
	m = updateModel(t, m, wheel)

	if m.week.scrollOffset != 8 {
		t.Errorf("scrollOffset = %d, want 8; wheel scroll must be blocked when OAuth modal is open", m.week.scrollOffset)
	}
}

// TestOAuthModalSuppressesGlobalHelpKey ensures the OAuth pending modal owns
// input: pressing "?" while it's open must NOT open the Help dialog over it
// (interceptGlobalKeys gates Help/quit behind text-entry surfaces, which now
// includes oauthFlowOpen).
func TestOAuthModalSuppressesGlobalHelpKey(t *testing.T) {
	help := tea.KeyPressMsg{Code: '?', Text: "?"}

	// Baseline: with no modal up, "?" is intercepted and opens Help.
	open := Model{keys: defaultAppKeys()}
	if _, cmd, handled := open.interceptGlobalKeys(help); !handled || cmd == nil {
		t.Fatal("baseline: ? should open Help when no overlay is up")
	}

	// With the OAuth modal up, "?" must fall through (not intercepted),
	// so the modal's own Update handles it instead of layering Help.
	m := Model{keys: defaultAppKeys(), oauthFlowOpen: true}
	if _, _, handled := m.interceptGlobalKeys(help); handled {
		t.Error("? should not be intercepted while the OAuth modal is open")
	}

	config := Model{keys: defaultAppKeys(), accountOAuthConfigOpen: true}
	if _, _, handled := config.interceptGlobalKeys(help); handled {
		t.Error("? should not be intercepted while OAuth client configuration is open")
	}
}

func TestSyncCompletionIsHandledBehindAccountSettings(t *testing.T) {
	m := Model{syncing: true}
	next, cmd := m.Update(syncFinishedMsg{summary: "Synced Personal Google"})
	m = next.(Model)
	if m.syncing || m.syncStatus != "Synced Personal Google" || cmd == nil {
		t.Fatalf("sync completion: syncing=%v status=%q cmd=%v",
			m.syncing, m.syncStatus, cmd == nil)
	}
}
