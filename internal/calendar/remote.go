package calendar

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
)

const hiddenAccountPrefix = "__calendar_"

// RemoteLink describes the CalDAV connection settings for a calendar.
// Callers are responsible for collecting any password or OAuth tokens into
// the accompanying auth.Credential before calling Connect.
type RemoteLink struct {
	RemoteURL     string
	Username      string
	AuthType      string // "basic", "bearer", "oauth2"
	AllowInsecure bool
	// RemoteColor is the Apple-style calendar-color advertised by the
	// remote collection (e.g. fetched via PROPFIND at link time). When
	// non-empty, Connect adopts it as the calendar's color so the UI shows
	// the same color the remote uses without waiting for the first sync.
	RemoteColor      string
	RemoteAccess     string
	RemoteComponents []string
}

func (s *Service) lockRemoteLifecycle(ctx context.Context, calendarID int64) (Calendar, func(), error) {
	for {
		before, err := s.Get(ctx, calendarID)
		if err != nil {
			return Calendar{}, nil, err
		}
		lockID := before.AccountID
		if lockID == 0 {
			// An unlinked calendar has no account key yet. Its negative calendar
			// ID gives concurrent processes a stable lock while Connect creates
			// and attaches the new hidden account.
			lockID = -calendarID
		}
		releaseAccount, err := synclock.Account(ctx, s.db, lockID)
		if err != nil {
			return Calendar{}, nil, fmt.Errorf("lock calendar account: %w", err)
		}

		calendarLock := synclock.Calendar(s.db, calendarID)
		calendarLock.Lock()
		after, err := s.Get(ctx, calendarID)
		if err != nil {
			calendarLock.Unlock()
			releaseAccount()
			return Calendar{}, nil, err
		}
		afterLockID := after.AccountID
		if afterLockID == 0 {
			afterLockID = -calendarID
		}
		if afterLockID != lockID {
			calendarLock.Unlock()
			releaseAccount()
			continue
		}
		return after, func() {
			calendarLock.Unlock()
			releaseAccount()
		}, nil
	}
}

