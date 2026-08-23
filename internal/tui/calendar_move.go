package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

type calendarMoveState struct {
	sourceID    int64
	sourceName  string
	accounts    []account.Account
	account     account.Account
	discovery   account.Discovery
	collections []account.DiscoveredCalendar
}

type calendarMoveDiscoveryReadyMsg struct {
	// state is the move snapshot the choice dispatched. The move working
	// state lives in the message, not in Model: no dialog is armed while
	// discovery or migration runs.
	state     calendarMoveState
	discovery account.Discovery
	err       error
}

type calendarMoveFinishedMsg struct {
	sourceID int64
	account  account.Account
	result   account.MigrateResult
	err      error
}

func (m Model) beginCalendarMove(msg CalendarMoveToAccountRequestedMsg) (Model, tea.Cmd) {
	if m.syncing || m.pending.isCalendarMove() {
		return m, m.toast.Failed("Finish the current account operation before moving a calendar")
	}
	info, ok := m.calendars[msg.ID]
	if !ok || info.Synced || info.AccountID != 0 {
		return m, m.toast.Failed("Only a local calendar can be moved to an account")
	}
	accounts := make([]account.Account, 0, len(m.accounts))
	for _, configured := range m.accounts {
		accounts = append(accounts, configured)
	}
	slices.SortFunc(accounts, func(a, b account.Account) int {
		if a.DisplayOrder != b.DisplayOrder {
			return int(a.DisplayOrder - b.DisplayOrder)
		}
		return strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName))
	})
	if len(accounts) == 0 {
		return m, m.toast.Failed("Add an account before moving this calendar")
	}
	labels := make([]string, len(accounts))
	for i, configured := range accounts {
		labels[i] = textsafe.Display(configured.DisplayName)
		if labels[i] == "" {
			labels[i] = textsafe.Display(configured.Name)
		}
	}
	return m.armChoice(
		pendingAction{
			kind: pendingActionCalendarMoveAccount,
			target: pendingTarget{move: &calendarMoveState{
				sourceID: msg.ID, sourceName: msg.Name, accounts: accounts,
			}},
		},
		NewChoiceDialogModel(
			fmt.Sprintf("Move %q to which account?", msg.Name), m.theme, labels...,
		),
	), nil
}

func (m Model) discoverCalendarMove(state calendarMoveState) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store, err := m.openCredentialStore()
		if err != nil {
			return calendarMoveDiscoveryReadyMsg{state: state, err: fmt.Errorf("open credential store: %w", err)}
		}
		discovery, err := m.app.Accounts.Discover(ctx, state.account.ID, store)
		return calendarMoveDiscoveryReadyMsg{state: state, discovery: discovery, err: err}
	}
}

func (m Model) finishCalendarMoveDiscovery(msg calendarMoveDiscoveryReadyMsg) (Model, tea.Cmd) {
	pending := m.pending.target.move
	if !m.pending.isCalendarMove() || pending == nil ||
		pending.sourceID != msg.state.sourceID ||
		pending.account.ID != msg.state.account.ID {
		return m, nil
	}
	m.syncing = false
	state := msg.state
	if msg.err != nil {
		m = m.clearPending()
		m.statusToken++
		m.syncStatus = "Couldn't load destination calendars: " + msg.err.Error()
		return m, m.expireStatusAfter(10*time.Second, m.statusToken)
	}
	collections := make([]account.DiscoveredCalendar, 0, len(msg.discovery.Calendars))
	for _, remote := range msg.discovery.Calendars {
		if remote.Importable && remote.Access != caldav.CalendarAccessRead {
			collections = append(collections, remote)
		}
	}
	if len(collections) == 0 {
		m = m.clearPending()
		return m, m.toast.Failed("This account has no writable calendar collections")
	}
	slices.SortFunc(collections, func(a, b account.DiscoveredCalendar) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	labels := make([]string, len(collections))
	for i, remote := range collections {
		labels[i] = textsafe.Display(remote.Name)
		if labels[i] == "" {
			labels[i] = textsafe.Display(remote.Path)
		}
	}
	state.discovery = msg.discovery
	state.collections = collections
	return m.armChoice(
		pendingAction{
			kind:   pendingActionCalendarMoveCollection,
			target: pendingTarget{move: &state},
		},
		NewChoiceDialogModel(
			fmt.Sprintf("Move %q into which calendar?", state.sourceName), m.theme, labels...,
		),
	), nil
}

func (m Model) migrateCalendarMove(pending calendarMoveState, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		result, err := m.app.Accounts.MigrateCalendarToAccount(ctx, pending.sourceID, pending.discovery, path)
		return calendarMoveFinishedMsg{sourceID: pending.sourceID, account: pending.account, result: result, err: err}
	}
}

func (m Model) finishCalendarMove(msg calendarMoveFinishedMsg) (Model, tea.Cmd) {
	pending := m.pending.target.move
	if !m.pending.isCalendarMove() || pending == nil ||
		pending.sourceID != msg.sourceID ||
		pending.account.ID != msg.account.ID {
		return m, nil
	}
	m.syncing = false
	m = m.clearPending()
	if msg.err != nil {
		m.statusToken++
		m.syncStatus = "Couldn't move calendar: " + msg.err.Error()
		return m, m.expireStatusAfter(10*time.Second, m.statusToken)
	}
	m.calendarManagerOpen = false
	m.syncing = true
	m.syncStatus = "Uploading moved calendar to " + textsafe.Display(msg.account.DisplayName) + "…"
	return m, tea.Batch(
		m.syncSpinner.Tick,
		m.loadCalendars(),
		m.loadEvents(),
		m.runSyncAccount(msg.account.ID, msg.account.DisplayName),
	)
}

// calendarMoveChoice resolves one step of the calendar move flow. act is
// the armed action captured by handleChoiceDialogResult before it reset
// the pending state; the working state rides in the dispatched message.
func (m Model) calendarMoveChoice(act pendingAction, choice int) (tea.Model, tea.Cmd) {
	move := act.target.move
	switch act.kind {
	case pendingActionCalendarMoveAccount:
		if move == nil || choice < 0 || choice >= len(move.accounts) {
			return m, nil
		}
		move.account = move.accounts[choice]
		m.syncing = true
		m.syncStatus = "Loading calendars for " + textsafe.Display(move.account.DisplayName) + "…"
		// Keep the move armed while discovery runs so a cancelled or
		// superseded flow ignores the stale result (ctrl+c clearPending).
		m.pending = pendingAction{
			kind:   pendingActionCalendarMoveAccount,
			target: pendingTarget{move: move},
		}
		return m, tea.Batch(
			m.syncSpinner.Tick,
			m.discoverCalendarMove(*move),
		)
	case pendingActionCalendarMoveCollection:
		if move == nil || choice < 0 || choice >= len(move.collections) {
			return m, nil
		}
		state := *move
		m.syncing = true
		m.syncStatus = "Moving " + textsafe.Display(move.sourceName) + "…"
		m.pending = pendingAction{
			kind:   pendingActionCalendarMoveCollection,
			target: pendingTarget{move: move},
		}
		return m, tea.Batch(m.syncSpinner.Tick, m.migrateCalendarMove(state, move.collections[choice].Path))
	default:
		return m, nil
	}
}
