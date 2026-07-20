package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func TestAppCalendarDiscoveryOpensPickerInsideCalendarDialog(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.syncing = true

	updated, _ := m.Update(accountDiscoveryReadyMsg{discovery: pickerDiscovery()})
	m = updated.(Model)
	if m.syncing || !m.calendarDialogOpen || m.calendarDialog.discoveryPicker == nil {
		t.Fatalf("discovery transition: syncing=%v calendarDialog=%v picker=%v",
			m.syncing, m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
	}
}

func TestAppOAuthCalendarDiscoveryRecordsIntegratedPurpose(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	req := CalendarDiscoveryRequestedMsg{
		ServerURL: "https://example.com/caldav", Username: "me@example.com",
		AuthType: "oauth2", OAuthClientID: "client.apps", OAuthClientSecret: "secret",
	}
	updated, cmd := m.Update(req)
	m = updated.(Model)
	if cmd == nil || !m.oauthFlowOpen || !m.oauthPurpose.calendarDiscovery {
		t.Fatalf("OAuth discovery state: flow=%v purpose=%v cmd=%v",
			m.oauthFlowOpen, m.oauthPurpose.calendarDiscovery, cmd)
	}
	if m.oauthPurpose.calendarDiscoveryMsg.Username != "me@example.com" {
		t.Fatalf("OAuth purpose lost discovery request: %+v", m.oauthPurpose.calendarDiscoveryMsg)
	}
	m.oauthFlow.Abort()
}

func TestAppCalendarDiscoveryCancelSchedulesTemporaryAccountCleanup(t *testing.T) {
	m := NewModel(nil, "")
	m.calendarDialogOpen = true
	m.pendingDiscoveryAccountID = 7
	m.pendingDiscoveryCreated = true

	updated, cmd := m.Update(AccountCalendarPickerClosedMsg{})
	m = updated.(Model)
	if cmd == nil || m.calendarDialogOpen || !m.syncing {
		t.Fatalf("cancel state: cmd=%v calendarDialog=%v syncing=%v", cmd, m.calendarDialogOpen, m.syncing)
	}
	if m.pendingDiscoveryAccountID != 0 || m.pendingDiscoveryCreated {
		t.Fatalf("temporary account state not cleared: id=%d created=%v",
			m.pendingDiscoveryAccountID, m.pendingDiscoveryCreated)
	}
}

func TestAppOAuthCalendarDiscoveryCancelReturnsToConnectionStep(t *testing.T) {
	m := NewModel(nil, "")
	m.oauthFlowOpen = true
	m.oauthFlow.state = OAuthFlowWaiting
	m.oauthPurpose.calendarDiscovery = true

	updated, _ := m.Update(oauthFlowDoneMsg{err: context.Canceled})
	m = updated.(Model)
	if m.oauthFlowOpen || !m.calendarDialogOpen {
		t.Fatalf("OAuth cancel state: flow=%v calendarDialog=%v", m.oauthFlowOpen, m.calendarDialogOpen)
	}
}

func TestCalendarDiscoveryAccountNameUsesFriendlyUniqueSuggestion(t *testing.T) {
	accounts := []account.Account{
		{ID: 3, Name: "maildodouglas@gmail.com", DisplayName: "Google", Username: "maildodouglas@gmail.com"},
	}
	if got := calendarDiscoveryAccountName(accounts, "other@gmail.com"); got != "Google (2)" {
		t.Fatalf("second Google account name = %q, want %q", got, "Google (2)")
	}
	if got := calendarDiscoveryAccountName(accounts, "douglas.moura@jaya.tech"); got != "Jaya" {
		t.Fatalf("workspace account name = %q, want %q", got, "Jaya")
	}
}

func TestExistingCalendarDiscoveryAccountMatchesConnectionIdentity(t *testing.T) {
	accounts := []account.Account{
		{ID: 3, DisplayName: "First", ServerURL: "https://example.com/dav", AuthType: "basic", Username: "other"},
		{ID: 7, DisplayName: "Existing", ServerURL: "https://example.com/dav", AuthType: "basic", Username: "me@example.com"},
	}
	got, ok := existingCalendarDiscoveryAccount(
		accounts,
		CalendarDiscoveryRequestedMsg{
			ServerURL: " https://example.com/dav ",
			AuthType:  "BASIC",
			Username:  "me@example.com",
		},
	)
	if !ok || got.ID != 7 {
		t.Fatalf("matched account = %+v, %v; want ID 7", got, ok)
	}
}

func TestAppAdditionalCalendarDiscoveryReturnsToUnsavedEditFormOnCancel(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")

	updated, cmd := m.Update(CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7})
	m = updated.(Model)
	if cmd == nil || !m.syncing || !m.discoveryReturnToEdit {
		t.Fatalf("additional discovery start: cmd=%v syncing=%v returnToEdit=%v",
			cmd, m.syncing, m.discoveryReturnToEdit)
	}

	updated, _ = m.Update(accountDiscoveryReadyMsg{
		discovery:        pickerDiscovery(),
		management:       true,
		originCalendarID: 11,
		originAccountID:  7,
		returnToEdit:     true,
	})
	m = updated.(Model)
	if m.calendarDialog.discoveryPicker == nil {
		t.Fatal("additional discovery did not open the picker")
	}
	if !m.calendarDialog.discoveryPicker.manage {
		t.Fatal("additional discovery opened the additive picker instead of account management")
	}

	updated, _ = m.Update(AccountCalendarPickerClosedMsg{})
	m = updated.(Model)
	if !m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil {
		t.Fatalf("picker cancel: calendarDialog=%v picker=%v",
			m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
	}
	if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
		t.Fatalf("unsaved name after picker cancel = %q", got)
	}
}

func TestAppAdditionalCalendarDiscoveryIgnoresResultAfterEditCloses(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.calendarDialogGeneration = 4

	updated, cmd := m.Update(CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7})
	m = updated.(Model)
	if cmd == nil || !m.syncing {
		t.Fatalf("additional discovery start: cmd=%v syncing=%v", cmd, m.syncing)
	}
	updated, _ = m.Update(CalendarDialogClosedMsg{})
	m = updated.(Model)
	updated, _ = m.Update(accountDiscoveryReadyMsg{
		discovery:        pickerDiscovery(),
		management:       true,
		dialogGeneration: 4,
	})
	m = updated.(Model)
	if m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil || m.syncing {
		t.Fatalf("stale discovery reopened dialog: open=%v picker=%v syncing=%v",
			m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil, m.syncing)
	}
}

