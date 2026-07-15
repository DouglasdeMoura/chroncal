package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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

// TestReauthDoublePressGuarded reproduces the red-team double-press race: the
// guard must reject a second Re-authenticate before the first flow's modal
// has opened (oauthFlowOpen still false, oauthPending true).
func TestReauthDoublePressGuarded(t *testing.T) {
	m := Model{}

	// First press: passes the guard and arms oauthPending synchronously.
	m = updateModel(t, m, CalendarReauthRequestedMsg{ID: 12, Name: "gmail"})
	if !m.oauthPending {
		t.Fatal("first Re-authenticate should arm oauthPending synchronously")
	}
	if m.oauthFlowOpen {
		t.Fatal("oauthFlowOpen should still be false before the async load lands")
	}

	// Second fast press: must be rejected (no new flow, guard unchanged).
	before := m.oauthPending
	m2, cmd := m.Update(CalendarReauthRequestedMsg{ID: 12, Name: "gmail"})
	if cmd != nil {
		t.Error("second Re-authenticate should be a no-op (nil cmd), not launch a second flow")
	}
	if got := m2.(Model).oauthPending; got != before {
		t.Errorf("oauthPending changed on rejected second press: %v", got)
	}
}

// TestReauthGuardRejectsWhileSyncing ensures a manual sync in flight blocks
// re-auth (the credential write would race the sync).
func TestReauthGuardRejectsWhileSyncing(t *testing.T) {
	m := Model{syncing: true}
	next, cmd := m.Update(CalendarReauthRequestedMsg{ID: 12, Name: "gmail"})
	if cmd != nil {
		t.Error("Re-authenticate should be rejected while syncing")
	}
	if next.(Model).oauthPending {
		t.Error("oauthPending should not arm while syncing")
	}
}

// TestReauthReadyClearsPendingAndOpensFlow verifies the success transition:
// the modal opens and the request guard releases.
func TestReauthReadyClearsPendingAndOpensFlow(t *testing.T) {
	m := Model{oauthPending: true, width: 100, height: 40}
	m = updateModel(t, m, calendarReauthReadyMsg{
		calendarID: 12, name: "gmail", accountID: 7,
		cred: auth.Credential{OAuthClientID: "cid", OAuthClientSecret: "shh"},
	})
	if m.oauthPending {
		t.Error("oauthPending should clear once the flow opens")
	}
	if !m.oauthFlowOpen {
		t.Error("oauthFlowOpen should be true after the flow starts")
	}
	if m.oauthPurpose.calendarID != 12 {
		t.Errorf("oauthPurpose.calendarID = %d, want 12", m.oauthPurpose.calendarID)
	}
}

// TestReauthReadyErrorReleasesPending ensures a failed credential load frees
// the guard so the user can retry.
func TestReauthReadyErrorReleasesPending(t *testing.T) {
	m := Model{oauthPending: true}
	m = updateModel(t, m, calendarReauthReadyMsg{err: errTestReauth})
	if m.oauthPending {
		t.Error("oauthPending should clear on a failed credential load")
	}
	if m.oauthFlowOpen {
		t.Error("no flow should open on error")
	}
	if !strings.Contains(m.syncStatus, "Re-authentication failed") {
		t.Errorf("syncStatus = %q, want re-auth failure", m.syncStatus)
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
}
