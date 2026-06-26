package calendar

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
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
	RemoteColor string
}

// Connect links a calendar to a remote CalDAV URL and stores the credential.
// When the calendar is already linked to a hidden account, Connect updates
// that account in place; otherwise it creates a new hidden account.
func (s *Service) Connect(ctx context.Context, cal Calendar, link RemoteLink, cred auth.Credential, credStore auth.CredentialStore) error {
	link.AuthType = NormalizeAuthType(link.AuthType)

	serverURL, err := DeriveServerURL(link.RemoteURL, link.AllowInsecure)
	if err != nil {
		return err
	}

	if cal.AccountID != 0 {
		existing, err := s.q.GetAccount(ctx, cal.AccountID)
		if err == nil && strings.HasPrefix(existing.Name, hiddenAccountPrefix) {
			cred.AccountID = existing.ID

			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin tx: %w", err)
			}
			qtx := s.q.WithTx(tx)
			if err := qtx.UpdateAccount(ctx, storage.UpdateAccountParams{
				ID:        existing.ID,
				Name:      hiddenAccountName(cal.ID),
				ServerUrl: serverURL,
				AuthType:  link.AuthType,
				Username:  link.Username,
			}); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("update hidden account: %w", err)
			}
			if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
				ID:        cal.ID,
				AccountID: &existing.ID,
				RemoteUrl: storage.StringToNullable(link.RemoteURL),
			}); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("link calendar: %w", err)
			}
			// Intentionally skip seeding RemoteColor on re-link: the calendar
			// is already linked, the user may have just edited Color in the
			// same save (Update set color_dirty=1), and the next sync's
			// syncCalendarMetadata reconciles colors with proper dirty-flag
			// handling. Seeding here would clobber the local edit.
			//
			// Capture the prior credential so a failed commit can roll the
			// keyring back to it; otherwise the account row keeps its old
			// settings while the keyring holds the new secret -- a mismatch
			// that breaks the next sync. Write the new credential only after
			// the DB writes succeed, mirroring the new-account path below.
			prevCred, prevErr := credStore.Get(existing.ID)
			if err := credStore.Set(cred); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("store credentials: %w", err)
			}
			if err := tx.Commit(); err != nil {
				if prevErr == nil {
					_ = credStore.Set(prevCred)
				} else {
					_ = credStore.Delete(existing.ID)
				}
				return fmt.Errorf("commit remote calendar link: %w", err)
			}
			return nil
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	qtx := s.q.WithTx(tx)
	account, err := qtx.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      hiddenAccountName(cal.ID),
		ServerUrl: serverURL,
		AuthType:  link.AuthType,
		Username:  link.Username,
	})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create hidden account: %w", err)
	}
	if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        cal.ID,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable(link.RemoteURL),
	}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("link calendar: %w", err)
	}
	if link.RemoteColor != "" {
		if err := qtx.UpdateCalendarColorFromSync(ctx, storage.UpdateCalendarColorFromSyncParams{
			ID:          cal.ID,
			Color:       link.RemoteColor,
			RemoteColor: storage.StringToNullable(link.RemoteColor),
		}); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("seed remote calendar color: %w", err)
		}
	}

	cred.AccountID = account.ID
	if err := credStore.Set(cred); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store credentials: %w", err)
	}
	if err := tx.Commit(); err != nil {
		_ = credStore.Delete(account.ID)
		return fmt.Errorf("commit remote calendar link: %w", err)
	}
	return nil
}

// Disconnect removes the remote link from a calendar and, when the account
// was a private hidden account with no other calendars attached, deletes the
// account and its stored credential.
func (s *Service) Disconnect(ctx context.Context, cal Calendar, credStore auth.CredentialStore) error {
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
	if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{ID: cal.ID}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("unlink calendar: %w", err)
	}

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
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remote calendar disconnect: %w", err)
	}

	if credStore != nil && strings.HasPrefix(account.Name, hiddenAccountPrefix) {
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
	cal, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

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

	if hasAccount && hiddenAccount {
		linked, err := qtx.ListCalendarsByAccount(ctx, &account.ID)
		if err != nil {
			return fmt.Errorf("list calendars by hidden account: %w", err)
		}
		if len(linked) == 0 {
			if err := qtx.DeleteAccount(ctx, account.ID); err != nil {
				return fmt.Errorf("delete hidden account: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	if hasAccount && hiddenAccount && credStore != nil {
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