func TestAppAdditionalCalendarImportReturnsToUnsavedEditForm(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")
	m.calendarDialog = m.calendarDialog.ShowDiscovery(pickerDiscovery()).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.discoveryReturnToEdit = true

	updated, cmd := m.Update(AccountCalendarsImportRequestedMsg{
		AccountID: 7,
		Paths:     []string{"/holidays/"},
	})
	m = updated.(Model)
	if cmd == nil || m.calendarDialogOpen || !m.syncing {
		t.Fatalf("calendar import start: cmd=%v dialog=%v syncing=%v",
			cmd, m.calendarDialogOpen, m.syncing)
	}

	updated, _ = m.Update(accountImportFinishedMsg{created: 1, synced: 1})
	m = updated.(Model)
	if !m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil {
		t.Fatalf("calendar import finish: dialog=%v picker=%v",
			m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
	}
	if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
		t.Fatalf("unsaved name after calendar import = %q", got)
	}
}

func TestAppAccountActionsMenuReturnsToUnsavedCalendarEdit(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")

	updated, _ := m.Update(CalendarAccountActionsRequestedMsg{})
	m = updated.(Model)
	if !m.calendarAccountMenuOpen {
		t.Fatal("Account action did not open its menu")
	}
	// The Edit Calendar Account menu keeps Disconnect (and its danger
	// styling); only the sidebar Account dialog omits it. Pinning this
	// here keeps the two flows' menu contracts distinct.
	if got, want := accountActionLabels(m.calendarAccountMenu.actions),
		"Manage calendars…,Disconnect…,Cancel"; got != want {
		t.Fatalf("Edit Calendar Account menu labels = %q, want %q", got, want)
	}
	disconnect := m.calendarAccountMenu.actions[1]
	if disconnect.label != "Disconnect…" || disconnect.variant != ButtonDanger {
		t.Errorf("Edit Calendar Disconnect = {label: %q, variant: %v}, want Disconnect…/ButtonDanger",
			disconnect.label, disconnect.variant)
	}

	updated, _ = m.Update(CalendarAccountMenuClosedMsg{})
	m = updated.(Model)
	if m.calendarAccountMenuOpen || !m.calendarDialogOpen {
		t.Fatalf("menu close: accountMenu=%v calendarDialog=%v",
			m.calendarAccountMenuOpen, m.calendarDialogOpen)
	}
	if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
		t.Fatalf("unsaved name after Account menu close = %q", got)
	}
}

func TestAppAccountMenuSelectionClosesMenuBeforeDispatch(t *testing.T) {
	m := NewModel(nil, "")
	m.calendarAccountMenuOpen = true
	want := CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7}

	updated, cmd := m.Update(CalendarAccountMenuSelectedMsg{Message: want})
	m = updated.(Model)
	if m.calendarAccountMenuOpen {
		t.Fatal("Account menu stayed open after selection")
	}
	if cmd == nil {
		t.Fatal("Account menu selection did not dispatch its action")
	}
	if got := cmd(); got != want {
		t.Fatalf("dispatched action = %#v, want %#v", got, want)
	}
}

func TestAppAccountSettingsRequestOpensCanonicalPanel(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.accounts = map[int64]account.Account{
		7: {
			ID: 7, DisplayName: "Personal Google",
			ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
			AuthType:  "oauth2", Username: "douglas@example.com",
		},
	}
	m.calendars = map[int64]CalendarInfo{
		2: {Name: "Personal", AccountID: 7},
		3: {Name: "Família", AccountID: 7, LastSyncError: "token expired"},
		9: {Name: "Local"},
	}

	updated, cmd := m.Update(AccountSettingsRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("opening Account settings returned command %T", cmd())
	}
	if !m.accountSettingsOpen {
		t.Fatal("Account settings did not open")
	}
	if m.calendarDialogOpen || m.calendarAccountMenuOpen || m.syncing || m.oauthPending || m.oauthFlowOpen {
		t.Fatalf("opening settings changed unrelated state: calendar=%v oldMenu=%v syncing=%v oauthPending=%v oauth=%v",
			m.calendarDialogOpen, m.calendarAccountMenuOpen, m.syncing, m.oauthPending, m.oauthFlowOpen)
	}
	if got := m.accountSettings.params; got.AccountID != 7 || got.DisplayName != "Personal Google" ||
		got.Username != "douglas@example.com" || got.Provider != "Google Account" ||
		got.CalendarCount != 2 || got.AttentionCount != 1 || got.AuthType != "oauth2" {
		t.Fatalf("Account settings params = %+v", got)
	}
}

// sidebarAccountActionsModel seeds a Model for the sidebar Account dialog
// tests. Calendar 2 is a remote OAuth calendar under account 7; calendar 5
// is a remote basic calendar under account 9; calendar 99 is local-only.
// The dialog starts closed, matching the sidebar click origin.
func sidebarAccountActionsModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendars = map[int64]CalendarInfo{
		2:  {Name: "Personal", Color: "#a6e3a1", AccountID: 7, AccountName: "Google", AccountAuthType: "oauth2"},
		5:  {Name: "Work", Color: "#89b4fa", AccountID: 9, AccountName: "Fastmail", AccountAuthType: "basic"},
		99: {Name: "Local", Color: "#f9e2af"},
	}
	return m
}

