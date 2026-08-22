package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/calendar"
)

func (m Model) loadCalendars() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		cals, err := m.app.Calendars.List(ctx)
		if err != nil {
			return calendarsLoadedMsg{err: err}
		}
		// Pre-fetch accounts once so linked rows carry their user-facing group
		// name, server URL, normalized auth type, and order without an account
		// query per calendar.
		accounts, _ := m.app.Accounts.List(ctx)
		info := buildCalendarInfoMap(cals, accounts, func(calendarID int64) (int64, error) {
			return m.app.Events.CountByCalendar(ctx, calendarID)
		})
		accountByID := make(map[int64]account.Account, len(accounts))
		for _, configured := range accounts {
			accountByID[configured.ID] = configured
		}
		return calendarsLoadedMsg{calendars: info, accounts: accountByID}
	}
}

// buildCalendarInfoMap assembles the sidebar's CalendarInfo cache from the
// persisted calendars and accounts. Accounts are read once so every linked
// row carries its account metadata: display name, server URL, normalized
// auth type, and display order. There is no per-calendar query. Local
// calendars (AccountID == 0) leave the account-linked fields empty,
// AccountAuthType included. A nil or empty accounts slice yields local-only
// metadata. That is the same effect as an account-list failure.
func buildCalendarInfoMap(
	cals []calendar.Calendar,
	accounts []account.Account,
	eventCount func(calendarID int64) (int64, error),
) map[int64]CalendarInfo {
	accountServerURLs := map[int64]string{}
	accountNames := map[int64]string{}
	accountOrders := map[int64]int64{}
	accountAuthTypes := map[int64]string{}
	for _, remoteAccount := range accounts {
		accountServerURLs[remoteAccount.ID] = remoteAccount.ServerURL
		accountNames[remoteAccount.ID] = remoteAccount.DisplayName
		accountOrders[remoteAccount.ID] = remoteAccount.DisplayOrder
		accountAuthTypes[remoteAccount.ID] = calendar.NormalizeAuthType(remoteAccount.AuthType)
	}
	info := make(map[int64]CalendarInfo, len(cals))
	for _, c := range cals {
		count, _ := eventCount(c.ID)
		info[c.ID] = CalendarInfo{
			Name:                c.Name,
			Color:               c.Color,
			OwnerEmail:          c.OwnerEmail,
			Description:         c.Description,
			EventCount:          count,
			DisplayOrder:        c.DisplayOrder,
			Synced:              c.AccountID != 0,
			AccountServerURL:    accountServerURLs[c.AccountID],
			AccountID:           c.AccountID,
			AccountName:         accountNames[c.AccountID],
			AccountOrder:        accountOrders[c.AccountID],
			AccountAuthType:     accountAuthTypes[c.AccountID],
			RemoteAccess:        c.RemoteAccess,
			RemoteComponents:    c.RemoteComponents,
			RemoteMissing:       c.RemoteMissing,
			LastSyncAt:          c.LastSyncAt,
			LastSyncAttemptedAt: c.LastSyncAttemptedAt,
			LastSyncError:       c.LastSyncError,
			CreatedAt:           c.CreatedAt,
			UpdatedAt:           c.UpdatedAt,
			IsDefault:           c.IsDefault,
		}
	}
	return info
}