// Connect links a calendar to a remote CalDAV URL and stores the credential.
// When the calendar is already linked to a hidden account, Connect updates
// that account in place; otherwise it creates a new hidden account.
func (s *Service) Connect(ctx context.Context, cal Calendar, link RemoteLink, cred auth.Credential, credStore auth.CredentialStore) error {
	lockedCal, release, err := s.lockRemoteLifecycle(ctx, cal.ID)
	if err != nil {
		return err
	}
	defer release()
	cal = lockedCal

	link.AuthType = NormalizeAuthType(link.AuthType)

	serverURL, err := DeriveServerURL(link.RemoteURL, link.AllowInsecure)
	if err != nil {
		return err
	}
	remoteChanged := cal.RemoteURL != "" && !sameRemoteCollection(cal.RemoteURL, link.RemoteURL)

	if cal.AccountID != 0 {
		existing, err := s.q.GetAccount(ctx, cal.AccountID)
		// Only sql.ErrNoRows (the account row is genuinely gone) may fall
		// through to the create-new path. A transient read failure must be
		// propagated; treating it as not-found would repoint the calendar to a
		// brand-new hidden account and orphan the old credential (issue #300).
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get account: %w", err)
		}
		if err == nil && strings.HasPrefix(existing.Name, hiddenAccountPrefix) {
			linked, listErr := s.q.ListCalendarsByAccount(ctx, &existing.ID)
			if listErr != nil {
				return fmt.Errorf("list hidden account calendars: %w", listErr)
			}
			if len(linked) != 1 {
				goto createAccount
			}
			cred.AccountID = existing.ID
			oldFingerprint := auth.AccountFingerprint(existing.ServerUrl, existing.AuthType, existing.Username)
			prevCred, prevErr := credStore.Get(existing.ID, oldFingerprint)
			if prevErr != nil &&
				!auth.IsCredentialNotFound(prevErr) &&
				!errors.Is(prevErr, auth.ErrCredentialIdentityMismatch) {
				return fmt.Errorf("read credentials before relink: %w", prevErr)
			}
			hasPrevCred := prevErr == nil
			cred.AccountFingerprint = auth.AccountFingerprint(serverURL, link.AuthType, link.Username)

			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin tx: %w", err)
			}
			defer tx.Rollback()
			qtx := s.q.WithTx(tx)
			if err := qtx.UpdateAccount(ctx, storage.UpdateAccountParams{
				ID:        existing.ID,
				Name:      existing.Name,
				ServerUrl: serverURL,
				AuthType:  link.AuthType,
				Username:  link.Username,
			}); err != nil {
				return fmt.Errorf("update hidden account: %w", err)
			}
			if remoteChanged {
				if err := clearCalendarRemoteState(ctx, qtx, cal.ID, cal.ColorDirty); err != nil {
					return err
				}
			}
			if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
				ID:        cal.ID,
				AccountID: &existing.ID,
				RemoteUrl: storage.StringToNullable(link.RemoteURL),
			}); err != nil {
				return fmt.Errorf("link calendar: %w", err)
			}
			if err := updateCalendarCapabilities(ctx, qtx, cal.ID, link); err != nil {
				return err
			}
			// Intentionally skip seeding RemoteColor on re-link: the calendar
			// is already linked, the user may have just edited Color in the
			// same save (Update set color_dirty=1), and the next sync's
			// syncCalendarMetadata reconciles colors with proper dirty-flag
			// handling. Seeding here would clobber the local edit.
			//
			// Write the new credential only after the DB writes succeed. The
			// fingerprint keeps a failed compensation from exposing it to the
			// old account identity.
			if err := credStore.Set(cred); err != nil {
				return fmt.Errorf("store credentials: %w", err)
			}
			if err := tx.Commit(); err != nil {
				if hasPrevCred {
					if restoreErr := credStore.Set(prevCred); restoreErr != nil {
						return fmt.Errorf("commit remote calendar link: %w (restore credentials: %w)", err, restoreErr)
					}
				} else if deleteErr := credStore.Delete(existing.ID); deleteErr != nil {
					return fmt.Errorf("commit remote calendar link: %w (delete replacement credentials: %w)", err, deleteErr)
				}
				return fmt.Errorf("commit remote calendar link: %w", err)
			}
			return nil
		}
	}

createAccount:
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	accountName := hiddenAccountName(cal.ID)
	for suffix := 2; ; suffix++ {
		if _, lookupErr := qtx.GetAccountByName(ctx, accountName); errors.Is(lookupErr, sql.ErrNoRows) {
			break
		} else if lookupErr != nil {
			return fmt.Errorf("check hidden account name: %w", lookupErr)
		}
		accountName = fmt.Sprintf("%s_%d", hiddenAccountName(cal.ID), suffix)
	}
	account, err := qtx.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      accountName,
		ServerUrl: serverURL,
		AuthType:  link.AuthType,
		Username:  link.Username,
	})
	if err != nil {
		return fmt.Errorf("create hidden account: %w", err)
	}
	if err := qtx.AdvanceCurrentCredentialAccountWatermark(ctx, account.ID); err != nil {
		return fmt.Errorf("advance credential account watermark: %w", err)
	}
	if remoteChanged {
		if err := clearCalendarRemoteState(ctx, qtx, cal.ID, cal.ColorDirty); err != nil {
			return err
		}
	}
	if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        cal.ID,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable(link.RemoteURL),
	}); err != nil {
		return fmt.Errorf("link calendar: %w", err)
	}
	if err := updateCalendarCapabilities(ctx, qtx, cal.ID, link); err != nil {
		return err
	}
	if link.RemoteColor != "" {
		if err := qtx.UpdateCalendarColorFromSync(ctx, storage.UpdateCalendarColorFromSyncParams{
			ID:          cal.ID,
			Color:       link.RemoteColor,
			RemoteColor: storage.StringToNullable(link.RemoteColor),
		}); err != nil {
			return fmt.Errorf("seed remote calendar color: %w", err)
		}
	}

	cred.AccountID = account.ID
	cred.AccountFingerprint = auth.AccountFingerprint(serverURL, link.AuthType, link.Username)
	if err := credStore.Set(cred); err != nil {
		return fmt.Errorf("store credentials: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if deleteErr := credStore.Delete(account.ID); deleteErr != nil {
			return fmt.Errorf("commit remote calendar link: %w (delete credentials: %w)", err, deleteErr)
		}
		return fmt.Errorf("commit remote calendar link: %w", err)
	}
	return nil
}