// TestAppSidebarAccountActionsOpensMenuWithoutSideEffects pins the
// first step of the sidebar Account dialog: emitting
// SidebarAccountActionsRequestedMsg opens the shared Account menu
// synchronously, with no discovery, sync, OAuth, or Edit Calendar side
// effects. The OAuth variant renders Manage/Re-authenticate/Cancel and
// never Disconnect; the basic variant drops Re-authenticate too.
func TestAppSidebarAccountActionsOpensMenuWithoutSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		calendarID int64
		accountID  int64
		wantLabels string
		wantReauth bool
	}{
		{
			name:       "oauth account includes reauthenticate",
			calendarID: 2,
			accountID:  7,
			wantLabels: "Manage calendars…,Re-authenticate…,Cancel",
			wantReauth: true,
		},
		{
			name:       "basic account omits reauthenticate",
			calendarID: 5,
			accountID:  9,
			wantLabels: "Manage calendars…,Cancel",
			wantReauth: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sidebarAccountActionsModel(t)

			updated, cmd := m.Update(SidebarAccountActionsRequestedMsg{
				AccountID:  tc.accountID,
				CalendarID: tc.calendarID,
			})
			m = updated.(Model)

			// Opening the menu is synchronous and side-effect free
			// beyond menu state.
			if cmd != nil {
				t.Fatalf("sidebar Account actions dispatched unexpected command: %v", cmd)
			}
			if !m.calendarAccountMenuOpen {
				t.Fatal("sidebar Account actions did not open the menu")
			}
			if m.calendarDialogOpen {
				t.Fatal("sidebar Account actions opened Edit Calendar")
			}
			if m.calendarDialogGeneration != 0 {
				t.Fatalf("sidebar Account actions bumped dialog generation: %d", m.calendarDialogGeneration)
			}
			if m.syncing {
				t.Fatal("sidebar Account actions started syncing")
			}
			if m.oauthFlowOpen || m.oauthPending {
				t.Fatalf("sidebar Account actions started OAuth: flow=%v pending=%v",
					m.oauthFlowOpen, m.oauthPending)
			}

			// Menu contents: always Manage + Cancel, optional Reauth,
			// never Disconnect, never destructive.
			if got := accountActionLabels(m.calendarAccountMenu.actions); got != tc.wantLabels {
				t.Fatalf("menu labels = %q, want %q", got, tc.wantLabels)
			}
			for i, a := range m.calendarAccountMenu.actions {
				if a.label == "Disconnect…" {
					t.Errorf("action %d: Disconnect must not appear in the sidebar Account menu", i)
				}
				if a.variant == ButtonDanger {
					t.Errorf("action %d (%q): sidebar Account menu must have no destructive entry", i, a.label)
				}
			}
			// The rendered View echoes the same label set.
			rendered := stripANSI(m.calendarAccountMenu.View())
			for _, want := range strings.Split(tc.wantLabels, ",") {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered menu missing %q:\n%s", want, rendered)
				}
			}
			if strings.Contains(rendered, "Disconnect") {
				t.Errorf("rendered sidebar Account menu must not contain Disconnect:\n%s", rendered)
			}

			// Manage carries the representative IDs for the second step.
			manageMsg, ok := m.calendarAccountMenu.actions[0].onPress().(AccountCalendarManagementRequestedMsg)
			if !ok {
				t.Fatalf("Manage dispatched %T, want AccountCalendarManagementRequestedMsg",
					m.calendarAccountMenu.actions[0].onPress())
			}
			if manageMsg.AccountID != tc.accountID || manageMsg.CalendarID != tc.calendarID {
				t.Errorf("Manage = %+v, want AccountID %d / CalendarID %d",
					manageMsg, tc.accountID, tc.calendarID)
			}

			if tc.wantReauth {
				reauthMsg, ok := m.calendarAccountMenu.actions[1].onPress().(CalendarReauthRequestedMsg)
				if !ok {
					t.Fatalf("Reauth dispatched %T, want CalendarReauthRequestedMsg",
						m.calendarAccountMenu.actions[1].onPress())
				}
				if reauthMsg.ID != tc.calendarID {
					t.Errorf("Reauth ID = %d, want %d", reauthMsg.ID, tc.calendarID)
				}
			}
		})
	}
}

// TestAppSidebarAccountActionsCancelClosesOnlyMenu verifies Cancel (or Esc)
// tears down just the Account menu — no Edit Calendar to restore, no sync
// to start — preserving the sidebar as the user's return point.
func TestAppSidebarAccountActionsCancelClosesOnlyMenu(t *testing.T) {
	m := sidebarAccountActionsModel(t)

	updated, _ := m.Update(SidebarAccountActionsRequestedMsg{AccountID: 7, CalendarID: 2})
	m = updated.(Model)
	if !m.calendarAccountMenuOpen {
		t.Fatal("precondition: sidebar Account actions did not open the menu")
	}

	updated, cmd := m.Update(CalendarAccountMenuClosedMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("Cancel dispatched unexpected command: %v", cmd)
	}
	if m.calendarAccountMenuOpen {
		t.Fatal("Cancel did not close the Account menu")
	}
	if m.calendarDialogOpen {
		t.Fatal("Cancel opened Edit Calendar")
	}
	if m.syncing {
		t.Fatal("Cancel started syncing")
	}
}

// TestAppSidebarAccountActionsManageSelectionClosesMenuBeforeDispatch
// verifies the routing seam: selecting Manage closes the Account menu and
// returns the second-step AccountCalendarManagementRequestedMsg, which a
// later Update turn consumes to start discovery. Discovery does not start
// synchronously here — the menu-close dispatch is its own step.
func TestAppSidebarAccountActionsManageSelectionClosesMenuBeforeDispatch(t *testing.T) {
	m := sidebarAccountActionsModel(t)
	updated, _ := m.Update(SidebarAccountActionsRequestedMsg{AccountID: 7, CalendarID: 2})
	m = updated.(Model)

	want := AccountCalendarManagementRequestedMsg{AccountID: 7, CalendarID: 2}
	updated, cmd := m.Update(CalendarAccountMenuSelectedMsg{Message: want})
	m = updated.(Model)

	if m.calendarAccountMenuOpen {
		t.Fatal("Account menu stayed open after Manage selection")
	}
	if m.syncing {
		t.Fatal("Manage selection started discovery synchronously; it must dispatch first")
	}
	if cmd == nil {
		t.Fatal("Manage selection did not dispatch its action")
	}
	if got := cmd(); got != want {
		t.Fatalf("dispatched action = %#v, want %#v", got, want)
	}
}

