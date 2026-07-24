package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/auth"
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
	sourceID  int64
	accountID int64
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
	if m.syncing || m.pendingCalendarMove != nil {
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
	m.pendingCalendarMove = &calendarMoveState{
		sourceID: msg.ID, sourceName: msg.Name, accounts: accounts,
	}
	m.pendingScopeKind = pendingScopeCalendarMoveAccount
	m.choiceDialog = NewChoiceDialogModel(
		fmt.Sprintf("Move %q to which account?", msg.Name), m.theme, labels...,
	).SetSize(m.width, m.height)
	m.choiceOpen = true
	return m, nil
}

func (m Model) discoverCalendarMove(sourceID, accountID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store, err := auth.NewCredentialStoreWithWarnings(
			m.app.CredentialNamespace,
			m.app.PreviousCredentialNamespaces,
			m.app.MigrateLegacyCredentials,
			m.app.AllowPlaintext,
			io.Discard,
		)
		if err != nil {
			return calendarMoveDiscoveryReadyMsg{sourceID: sourceID, accountID: accountID, err: fmt.Errorf("open credential store: %w", err)}
		}
		discovery, err := m.app.Accounts.Discover(ctx, accountID, store)
		return calendarMoveDiscoveryReadyMsg{sourceID: sourceID, accountID: accountID, discovery: discovery, err: err}
	}
}

func (m Model) finishCalendarMoveDiscovery(msg calendarMoveDiscoveryReadyMsg) (Model, tea.Cmd) {
	pending := m.pendingCalendarMove
	if pending == nil || pending.sourceID != msg.sourceID || pending.account.ID != msg.accountID {
		return m, nil
	}
	m.syncing = false
	if msg.err != nil {
		m.pendingCalendarMove = nil
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
		m.pendingCalendarMove = nil
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
	pending.discovery = msg.discovery
	pending.collections = collections
	m.pendingScopeKind = pendingScopeCalendarMoveCollection
	m.choiceDialog = NewChoiceDialogModel(
		fmt.Sprintf("Move %q into which calendar?", pending.sourceName), m.theme, labels...,
	).SetSize(m.width, m.height)
	m.choiceOpen = true
	m.syncStatus = ""
	return m, nil
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
	pending := m.pendingCalendarMove
	if pending == nil || pending.sourceID != msg.sourceID || pending.account.ID != msg.account.ID {
		return m, nil
	}
	m.syncing = false
	m.pendingCalendarMove = nil
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

func (m Model) handleCalendarMoveChoice(kind pendingScopeKind, choice int) (Model, tea.Cmd, bool) {
	pending := m.pendingCalendarMove
	switch kind {
	case pendingScopeCalendarMoveAccount:
		if pending == nil || choice < 0 || choice >= len(pending.accounts) {
			m.pendingCalendarMove = nil
			return m, nil, true
		}
		pending.account = pending.accounts[choice]
		m.syncing = true
		m.syncStatus = "Loading calendars for " + textsafe.Display(pending.account.DisplayName) + "…"
		return m, tea.Batch(
			m.syncSpinner.Tick,
			m.discoverCalendarMove(pending.sourceID, pending.account.ID),
		), true
	case pendingScopeCalendarMoveCollection:
		if pending == nil || choice < 0 || choice >= len(pending.collections) {
			m.pendingCalendarMove = nil
			return m, nil, true
		}
		path := pending.collections[choice].Path
		state := *pending
		m.syncing = true
		m.syncStatus = "Moving " + textsafe.Display(pending.sourceName) + "…"
		return m, tea.Batch(m.syncSpinner.Tick, m.migrateCalendarMove(state, path)), true
	default:
		return m, nil, false
	}
}
