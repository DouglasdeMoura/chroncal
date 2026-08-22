package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
)

// ErrSelectionStale means the account's imported calendar set changed after
// discovery. Apply of the old checklist could then remove an unseen calendar.
var ErrSelectionStale = errors.New("calendar selection is stale")

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
	var prior auth.PriorCredential
	if removeAccount {
		if store == nil {
			return SelectionResult{}, fmt.Errorf("credential store is required to remove an empty account")
		}
		prior, err = auth.CapturePriorCredential(store, discovery.Account.ID, discovery.Account.CredentialFingerprint())
		if err != nil {
			return SelectionResult{}, fmt.Errorf("read account credentials before removal: %w", err)
		}
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

	taken := make(map[string]struct{}, len(allCalendars))
	for _, row := range allCalendars {
		if _, removed := removedIDs[row.ID]; !removed {
			taken[row.Name] = struct{}{}
		}
	}
	unlinkedByURL := uniqueUnlinkedByRemoteIdentity(allCalendars, discovery.Account.ServerURL)

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
		if id, ok := unlinkedByURL[key]; ok {
			if err := relinkDiscoveredCalendar(ctx, qtx, discovery.Account, item, id); err != nil {
				return SelectionResult{}, fmt.Errorf("re-link calendar %q: %w", item.Name, err)
			}
			delete(unlinkedByURL, key)
			finalByKey[key] = id
			result.CreatedIDs = append(result.CreatedIDs, id)
			continue
		}
		row, err := createDiscoveredCalendarRow(ctx, qtx, discovery.Account, item, taken)
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
		if err := calendar.PromoteDefault(ctx, qtx, removedIDs, replacementID); err != nil {
			return SelectionResult{}, err
		}
	}

	if removeAccount {
		if err := qtx.DeleteAccount(ctx, discovery.Account.ID); err != nil {
			return SelectionResult{}, fmt.Errorf("remove empty account: %w", err)
		}
		if err := store.Delete(discovery.Account.ID); err != nil {
			return SelectionResult{}, prior.Restore(store, discovery.Account.ID, false, "delete empty account credentials", err)
		}
		result.AccountRemoved = true
	}

	if err := auth.CommitWithCredentialCompensation(tx, store, discovery.Account.ID, prior, false, "commit account calendar reconciliation"); err != nil {
		return SelectionResult{}, err
	}
	return result, nil
}
