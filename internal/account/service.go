package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

const defaultCalendarColor = "#7C3AED"

// ErrSelectionStale means the account's imported calendar set changed after
// discovery, so applying the old checklist could remove an unseen calendar.
var ErrSelectionStale = errors.New("calendar selection is stale")

type discoverFunc func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error)

// Service owns first-class CalDAV accounts and their discovered collections.
type Service struct {
	db       *sql.DB
	q        *storage.Queries
	discover discoverFunc
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q, discover: discoverRemoteCalendars}
}

func (s *Service) List(ctx context.Context) ([]Account, error) {
	rows, err := s.q.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, len(rows))
	for i, row := range rows {
		accounts[i] = fromStorage(row)
	}
	return accounts, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Account, error) {
	row, err := s.q.GetAccount(ctx, id)
	if err != nil {
		return Account{}, err
	}
	return fromStorage(row), nil
}

// Rename updates the account's human-facing description without changing its
// connection identity or credential lookup key.
func (s *Service) Rename(ctx context.Context, id int64, name string) (Account, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Account{}, fmt.Errorf("account name is required")
	}
	if strings.HasPrefix(name, legacyHiddenPrefix) {
		return Account{}, fmt.Errorf("account name uses reserved prefix %q", legacyHiddenPrefix)
	}
	release, err := synclock.Account(ctx, s.db, id)
	if err != nil {
		return Account{}, fmt.Errorf("lock account rename: %w", err)
	}
	defer release()
	current, err := s.q.GetAccount(ctx, id)
	if err != nil {
		return Account{}, fmt.Errorf("get account: %w", err)
	}
	if err := s.q.UpdateAccount(ctx, storage.UpdateAccountParams{
		ID:        id,
		Name:      name,
		ServerUrl: current.ServerUrl,
		AuthType:  current.AuthType,
		Username:  current.Username,
	}); err != nil {
		return Account{}, fmt.Errorf("rename account: %w", err)
	}
	updated, err := s.q.GetAccount(ctx, id)
	if err != nil {
		return Account{}, fmt.Errorf("get renamed account: %w", err)
	}
	return fromStorage(updated), nil
}

// SetOrder persists the complete remote-account section order atomically.
func (s *Service) SetOrder(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account order: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	for i, id := range ids {
		if err := qtx.SetAccountDisplayOrder(ctx, storage.SetAccountDisplayOrderParams{
			DisplayOrder: int64(i),
			ID:           id,
		}); err != nil {
			return fmt.Errorf("set account display order: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit account order: %w", err)
	}
	return nil
}

// Create stores account settings and credentials as one logical operation.
// If either side fails, the other is rolled back.
func (s *Service) Create(ctx context.Context, params CreateParams, cred auth.Credential, store auth.CredentialStore) (Account, error) {
	params.Name = strings.TrimSpace(params.Name)
	params.Username = strings.TrimSpace(params.Username)
	params.AuthType = strings.ToLower(strings.TrimSpace(params.AuthType))
	serverURL, err := validateServerURL(params.ServerURL, params.AllowInsecure)
	if err != nil {
		return Account{}, err
	}
	if params.Name == "" {
		return Account{}, fmt.Errorf("account name is required")
	}
	if strings.HasPrefix(params.Name, legacyHiddenPrefix) {
		return Account{}, fmt.Errorf("account name uses reserved prefix %q", legacyHiddenPrefix)
	}
	if params.Username == "" {
		return Account{}, fmt.Errorf("username is required")
	}
	switch params.AuthType {
	case "basic", "bearer", "oauth2":
	default:
		return Account{}, fmt.Errorf("invalid auth type %q", params.AuthType)
	}
	// OAuth2 credentials use Google's token-refresh path exclusively, so
	// accepting a non-Google server would store a refresh token that only
	// Google can validate and silently misroute discovery. Gate creation on
	// the configured host before any credential is written.
	if params.AuthType == "oauth2" && !caldav.IsGoogleCalendarEndpoint(serverURL) {
		return Account{}, fmt.Errorf("oauth2 accounts are only supported for Google Calendar")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("begin account create: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	row, err := qtx.CreateAccount(ctx, storage.CreateAccountParams{
		Name: params.Name, ServerUrl: serverURL, AuthType: params.AuthType, Username: params.Username,
	})
	if err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	if err := qtx.AdvanceCurrentCredentialAccountWatermark(ctx, row.ID); err != nil {
		return Account{}, fmt.Errorf("advance credential account watermark: %w", err)
	}

	cred.AccountID = row.ID
	cred.Username = params.Username
	cred.AccountFingerprint = auth.AccountFingerprint(serverURL, params.AuthType, params.Username)
	if err := store.Set(cred); err != nil {
		return Account{}, fmt.Errorf("store account credentials: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if deleteErr := store.Delete(row.ID); deleteErr != nil {
			return Account{}, fmt.Errorf("commit account create: %w (delete credentials: %w)", err, deleteErr)
		}
		return Account{}, fmt.Errorf("commit account create: %w", err)
	}
	return fromStorage(row), nil
}

// LoadCredential reads the single credential shared by every calendar in an
// account while holding the account lifecycle lock.
func (s *Service) LoadCredential(ctx context.Context, accountID int64, store auth.CredentialStore) (auth.Credential, error) {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return auth.Credential{}, fmt.Errorf("lock account credential read: %w", err)
	}
	defer release()
	return s.loadCredential(ctx, accountID, store)
}

