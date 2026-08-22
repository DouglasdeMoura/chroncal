package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

func (m Model) accountSettingsParams(accountID int64) (AccountSettingsParams, bool) {
	configured, ok := m.accounts[accountID]
	if !ok || accountID == 0 {
		return AccountSettingsParams{}, false
	}
	params := AccountSettingsParams{
		AccountID:   accountID,
		DisplayName: configured.DisplayName,
		Provider:    "CalDAV Account",
		ServerURL:   configured.ServerURL,
		Username:    configured.Username,
		AuthType:    calendar.NormalizeAuthType(configured.AuthType),
	}
	if caldav.IsGoogleCalendarEndpoint(configured.ServerURL) {
		params.Provider = "Google Account"
	}
	for _, info := range m.calendars {
		if info.AccountID != accountID {
			continue
		}
		params.CalendarCount++
		if info.LastSyncError != "" {
			params.AttentionCount++
		}
	}
	return params, true
}

func (m Model) reopenAccountSettings(accountID int64) Model {
	if params, ok := m.accountSettingsParams(accountID); ok {
		m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
		m.calendarManagerOpen = true
	}
	return m
}

func (m Model) finishAccountRename(msg accountRenameFinishedMsg) (Model, tea.Cmd) {
	m.syncing = false
	m.statusToken++
	if msg.err != nil {
		if m.accountRenameFromSettings {
			m.accountRename.form.SetError(0, msg.err.Error())
			m.accountRenameOpen = true
		}
		m.syncStatus = "Account rename failed: " + msg.err.Error()
		return m, tea.Batch(
			m.toast.Failed(msg.err.Error()),
			m.expireStatusAfter(8*time.Second, m.statusToken),
		)
	}
	m.accountRenameFromSettings = false
	if m.accounts == nil {
		m.accounts = make(map[int64]account.Account)
	}
	m.accounts[msg.account.ID] = msg.account
	if m.calendarManagerOpen && m.calendarManager.ActiveAccountID() == msg.account.ID {
		if params, ok := m.accountSettingsParams(msg.account.ID); ok {
			m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
		}
	}
	if draft := m.calendarManager.LocalDraft(); draft != nil &&
		draft.AccountID == msg.account.ID {
		m.calendarManager = m.calendarManager.SetAccountName(msg.account.DisplayName)
	}
	m.syncStatus = fmt.Sprintf("Renamed account to %s", msg.account.DisplayName)
	return m, tea.Batch(
		m.loadCalendars(),
		m.expireStatusAfter(6*time.Second, m.statusToken),
	)
}

func (m Model) finishAccountRemoval(msg accountRemovalFinishedMsg) (Model, tea.Cmd) {
	m.syncing = false
	m.statusToken++
	if msg.err != nil {
		if !m.calendarManagerOpen {
			m = m.reopenAccountSettings(msg.accountID)
		}
		m.syncStatus = "Account removal failed: " + msg.err.Error()
		return m, tea.Batch(
			m.toast.Failed(msg.err.Error()),
			m.expireStatusAfter(10*time.Second, m.statusToken),
		)
	}
	if m.calendarManagerOpen {
		if settings, ok := m.calendarManager.AccountSettings(); ok && settings.params.AccountID == msg.accountID {
			m.calendarManagerOpen = false
		}
	}
	if draft := m.calendarManager.LocalDraft(); draft != nil && draft.AccountID == msg.accountID {
		m.calendarManagerOpen = false
	}
	m.syncStatus = fmt.Sprintf(
		"Removed account %s; downloaded calendars are now local",
		textsafe.Display(msg.name),
	)
	return m, tea.Batch(
		m.loadCalendars(),
		m.expireStatusAfter(10*time.Second, m.statusToken),
	)
}