// TestAppSidebarAccountManagementStartsDiscovery feeds the second-step
// AccountCalendarManagementRequestedMsg into Model.Update and verifies it
// revalidates ownership against the current cache, starts discovery with
// the sidebar-returning completion path (discoveryReturnToEdit=false),
// and keeps Edit Calendar closed while discovery is pending. Discovery
// completion then opens the management picker on a freshly-built dialog,
// preserving the sidebar return.
func TestAppSidebarAccountManagementStartsDiscovery(t *testing.T) {
	m := sidebarAccountActionsModel(t)

	updated, cmd := m.Update(AccountCalendarManagementRequestedMsg{AccountID: 7, CalendarID: 2})
	m = updated.(Model)

	if cmd == nil || !m.syncing {
		t.Fatalf("sidebar management start: command=%v syncing=%v", cmd, m.syncing)
	}
	if m.discoveryReturnToEdit {
		t.Fatal("sidebar management should return to the sidebar, not a calendar edit")
	}
	if m.calendarDialogOpen {
		t.Fatal("sidebar management exposed an Edit Calendar dialog while discovery was pending")
	}
	if m.oauthFlowOpen || m.oauthPending {
		t.Fatalf("sidebar management started OAuth: flow=%v pending=%v",
			m.oauthFlowOpen, m.oauthPending)
	}
	dialogGeneration := m.calendarDialogGeneration
	if dialogGeneration == 0 {
		t.Fatal("sidebar management did not allocate a calendar dialog generation for the pending picker")
	}

	// Discovery completion attaches the management picker to the
	// freshly-built dialog and opens it; the sidebar return is preserved
	// by discoveryReturnToEdit=false.
	updated, _ = m.Update(accountDiscoveryReadyMsg{
		discovery:        pickerDiscovery(),
		management:       true,
		originCalendarID: 2,
		originAccountID:  7,
		dialogGeneration: dialogGeneration,
	})
	m = updated.(Model)
	if m.syncing || !m.calendarDialogOpen || m.calendarDialog.discoveryPicker == nil {
		t.Fatalf("management completion: syncing=%v open=%v picker=%v",
			m.syncing, m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
	}
	if !m.calendarDialog.discoveryPicker.manage {
		t.Fatal("sidebar management opened the additive picker instead of account management")
	}
}

// TestAppSidebarAccountManagementStaleCompletionDropsSilently covers the
// async stale-completion path for sidebar-originated management: if the
// representative calendar drifts away (removed or relinked) while
// discovery is in flight, the matching-generation completion is dropped
// silently — syncing clears, Edit Calendar stays closed, and no picker
// attaches. A toast would be noise here because the section the user
// acted on no longer exists.
func TestAppSidebarAccountManagementStaleCompletionDropsSilently(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift func(map[int64]CalendarInfo)
	}{
		{
			name:  "representative calendar removed",
			drift: func(cals map[int64]CalendarInfo) { delete(cals, 2) },
		},
		{
			name: "representative calendar relinked",
			drift: func(cals map[int64]CalendarInfo) {
				info := cals[2]
				info.AccountID = 99
				cals[2] = info
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sidebarAccountActionsModel(t)

			updated, _ := m.Update(AccountCalendarManagementRequestedMsg{AccountID: 7, CalendarID: 2})
			m = updated.(Model)
			if !m.syncing {
				t.Fatal("precondition: sidebar management did not start syncing")
			}
			dialogGeneration := m.calendarDialogGeneration

			// Ownership drifts while discovery is in flight: the
			// representative calendar is removed or relinked before
			// the matching-generation completion arrives.
			tc.drift(m.calendars)

			updated, cmd := m.Update(accountDiscoveryReadyMsg{
				discovery:        pickerDiscovery(),
				management:       true,
				originCalendarID: 2,
				originAccountID:  7,
				dialogGeneration: dialogGeneration,
			})
			m = updated.(Model)

			if m.syncing {
				t.Fatal("stale completion left syncing active")
			}
			if m.calendarDialogOpen {
				t.Fatal("stale completion opened Edit Calendar")
			}
			if m.calendarDialog.discoveryPicker != nil {
				t.Fatal("stale completion attached the management picker")
			}
			if m.discoveryReturnToEdit {
				t.Fatal("stale completion left discoveryReturnToEdit set")
			}
			// Silent drop: no toast, no status expiration.
			if cmd != nil {
				t.Fatalf("stale completion dispatched unexpected command: %v", cmd)
			}
		})
	}
}

// TestAppSidebarAccountManagementDiscoveryFailureToasts covers the
// sidebar-originated discovery error path: a failing completion clears
// syncing, keeps Edit Calendar closed, and surfaces the failure as a
// toast command so the user knows why nothing opened.
func TestAppSidebarAccountManagementDiscoveryFailureToasts(t *testing.T) {
	m := sidebarAccountActionsModel(t)

	updated, _ := m.Update(AccountCalendarManagementRequestedMsg{AccountID: 7, CalendarID: 2})
	m = updated.(Model)
	if !m.syncing {
		t.Fatal("precondition: sidebar management did not start syncing")
	}
	dialogGeneration := m.calendarDialogGeneration

	discoveryErr := errors.New("discovery failed: 503 service unavailable")
	updated, cmd := m.Update(accountDiscoveryReadyMsg{
		err:              discoveryErr,
		management:       true,
		originCalendarID: 2,
		originAccountID:  7,
		dialogGeneration: dialogGeneration,
	})
	m = updated.(Model)

	if m.syncing {
		t.Fatal("failed discovery left syncing active")
	}
	if m.calendarDialogOpen {
		t.Fatal("failed sidebar discovery opened Edit Calendar")
	}
	if m.calendarDialog.discoveryPicker != nil {
		t.Fatal("failed sidebar discovery attached the management picker")
	}
	if cmd == nil {
		t.Fatal("failed sidebar discovery should surface a toast command")
	}
}

// TestAppSidebarAccountActionsRejectsStaleRequests covers the first-step
// guards: unknown calendar, account mismatch, a local calendar (no
// account), and concurrent syncing/OAuth state. Every guard returns no
// menu and no command — the click is silently dropped.
func TestAppSidebarAccountActionsRejectsStaleRequests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		msg       SidebarAccountActionsRequestedMsg
		calendars map[int64]CalendarInfo
		syncing   bool
		oauth     bool
	}{
		{
			name:      "unknown calendar",
			msg:       SidebarAccountActionsRequestedMsg{AccountID: 7, CalendarID: 404},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
		},
		{
			name:      "account mismatch",
			msg:       SidebarAccountActionsRequestedMsg{AccountID: 9, CalendarID: 2},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
		},
		{
			name:      "local calendar",
			msg:       SidebarAccountActionsRequestedMsg{AccountID: 0, CalendarID: 99},
			calendars: map[int64]CalendarInfo{99: {Name: "Local"}},
		},
		{
			name:      "concurrent syncing",
			msg:       SidebarAccountActionsRequestedMsg{AccountID: 7, CalendarID: 2},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
			syncing:   true,
		},
		{
			name:      "concurrent oauth",
			msg:       SidebarAccountActionsRequestedMsg{AccountID: 7, CalendarID: 2},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
			oauth:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(nil, "")
			m.width, m.height = 120, 40
			m.calendars = tc.calendars
			m.syncing = tc.syncing
			m.oauthFlowOpen = tc.oauth

			updated, cmd := m.Update(tc.msg)
			m = updated.(Model)

			if m.calendarAccountMenuOpen {
				t.Fatal("stale sidebar Account request opened the menu")
			}
			if m.calendarDialogOpen {
				t.Fatal("stale sidebar Account request opened Edit Calendar")
			}
			if m.syncing != tc.syncing {
				t.Fatalf("stale sidebar Account request changed syncing: got %v want %v",
					m.syncing, tc.syncing)
			}
			if m.oauthFlowOpen != tc.oauth {
				t.Fatalf("stale sidebar Account request changed oauthFlowOpen: got %v want %v",
					m.oauthFlowOpen, tc.oauth)
			}
			if cmd != nil {
				t.Fatalf("stale sidebar Account request dispatched unexpected command: %v", cmd)
			}
		})
	}
}