// LoadCredentialForCalendar reads the account credential under the account
// lifecycle lock and additionally verifies calendar ownership for callers
// whose operation originates from one calendar.
func (s *Service) LoadCredentialForCalendar(ctx context.Context, calendarID, accountID int64, store auth.CredentialStore) (auth.Credential, error) {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return auth.Credential{}, fmt.Errorf("lock account credential read: %w", err)
	}
	defer release()
	calendar, err := s.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return auth.Credential{}, fmt.Errorf("get calendar: %w", err)
	}
	if calendar.AccountID == nil || *calendar.AccountID != accountID {
		return auth.Credential{}, fmt.Errorf("calendar is no longer linked to account %d", accountID)
	}
	return s.loadCredential(ctx, accountID, store)
}

// loadCredential reads a credential while its caller holds the account
// lifecycle lock. Keeping lookup here prevents account- and calendar-originated
// reads from drifting without recursively acquiring the same lock.
func (s *Service) loadCredential(ctx context.Context, accountID int64, store auth.CredentialStore) (auth.Credential, error) {
	account, err := s.Get(ctx, accountID)
	if err != nil {
		return auth.Credential{}, fmt.Errorf("get account: %w", err)
	}
	cred, err := store.Get(accountID, account.CredentialFingerprint())
	if err != nil {
		return auth.Credential{}, fmt.Errorf("get account credentials: %w", err)
	}
	seedCredentialIdentity(&cred, account)
	return cred, nil
}

// seedCredentialIdentity stamps a credential with the account's connection
// identity: owner ID, fingerprint, and the account username when the
// credential carries none.
func seedCredentialIdentity(cred *auth.Credential, account Account) {
	cred.AccountID = account.ID
	cred.AccountFingerprint = account.CredentialFingerprint()
	if cred.Username == "" {
		cred.Username = account.Username
	}
}

// StoreCredentialForCalendar replaces a credential only if the calendar and
// account connection identity still match the state that launched reauth.
func (s *Service) StoreCredentialForCalendar(ctx context.Context, calendarID, accountID int64, expectedFingerprint string, cred auth.Credential, store auth.CredentialStore) error {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return fmt.Errorf("lock account credential update: %w", err)
	}
	defer release()
	calendar, err := s.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("get calendar: %w", err)
	}
	if calendar.AccountID == nil || *calendar.AccountID != accountID {
		return fmt.Errorf("calendar is no longer linked to account %d", accountID)
	}
	return s.storeCredentialLocked(ctx, accountID, expectedFingerprint, cred, store)
}

// StoreCredential replaces an account credential only while the account still
// has the connection identity that initiated the update.
func (s *Service) StoreCredential(ctx context.Context, accountID int64, expectedFingerprint string, cred auth.Credential, store auth.CredentialStore) error {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return fmt.Errorf("lock account credential update: %w", err)
	}
	defer release()
	return s.storeCredentialLocked(ctx, accountID, expectedFingerprint, cred, store)
}

// storeCredentialLocked is the fingerprint-checked credential replacement
// shared by the account- and calendar-scoped stores; the caller must hold the
// account lifecycle lock.
func (s *Service) storeCredentialLocked(ctx context.Context, accountID int64, expectedFingerprint string, cred auth.Credential, store auth.CredentialStore) error {
	account, err := s.Get(ctx, accountID)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}
	if expectedFingerprint != "" && account.CredentialFingerprint() != expectedFingerprint {
		return auth.ErrCredentialIdentityMismatch
	}
	seedCredentialIdentity(&cred, account)
	if err := store.Set(cred); err != nil {
		return fmt.Errorf("store account credentials: %w", err)
	}
	return nil
}

// Discover retrieves a complete remote inventory and reconciles metadata for
// already-imported calendars. Missing flags change only after the remote
// discovery succeeds, so transient and partial failures preserve local state.
func (s *Service) Discover(ctx context.Context, accountID int64, store auth.CredentialStore) (Discovery, error) {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("lock account discovery: %w", err)
	}
	defer release()
	return s.discoverLocked(ctx, accountID, store)
}

