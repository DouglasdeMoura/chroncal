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
// Callers must collect any password or OAuth tokens into the
// auth.Credential that accompanies this link before they call Connect.
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

// lockRemoteLifecycle locks the account and the calendar of calendarID,
// and the calendars that a move links to it, both ways. An unlink deletes
// the sync state of the calendar and detaches the moves from it
// (clearCalendarRemoteState). The tombstones of a move into it then wait
// by the rules of a local target (issue #841).
func (s *Service) lockRemoteLifecycle(ctx context.Context, calendarID int64) (Calendar, func(), error) {
	// A calendar that does not exist returns the error of the lookup as it
	// is.
	if _, err := s.Get(ctx, calendarID); err != nil {
		return Calendar{}, nil, err
	}
	release, err := synclock.Calendars(ctx, s.db, storage.MoveLockSet(s.q, calendarID))
	if err != nil {
		return Calendar{}, nil, fmt.Errorf("lock calendar account: %w", err)
	}
	cal, err := s.Get(ctx, calendarID)
	if err != nil {
		release()
		return Calendar{}, nil, err
	}
	return cal, release, nil
}

// reusableConnectAccount returns the account that Connect relinks instead of a
// new one. Connect reuses the hidden account of a calendar when that account
// links to this calendar only. The second result is false when Connect creates
// an account.
func (s *Service) reusableConnectAccount(ctx context.Context, cal Calendar) (storage.Account, bool, error) {
	if cal.AccountID == 0 {
		return storage.Account{}, false, nil
	}
	existing, err := s.q.GetAccount(ctx, cal.AccountID)
	// Only sql.ErrNoRows (the account row is genuinely gone) may fall through
	// to the create-new path. A transient read failure must be propagated;
	// treating it as not-found would repoint the calendar to a brand-new
	// hidden account and orphan the old credential (issue #300).
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Account{}, false, nil
	}
	if err != nil {
		return storage.Account{}, false, fmt.Errorf("get account: %w", err)
	}
	if !strings.HasPrefix(existing.Name, hiddenAccountPrefix) {
		return storage.Account{}, false, nil
	}
	linked, err := s.q.ListCalendarsByAccount(ctx, &existing.ID)
	if err != nil {
		return storage.Account{}, false, fmt.Errorf("list hidden account calendars: %w", err)
	}
	if len(linked) != 1 {
		return storage.Account{}, false, nil
	}
	return existing, true, nil
}

// ConnectTarget names the credential that Connect writes for cal and link. It
// returns the account ID and the credential fingerprint of that write.
//
// A caller checks the credential store with these values before it asks the
// user for a secret. The check then reads the account that Connect writes to,
// not the account the calendar points at now. Connect creates an account for
// most calendars, and a new account holds no credential, so the ID is 0.
//
// Connect takes the remote lifecycle lock and reads the account again, so the
// answer is a prediction. The store keeps the final say on the write.
func (s *Service) ConnectTarget(ctx context.Context, cal Calendar, link RemoteLink) (int64, string, error) {
	if err := s.CheckLinkable(cal); err != nil {
		return 0, "", err
	}
	serverURL, err := DeriveServerURL(link.RemoteURL, link.AllowInsecure)
	if err != nil {
		return 0, "", err
	}
	// The check runs before the prompt for a secret, so the user does no
	// work for a link that Connect refuses (issue #874).
	if err := checkConnectFree(ctx, s.q, cal, link, serverURL); err != nil {
		return 0, "", err
	}
	fingerprint := auth.AccountFingerprint(serverURL, NormalizeAuthType(link.AuthType), link.Username)
	existing, reuse, err := s.reusableConnectAccount(ctx, cal)
	if err != nil {
		return 0, "", err
	}
	if !reuse {
		return 0, fingerprint, nil
	}
	return existing.ID, fingerprint, nil
}