// TestAppSidebarAccountManagementRejectsStaleOwnership covers the
// second-step guards. Concurrent syncing/OAuth silently no-ops (the
// operation is already in flight); ownership drift toasts an explanation
// so the user knows why nothing happened after picking Manage.
func TestAppSidebarAccountManagementRejectsStaleOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		msg       AccountCalendarManagementRequestedMsg
		calendars map[int64]CalendarInfo
		syncing   bool
		oauth     bool
		wantCmd   bool
	}{
		{
			name:      "unknown calendar",
			msg:       AccountCalendarManagementRequestedMsg{AccountID: 7, CalendarID: 404},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
			wantCmd:   true,
		},
		{
			name:      "account mismatch",
			msg:       AccountCalendarManagementRequestedMsg{AccountID: 9, CalendarID: 2},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
			wantCmd:   true,
		},
		{
			name:      "local calendar",
			msg:       AccountCalendarManagementRequestedMsg{AccountID: 0, CalendarID: 99},
			calendars: map[int64]CalendarInfo{99: {Name: "Local"}},
			wantCmd:   true,
		},
		{
			name:      "concurrent syncing",
			msg:       AccountCalendarManagementRequestedMsg{AccountID: 7, CalendarID: 2},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
			syncing:   true,
			wantCmd:   false,
		},
		{
			name:      "concurrent oauth",
			msg:       AccountCalendarManagementRequestedMsg{AccountID: 7, CalendarID: 2},
			calendars: map[int64]CalendarInfo{2: {Name: "Personal", AccountID: 7}},
			oauth:     true,
			wantCmd:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(nil, "")
			m.width, m.height = 120, 40
			m.calendars = tc.calendars
			m.syncing = tc.syncing
			m.oauthFlowOpen = tc.oauth

			updated, cmd := m.Update(tc.msg)
			m = updated.(Model)

			if m.calendarAccountMenuOpen {
				t.Fatal("stale management request opened the Account menu")
			}
			if m.calendarDialogOpen {
				t.Fatal("stale management request opened Edit Calendar")
			}
			if m.syncing != tc.syncing {
				t.Fatalf("stale management request changed syncing: got %v want %v",
					m.syncing, tc.syncing)
			}
			if tc.wantCmd && cmd == nil {
				t.Fatal("stale management request should surface a toast")
			}
			if !tc.wantCmd && cmd != nil {
				t.Fatalf("concurrent-state management request dispatched unexpected command: %v", cmd)
			}
		})
	}
}

func accountManagementAppModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
		ID:             42,
		AccountID:      7,
		Name:           "Personal",
		Color:          "#a6e3a1",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
	}, m.theme).SetSize(m.width, m.height)
	m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("Unsaved rename")
	m.calendarDialog = m.calendarDialog.ShowCalendarManagement(pickerDiscovery()).SetSize(m.width, m.height)
	m.calendarDialogOpen = true
	m.discoveryReturnToEdit = true
	m.calendars = map[int64]CalendarInfo{
		42: {Name: "Personal", IsDefault: false, AccountID: 7},
		99: {Name: "Local", IsDefault: true},
	}
	return m
}
func TestAppAccountReorderUpdatesSidebarAndSurvivesRacingReload(t *testing.T) {
	m := accountManagementAppModel(t)
	m.calendarDialogOpen = false
	m.calendars[100] = CalendarInfo{Name: "Work", AccountID: 9, AccountName: "Work"}
	m.calendars[42] = CalendarInfo{Name: "Personal", AccountID: 7, AccountName: "Personal"}
	m.sidebar = m.sidebar.SetList(m.sidebar.List().SetItems(sortedCalendarListItems(m.calendars)))

	updated, cmd := m.Update(AccountReorderedMsg{IDs: []int64{9, 7}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("account reorder did not schedule persistence")
	}
	if m.calendars[100].AccountOrder != 0 || m.calendars[42].AccountOrder != 1 {
		t.Fatalf("optimistic account order = work:%d personal:%d",
			m.calendars[100].AccountOrder, m.calendars[42].AccountOrder)
	}

	stale := map[int64]CalendarInfo{
		42:  {Name: "Personal", AccountID: 7, AccountName: "Personal", AccountOrder: 0},
		99:  {Name: "Local"},
		100: {Name: "Work", AccountID: 9, AccountName: "Work", AccountOrder: 1},
	}
	updated, _ = m.Update(calendarsLoadedMsg{calendars: stale})
	m = updated.(Model)
	if m.calendars[100].AccountOrder != 0 || m.calendars[42].AccountOrder != 1 {
		t.Fatalf("racing reload reverted account order = %+v", m.calendars)
	}

	updated, _ = m.Update(accountOrderSavedMsg{ids: []int64{9, 7}})
	m = updated.(Model)
	if len(m.pendingAccountOrder) != 0 {
		t.Fatalf("confirmed account order remains pending: %+v", m.pendingAccountOrder)
	}
}
func TestAppAccountReorderSerializesLatestOrder(t *testing.T) {
	m := accountManagementAppModel(t)
	m.calendarDialogOpen = false
	m.calendars[100] = CalendarInfo{Name: "Work", AccountID: 9, AccountName: "Work"}

	updated, firstSave := m.Update(AccountReorderedMsg{IDs: []int64{9, 7}})
	m = updated.(Model)
	if firstSave == nil {
		t.Fatal("first account reorder did not start a save")
	}
	updated, secondSave := m.Update(AccountReorderedMsg{IDs: []int64{7, 9}})
	m = updated.(Model)
	if secondSave != nil {
		t.Fatal("second account reorder started concurrently instead of being coalesced")
	}

	updated, latestSave := m.Update(accountOrderSavedMsg{ids: []int64{9, 7}})
	m = updated.(Model)
	if latestSave == nil || !m.accountOrderSaveInFlight {
		t.Fatalf("older completion did not start latest save: command=%v inFlight=%v",
			latestSave, m.accountOrderSaveInFlight)
	}
	updated, finalCmd := m.Update(accountOrderSavedMsg{ids: []int64{7, 9}})
	m = updated.(Model)
	if finalCmd != nil || m.accountOrderSaveInFlight || len(m.pendingAccountOrder) != 0 {
		t.Fatalf("latest completion state: command=%v inFlight=%v pending=%+v",
			finalCmd, m.accountOrderSaveInFlight, m.pendingAccountOrder)
	}
}

func TestAppAccountReorderFailureRollsBackOptimisticOrder(t *testing.T) {
	m := accountManagementAppModel(t)
	m.calendarDialogOpen = false
	m.calendars[100] = CalendarInfo{Name: "Work", AccountID: 9, AccountName: "Work"}

	updated, _ := m.Update(AccountReorderedMsg{IDs: []int64{9, 7}})
	m = updated.(Model)
	updated, rollback := m.Update(accountOrderSavedMsg{
		ids: []int64{9, 7},
		err: errors.New("database busy"),
	})
	m = updated.(Model)
	if rollback == nil {
		t.Fatal("failed account reorder did not schedule a database reload")
	}
	if m.accountOrderSaveInFlight || len(m.pendingAccountOrder) != 0 {
		t.Fatalf("failed account order remains optimistic: inFlight=%v pending=%+v",
			m.accountOrderSaveInFlight, m.pendingAccountOrder)
	}
}

func TestAppAccountRenameRoutesThroughOpenManager(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountRenameRequestedMsg{AccountID: 7, Name: "Personal Google"})
	m = updated.(Model)
	if cmd == nil || !m.syncing {
		t.Fatalf("rename start: command=%v syncing=%v", cmd, m.syncing)
	}

	updated, cmd = m.Update(accountRenameFinishedMsg{account: account.Account{
		ID: 7, Name: "Personal Google", DisplayName: "Personal Google", Username: "douglas@example.com",
	}})
	m = updated.(Model)
	if m.syncing {
		t.Fatal("rename completion left syncing active")
	}
	if m.calendarDialog.discoveryPicker == nil ||
		m.calendarDialog.discoveryPicker.discovery.Account.DisplayName != "Personal Google" {
		t.Fatalf("rename completion did not refresh open manager: %+v", m.calendarDialog.discoveryPicker)
	}
	if cmd == nil {
		t.Fatal("rename completion should reload calendars")
	}
}

