package account

import (
	"context"
	"database/sql"
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

// Rename updates the account's human-facing description. It does not change its
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
// account while it holds the account lifecycle lock.
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
// lifecycle lock. Lookup stays here so account- and calendar-originated
// reads cannot drift without a recursive acquire of the same lock.
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
	prior, err := auth.CapturePriorCredential(store, accountID, configured.CredentialFingerprint())
	if err != nil {
		return RemoveResult{}, fmt.Errorf("read account credentials before removal: %w", err)
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
	} else if params.NewDefaultID != 0 {
		return RemoveResult{}, calendar.ErrInvalidPromotionTarget
	}

	for _, row := range linked {
		if err := qtx.DeleteCalendar(ctx, row.ID); err != nil {
			return RemoveResult{}, fmt.Errorf("remove calendar %q: %w", row.Name, err)
		}
	}
	if removedDefault {
		if err := calendar.PromoteDefault(ctx, qtx, removedIDs, params.NewDefaultID); err != nil {
			return RemoveResult{}, err
		}
	}
	if err := qtx.DeleteAccount(ctx, accountID); err != nil {
		return RemoveResult{}, fmt.Errorf("delete account: %w", err)
	}
	if err := store.Delete(accountID); err != nil {
		return RemoveResult{}, prior.Restore(store, accountID, false, "delete account credentials", err)
	}
	if err := auth.CommitWithCredentialCompensation(tx, store, accountID, prior, false, "commit account removal"); err != nil {
		return RemoveResult{}, err
	}
	return result, nil
}

// Delete removes an account and its credential. Every local calendar and its
// downloaded data stay as a disconnected local calendar.
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
	account, err := s.q.GetAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}
	prior, err := auth.CapturePriorCredential(store, accountID, auth.AccountFingerprint(
		account.ServerUrl, account.AuthType, account.Username,
	))
	if err != nil {
		return fmt.Errorf("read account credentials before delete: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account delete: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	// Keep hrefs, tombstones, and conflicts. Account remove is not a move to a
	// new server: a later add of the same origin should re-link these rows
	// instead of PUTting them as first-time creates. calendar connect still
	// detaches when the collection actually changes.
	// Persist an absolute remote_url so a different server with the same path
	// cannot steal the unlinked row.
	for _, cal := range linked {
		origin := remoteIdentityKey(storage.NullableToString(cal.RemoteUrl), account.ServerUrl)
		if origin == "" {
			continue
		}
		if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID:        cal.ID,
			AccountID: &accountID,
			RemoteUrl: storage.StringToNullable(origin),
		}); err != nil {
			return fmt.Errorf("preserve calendar origin: %w", err)
		}
	}
	if err := qtx.ClearRemoteLinksByAccount(ctx, &accountID); err != nil {
		return fmt.Errorf("disconnect account calendars: %w", err)
	}
	if err := qtx.DeleteAccount(ctx, accountID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if err := store.Delete(accountID); err != nil {
		return prior.Restore(store, accountID, false, "delete account credentials", err)
	}
	if err := auth.CommitWithCredentialCompensation(tx, store, accountID, prior, false, "commit account delete"); err != nil {
		return err
	}
	return nil
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
// key. A legacy absolute link (for example "https://host/cal/work") and the
// server-relative path discovery returns ("/cal/work/") then reconcile to the
// same row instead of a duplicate. Relative references resolve against the
// account server URL. Absolute references are kept as-is. Both are normalized
// to a form with no slash at the end.
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
