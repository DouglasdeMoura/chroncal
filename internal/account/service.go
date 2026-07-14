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
	"github.com/douglasdemoura/chroncal/internal/storage"
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
	if params.Username == "" {
		return Account{}, fmt.Errorf("username is required")
	}
	switch params.AuthType {
	case "basic", "bearer", "oauth2":
	default:
		return Account{}, fmt.Errorf("invalid auth type %q", params.AuthType)
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

	cred.AccountID = row.ID
	cred.Username = params.Username
	if err := store.Set(cred); err != nil {
		return Account{}, fmt.Errorf("store account credentials: %w", err)
	}
	if err := tx.Commit(); err != nil {
		_ = store.Delete(row.ID)
		return Account{}, fmt.Errorf("commit account create: %w", err)
	}
	return fromStorage(row), nil
}

// Discover retrieves a complete remote inventory and reconciles metadata for
// already-imported calendars. Missing flags change only after the remote
// discovery succeeds, so transient and partial failures preserve local state.
func (s *Service) Discover(ctx context.Context, accountID int64, store auth.CredentialStore) (Discovery, error) {
	account, err := s.Get(ctx, accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("get account: %w", err)
	}
	cred, err := store.Get(accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("get account credentials: %w", err)
	}
	found, err := s.discover(ctx, account, cred, func(updated auth.Credential) error {
		updated.AccountID = accountID
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
		existingByURL[storage.NullableToString(row.RemoteUrl)] = row
	}
	if err := qtx.MarkAccountCalendarsMissing(ctx, &accountID); err != nil {
		return Discovery{}, fmt.Errorf("mark account calendars missing: %w", err)
	}

	calendars := make([]DiscoveredCalendar, 0, len(found))
	seen := make(map[string]struct{}, len(found))
	for _, remote := range found {
		if _, duplicate := seen[remote.Path]; duplicate {
			continue
		}
		seen[remote.Path] = struct{}{}
		remote.Name = remoteCalendarName(remote)
		remote.Access = normalizedAccess(remote.Access)
		remote.SupportedComponentSet = normalizedComponents(remote.SupportedComponentSet)
		item := DiscoveredCalendar{RemoteCalendar: remote, Importable: supportsChroncal(remote.SupportedComponentSet)}
		if local, ok := existingByURL[remote.Path]; ok {
			item.Imported = true
			item.CalendarID = local.ID
			if _, err := qtx.UpdateCalendarDiscovery(ctx, storage.UpdateCalendarDiscoveryParams{
				RemoteName:       remote.Name,
				RemoteColor:      remote.Color,
				RemoteAccess:     string(remote.Access),
				RemoteComponents: strings.Join(remote.SupportedComponentSet, ","),
				AccountID:        &accountID,
				RemoteUrl:        storage.StringToNullable(remote.Path),
			}); err != nil {
				return Discovery{}, fmt.Errorf("update discovered calendar %q: %w", remote.Name, err)
			}
		}
		calendars = append(calendars, item)
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
		existingByURL[storage.NullableToString(row.RemoteUrl)] = row.ID
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
		if id, ok := existingByURL[path]; ok {
			result.ExistingIDs = append(result.ExistingIDs, id)
			continue
		}

		color := item.Color
		if color == "" {
			color = defaultCalendarColor
		}
		accountID := discovery.Account.ID
		row, err := qtx.CreateDiscoveredCalendar(ctx, storage.CreateDiscoveredCalendarParams{
			Name:             remoteCalendarName(item.RemoteCalendar),
			Color:            color,
			Description:      storage.StringToNullable(item.Description),
			AccountID:        &accountID,
			RemoteUrl:        storage.StringToNullable(path),
			RemoteColor:      storage.StringToNullable(item.Color),
			RemoteName:       remoteCalendarName(item.RemoteCalendar),
			RemoteAccess:     string(normalizedAccess(item.Access)),
			RemoteComponents: strings.Join(normalizedComponents(item.SupportedComponentSet), ","),
		})
		if err != nil {
			return ImportResult{}, fmt.Errorf("import calendar %q: %w", item.Name, err)
		}
		existingByURL[path] = row.ID
		result.CreatedIDs = append(result.CreatedIDs, row.ID)
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{}, fmt.Errorf("commit calendar import: %w", err)
	}
	return result, nil
}

// Delete removes an account and its credential while preserving every local
// calendar and its downloaded data as a disconnected local calendar.
func (s *Service) Delete(ctx context.Context, accountID int64, store auth.CredentialStore) error {
	if _, err := s.q.GetAccount(ctx, accountID); err != nil {
		return fmt.Errorf("get account: %w", err)
	}
	previous, previousErr := store.Get(accountID)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account delete: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.ClearRemoteLinksByAccount(ctx, &accountID); err != nil {
		return fmt.Errorf("disconnect account calendars: %w", err)
	}
	if err := qtx.DeleteAccount(ctx, accountID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if err := store.Delete(accountID); err != nil {
		return fmt.Errorf("delete account credentials: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if previousErr == nil {
			_ = store.Set(previous)
		}
		return fmt.Errorf("commit account delete: %w", err)
	}
	return nil
}

func discoverRemoteCalendars(ctx context.Context, account Account, cred auth.Credential, persist func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
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
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return "", fmt.Errorf("server URL must use HTTPS; allow-insecure is required for HTTP")
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

func fromStorage(row storage.Account) Account {
	return Account{
		ID:          row.ID,
		Name:        row.Name,
		DisplayName: UserFacingName(row.Name, row.Username, row.ID),
		ServerURL:   row.ServerUrl,
		AuthType:    row.AuthType,
		Username:    row.Username,
		CreatedAt:   timeutil.ParseDateTime(row.CreatedAt),
		UpdatedAt:   timeutil.ParseDateTime(row.UpdatedAt),
	}
}
