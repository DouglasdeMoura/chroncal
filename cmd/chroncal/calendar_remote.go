package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"golang.org/x/term"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	calendarpkg "github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

const hiddenAccountPrefix = "__calendar_"

var newCalendarCredentialStore = auth.NewCredentialStore

type calendarRemoteFlags struct {
	RemoteURL     string
	Username      string
	AuthType      string
	OAuthClientID string
	AllowInsecure bool
}

func validateCalendarRemoteFlags(remoteURL, username, authType, oauthClientID string, allowInsecure, disconnectRemote bool) error {
	remoteURL = strings.TrimSpace(remoteURL)
	username = strings.TrimSpace(username)
	authType = strings.ToLower(strings.TrimSpace(authType))
	oauthClientID = strings.TrimSpace(oauthClientID)

	if disconnectRemote {
		if remoteURL != "" || username != "" || oauthClientID != "" || allowInsecure || authType != "" && authType != "basic" {
			return fmt.Errorf("--disconnect-remote cannot be combined with remote connection flags like --remote-url")
		}
		return nil
	}

	if remoteURL == "" {
		if username != "" || oauthClientID != "" || allowInsecure || authType != "" && authType != "basic" {
			return fmt.Errorf("remote flags require --remote-url")
		}
		return nil
	}

	if username == "" {
		return fmt.Errorf("--username is required when --remote-url is set")
	}
	switch authType {
	case "", "basic", "bearer", "oauth2":
	default:
		return fmt.Errorf("invalid auth type %q", authType)
	}
	if authType == "oauth2" && oauthClientID == "" {
		return fmt.Errorf("--oauth-client-id is required for OAuth 2.0")
	}
	if _, err := deriveCalendarServerURL(remoteURL, allowInsecure); err != nil {
		return err
	}
	return nil
}

func connectCalendarRemote(ctx context.Context, a *app.App, cal calendarpkg.Calendar, flags calendarRemoteFlags) error {
	flags.AuthType = normalizeAuthType(flags.AuthType)

	serverURL, err := deriveCalendarServerURL(flags.RemoteURL, flags.AllowInsecure)
	if err != nil {
		return err
	}

	credStore, err := newCalendarCredentialStore(true)
	if err != nil {
		return fmt.Errorf("credential store: %w", err)
	}

	cred, err := buildCalendarCredential(ctx, flags)
	if err != nil {
		return err
	}

	if cal.AccountID != 0 {
		existingAccount, err := a.Queries.GetAccount(ctx, cal.AccountID)
		if err == nil && strings.HasPrefix(existingAccount.Name, hiddenAccountPrefix) {
			cred.AccountID = existingAccount.ID
			if err := credStore.Set(cred); err != nil {
				return fmt.Errorf("store credentials: %w", err)
			}

			tx, err := a.DB.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin tx: %w", err)
			}
			qtx := a.Queries.WithTx(tx)
			if err := qtx.UpdateAccount(ctx, storage.UpdateAccountParams{
				ID:        existingAccount.ID,
				Name:      hiddenAccountName(cal.ID),
				ServerUrl: serverURL,
				AuthType:  flags.AuthType,
				Username:  flags.Username,
			}); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("update hidden account: %w", err)
			}
			if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
				ID:        cal.ID,
				AccountID: &existingAccount.ID,
				RemoteUrl: storage.StringToNullable(flags.RemoteURL),
			}); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("link calendar: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit remote calendar link: %w", err)
			}
			return nil
		}
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	qtx := a.Queries.WithTx(tx)
	account, err := qtx.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      hiddenAccountName(cal.ID),
		ServerUrl: serverURL,
		AuthType:  flags.AuthType,
		Username:  flags.Username,
	})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create hidden account: %w", err)
	}
	if err := qtx.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        cal.ID,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable(flags.RemoteURL),
	}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("link calendar: %w", err)
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

func disconnectCalendarRemote(ctx context.Context, a *app.App, cal calendarpkg.Calendar) error {
	if cal.AccountID == 0 {
		return nil
	}

	account, err := a.Queries.GetAccount(ctx, cal.AccountID)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	qtx := a.Queries.WithTx(tx)
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

	credStore, err := newCalendarCredentialStore(true)
	if err == nil && strings.HasPrefix(account.Name, hiddenAccountPrefix) {
		_ = credStore.Delete(account.ID)
	}
	return nil
}

func deleteCalendarWithCleanup(ctx context.Context, a *app.App, id int64) error {
	cal, err := a.Calendars.Get(ctx, id)
	if err != nil {
		return err
	}

	count, err := a.Queries.CountCalendars(ctx)
	if err != nil {
		return fmt.Errorf("count calendars: %w", err)
	}
	if count <= 1 {
		return calendarpkg.ErrLastCalendar
	}

	var (
		account      storage.Account
		hasAccount   bool
		hiddenAccount bool
	)
	if cal.AccountID != 0 {
		account, err = a.Queries.GetAccount(ctx, cal.AccountID)
		if err != nil {
			return fmt.Errorf("get account: %w", err)
		}
		hasAccount = true
		hiddenAccount = strings.HasPrefix(account.Name, hiddenAccountPrefix)
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := a.Queries.WithTx(tx)
	if err := qtx.DeleteCalendar(ctx, id); err != nil {
		return err
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

	if hasAccount && hiddenAccount {
		credStore, err := newCalendarCredentialStore(true)
		if err == nil {
			_ = credStore.Delete(account.ID)
		}
	}
	return nil
}

func buildCalendarCredential(ctx context.Context, flags calendarRemoteFlags) (auth.Credential, error) {
	switch normalizeAuthType(flags.AuthType) {
	case "", "bearer":
		return auth.Credential{Username: flags.Username}, nil
	case "basic":
		fmt.Print("Password: ")
		passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return auth.Credential{}, fmt.Errorf("read password: %w", err)
		}
		return auth.Credential{Username: flags.Username, Password: string(passwordBytes)}, nil
	case "oauth2":
		result, err := auth.GoogleOAuthFlow(ctx, flags.OAuthClientID)
		if err != nil {
			return auth.Credential{}, fmt.Errorf("OAuth flow: %w", err)
		}
		return auth.Credential{
			Username:      flags.Username,
			AccessToken:   result.AccessToken,
			RefreshToken:  result.RefreshToken,
			TokenExpiry:   result.Expiry.Format("2006-01-02T15:04:05Z07:00"),
			OAuthClientID: flags.OAuthClientID,
		}, nil
	default:
		return auth.Credential{}, fmt.Errorf("invalid auth type %q", flags.AuthType)
	}
}

func deriveCalendarServerURL(remoteURL string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(remoteURL))
	if err != nil {
		return "", fmt.Errorf("parse --remote-url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("--remote-url must include scheme and host")
	}
	if parsed.Scheme != "https" && !allowInsecure {
		return "", fmt.Errorf("remote URL must use HTTPS; use --allow-insecure for HTTP (e.g., local development)")
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

func normalizeAuthType(authType string) string {
	return strings.ToLower(strings.TrimSpace(authType))
}

func hiddenAccountName(calendarID int64) string {
	return fmt.Sprintf("%s%d", hiddenAccountPrefix, calendarID)
}