func sortedCalendarListItems(calendars map[int64]CalendarInfo) []CalendarListItem {
	items := make([]CalendarListItem, 0, len(calendars))
	for id, calendarInfo := range calendars {
		accountName := calendarInfo.AccountName
		if calendarInfo.AccountID == 0 {
			accountName = "Local"
		}
		items = append(items, CalendarListItem{
			ID:           id,
			Name:         calendarInfo.Name,
			Color:        calendarInfo.Color,
			Health:       syncHealthFor(calendarInfo),
			Order:        calendarInfo.DisplayOrder,
			AccountID:    calendarInfo.AccountID,
			AccountName:  accountName,
			AccountOrder: calendarInfo.AccountOrder,
			Access:       calendarInfo.RemoteAccess,
			Missing:      calendarInfo.RemoteMissing,
		})
	}
	slices.SortFunc(items, func(a, b CalendarListItem) int {
		if a.AccountID == 0 && b.AccountID != 0 {
			return -1
		}
		if a.AccountID != 0 && b.AccountID == 0 {
			return 1
		}
		if a.AccountID != b.AccountID {
			if a.AccountOrder < b.AccountOrder {
				return -1
			}
			if a.AccountOrder > b.AccountOrder {
				return 1
			}
			if a.AccountID < b.AccountID {
				return -1
			}
			return 1
		}
		return compareCalendarOrder(a.Order, a.Name, b.Order, b.Name)
	})
	return items
}

func (m *Model) beginAccountOrderSave(ids []int64) tea.Cmd {
	saved := slices.Clone(ids)
	m.accountOrderSaveInFlight = true
	return func() tea.Msg {
		return accountOrderSavedMsg{
			ids: saved,
			err: m.app.Accounts.SetOrder(context.Background(), saved),
		}
	}
}

// blockReadOnlyCalendarMutation rejects event mutations on calendars the remote
// server has declared off-limits for VEVENT writes. Those are read-only
// collections, and collections whose non-empty supported-component set omits
// VEVENT. A calendar with no reported components is treated as unconstrained
// (backward compatible). Service-layer guards are enforced separately.
func (m *Model) blockReadOnlyCalendarMutation(calendarID int64) (tea.Cmd, bool) {
	info, ok := m.calendars[calendarID]
	if !ok {
		return nil, false
	}
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = "This calendar"
	}
	if strings.EqualFold(strings.TrimSpace(info.RemoteAccess), "read") {
		return m.toast.Failed(fmt.Sprintf("%s is read-only", name)), true
	}
	if info.RemoteComponents != "" && !supportsVEVENT(info.RemoteComponents) {
		return m.toast.Failed(fmt.Sprintf("%s does not support events", name)), true
	}
	return nil, false
}

// supportsVEVENT reports whether the comma-separated component set contains
// VEVENT. An empty string means "no advertised components" and is handled by
// the caller, not here.
func supportsVEVENT(remoteComponents string) bool {
	for _, part := range strings.Split(remoteComponents, ",") {
		if strings.EqualFold(strings.TrimSpace(part), "VEVENT") {
			return true
		}
	}
	return false
}

// eventFormCalendars omits collections that cannot accept VEVENT writes so
// create and move pickers never offer a destination that the service must
// reject on submit. Empty component metadata remains backward compatible.
func eventFormCalendars(calendars map[int64]CalendarInfo) map[int64]CalendarInfo {
	filtered := make(map[int64]CalendarInfo, len(calendars))
	for id, info := range calendars {
		if strings.EqualFold(strings.TrimSpace(info.RemoteAccess), "read") {
			continue
		}
		if info.RemoteComponents != "" && !supportsVEVENT(info.RemoteComponents) {
			continue
		}
		filtered[id] = info
	}
	return filtered
}

// syncHealthFor derives the sidebar health marker state from a calendar's
// persisted sync fields. Local-only calendars (not Synced) get no marker.
// A recorded last_sync_error is the only loud (SyncHealthError) state.
// Every sync attempt writes these fields: manual, and the background
// `chroncal tick` cron. The marker then surfaces failures the user never
// triggered.
func syncHealthFor(info CalendarInfo) SyncHealth {
	switch {
	case !info.Synced:
		return SyncHealthNone
	case info.LastSyncError != "":
		return SyncHealthError
	case info.LastSyncAt != "":
		return SyncHealthOK
	default:
		return SyncHealthPending
	}
}

// openCredentialStore opens the credential store with the namespace settings and
// the migration settings of the app. It discards the warnings, so keyring output
// never overwrites the rendered TUI. Every TUI flow that touches a credential
// goes through this one constructor. A change to how the store opens thus
// applies to all the flows at the same time.