// DiscoverWithCredential replaces an existing account's credential and runs a
// complete discovery under the same lifecycle lock. A failed discovery restores
// the previous credential so reconnecting with a typo cannot break working sync.
func (s *Service) DiscoverWithCredential(ctx context.Context, accountID int64, replacement auth.Credential, store auth.CredentialStore) (Discovery, error) {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("lock account credential discovery: %w", err)
	}
	defer release()

	configured, err := s.Get(ctx, accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("get account: %w", err)
	}
	fingerprint := configured.CredentialFingerprint()
	previous, err := store.Get(accountID, fingerprint)
	if err != nil {
		return Discovery{}, fmt.Errorf("get previous account credentials: %w", err)
	}
	seedCredentialIdentity(&replacement, configured)
	if replacement.RefreshToken == "" {
		replacement.RefreshToken = previous.RefreshToken
	}
	if err := store.Set(replacement); err != nil {
		return Discovery{}, fmt.Errorf("store replacement account credentials: %w", err)
	}
	discovery, err := s.discoverLocked(ctx, accountID, store)
	if err == nil {
		return discovery, nil
	}
	if restoreErr := store.Set(previous); restoreErr != nil {
		return Discovery{}, fmt.Errorf("%w (restore previous account credentials: %w)", err, restoreErr)
	}
	return Discovery{}, err
}