func updateCalendarCapabilities(ctx context.Context, q *storage.Queries, calendarID int64, link RemoteLink) error {
	if strings.TrimSpace(link.RemoteAccess) == "" {
		return nil
	}
	components := make([]string, 0, len(link.RemoteComponents))
	seen := make(map[string]struct{}, len(link.RemoteComponents))
	for _, component := range link.RemoteComponents {
		component = strings.ToUpper(strings.TrimSpace(component))
		if component == "" {
			continue
		}
		if _, duplicate := seen[component]; duplicate {
			continue
		}
		seen[component] = struct{}{}
		components = append(components, component)
	}
	slices.Sort(components)
	if err := q.UpdateCalendarCapabilitiesFromLink(ctx, storage.UpdateCalendarCapabilitiesFromLinkParams{
		RemoteAccess:     strings.ToLower(strings.TrimSpace(link.RemoteAccess)),
		RemoteComponents: strings.Join(components, ","),
		ID:               calendarID,
	}); err != nil {
		return fmt.Errorf("seed remote calendar capabilities: %w", err)
	}
	return nil
}

func clearCalendarRemoteState(ctx context.Context, q *storage.Queries, calendarID int64, preserveColorDirty bool) error {
	if err := q.DetachSyncResourcesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("detach calendar sync resources: %w", err)
	}
	if err := q.DeleteTombstonesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete calendar tombstones: %w", err)
	}
	if err := q.DeleteSyncConflictsByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete calendar conflicts: %w", err)
	}
	if err := q.ClearRemoteLinkByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("clear calendar remote link: %w", err)
	}
	if preserveColorDirty {
		if err := q.MarkCalendarColorDirty(ctx, calendarID); err != nil {
			return fmt.Errorf("preserve calendar color edit: %w", err)
		}
	}
	return nil
}

func sameRemoteCollection(left, right string) bool {
	normalize := func(raw string) (scheme, host, collectionPath string, ok bool) {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", "", "", false
		}
		return strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host), strings.TrimRight(parsed.Path, "/"), true
	}
	leftScheme, leftHost, leftPath, leftOK := normalize(left)
	rightScheme, rightHost, rightPath, rightOK := normalize(right)
	return leftOK && rightOK && leftScheme == rightScheme && leftHost == rightHost && leftPath == rightPath
}

// Disconnect removes the remote link from a calendar and, when the account
// was a private hidden account with no other calendars attached, deletes the
// account and its stored credential.
func (s *Service) Disconnect(ctx context.Context, cal Calendar, credStore auth.CredentialStore) error {
	lockedCal, release, err := s.lockRemoteLifecycle(ctx, cal.ID)
	if err != nil {
		return err
	}
	defer release()
	cal = lockedCal
	if cal.AccountID == 0 {
		return nil
	}

	account, err := s.q.GetAccount(ctx, cal.AccountID)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	qtx := s.q.WithTx(tx)
	if err := clearCalendarRemoteState(ctx, qtx, cal.ID, false); err != nil {
		_ = tx.Rollback()
		return err
	}

	var deleteCredential bool
	if strings.HasPrefix(account.Name, hiddenAccountPrefix) {
		linked, err := qtx.ListCalendarsByAccount(ctx, &account.ID)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("list calendars by hidden account: %w", err)
		}
		if len(linked) == 0 {
			if err := qtx.DeleteAccount(ctx, account.ID); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("delete hidden account: %w", err)
			}
			deleteCredential = true
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remote calendar disconnect: %w", err)
	}

	if credStore != nil && deleteCredential {
		_ = credStore.Delete(account.ID)
	}
	return nil
}