func (m Model) finishAccountManagementDiscovery(
	msg accountManagementDiscoveryReadyMsg,
) (Model, tea.Cmd) {
	stale := msg.generation != m.accountManagementGeneration ||
		msg.accountID != m.pendingAccountManagementID
	if configured, ok := m.accounts[msg.accountID]; !ok || configured.ID == 0 {
		stale = true
	}
	if msg.err == nil && msg.discovery.Account.ID != msg.accountID {
		stale = true
	}
	m.syncing = false
	if stale {
		m.pendingAccountManagementID = 0
		m.syncStatus = ""
		return m, nil
	}
	if msg.err != nil {
		m.pendingAccountManagementID = 0
		if params, ok := m.accountSettingsParams(msg.accountID); ok {
			m.calendarManager = m.calendarManager.OpenAccount(params).SetSize(m.width, m.height)
			m.calendarManagerOpen = true
		}
		m.statusToken++
		m.syncStatus = "Calendar discovery failed: " + msg.err.Error()
		return m, tea.Batch(
			m.toast.Failed(msg.err.Error()),
			m.expireStatusAfter(10*time.Second, m.statusToken),
		)
	}
	m.pendingAccountManagementID = 0
	m.calendarManager = m.calendarManager.OpenAccountCalendars(msg.discovery).SetSize(m.width, m.height)
	m.calendarManagerOpen = true
	m.syncStatus = ""
	return m, m.loadCalendars()
}

// sortedCalendarListItems builds the sidebar's calendar rows from the calendar
// map and sorts them by the user's persisted display order (name as a tiebreak,
// e.g. rows that share a backfilled default). Shared by the reload handler and
// the reorder handler so both produce identical row order.

func (m Model) openCredentialStore() (auth.CredentialStore, error) {
	return auth.NewCredentialStoreWithWarnings(
		m.app.CredentialNamespace,
		m.app.PreviousCredentialNamespaces,
		m.app.MigrateLegacyCredentials,
		m.app.AllowPlaintext,
		io.Discard,
	)
}

// newSyncService builds a sync.Service using the app's shared SQLite handle.
// Credential-store warnings are discarded so sync work does not clobber the
// rendered TUI. Token-refresh persist mid-run is included. The engine logs
// to the state-dir log file (never stderr — Bubble Tea owns the terminal).
// Sync detail like import warnings then stays inspectable. Users run
// `chroncal sync run` from a shell if they need verbose output live.

func (m Model) startOAuthFlow(clientID, clientSecret string) (Model, tea.Cmd) {
	m.oauthPending = false // the flow is opening now; release the request guard
	if m.oauthPurpose.calendarDiscovery {
		m.calendarManagerOpen = false
	}
	m.calendarManagerOpen = false
	m.accountOAuthConfigOpen = false
	m.oauthFlowOpen = true
	m.oauthFlow = m.oauthFlow.SetSize(m.width, m.height)
	var cmd tea.Cmd
	m.oauthFlow, cmd = m.oauthFlow.Start(clientID, clientSecret)
	return m, cmd
}

func (m Model) prepareAccountReauth(
	configured account.Account,
	clientID, clientSecret string,
) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		credStore, err := m.openCredentialStore()
		if err != nil {
			return accountReauthReadyMsg{accountID: configured.ID, name: configured.DisplayName, err: err}
		}
		cred, err := m.app.Accounts.LoadCredential(ctx, configured.ID, credStore)
		if err != nil {
			return accountReauthReadyMsg{
				accountID: configured.ID,
				name:      configured.DisplayName,
				err:       fmt.Errorf("load credential: %w", err),
			}
		}
		if clientID != "" {
			cred.OAuthClientID = clientID
		}
		if clientSecret != "" {
			cred.OAuthClientSecret = clientSecret
		}
		return accountReauthReadyMsg{
			accountID: configured.ID,
			name:      configured.DisplayName,
			cred:      cred,
		}
	}
}