// discoverLocked performs discovery and reconciliation while the caller holds
// the account lifecycle lock.
func (s *Service) discoverLocked(ctx context.Context, accountID int64, store auth.CredentialStore) (Discovery, error) {
	account, err := s.Get(ctx, accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("get account: %w", err)
	}
	cred, err := store.Get(accountID, account.CredentialFingerprint())
	if err != nil {
		return Discovery{}, fmt.Errorf("get account credentials: %w", err)
	}
	found, err := s.discover(ctx, account, cred, func(updated auth.Credential) error {
		updated.AccountID = accountID
		updated.AccountFingerprint = account.CredentialFingerprint()
		return store.Set(updated)
	})
	if err != nil {
		return Discovery{}, fmt.Errorf("discover calendars: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Discovery{}, fmt.Errorf("begin discovery reconciliation: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	existingRows, err := qtx.ListCalendarsByAccount(ctx, &accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("list account calendars: %w", err)
	}
	existingByURL := make(map[string]storage.Calendar, len(existingRows))
	for _, row := range existingRows {
		existingByURL[remoteIdentityKey(storage.NullableToString(row.RemoteUrl), account.ServerURL)] = row
	}
	if err := qtx.MarkAccountCalendarsMissing(ctx, &accountID); err != nil {
		return Discovery{}, fmt.Errorf("mark account calendars missing: %w", err)
	}

	calendars := make([]DiscoveredCalendar, 0, len(found))
	seen := make(map[string]struct{}, len(found))
	for _, remote := range found {
		key := remoteIdentityKey(remote.Path, account.ServerURL)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		remote.Name = remoteCalendarName(remote)
		remote.Access = normalizedAccess(remote.Access)
		remote.SupportedComponentSet = normalizedComponents(remote.SupportedComponentSet)
		item := DiscoveredCalendar{RemoteCalendar: remote, Importable: supportsChroncal(remote.SupportedComponentSet)}
		if local, ok := existingByURL[key]; ok {
			item.Imported = true
			item.CalendarID = local.ID
			if err := qtx.AdoptCalendarRemoteName(ctx, storage.AdoptCalendarRemoteNameParams{
				Name: remote.Name,
				ID:   local.ID,
			}); err != nil {
				return Discovery{}, fmt.Errorf("adopt discovered calendar name %q: %w", remote.Name, err)
			}
			if _, err := qtx.UpdateCalendarDiscovery(ctx, storage.UpdateCalendarDiscoveryParams{
				RemoteName:       remote.Name,
				RemoteColor:      remote.Color,
				RemoteAccess:     string(remote.Access),
				RemoteComponents: strings.Join(remote.SupportedComponentSet, ","),
				ID:               local.ID,
			}); err != nil {
				return Discovery{}, fmt.Errorf("update discovered calendar %q: %w", remote.Name, err)
			}
		}
		calendars = append(calendars, item)
	}
	for _, local := range existingRows {
		path := storage.NullableToString(local.RemoteUrl)
		if _, found := seen[remoteIdentityKey(path, account.ServerURL)]; found {
			continue
		}
		name := strings.TrimSpace(local.RemoteName)
		if name == "" {
			name = local.Name
		}
		color := storage.NullableToString(local.RemoteColor)
		if color == "" {
			color = local.Color
		}
		components := normalizedComponents(strings.Split(local.RemoteComponents, ","))
		calendars = append(calendars, DiscoveredCalendar{
			RemoteCalendar: caldav.RemoteCalendar{
				Path:                  path,
				Name:                  name,
				Description:           storage.NullableToString(local.Description),
				Color:                 color,
				Access:                normalizedAccess(caldav.CalendarAccess(local.RemoteAccess)),
				SupportedComponentSet: components,
			},
			CalendarID: local.ID,
			Imported:   true,
			Importable: supportsChroncal(components),
			Missing:    true,
		})
	}
	if err := tx.Commit(); err != nil {
		return Discovery{}, fmt.Errorf("commit discovery reconciliation: %w", err)
	}
	return Discovery{Account: account, Calendars: calendars}, nil
}

// Import creates local calendars for the selected paths. Repeating an import
// is idempotent and returns the already-linked IDs instead of duplicating rows.
func (s *Service) Import(ctx context.Context, discovery Discovery, selectedPaths []string) (ImportResult, error) {
	if discovery.Account.ID == 0 {
		return ImportResult{}, fmt.Errorf("discovery account is required")
	}
	release, err := synclock.Account(ctx, s.db, discovery.Account.ID)
	if err != nil {
		return ImportResult{}, fmt.Errorf("lock account import: %w", err)
	}
	defer release()

	if _, err := s.q.GetAccount(ctx, discovery.Account.ID); err != nil {
		return ImportResult{}, fmt.Errorf("get discovery account: %w", err)
	}
	byPath := make(map[string]DiscoveredCalendar, len(discovery.Calendars))
	for _, item := range discovery.Calendars {
		byPath[item.Path] = item
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("begin calendar import: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	existingRows, err := qtx.ListCalendarsByAccount(ctx, &discovery.Account.ID)
	if err != nil {
		return ImportResult{}, fmt.Errorf("list existing account calendars: %w", err)
	}
	existingByURL := make(map[string]int64, len(existingRows))
	for _, row := range existingRows {
		existingByURL[remoteIdentityKey(storage.NullableToString(row.RemoteUrl), discovery.Account.ServerURL)] = row.ID
	}
	// calendars.name is UNIQUE across the whole table, so a local display name
	// must not collide with any existing calendar — local, linked to another
	// account, or already created in this import batch. Seed the reserved set
	// from every existing name and append a counter suffix on collision while
	// keeping the pristine remote name in remote_name.
	allCalendars, err := qtx.ListCalendars(ctx)
	if err != nil {
		return ImportResult{}, fmt.Errorf("list existing calendar names: %w", err)
	}
	taken := make(map[string]struct{}, len(allCalendars))
	for _, row := range allCalendars {
		taken[row.Name] = struct{}{}
	}

	result := ImportResult{}
	selected := make(map[string]struct{}, len(selectedPaths))
	for _, path := range selectedPaths {
		if _, duplicate := selected[path]; duplicate {
			continue
		}
		selected[path] = struct{}{}
		item, ok := byPath[path]
		if !ok {
			return ImportResult{}, fmt.Errorf("calendar %q was not part of this discovery", path)
		}
		if !item.Importable {
			return ImportResult{}, fmt.Errorf("calendar %q has no supported event, todo, or journal components", item.Name)
		}
		if id, ok := existingByURL[remoteIdentityKey(path, discovery.Account.ServerURL)]; ok {
			result.ExistingIDs = append(result.ExistingIDs, id)
			continue
		}

		remoteName := remoteCalendarName(item.RemoteCalendar)
		color := item.Color
		if color == "" {
			color = defaultCalendarColor
		}
		accountID := discovery.Account.ID
		row, err := qtx.CreateDiscoveredCalendar(ctx, storage.CreateDiscoveredCalendarParams{
			Name:             uniqueLocalName(remoteName, taken),
			Color:            color,
			Description:      storage.StringToNullable(item.Description),
			AccountID:        &accountID,
			RemoteUrl:        storage.StringToNullable(path),
			RemoteColor:      storage.StringToNullable(item.Color),
			RemoteName:       remoteName,
			RemoteAccess:     string(normalizedAccess(item.Access)),
			RemoteComponents: strings.Join(normalizedComponents(item.SupportedComponentSet), ","),
			OwnerEmail:       discovery.Account.Username,
		})
		if err != nil {
			return ImportResult{}, fmt.Errorf("import calendar %q: %w", item.Name, err)
		}
		taken[row.Name] = struct{}{}
		existingByURL[remoteIdentityKey(path, discovery.Account.ServerURL)] = row.ID
		result.CreatedIDs = append(result.CreatedIDs, row.ID)
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{}, fmt.Errorf("commit calendar import: %w", err)
	}
	return result, nil
}

// ReconcileSelection atomically changes one account's local calendar set to
// match the checked paths from a complete discovery. Remote collections are
// never deleted. If no paths remain, the now-empty account and credential are
// removed as part of the same operation.
func (s *Service) ReconcileSelection(
	ctx context.Context,
	discovery Discovery,
	params SelectionParams,
	store auth.CredentialStore,
) (SelectionResult, error) {
	if discovery.Account.ID == 0 {
		return SelectionResult{}, fmt.Errorf("discovery account is required")
	}
	release, err := synclock.Account(ctx, s.db, discovery.Account.ID)
	if err != nil {
		return SelectionResult{}, fmt.Errorf("lock account calendar selection: %w", err)
	}
	defer release()

	configured, err := s.Get(ctx, discovery.Account.ID)
	if err != nil {
		return SelectionResult{}, fmt.Errorf("get discovery account: %w", err)
	}
	if configured.CredentialFingerprint() != discovery.Account.CredentialFingerprint() {
		return SelectionResult{}, fmt.Errorf("%w: account connection changed", ErrSelectionStale)
	}

	discoveredByKey := make(map[string]DiscoveredCalendar, len(discovery.Calendars))
	for _, item := range discovery.Calendars {
		discoveredByKey[remoteIdentityKey(item.Path, discovery.Account.ServerURL)] = item
	}
	selected := make(map[string]struct{}, len(params.SelectedPaths))
	selectedKeys := make([]string, 0, len(params.SelectedPaths))
	for _, path := range params.SelectedPaths {
		key := remoteIdentityKey(path, discovery.Account.ServerURL)
		if _, duplicate := selected[key]; duplicate {
			continue
		}
		item, ok := discoveredByKey[key]
		if !ok {
			return SelectionResult{}, fmt.Errorf("calendar %q was not part of this discovery", path)
		}
		if !item.Imported && (!item.Importable || item.Missing) {
			return SelectionResult{}, fmt.Errorf("calendar %q cannot be added", item.Name)
		}
		selected[key] = struct{}{}
		selectedKeys = append(selectedKeys, key)
	}

	removeAccount := len(selectedKeys) == 0
	var (
		previous    auth.Credential
		hasPrevious bool
	)
	if removeAccount {
		if store == nil {
			return SelectionResult{}, fmt.Errorf("credential store is required to remove an empty account")
		}
		previous, err = store.Get(
			discovery.Account.ID,
			discovery.Account.CredentialFingerprint(),
		)
		if err != nil &&
			!auth.IsCredentialNotFound(err) &&
			!errors.Is(err, auth.ErrCredentialIdentityMismatch) {
			return SelectionResult{}, fmt.Errorf("read account credentials before removal: %w", err)
		}
		hasPrevious = err == nil
	}
	restorePrevious := func(cause error, operation string) error {
		if !hasPrevious {
			return fmt.Errorf("%s: %w", operation, cause)
		}
		if restoreErr := store.Set(previous); restoreErr != nil {
			return fmt.Errorf("%s: %w (restore credentials: %w)", operation, cause, restoreErr)
		}
		return fmt.Errorf("%s: %w", operation, cause)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SelectionResult{}, fmt.Errorf("begin account calendar reconciliation: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	existingRows, err := qtx.ListCalendarsByAccount(ctx, &discovery.Account.ID)
	if err != nil {
		return SelectionResult{}, fmt.Errorf("list account calendars: %w", err)
	}
	existingByKey := make(map[string]storage.Calendar, len(existingRows))
	for _, row := range existingRows {
		key := remoteIdentityKey(storage.NullableToString(row.RemoteUrl), discovery.Account.ServerURL)
		item, ok := discoveredByKey[key]
		if !ok || !item.Imported || item.CalendarID != row.ID {
			return SelectionResult{}, fmt.Errorf("%w: imported calendars changed", ErrSelectionStale)
		}
		existingByKey[key] = row
	}
	for key, item := range discoveredByKey {
		if !item.Imported {
			continue
		}
		row, ok := existingByKey[key]
		if !ok || row.ID != item.CalendarID {
			return SelectionResult{}, fmt.Errorf("%w: imported calendars changed", ErrSelectionStale)
		}
	}

	removedRows := make([]storage.Calendar, 0, len(existingRows))
	removedIDs := make(map[int64]struct{}, len(existingRows))
	finalByKey := make(map[string]int64, len(selectedKeys))
	for key, row := range existingByKey {
		if _, keep := selected[key]; keep {
			finalByKey[key] = row.ID
			continue
		}
		removedRows = append(removedRows, row)
		removedIDs[row.ID] = struct{}{}
	}
	addedCount := 0
	for _, key := range selectedKeys {
		if _, exists := existingByKey[key]; !exists {
			addedCount++
		}
	}

	allCalendars, err := qtx.ListCalendars(ctx)
	if err != nil {
		return SelectionResult{}, fmt.Errorf("list existing calendars: %w", err)
	}
	if len(allCalendars)-len(removedRows)+addedCount < 1 {
		return SelectionResult{}, calendar.ErrLastCalendar
	}

	var removedDefault bool
	for _, row := range removedRows {
		if row.IsDefault == 1 {
			removedDefault = true
			break
		}
	}
	if removedDefault && params.NewDefaultID == 0 && strings.TrimSpace(params.NewDefaultPath) == "" {
		return SelectionResult{}, calendar.ErrDefaultCalendarRequiresPromotion
	}
	if removedDefault && params.NewDefaultID != 0 && strings.TrimSpace(params.NewDefaultPath) != "" {
		return SelectionResult{}, calendar.ErrInvalidPromotionTarget
	}

	replacementID := params.NewDefaultID
	if removedDefault && replacementID != 0 {
		if _, removed := removedIDs[replacementID]; removed {
			return SelectionResult{}, calendar.ErrInvalidPromotionTarget
		}
		if _, err := qtx.GetCalendar(ctx, replacementID); err != nil {
			return SelectionResult{}, calendar.ErrInvalidPromotionTarget
		}
	}

	taken := make(map[string]struct{}, len(allCalendars))
	for _, row := range allCalendars {
		if _, removed := removedIDs[row.ID]; !removed {
			taken[row.Name] = struct{}{}
		}
	}

	result := SelectionResult{RemovedIDs: make([]int64, 0, len(removedRows))}
	for _, row := range removedRows {
		if err := qtx.DeleteCalendar(ctx, row.ID); err != nil {
			return SelectionResult{}, fmt.Errorf("remove calendar %q: %w", row.Name, err)
		}
		result.RemovedIDs = append(result.RemovedIDs, row.ID)
	}

	for _, key := range selectedKeys {
		if _, exists := finalByKey[key]; exists {
			continue
		}
		item := discoveredByKey[key]
		remoteName := remoteCalendarName(item.RemoteCalendar)
		color := item.Color
		if color == "" {
			color = defaultCalendarColor
		}
		accountID := discovery.Account.ID
		row, err := qtx.CreateDiscoveredCalendar(ctx, storage.CreateDiscoveredCalendarParams{
			Name:             uniqueLocalName(remoteName, taken),
			Color:            color,
			Description:      storage.StringToNullable(item.Description),
			AccountID:        &accountID,
			RemoteUrl:        storage.StringToNullable(item.Path),
			RemoteColor:      storage.StringToNullable(item.Color),
			RemoteName:       remoteName,
			RemoteAccess:     string(normalizedAccess(item.Access)),
			RemoteComponents: strings.Join(normalizedComponents(item.SupportedComponentSet), ","),
			OwnerEmail:       discovery.Account.Username,
		})
		if err != nil {
			return SelectionResult{}, fmt.Errorf("add calendar %q: %w", item.Name, err)
		}
		taken[row.Name] = struct{}{}
		finalByKey[key] = row.ID
		result.CreatedIDs = append(result.CreatedIDs, row.ID)
	}

	if removedDefault {
		if path := strings.TrimSpace(params.NewDefaultPath); path != "" {
			key := remoteIdentityKey(path, discovery.Account.ServerURL)
			var ok bool
			replacementID, ok = finalByKey[key]
			if !ok {
				return SelectionResult{}, calendar.ErrInvalidPromotionTarget
			}
		}
		if replacementID == 0 {
			return SelectionResult{}, calendar.ErrInvalidPromotionTarget
		}
		if err := qtx.ClearDefaultCalendar(ctx); err != nil {
			return SelectionResult{}, fmt.Errorf("clear default calendar: %w", err)
		}
		if err := qtx.SetCalendarAsDefault(ctx, replacementID); err != nil {
			return SelectionResult{}, fmt.Errorf("set replacement default: %w", err)
		}
	}

	if removeAccount {
		if err := qtx.DeleteAccount(ctx, discovery.Account.ID); err != nil {
			return SelectionResult{}, fmt.Errorf("remove empty account: %w", err)
		}
		if err := store.Delete(discovery.Account.ID); err != nil {
			return SelectionResult{}, restorePrevious(err, "delete empty account credentials")
		}
		result.AccountRemoved = true
	}

	if err := tx.Commit(); err != nil {
		if removeAccount {
			return SelectionResult{}, restorePrevious(err, "commit account calendar reconciliation")
		}
		return SelectionResult{}, fmt.Errorf("commit account calendar reconciliation: %w", err)
	}
	return result, nil
}

// RemoveWithCalendars deletes an account and every local calendar attached to
// it. It never contacts the remote server. Delete has a different contract:
// that method preserves the calendars as disconnected local rows.
func (s *Service) RemoveWithCalendars(
	ctx context.Context,
	accountID int64,
	params RemoveParams,
	store auth.CredentialStore,
) (RemoveResult, error) {
	if store == nil {
		return RemoveResult{}, fmt.Errorf("credential store is required")
	}
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("lock account removal: %w", err)
	}
	defer release()

	configured, err := s.Get(ctx, accountID)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("get account: %w", err)
	}
	previous, previousErr := store.Get(accountID, configured.CredentialFingerprint())
	if previousErr != nil &&
		!auth.IsCredentialNotFound(previousErr) &&
		!errors.Is(previousErr, auth.ErrCredentialIdentityMismatch) {
		return RemoveResult{}, fmt.Errorf("read account credentials before removal: %w", previousErr)
	}
	hasPrevious := previousErr == nil
	restorePrevious := func(cause error, operation string) error {
		if !hasPrevious {
			return fmt.Errorf("%s: %w", operation, cause)
		}
		if restoreErr := store.Set(previous); restoreErr != nil {
			return fmt.Errorf("%s: %w (restore credentials: %w)", operation, cause, restoreErr)
		}
		return fmt.Errorf("%s: %w", operation, cause)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("begin account removal: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	linked, err := qtx.ListCalendarsByAccount(ctx, &accountID)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("list account calendars: %w", err)
	}
	all, err := qtx.ListCalendars(ctx)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("list calendars: %w", err)
	}
	if len(all)-len(linked) < 1 {
		return RemoveResult{}, calendar.ErrLastCalendar
	}

	removedIDs := make(map[int64]struct{}, len(linked))
	removedDefault := false
	result := RemoveResult{RemovedIDs: make([]int64, 0, len(linked))}
	for _, row := range linked {
		removedIDs[row.ID] = struct{}{}
		result.RemovedIDs = append(result.RemovedIDs, row.ID)
		removedDefault = removedDefault || row.IsDefault == 1
	}

	if removedDefault {
		if params.NewDefaultID == 0 {
			return RemoveResult{}, calendar.ErrDefaultCalendarRequiresPromotion
		}
		if _, removed := removedIDs[params.NewDefaultID]; removed {
			return RemoveResult{}, calendar.ErrInvalidPromotionTarget
		}
		if _, err := qtx.GetCalendar(ctx, params.NewDefaultID); err != nil {
			return RemoveResult{}, calendar.ErrInvalidPromotionTarget
		}
	} else if params.NewDefaultID != 0 {
		return RemoveResult{}, calendar.ErrInvalidPromotionTarget
	}

	for _, row := range linked {
		if err := qtx.DeleteCalendar(ctx, row.ID); err != nil {
			return RemoveResult{}, fmt.Errorf("remove calendar %q: %w", row.Name, err)
		}
	}
	if removedDefault {
		if err := qtx.ClearDefaultCalendar(ctx); err != nil {
			return RemoveResult{}, fmt.Errorf("clear default calendar: %w", err)
		}
		if err := qtx.SetCalendarAsDefault(ctx, params.NewDefaultID); err != nil {
			return RemoveResult{}, fmt.Errorf("set replacement default: %w", err)
		}
	}
	if err := qtx.DeleteAccount(ctx, accountID); err != nil {
		return RemoveResult{}, fmt.Errorf("delete account: %w", err)
	}
	if err := store.Delete(accountID); err != nil {
		return RemoveResult{}, restorePrevious(err, "delete account credentials")
	}
	if err := tx.Commit(); err != nil {
		return RemoveResult{}, restorePrevious(err, "commit account removal")
	}
	return result, nil
}

// Delete removes an account and its credential while preserving every local
// calendar and its downloaded data as a disconnected local calendar.
func (s *Service) Delete(ctx context.Context, accountID int64, store auth.CredentialStore) error {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return fmt.Errorf("lock account delete: %w", err)
	}
	defer release()

	linked, err := s.q.ListCalendarsByAccount(ctx, &accountID)
	if err != nil {
		return fmt.Errorf("list account calendars: %w", err)
	}
	calendarIDs := make([]int64, len(linked))
	for i, cal := range linked {
		calendarIDs[i] = cal.ID
	}
	account, err := s.q.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}
	previous, previousErr := store.Get(accountID, auth.AccountFingerprint(
		account.ServerUrl, account.AuthType, account.Username,
	))
	if previousErr != nil &&
		!auth.IsCredentialNotFound(previousErr) &&
		!errors.Is(previousErr, auth.ErrCredentialIdentityMismatch) {
		return fmt.Errorf("read account credentials before delete: %w", previousErr)
	}
	hasPrevious := previousErr == nil
	restorePrevious := func(cause error, operation string) error {
		if !hasPrevious {
			return fmt.Errorf("%s: %w", operation, cause)
		}
		if restoreErr := store.Set(previous); restoreErr != nil {
			return fmt.Errorf("%s: %w (restore credentials: %w)", operation, cause, restoreErr)
		}
		return fmt.Errorf("%s: %w", operation, cause)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account delete: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	for _, calendarID := range calendarIDs {
		if err := qtx.DetachSyncResourcesByCalendar(ctx, calendarID); err != nil {
			return fmt.Errorf("detach calendar sync resources: %w", err)
		}
		if err := qtx.DeleteTombstonesByCalendar(ctx, calendarID); err != nil {
			return fmt.Errorf("delete calendar tombstones: %w", err)
		}
		if err := qtx.DeleteSyncConflictsByCalendar(ctx, calendarID); err != nil {
			return fmt.Errorf("delete calendar conflicts: %w", err)
		}
	}
	if err := qtx.ClearRemoteLinksByAccount(ctx, &accountID); err != nil {
		return fmt.Errorf("disconnect account calendars: %w", err)
	}
	if err := qtx.DeleteAccount(ctx, accountID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if err := store.Delete(accountID); err != nil {
		return restorePrevious(err, "delete account credentials")
	}
	if err := tx.Commit(); err != nil {
		return restorePrevious(err, "commit account delete")
	}
	return nil
}

func discoverRemoteCalendars(ctx context.Context, account Account, cred auth.Credential, persist func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
	if caldav.IsGoogleCalendarEndpoint(account.ServerURL) && cred.AccessToken != "" {
		return caldav.DiscoverGoogleCalendars(ctx, cred, persist)
	}
	client, err := caldav.NewClientFromCredential(account.ServerURL, cred, persist)
	if err != nil {
		return nil, err
	}
	return client.DiscoverCalendars(ctx)
}

func validateServerURL(raw string, allowInsecure bool) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("server URL must include scheme and host")
	}
	if parsed.Scheme != "https" && (!allowInsecure || parsed.Scheme != "http") {
		return "", fmt.Errorf("server URL must use HTTPS; allow-insecure is required for HTTP")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("server URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("server URL must not include query or fragment")
	}
	return parsed.String(), nil
}

func normalizedAccess(access caldav.CalendarAccess) caldav.CalendarAccess {
	switch access {
	case caldav.CalendarAccessRead, caldav.CalendarAccessWrite, caldav.CalendarAccessOwner:
		return access
	default:
		return caldav.CalendarAccessUnknown
	}
}

func normalizedComponents(components []string) []string {
	if len(components) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(components))
	for _, component := range components {
		component = strings.ToUpper(strings.TrimSpace(component))
		if component != "" {
			set[component] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for component := range set {
		out = append(out, component)
	}
	slices.Sort(out)
	return out
}

func supportsChroncal(components []string) bool {
	if len(components) == 0 {
		return true
	}
	for _, component := range components {
		switch component {
		case "VEVENT", "VTODO", "VJOURNAL":
			return true
		}
	}
	return false
}

func remoteCalendarName(remote caldav.RemoteCalendar) string {
	if name := strings.TrimSpace(remote.Name); name != "" {
		return name
	}
	path := strings.Trim(remote.Path, "/")
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	if path == "" {
		return "Remote calendar"
	}
	return path
}

// remoteIdentityKey collapses equivalent remote collection identities into one
// key so a legacy absolute link (e.g. "https://host/cal/work") and the
// server-relative path discovery returns ("/cal/work/") reconcile to the same
// row instead of duplicating it. Relative references resolve against the
// account server URL; absolute references are kept as-is. Both are normalized
// to a trailing-slash-free form.
func remoteIdentityKey(raw, serverURL string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	if base, baseErr := url.Parse(strings.TrimSpace(serverURL)); baseErr == nil && base.IsAbs() && !ref.IsAbs() {
		ref = base.ResolveReference(ref)
	}
	ref.Path = strings.TrimRight(ref.Path, "/")
	ref.RawPath = ""
	return ref.String()
}

// uniqueLocalName returns base when it is free, otherwise appends " (n)" until
// it finds a name not already in taken. The caller records the chosen name in
// taken so a single import batch with several same-named collections produces
// distinct local names ("Work", "Work (2)", ...).
func uniqueLocalName(base string, taken map[string]struct{}) string {
	if _, exists := taken[base]; !exists {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)", base, n)
		if _, exists := taken[candidate]; !exists {
			return candidate
		}
	}
}

func fromStorage(row storage.Account) Account {
	return Account{
		ID:           row.ID,
		Name:         row.Name,
		DisplayName:  UserFacingName(row.Name, row.Username, row.ID),
		ServerURL:    row.ServerUrl,
		AuthType:     row.AuthType,
		Username:     row.Username,
		DisplayOrder: row.DisplayOrder,
		CreatedAt:    timeutil.ParseDateTime(row.CreatedAt),
		UpdatedAt:    timeutil.ParseDateTime(row.UpdatedAt),
	}
}