// DeleteWithRemoteCleanup deletes a calendar, and when its backing account is
// a hidden per-calendar account with no other calendars attached, also deletes
// that account and its stored credential.
//
// When the target is the current default, newDefaultID must point to a
// different existing calendar; the promotion happens in the same transaction
// so the database never observes a missing default. Pass newDefaultID = 0
// when the target is not the default.
func (s *Service) DeleteWithRemoteCleanup(ctx context.Context, id, newDefaultID int64, credStore auth.CredentialStore) error {
	cal, release, err := s.lockRemoteLifecycle(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	count, err := s.q.CountCalendars(ctx)
	if err != nil {
		return fmt.Errorf("count calendars: %w", err)
	}
	if count <= 1 {
		return ErrLastCalendar
	}

	if cal.IsDefault {
		if newDefaultID == 0 {
			return ErrDefaultCalendarRequiresPromotion
		}
		if newDefaultID == id {
			return ErrInvalidPromotionTarget
		}
		if _, err := s.q.GetCalendar(ctx, newDefaultID); err != nil {
			return ErrInvalidPromotionTarget
		}
	}

	var (
		account       storage.Account
		hasAccount    bool
		hiddenAccount bool
	)
	if cal.AccountID != 0 {
		account, err = s.q.GetAccount(ctx, cal.AccountID)
		if err != nil {
			return fmt.Errorf("get account: %w", err)
		}
		hasAccount = true
		hiddenAccount = strings.HasPrefix(account.Name, hiddenAccountPrefix)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteCalendar(ctx, id); err != nil {
		return err
	}

	if cal.IsDefault {
		if err := qtx.ClearDefaultCalendar(ctx); err != nil {
			return fmt.Errorf("clear default: %w", err)
		}
		if err := qtx.SetCalendarAsDefault(ctx, newDefaultID); err != nil {
			return fmt.Errorf("promote default: %w", err)
		}
	}

	var deleteCredential bool
	if hasAccount && hiddenAccount {
		linked, err := qtx.ListCalendarsByAccount(ctx, &account.ID)
		if err != nil {
			return fmt.Errorf("list calendars by hidden account: %w", err)
		}
		if len(linked) == 0 {
			if err := qtx.DeleteAccount(ctx, account.ID); err != nil {
				return fmt.Errorf("delete hidden account: %w", err)
			}
			deleteCredential = true
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	if deleteCredential && credStore != nil {
		_ = credStore.Delete(account.ID)
	}
	return nil
}

// NormalizeAuthType lowercases and trims an auth type string for comparison.
func NormalizeAuthType(authType string) string {
	return strings.ToLower(strings.TrimSpace(authType))
}

// DeriveServerURL returns the server root URL for a CalDAV calendar URL.
// The server URL is scheme+host plus at most the first path segment
// (e.g. "/dav" from "https://host/dav/calendars/work/").
func DeriveServerURL(remoteURL string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(remoteURL))
	if err != nil {
		return "", fmt.Errorf("parse remote URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("remote URL must include scheme and host")
	}
	if parsed.Scheme != "https" && !allowInsecure {
		return "", fmt.Errorf("remote URL must use HTTPS; allow-insecure is required for HTTP (e.g., local development)")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(), nil
	}

	cleaned := path.Clean(parsed.Path)
	parts := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(), nil
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/" + parts[0]}).String(), nil
}

func hiddenAccountName(calendarID int64) string {
	return fmt.Sprintf("%s%d", hiddenAccountPrefix, calendarID)
}
