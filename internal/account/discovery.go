package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
)

// Discover retrieves a complete remote inventory and reconciles metadata for
// already-imported calendars. Missing flags change only after the remote
// discovery succeeds. Transient and partial failures then preserve local state.
func (s *Service) Discover(ctx context.Context, accountID int64, store auth.CredentialStore) (Discovery, error) {
	release, err := synclock.Account(ctx, s.db, accountID)
	if err != nil {
		return Discovery{}, fmt.Errorf("lock account discovery: %w", err)
	}
	defer release()
	return s.discoverLocked(ctx, accountID, store)
}

// DiscoverWithCredential replaces an account's credential and runs a
// complete discovery under the same lifecycle lock. A failed discovery restores
// the previous credential. A reconnect with a typo then cannot break live sync.
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
			if err := applyDiscoveredCalendarMetadata(ctx, qtx, item, local.ID); err != nil {
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

// applyDiscoveredCalendarMetadata writes live discovery color, name, access,
// and components onto a local calendar. AdoptCalendarRemoteName keeps a
// user-chosen display name. UpdateCalendarDiscovery is the one write of the
// remote_* mirrors and the color CASE rules.
func applyDiscoveredCalendarMetadata(ctx context.Context, qtx *storage.Queries, item DiscoveredCalendar, calendarID int64) error {
	remoteName := remoteCalendarName(item.RemoteCalendar)
	if err := qtx.AdoptCalendarRemoteName(ctx, storage.AdoptCalendarRemoteNameParams{
		Name: remoteName,
		ID:   calendarID,
	}); err != nil {
		return err
	}
	_, err := qtx.UpdateCalendarDiscovery(ctx, storage.UpdateCalendarDiscoveryParams{
		RemoteName:       remoteName,
		RemoteColor:      item.Color,
		RemoteAccess:     string(normalizedAccess(item.Access)),
		RemoteComponents: strings.Join(normalizedComponents(item.SupportedComponentSet), ","),
		ID:               calendarID,
	})
	return err
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