// Connect links a calendar to a remote CalDAV URL and stores the credential.
// When the calendar is already linked to a hidden account, Connect updates
// that account in place; otherwise it creates a new hidden account. Connect
// refuses a collection that another local calendar links with the same user
// (ErrCollectionLinked).
func (s *Service) Connect(ctx context.Context, cal Calendar, link RemoteLink, cred auth.Credential, credStore auth.CredentialStore) error {
	lockedCal, release, err := s.lockRemoteLifecycle(ctx, cal.ID)
	if err != nil {
		return err
	}
	defer release()
	cal = lockedCal

	// The check runs again on the locked row (issue #809). ConnectTarget
	// checked an earlier read, and a path or an apply can land after that
	// read.
	if err := s.CheckLinkable(cal); err != nil {
		return err
	}

	link.AuthType = NormalizeAuthType(link.AuthType)

	serverURL, err := DeriveServerURL(link.RemoteURL, link.AllowInsecure)
	if err != nil {
		return err
	}
	remoteChanged := cal.RemoteURL != "" && !sameRemoteCollection(cal.RemoteURL, link.RemoteURL)

	existing, reuse, err := s.reusableConnectAccount(ctx, cal)
	if err != nil {
		return err
	}
	if reuse {
		cred.AccountID = existing.ID
		oldFingerprint := auth.AccountFingerprint(existing.ServerUrl, existing.AuthType, existing.Username)
		prior, prevErr := auth.CapturePriorCredential(credStore, existing.ID, oldFingerprint)
		if prevErr != nil {
			return fmt.Errorf("read credentials before relink: %w", prevErr)
		}
		cred.AccountFingerprint = auth.AccountFingerprint(serverURL, link.AuthType, link.Username)

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)
		if err := checkConnectFree(ctx, qtx, cal, link, serverURL); err != nil {
			return err
		}
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
		// An item of a calendar that was local has no sync row. The link
		// gives each one a dirty row, so the next push uploads it (issue
		// #866). A moved copy then pushes and counts as live (issue #841).
		if err := markLinkedItemsDirty(ctx, qtx, cal.ID); err != nil {
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
		if err := auth.CommitWithCredentialCompensation(tx, credStore, existing.ID, prior, true, "commit remote calendar link"); err != nil {
			return err
		}
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	// One collection links to one local calendar only (issue #874). Connect
	// does not reuse the account of another calendar. A removal of that
	// account would then unlink this calendar too, and a selection change
	// of that account could delete it.
	if err := checkConnectFree(ctx, qtx, cal, link, serverURL); err != nil {
		return err
	}
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
	// An item of a calendar that was local has no sync row. The link gives
	// each one a dirty row, so the next push uploads it (issue #866). A
	// moved copy then pushes and counts as live (issue #841).
	if err := markLinkedItemsDirty(ctx, qtx, cal.ID); err != nil {
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
	if err := auth.CommitWithCredentialCompensation(tx, credStore, account.ID, auth.PriorCredential{}, true, "commit remote calendar link"); err != nil {
		return err
	}
	return nil
}

// checkConnectFree refuses a link of cal to a collection that another local
// calendar links (CheckCollectionFree). A calendar that an account links to
// the same collection with the same user passes. A relink then changes the
// credential only, and a double link from before issue #874 keeps working.
func checkConnectFree(ctx context.Context, q *storage.Queries, cal Calendar, link RemoteLink, serverURL string) error {
	if cal.AccountID != 0 {
		current, err := q.GetAccount(ctx, cal.AccountID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get account: %w", err)
		}
		if err == nil && strings.EqualFold(strings.TrimSpace(current.Username), strings.TrimSpace(link.Username)) &&
			CollectionKey(cal.RemoteURL, current.ServerUrl) == CollectionKey(link.RemoteURL, serverURL) {
			return nil
		}
	}
	return CheckCollectionFree(ctx, q, link.RemoteURL, serverURL, link.Username, CollectionLinkScope{CalendarID: cal.ID})
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

// markLinkedItemsDirty gives each local item of a newly linked calendar a
// dirty sync row (MarkLocalItemsDirtyInCalendar) and each moved copy its
// sync row (MarkMovedCopiesDirtyInCalendar). The next push then uploads the
// items. Each create sends If-None-Match, so an object of the remote
// collection at the same href gets a conflict, not an overwrite.
func markLinkedItemsDirty(ctx context.Context, qtx *storage.Queries, calendarID int64) error {
	if err := qtx.MarkLocalItemsDirtyInCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("mark the local items dirty: %w", err)
	}
	if err := qtx.MarkMovedCopiesDirtyInCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("mark the moved copies dirty: %w", err)
	}
	// An edit while the calendar had no account can sit on a row that the
	// collection cannot take (issue #876).
	if err := qtx.SettleUntakeableEditsInCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("settle the edits that the collection cannot take: %w", err)
	}
	return nil
}

func clearCalendarRemoteState(ctx context.Context, q *storage.Queries, calendarID int64, preserveColorDirty bool) error {
	// The moves from the calendar end first (issue #841). lockRemoteLifecycle
	// holds the locks of their target calendars. A move into the calendar
	// follows the rules of a local target after the unlink.
	if err := storage.DetachMovesFromCalendar(ctx, q, calendarID); err != nil {
		return err
	}
	if err := q.DetachSyncResourcesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("detach calendar sync resources: %w", err)
	}
	if err := q.DeleteTombstonesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete calendar tombstones: %w", err)
	}
	if err := q.DeleteSyncHrefRetriesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("calendar href retries: %w", err)
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
	leftKey, rightKey := CollectionKey(left, ""), CollectionKey(right, "")
	return isAbsoluteURL(leftKey) && leftKey == rightKey
}

// isAbsoluteURL reports whether raw parses as a URL with a scheme and a host.
func isAbsoluteURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

// Disconnect removes the remote link from a calendar. When the account
// was a private hidden account with no other calendars attached, it also
// deletes the account and its stored credential.
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

// DeleteWithRemoteCleanup deletes a calendar. When the account behind it is
// a hidden per-calendar account with no other calendars attached, it also
// deletes that account and its stored credential.
//
// When the target is the current default, newDefaultID must point to a
// different calendar that still exists. The promotion happens in the same
// transaction. The database then never observes a gone default. Pass
// newDefaultID = 0 when the target is not the default.
func (s *Service) DeleteWithRemoteCleanup(ctx context.Context, id, newDefaultID int64, credStore auth.CredentialStore) error {
	cal, release, err := s.lockRemoteLifecycle(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if err := s.refuseDeclared(cal.Name); err != nil {
		return err
	}

	count, err := s.q.CountCalendars(ctx)
	if err != nil {
		return fmt.Errorf("count calendars: %w", err)
	}
	if count <= 1 {
		return ErrLastCalendar
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
		if err := PromoteDefault(ctx, qtx, map[int64]struct{}{id: {}}, newDefaultID); err != nil {
			return err
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