func TestAppAccountCalendarAddOnlySelectionStartsWithoutConfirmation(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/", "/personal/"},
	})
	m = updated.(Model)

	if cmd == nil || !m.syncing || m.calendarDialogOpen || m.confirmOpen {
		t.Fatalf("add-only selection: cmd=%v syncing=%v dialog=%v confirm=%v",
			cmd, m.syncing, m.calendarDialogOpen, m.confirmOpen)
	}
}

func TestAppAccountCalendarRemovalRequiresDestructiveConfirmation(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/"},
	})
	m = updated.(Model)

	if cmd != nil || !m.confirmOpen || !m.calendarDialogOpen || m.pendingAccountSelection == nil {
		t.Fatalf("removal selection: cmd=%v confirm=%v dialog=%v pending=%v",
			cmd, m.confirmOpen, m.calendarDialogOpen, m.pendingAccountSelection)
	}
	plain := stripANSI(m.confirmDialog.View())
	for _, want := range []string{
		"Remove “Personal” from Chroncal?",
		"Nothing will be deleted from the server.",
		"changes not yet uploaded",
		"unsaved calendar edits",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("removal confirmation missing %q:\n%s", want, plain)
		}
	}

	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: false})
	m = updated.(Model)
	if cmd != nil || m.confirmOpen || m.pendingAccountSelection != nil ||
		!m.calendarDialogOpen || m.calendarDialog.discoveryPicker == nil {
		t.Fatalf("cancel removal: cmd=%v confirm=%v pending=%v dialog=%v picker=%v",
			cmd, m.confirmOpen, m.pendingAccountSelection, m.calendarDialogOpen,
			m.calendarDialog.discoveryPicker != nil)
	}
}

func TestAppAccountCalendarDefaultRemovalOffersKeptAndNewReplacements(t *testing.T) {
	m := accountManagementAppModel(t)
	info := m.calendars[42]
	info.IsDefault = true
	m.calendars[42] = info
	info = m.calendars[99]
	info.IsDefault = false
	m.calendars[99] = info

	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/"},
	})
	m = updated.(Model)
	if cmd != nil || !m.choiceOpen || m.confirmOpen || len(m.pendingAccountDefaultCandidates) != 2 {
		t.Fatalf("default removal: cmd=%v choice=%v confirm=%v candidates=%+v",
			cmd, m.choiceOpen, m.confirmOpen, m.pendingAccountDefaultCandidates)
	}
	plain := stripANSI(m.choiceDialog.View())
	for _, want := range []string{"Choose a new default", "Local", "Primary"} {
		if !strings.Contains(plain, want) {
			t.Errorf("default replacement dialog missing %q:\n%s", want, plain)
		}
	}

	newPathChoice := -1
	for idx, candidate := range m.pendingAccountDefaultCandidates {
		if candidate.path == "/primary/" {
			newPathChoice = idx
			break
		}
	}
	if newPathChoice < 0 {
		t.Fatalf("newly selected calendar is not a default candidate: %+v", m.pendingAccountDefaultCandidates)
	}
	updated, _ = m.Update(ChoiceDialogResultMsg{Choice: newPathChoice})
	m = updated.(Model)
	if m.choiceOpen || !m.confirmOpen || m.pendingAccountSelection == nil ||
		m.pendingAccountSelection.params.NewDefaultPath != "/primary/" {
		t.Fatalf("replacement choice: choice=%v confirm=%v pending=%+v",
			m.choiceOpen, m.confirmOpen, m.pendingAccountSelection)
	}
}

func TestAppAccountCalendarDefaultCandidatesSanitizeRemoteNames(t *testing.T) {
	m := accountManagementAppModel(t)
	personal := m.calendars[42]
	personal.IsDefault = true
	m.calendars[42] = personal
	local := m.calendars[99]
	local.IsDefault = false
	m.calendars[99] = local
	m.calendarDialog.discoveryPicker.discovery.Calendars[1].Name = "\x1b]8;;https://evil.example\aOwned\x1b]8;;\a"

	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{
		AccountID:     7,
		SelectedPaths: []string{"/primary/", "/holidays/"},
	})
	m = updated.(Model)
	if cmd != nil || !m.choiceOpen {
		t.Fatalf("unsafe default candidate: cmd=%v choice=%v", cmd, m.choiceOpen)
	}
	if view := m.choiceDialog.View(); strings.Contains(view, "\x1b]8;;") || strings.Contains(view, "\a") {
		t.Fatalf("default candidate emitted terminal controls: %q", view)
	}
}

func TestAppAccountCalendarRemovalCompletionHandlesUnderlyingEdit(t *testing.T) {
	t.Run("kept current calendar returns to unsaved edit", func(t *testing.T) {
		m := accountManagementAppModel(t)
		updated, _ := m.Update(accountSelectionFinishedMsg{created: 1, synced: 1})
		m = updated.(Model)
		if !m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil {
			t.Fatalf("selection finish: dialog=%v picker=%v",
				m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil)
		}
		if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "Unsaved rename" {
			t.Fatalf("unsaved name after selection = %q", got)
		}
	})

	t.Run("removed current calendar closes edit", func(t *testing.T) {
		m := accountManagementAppModel(t)
		updated, _ := m.Update(accountSelectionFinishedMsg{
			removed:        1,
			removedCurrent: true,
			accountRemoved: true,
		})
		m = updated.(Model)
		if m.calendarDialogOpen || m.calendarDialog.discoveryPicker != nil || m.discoveryReturnToEdit {
			t.Fatalf("removed-current finish: dialog=%v picker=%v return=%v",
				m.calendarDialogOpen, m.calendarDialog.discoveryPicker != nil, m.discoveryReturnToEdit)
		}
		if !strings.Contains(m.syncStatus, "Removed account") {
			t.Fatalf("removed-account status = %q", m.syncStatus)
		}
	})
}