// finishOAuthReauth persists the fresh tokens for a re-authenticated
// account. A full re-consent always returns a new refresh token. That
// differs from RefreshGoogleToken, which only returns a new one if the
// server rotates it. The stored credential's username and client config
// are kept. The token triple is replaced.
func (m Model) finishOAuthReauth(result *auth.GoogleOAuthResult) tea.Cmd {
	p := m.oauthPurpose
	storedMsg := func(err error) oauthCredentialStoredMsg {
		return oauthCredentialStoredMsg{
			accountID: p.accountID,
			name:      p.accountName,
			err:       err,
		}
	}
	return func() tea.Msg {
		credStore, err := m.openCredentialStore()
		if err != nil {
			return storedMsg(err)
		}
		cred := p.cred
		cred.AccountID = p.accountID
		cred.AccessToken = result.AccessToken
		// Keep the current refresh token if Google did not return a new
		// one. access_type=offline&prompt=consent normally forces a fresh
		// refresh token. If it comes back empty, an overwrite would
		// strip the account's ability to refresh and brick sync once the
		// access token expires (~1h). Keep the old one in that case.
		if result.RefreshToken != "" {
			cred.RefreshToken = result.RefreshToken
		}
		cred.TokenExpiry = result.Expiry.Format(time.RFC3339)
		err = m.app.Accounts.StoreCredential(
			context.Background(), p.accountID, p.cred.AccountFingerprint,
			cred, credStore,
		)
		return storedMsg(err)
	}
}

func (m Model) finishOAuthCredentialStore(msg oauthCredentialStoredMsg) (Model, tea.Cmd) {
	m.oauthPending = false
	if msg.err != nil {
		m.statusToken++
		m.syncStatus = fmt.Sprintf(
			"Authorized, but storing the tokens failed: %s — re-authenticating again is safe",
			msg.err,
		)
		m = m.reopenAccountSettings(msg.accountID)
		return m, m.expireStatusAfter(10*time.Second, m.statusToken)
	}
	m = m.reopenAccountSettings(msg.accountID)
	m.syncing = true
	m.syncStatus = fmt.Sprintf("Syncing %s…", syncProgressLabel(msg.name))
	return m, tea.Batch(
		m.syncSpinner.Tick,
		m.runSyncAccount(msg.accountID, msg.name),
	)
}

// updateAccountCredentials rotates one account's secret in place. The stored
// credential is loaded so its non-secret identity (username, client config) is
// kept. Only the password (basic) or access token (bearer) is replaced.
// StoreCredential re-checks the account fingerprint under the lifecycle lock.
// A concurrent rename or removal then aborts the write instead of a corrupt
// write.
func (m Model) updateAccountCredentials(configured account.Account, secret string) tea.Cmd {
	storedMsg := func(err error) accountCredentialStoredMsg {
		return accountCredentialStoredMsg{
			accountID: configured.ID,
			name:      configured.DisplayName,
			err:       err,
		}
	}
	return func() tea.Msg {
		ctx := context.Background()
		credStore, err := m.openCredentialStore()
		if err != nil {
			return storedMsg(err)
		}
		fingerprint := configured.CredentialFingerprint()
		cred, err := credentialForRotation(credStore.Get(configured.ID, fingerprint))
		if err != nil {
			return storedMsg(err)
		}
		if accountAuthIsBearer(configured.AuthType) {
			cred.AccessToken = secret
		} else {
			cred.Password = secret
		}
		err = m.app.Accounts.StoreCredential(ctx, configured.ID, fingerprint, cred, credStore)
		return storedMsg(err)
	}
}

// credentialForRotation maps the pre-rotation Get outcome to the credential
// the new secret is written into. A keyring entry that is gone or identity-
// mismatched is one of the broken states rotation exists to repair. Those
// start from an empty credential instead of a refusal. StoreCredential
// re-seeds the account identity. Any other error aborts the rotation.
func credentialForRotation(cred auth.Credential, err error) (auth.Credential, error) {
	if err == nil {
		return cred, nil
	}
	if auth.IsCredentialNotFound(err) || errors.Is(err, auth.ErrCredentialIdentityMismatch) {
		return auth.Credential{}, nil
	}
	return auth.Credential{}, fmt.Errorf("load current credentials: %w", err)
}

