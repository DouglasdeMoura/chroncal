package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
)

// Import creates local calendars for the selected paths. A repeat of an import
// is idempotent. It returns the already-linked IDs instead of duplicate rows.
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
	unlinkedByURL := uniqueUnlinkedByRemoteIdentity(allCalendars, discovery.Account.ServerURL)

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
		key := remoteIdentityKey(path, discovery.Account.ServerURL)
		if id, ok := existingByURL[key]; ok {
			result.ExistingIDs = append(result.ExistingIDs, id)
			continue
		}
		if id, ok := unlinkedByURL[key]; ok {
			if err := relinkDiscoveredCalendar(ctx, qtx, discovery.Account, item, id); err != nil {
				return ImportResult{}, fmt.Errorf("re-link calendar %q: %w", item.Name, err)
			}
			existingByURL[key] = id
			delete(unlinkedByURL, key)
			result.CreatedIDs = append(result.CreatedIDs, id)
			continue
		}

		row, err := createDiscoveredCalendarRow(ctx, qtx, discovery.Account, item, taken)
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

// uniqueUnlinkedByRemoteIdentity maps a remote identity to one unlinked local
// calendar. Duplicate keys are dropped so Import and ReconcileSelection create
// a fresh row. They must not pick an arbitrary snapshot.
func uniqueUnlinkedByRemoteIdentity(rows []storage.Calendar, serverURL string) map[string]int64 {
	byKey := make(map[string]int64)
	ambiguous := make(map[string]struct{})
	for _, row := range rows {
		if row.AccountID != nil {
			continue
		}
		key := remoteIdentityKey(storage.NullableToString(row.RemoteUrl), serverURL)
		if key == "" {
			continue
		}
		if _, taken := byKey[key]; taken {
			delete(byKey, key)
			ambiguous[key] = struct{}{}
			continue
		}
		if _, skip := ambiguous[key]; skip {
			continue
		}
		byKey[key] = row.ID
	}
	return byKey
}

// relinkDiscoveredCalendar attaches one unlinked local calendar to the account.
// It writes the live collection onto the row through applyDiscoveredCalendarMetadata.
func relinkDiscoveredCalendar(ctx context.Context, qtx *storage.Queries, acct Account, item DiscoveredCalendar, calendarID int64) error {
	accountID := acct.ID
	if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        calendarID,
		AccountID: &accountID,
		RemoteUrl: storage.StringToNullable(item.Path),
	}); err != nil {
		return err
	}
	if err := applyDiscoveredCalendarMetadata(ctx, qtx, item, calendarID); err != nil {
		return err
	}
	return qtx.UpdateCalendarOwnerEmail(ctx, storage.UpdateCalendarOwnerEmailParams{
		OwnerEmail: acct.Username,
		ID:         calendarID,
	})
}

// createDiscoveredCalendarRow creates the local calendar row for a discovered
// remote collection. It reserves a local name from taken that does not collide.
// The caller adds the returned row's name to taken when it creates in a loop.
// It is the single DiscoveredCalendar-to-row mapping shared by Import,
// ReconcileSelection, and calendar migration. Remote-metadata columns then stay
// in lockstep no matter which flow creates the row.
func createDiscoveredCalendarRow(
	ctx context.Context,
	qtx *storage.Queries,
	acct Account,
	item DiscoveredCalendar,
	taken map[string]struct{},
) (storage.Calendar, error) {
	remoteName := remoteCalendarName(item.RemoteCalendar)
	color := item.Color
	if color == "" {
		color = defaultCalendarColor
	}
	accountID := acct.ID
	return qtx.CreateDiscoveredCalendar(ctx, storage.CreateDiscoveredCalendarParams{
		Name:             uniqueLocalName(remoteName, taken),
		Color:            color,
		Description:      storage.StringToNullable(item.Description),
		AccountID:        &accountID,
		RemoteUrl:        storage.StringToNullable(item.Path),
		RemoteColor:      storage.StringToNullable(item.Color),
		RemoteName:       remoteName,
		RemoteAccess:     string(normalizedAccess(item.Access)),
		RemoteComponents: strings.Join(normalizedComponents(item.SupportedComponentSet), ","),
		OwnerEmail:       acct.Username,
	})
}