func TestAppAccountCalendarCompletionRespectsReplacementEdit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		accountID int64
		msg       accountSelectionFinishedMsg
		wantOpen  bool
	}{
		{
			name:     "stale reconcile error preserves unrelated edit",
			msg:      accountSelectionFinishedMsg{err: account.ErrSelectionStale},
			wantOpen: true,
		},
		{
			name: "removed account preserves unrelated edit",
			msg: accountSelectionFinishedMsg{
				removed:        1,
				removedCurrent: true,
				accountRemoved: true,
			},
			wantOpen: true,
		},
		{
			name:      "removed replacement row closes its edit",
			accountID: 7,
			msg: accountSelectionFinishedMsg{
				removed:    1,
				removedIDs: []int64{99},
			},
			wantOpen: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := accountManagementAppModel(t)
			m.calendarDialogGeneration = 4
			tc.msg.dialogGeneration = 4
			m.calendarDialog = NewCalendarDialogModel(CalendarDialogParams{
				ID:        99,
				AccountID: tc.accountID,
				Name:      "Replacement",
				Color:     "#a6e3a1",
			}, m.theme).SetSize(m.width, m.height)
			m.calendarDialog.form.Field(cdIdxName).(*TextField).SetValue("New unsaved edit")
			m.calendarDialogGeneration = 5
			m.calendarDialogOpen = true

			updated, _ := m.Update(tc.msg)
			m = updated.(Model)
			if m.calendarDialogOpen != tc.wantOpen {
				t.Fatalf("replacement edit open = %v, want %v", m.calendarDialogOpen, tc.wantOpen)
			}
			if tc.wantOpen {
				if got := m.calendarDialog.form.Field(cdIdxName).(*TextField).Value(); got != "New unsaved edit" {
					t.Fatalf("replacement edit name = %q", got)
				}
			}
		})
	}
}

func TestAppRemovingEveryAccountCalendarAlsoRemovesAccount(t *testing.T) {
	m := accountManagementAppModel(t)
	updated, cmd := m.Update(AccountCalendarsReconcileRequestedMsg{AccountID: 7})
	m = updated.(Model)
	if cmd != nil || !m.confirmOpen || m.pendingAccountSelection == nil {
		t.Fatalf("empty selection: cmd=%v confirm=%v pending=%v",
			cmd, m.confirmOpen, m.pendingAccountSelection)
	}
	plain := strings.Join(strings.Fields(stripANSI(m.confirmDialog.View())), " ")
	for _, want := range []string{"stored", "sign-in", "Remove Account"} {
		if !strings.Contains(plain, want) {
			t.Errorf("empty-account confirmation missing %q:\n%s", want, plain)
		}
	}

	updated, cmd = m.Update(ConfirmDialogResultMsg{Confirmed: true})
	m = updated.(Model)
	if cmd == nil || !m.syncing || m.calendarDialogOpen || m.pendingAccountSelection != nil {
		t.Fatalf("confirmed empty selection: cmd=%v syncing=%v dialog=%v pending=%v",
			cmd, m.syncing, m.calendarDialogOpen, m.pendingAccountSelection)
	}
}

// TestCalendarAccountActionsBuilder_AssemblesFullSet pins the shared menu
// builder's fixed ordering and danger styling when every optional message is
// supplied. The sidebar Account dialog and the calendar edit's Account menu
// both assemble through this builder, so the contract lives here.
func TestCalendarAccountActionsBuilder_AssemblesFullSet(t *testing.T) {
	actions := buildCalendarAccountActions(calendarAccountActionMessages{
		Manage:     CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7},
		Reauth:     CalendarReauthRequestedMsg{ID: 11, Name: "Personal"},
		Disconnect: CalendarDisconnectRemoteRequestedMsg{ID: 11, Name: "Personal"},
	})
	if got, want := accountActionLabels(actions), "Manage calendars…,Re-authenticate…,Disconnect…,Cancel"; got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	// Disconnect is the only destructive action and lands right before Cancel.
	if actions[2].label != "Disconnect…" || actions[2].variant != ButtonDanger {
		t.Errorf("Disconnect action = {label: %q, variant: %v}, want Disconnect…/ButtonDanger",
			actions[2].label, actions[2].variant)
	}
	for i, a := range actions {
		if a.label != "Disconnect…" && a.variant == ButtonDanger {
			t.Errorf("action %d (%q) unexpectedly destructive", i, a.label)
		}
	}
	// Each non-Cancel action dispatches the exact message it was given,
	// including IDs, name, and (empty) client config fields.
	if manage, ok := actions[0].onPress().(CalendarDiscoverAdditionalRequestedMsg); !ok {
		t.Errorf("Manage dispatched %T, want CalendarDiscoverAdditionalRequestedMsg", actions[0].onPress())
	} else if manage.CalendarID != 11 || manage.AccountID != 7 {
		t.Errorf("Manage = %+v, want CalendarID 11 / AccountID 7", manage)
	}
	if reauth, ok := actions[1].onPress().(CalendarReauthRequestedMsg); !ok {
		t.Errorf("Reauth dispatched %T, want CalendarReauthRequestedMsg", actions[1].onPress())
	} else if reauth.ID != 11 || reauth.Name != "Personal" || reauth.ClientID != "" || reauth.ClientSecret != "" {
		t.Errorf("Reauth = %+v, want ID 11 / Personal / empty client config", reauth)
	}
	if disconnect, ok := actions[2].onPress().(CalendarDisconnectRemoteRequestedMsg); !ok {
		t.Errorf("Disconnect dispatched %T, want CalendarDisconnectRemoteRequestedMsg", actions[2].onPress())
	} else if disconnect.ID != 11 || disconnect.Name != "Personal" {
		t.Errorf("Disconnect = %+v, want ID 11 / Personal", disconnect)
	}
	if _, ok := actions[3].onPress().(CalendarAccountMenuClosedMsg); !ok {
		t.Errorf("Cancel dispatched %T, want CalendarAccountMenuClosedMsg", actions[3].onPress())
	}
}