// finishAccountCredentialStore lands the in-place rotation. Success reopens
// Account Settings and syncs to confirm the new secret works; failure reopens
// Settings with the error and leaves the previous credential intact.
func (m Model) finishAccountCredentialStore(msg accountCredentialStoredMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.syncing = false
		m.statusToken++
		m.syncStatus = fmt.Sprintf(
			"Couldn't update credentials: %s — the previous sign-in is unchanged",
			msg.err,
		)
		m = m.reopenAccountSettings(msg.accountID)
		return m, m.expireStatusAfter(10*time.Second, m.statusToken)
	}
	m = m.reopenAccountSettings(msg.accountID)
	m.syncing = true
	m.syncStatus = fmt.Sprintf("Syncing %s…", syncProgressLabel(msg.name))
	return m, tea.Batch(
		m.syncSpinner.Tick,
		m.runSyncAccount(msg.accountID, msg.name),
	)
}

func calendarDiscoveryAccountName(accounts []account.Account, username string) string {
	base := account.SuggestedName(username)
	if base == "" {
		base = "CalDAV"
	}
	taken := make(map[string]struct{}, len(accounts))
	for _, configured := range accounts {
		name := strings.TrimSpace(configured.DisplayName)
		if name == "" {
			name = account.UserFacingName(configured.Name, configured.Username, configured.ID)
		}
		taken[strings.ToLower(name)] = struct{}{}
	}
	if _, exists := taken[strings.ToLower(base)]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s (%d)", base, suffix)
		if _, exists := taken[strings.ToLower(candidate)]; !exists {
			return candidate
		}
	}
}
func existingCalendarDiscoveryAccount(accounts []account.Account, req CalendarDiscoveryRequestedMsg) (account.Account, bool) {
	fingerprint := auth.AccountFingerprint(req.ServerURL, req.AuthType, req.Username)
	for _, configured := range accounts {
		if configured.CredentialFingerprint() == fingerprint {
			return configured, true
		}
	}
	return account.Account{}, false
}

func (m Model) connectAndDiscoverCalendar(req CalendarDiscoveryRequestedMsg, cred auth.Credential) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store, err := m.openCredentialStore()
		if err != nil {
			return accountDiscoveryReadyMsg{err: err}
		}
		configured, err := m.app.Accounts.List(ctx)
		if err != nil {
			return accountDiscoveryReadyMsg{err: fmt.Errorf("list accounts: %w", err)}
		}
		if existing, ok := existingCalendarDiscoveryAccount(configured, req); ok {
			discovery, err := m.app.Accounts.DiscoverWithCredential(ctx, existing.ID, cred, store)
			if err != nil {
				return accountDiscoveryReadyMsg{err: err}
			}
			return accountDiscoveryReadyMsg{discovery: discovery}
		}
		created, err := m.app.Accounts.Create(ctx, account.CreateParams{
			Name:          calendarDiscoveryAccountName(configured, req.Username),
			ServerURL:     req.ServerURL,
			AuthType:      req.AuthType,
			Username:      req.Username,
			AllowInsecure: req.AllowInsecure,
		}, cred, store)
		if err != nil {
			return accountDiscoveryReadyMsg{err: err}
		}
		discovery, err := m.app.Accounts.Discover(ctx, created.ID, store)
		if err != nil {
			if cleanupErr := m.app.Accounts.Delete(ctx, created.ID, store); cleanupErr != nil {
				err = fmt.Errorf("%w (remove incomplete connection: %w)", err, cleanupErr)
			}
			return accountDiscoveryReadyMsg{err: err}
		}
		return accountDiscoveryReadyMsg{discovery: discovery, createdAccount: true}
	}
}

func (m Model) finishOAuthCalendarDiscovery(result *auth.GoogleOAuthResult) tea.Cmd {
	req := m.oauthPurpose.calendarDiscoveryMsg
	cred := auth.Credential{
		Username:          req.Username,
		AccessToken:       result.AccessToken,
		RefreshToken:      result.RefreshToken,
		TokenExpiry:       result.Expiry.Format(time.RFC3339),
		OAuthClientID:     req.OAuthClientID,
		OAuthClientSecret: req.OAuthClientSecret,
	}
	return m.connectAndDiscoverCalendar(req, cred)
}