// TestCalendarAccountActionsBuilder_OmitDisconnect proves a nil Disconnect
// message drops the destructive entry while preserving the order of the rest.
func TestCalendarAccountActionsBuilder_OmitDisconnect(t *testing.T) {
	actions := buildCalendarAccountActions(calendarAccountActionMessages{
		Manage: CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7},
		Reauth: CalendarReauthRequestedMsg{ID: 11, Name: "Personal"},
	})
	if got, want := accountActionLabels(actions), "Manage calendars…,Re-authenticate…,Cancel"; got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	for _, a := range actions {
		if a.variant == ButtonDanger {
			t.Errorf("no destructive action expected when Disconnect omitted; got %q", a.label)
		}
	}
}

// TestCalendarAccountActionsBuilder_OmitReauth proves a nil Re-authenticate
// message drops that entry while keeping Manage before Disconnect.
func TestCalendarAccountActionsBuilder_OmitReauth(t *testing.T) {
	actions := buildCalendarAccountActions(calendarAccountActionMessages{
		Manage:     CalendarDiscoverAdditionalRequestedMsg{CalendarID: 11, AccountID: 7},
		Disconnect: CalendarDisconnectRemoteRequestedMsg{ID: 11, Name: "Personal"},
	})
	if got, want := accountActionLabels(actions), "Manage calendars…,Disconnect…,Cancel"; got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	if actions[1].label != "Disconnect…" || actions[1].variant != ButtonDanger {
		t.Errorf("Disconnect action = {label: %q, variant: %v}, want Disconnect…/ButtonDanger",
			actions[1].label, actions[1].variant)
	}
}

// TestCalendarAccountActionsBuilder_OmitManage proves a nil Manage message
// drops that entry while keeping Re-authenticate before Disconnect.
func TestCalendarAccountActionsBuilder_OmitManage(t *testing.T) {
	actions := buildCalendarAccountActions(calendarAccountActionMessages{
		Reauth:     CalendarReauthRequestedMsg{ID: 11, Name: "Personal"},
		Disconnect: CalendarDisconnectRemoteRequestedMsg{ID: 11, Name: "Personal"},
	})
	if got, want := accountActionLabels(actions), "Re-authenticate…,Disconnect…,Cancel"; got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	// Disconnect stays destructive and lands right after Re-authenticate.
	if actions[1].label != "Disconnect…" || actions[1].variant != ButtonDanger {
		t.Errorf("Disconnect action = {label: %q, variant: %v}, want Disconnect…/ButtonDanger",
			actions[1].label, actions[1].variant)
	}
	for i, a := range actions {
		if a.label != "Disconnect…" && a.variant == ButtonDanger {
			t.Errorf("action %d (%q) unexpectedly destructive", i, a.label)
		}
	}
}

// TestCalendarDialog_AccountActionsMenuCarriesLiveDraftValues proves the
// dialog's Account menu reads the live, unsaved form state — the renamed
// calendar and the freshly-typed OAuth client config — rather than the
// params snapshot saved when the dialog opened.
func TestCalendarDialog_AccountActionsMenuCarriesLiveDraftValues(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:                   42,
		AccountID:            9,
		Name:                 "saved-name",
		RemoteLinked:         true,
		RemoteAuthType:       "oauth2",
		NeedOAuthConfig:      true,
		OAuthClientIDPrefill: "stored-cid.apps",
	}, Theme{}).SetSize(120, 40)

	// Unsaved edits: a renamed calendar plus freshly-typed OAuth config.
	m.form.Field(cdIdxName).(*TextField).SetValue("unsaved-name")
	m.oauthIDField.SetValue("live-cid.apps")
	m.oauthSecretField.SetValue("live-secret")

	actions := m.AccountActionsMenu().actions
	manage, ok := actions[0].onPress().(CalendarDiscoverAdditionalRequestedMsg)
	if !ok || manage.CalendarID != 42 || manage.AccountID != 9 {
		t.Errorf("Manage = %+v, want CalendarID 42 / AccountID 9", manage)
	}
	reauth, ok := actions[1].onPress().(CalendarReauthRequestedMsg)
	if !ok || reauth.ID != 42 || reauth.Name != "unsaved-name" ||
		reauth.ClientID != "live-cid.apps" || reauth.ClientSecret != "live-secret" {
		t.Errorf("Reauth = %+v, want ID 42 / unsaved-name / live-cid.apps / live-secret", reauth)
	}
	disconnect, ok := actions[2].onPress().(CalendarDisconnectRemoteRequestedMsg)
	if !ok || disconnect.ID != 42 || disconnect.Name != "unsaved-name" {
		t.Errorf("Disconnect = %+v, want ID 42 / unsaved-name", disconnect)
	}
	if actions[2].variant != ButtonDanger {
		t.Errorf("Disconnect variant = %v, want ButtonDanger", actions[2].variant)
	}
	if _, ok := actions[3].onPress().(CalendarAccountMenuClosedMsg); !ok {
		t.Errorf("Cancel dispatched %T, want CalendarAccountMenuClosedMsg", actions[3].onPress())
	}
}

// TestCalendarDialog_AccountActionsMenuCancelOnlyWhenNoDraft proves a dialog
// with no draft (nil localDraft) still yields a neutral Cancel-only menu
// without touching the form.
func TestCalendarDialog_AccountActionsMenuCancelOnlyWhenNoDraft(t *testing.T) {
	menu := CalendarDialogModel{}.AccountActionsMenu()
	if len(menu.actions) != 1 {
		t.Fatalf("action count = %d, want 1 (Cancel only)", len(menu.actions))
	}
	if menu.actions[0].label != "Cancel" {
		t.Errorf("label = %q, want Cancel", menu.actions[0].label)
	}
	if menu.actions[0].variant != Button {
		t.Errorf("Cancel variant = %v, want Button (neutral)", menu.actions[0].variant)
	}
	if _, ok := menu.actions[0].onPress().(CalendarAccountMenuClosedMsg); !ok {
		t.Errorf("Cancel dispatched %T, want CalendarAccountMenuClosedMsg", menu.actions[0].onPress())
	}
}

// TestCalendarAccountActionsBuilder_CancelOnlyFallback proves the builder
// always emits at least Cancel, so callers can hand it an empty message set
// for the no-draft case.
func TestCalendarAccountActionsBuilder_CancelOnlyFallback(t *testing.T) {
	actions := buildCalendarAccountActions(calendarAccountActionMessages{})
	if got, want := accountActionLabels(actions), "Cancel"; got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
	if _, ok := actions[0].onPress().(CalendarAccountMenuClosedMsg); !ok {
		t.Errorf("Cancel dispatched %T, want CalendarAccountMenuClosedMsg", actions[0].onPress())
	}
}

func accountActionLabels(actions []calendarAccountMenuAction) string {
	labels := make([]string, len(actions))
	for i, a := range actions {
		labels[i] = a.label
	}
	return strings.Join(labels, ",")
}