func (m Model) importAndSyncAccountCalendars(paths []string) tea.Cmd {
	if m.calendarManager.DiscoveryPicker() == nil {
		return func() tea.Msg {
			return accountImportFinishedMsg{err: fmt.Errorf("calendar discovery is no longer open")}
		}
	}
	discovery := m.calendarManager.DiscoveryPicker().discovery
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := m.app.Accounts.Import(ctx, discovery, paths)
		if err != nil {
			return accountImportFinishedMsg{err: err}
		}
		finished := accountImportFinishedMsg{
			created:  len(result.CreatedIDs),
			existing: len(result.ExistingIDs),
		}
		if len(result.CreatedIDs) == 0 {
			return finished
		}
		syncService, err := m.newSyncService()
		if err != nil {
			finished.syncErr = err
			return finished
		}
		finished.synced, finished.warnings, finished.syncErr = syncNewlyLinkedCalendars(ctx, syncService, result.CreatedIDs, m.fullSyncStrategy)
		return finished
	}
}

func (m Model) reconcileAndSyncAccountCalendars(selection *accountCalendarSelection) tea.Cmd {
	finished := accountSelectionFinishedMsg{
		removedCurrent:    selection.removedCurrent,
		accountManagement: selection.accountManagement,
	}
	discovery := selection.discovery
	params := selection.params
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		store, err := m.openCredentialStore()
		if err != nil {
			finished.err = err
			return finished
		}
		result, err := m.app.Accounts.ReconcileSelection(ctx, discovery, params, store)
		if err != nil {
			finished.err = err
			return finished
		}
		finished.created = len(result.CreatedIDs)
		finished.removed = len(result.RemovedIDs)
		finished.removedIDs = result.RemovedIDs
		finished.accountRemoved = result.AccountRemoved
		if len(result.CreatedIDs) == 0 {
			return finished
		}
		syncService, err := m.newSyncService()
		if err != nil {
			finished.syncErr = err
			return finished
		}
		finished.synced, finished.warnings, finished.syncErr = syncNewlyLinkedCalendars(ctx, syncService, result.CreatedIDs, m.fullSyncStrategy)
		return finished
	}
}

func (m Model) prepareAccountCalendarSelection(
	msg AccountCalendarsReconcileRequestedMsg,
) (*accountCalendarSelection, []accountDefaultCandidate, error) {
	picker := m.calendarManager.DiscoveryPicker()
	if picker == nil || !picker.manage || msg.AccountID != picker.discovery.Account.ID {
		return nil, nil, fmt.Errorf("calendar selection no longer matches the open account")
	}

	discoveredByPath := make(map[string]account.DiscoveredCalendar, len(picker.discovery.Calendars))
	for _, item := range picker.discovery.Calendars {
		discoveredByPath[item.Path] = item
	}
	selected := make(map[string]struct{}, len(msg.SelectedPaths))
	selectedPaths := make([]string, 0, len(msg.SelectedPaths))
	for _, path := range msg.SelectedPaths {
		if _, ok := discoveredByPath[path]; !ok {
			return nil, nil, fmt.Errorf("calendar %q was not part of the open discovery", path)
		}
		if _, duplicate := selected[path]; duplicate {
			continue
		}
		selected[path] = struct{}{}
		selectedPaths = append(selectedPaths, path)
	}

	selection := &accountCalendarSelection{
		discovery:         picker.discovery,
		params:            account.SelectionParams{SelectedPaths: selectedPaths},
		accountManagement: true,
	}
	removedIDs := make(map[int64]struct{})
	var removedDefault bool
	for _, item := range picker.discovery.Calendars {
		if !item.Imported {
			continue
		}
		if _, keep := selected[item.Path]; keep {
			continue
		}
		selection.removed = append(selection.removed, item)
		removedIDs[item.CalendarID] = struct{}{}
		if m.calendarManagerOpen && m.calendarManager.LocalDraft() != nil &&
			item.CalendarID == m.calendarManager.LocalDraft().ID {
			selection.removedCurrent = true
		}
		if info, ok := m.calendars[item.CalendarID]; ok && info.IsDefault {
			removedDefault = true
		}
	}
	if !removedDefault {
		return selection, nil, nil
	}

	candidates := make([]accountDefaultCandidate, 0, len(m.calendars)+len(picker.discovery.Calendars))
	for id, info := range m.calendars {
		if _, removed := removedIDs[id]; removed {
			continue
		}
		candidates = append(candidates, accountDefaultCandidate{id: id, name: info.Name})
	}
	for _, item := range picker.discovery.Calendars {
		if item.Imported {
			continue
		}
		if _, add := selected[item.Path]; add {
			candidates = append(candidates, accountDefaultCandidate{path: item.Path, name: item.Name})
		}
	}
	slices.SortFunc(candidates, func(a, b accountDefaultCandidate) int {
		if byName := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); byName != 0 {
			return byName
		}
		if byPath := strings.Compare(a.path, b.path); byPath != 0 {
			return byPath
		}
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		default:
			return 0
		}
	})
	if len(candidates) == 0 {
		return nil, nil, calendar.ErrLastCalendar
	}
	return selection, candidates, nil
}

func (m Model) showAccountCalendarRemovalConfirmation(selection *accountCalendarSelection) Model {
	names := make([]string, 0, len(selection.removed))
	for _, item := range selection.removed {
		names = append(names, textsafe.Display(item.Name))
	}
	var message string
	if len(names) == 1 {
		message = fmt.Sprintf("Remove “%s” from Chroncal?", names[0])
	} else {
		message = fmt.Sprintf("Remove %d calendars from Chroncal?\n\n• %s",
			len(names), strings.Join(names, "\n• "))
	}
	message += "\n\nNothing will be deleted from the server."
	message += "\nLocal copies and changes not yet uploaded will be removed."
	message += "\nYou can add these calendars again later."
	buttonLabel := "Save Changes"
	if len(selection.params.SelectedPaths) == 0 {
		message += "\n\nThis also removes the account and stored sign-in from Chroncal."
		buttonLabel = "Remove Account"
	}
	if selection.removedCurrent {
		message += "\n\nYour unsaved calendar edits will be discarded."
	}
	m.pendingAccountSelection = selection
	m.confirmDialog = NewConfirmDialogModel(message, buttonLabel, m.theme).
		Destructive().
		SetSize(m.width, m.height)
	m.confirmOpen = true
	return m
}
func (m Model) discardDiscoveryAccount(accountID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store, err := m.openCredentialStore()
		if err == nil {
			err = m.app.Accounts.Delete(ctx, accountID, store)
		}
		return calendarDiscoveryDiscardedMsg{err: err}
	}
}

func (m Model) removeAccount(accountID int64, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store, err := m.openCredentialStore()
		if err == nil {
			err = m.app.Accounts.Delete(ctx, accountID, store)
		}
		return accountRemovalFinishedMsg{accountID: accountID, name: name, err: err}
	}
}

func (m Model) discoverAccountCalendars(accountID int64, generation uint64) tea.Cmd {
	result := func(discovery account.Discovery, err error) accountManagementDiscoveryReadyMsg {
		return accountManagementDiscoveryReadyMsg{
			discovery: discovery, accountID: accountID, generation: generation, err: err,
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store, err := m.openCredentialStore()
		if err != nil {
			return result(account.Discovery{}, fmt.Errorf("open credential store: %w", err))
		}
		discovery, err := m.app.Accounts.Discover(ctx, accountID, store)
		return result(discovery, err)
	}
}

// syncProgressLabel produces a short calendar label for the per-calendar
// progress footer. Calendar names can be long (Apple/Google often exceed 40
// chars) and would push the spinner off the visible status line.
