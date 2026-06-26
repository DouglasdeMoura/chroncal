package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	calendarpkg "github.com/douglasdemoura/chroncal/internal/calendar"
)

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
	if _, err := calendarpkg.DeriveServerURL(remoteURL, allowInsecure); err != nil {
		return err
	}
	return nil
}

func connectCalendarRemote(ctx context.Context, a *app.App, cal calendarpkg.Calendar, flags calendarRemoteFlags) error {
	credStore, err := newCalendarCredentialStore(a.AllowPlaintext)
	if err != nil {
		return fmt.Errorf("credential store: %w", err)
	}

	cred, err := buildCalendarCredential(ctx, flags)
	if err != nil {
		return err
	}

	// Best-effort PROPFIND for the remote calendar-color so the calendar
	// adopts the server's color on link. Auth password for the metadata
	// fetch is whichever secret matches the auth type — basic password,
	// bearer token, or OAuth access token.
	metaPassword := cred.Password
	if cred.AccessToken != "" {
		metaPassword = cred.AccessToken
	}
	metaCtx, metaCancel := context.WithTimeout(ctx, 10*time.Second)
	meta, _ := caldav.FetchCalendarMetadata(metaCtx, flags.RemoteURL, flags.Username, metaPassword, flags.AuthType, flags.AllowInsecure)
	metaCancel()

	return a.Calendars.Connect(ctx, cal, calendarpkg.RemoteLink{
		RemoteURL:     flags.RemoteURL,
		Username:      flags.Username,
		AuthType:      flags.AuthType,
		AllowInsecure: flags.AllowInsecure,
		RemoteColor:   meta.Color,
	}, cred, credStore)
}

func disconnectCalendarRemote(ctx context.Context, a *app.App, cal calendarpkg.Calendar) error {
	credStore, _ := newCalendarCredentialStore(a.AllowPlaintext)
	return a.Calendars.Disconnect(ctx, cal, credStore)
}

func deleteCalendarWithCleanup(ctx context.Context, a *app.App, id, newDefaultID int64) error {
	credStore, _ := newCalendarCredentialStore(a.AllowPlaintext)
	return a.Calendars.DeleteWithRemoteCleanup(ctx, id, newDefaultID, credStore)
}

func buildCalendarCredential(ctx context.Context, flags calendarRemoteFlags) (auth.Credential, error) {
	switch normalizeAuthType(flags.AuthType) {
	case "":
		return auth.Credential{Username: flags.Username}, nil
	case "bearer":
		token, err := readBearerToken()
		if err != nil {
			return auth.Credential{}, err
		}
		return auth.Credential{Username: flags.Username, AccessToken: token}, nil
	case "basic":
		fmt.Print("Password: ")
		passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return auth.Credential{}, fmt.Errorf("read password: %w", err)
		}
		return auth.Credential{Username: flags.Username, Password: string(passwordBytes)}, nil
	case "oauth2":
		clientSecret, err := readGoogleClientSecret()
		if err != nil {
			return auth.Credential{}, err
		}
		result, err := auth.GoogleOAuthFlow(ctx, flags.OAuthClientID, clientSecret)
		if err != nil {
			return auth.Credential{}, fmt.Errorf("OAuth flow: %w", err)
		}
		return auth.Credential{
			Username:          flags.Username,
			AccessToken:       result.AccessToken,
			RefreshToken:      result.RefreshToken,
			TokenExpiry:       result.Expiry.Format("2006-01-02T15:04:05Z07:00"),
			OAuthClientID:     flags.OAuthClientID,
			OAuthClientSecret: clientSecret,
		}, nil
	default:
		return auth.Credential{}, fmt.Errorf("invalid auth type %q", flags.AuthType)
	}
}

func normalizeAuthType(authType string) string {
	return calendarpkg.NormalizeAuthType(authType)
}

// readBearerToken obtains the bearer token for --auth bearer. We never accept
// it as a CLI flag to avoid exposing secrets in /proc/<pid>/cmdline and shell
// history. Sources, in order:
//
//  1. CHRONCAL_BEARER_TOKEN env var (handy for scripted/CI setup).
//  2. Interactive prompt via terminal (echo disabled), matching basic-auth UX.
func readBearerToken() (string, error) {
	if s := strings.TrimSpace(os.Getenv("CHRONCAL_BEARER_TOKEN")); s != "" {
		return s, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("a bearer token is required: set CHRONCAL_BEARER_TOKEN or run interactively")
	}
	fmt.Print("Bearer token: ")
	tokenBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read bearer token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return "", fmt.Errorf("bearer token is required")
	}
	return token, nil
}

// readGoogleClientSecret obtains the Desktop OAuth client secret. Google's
// token endpoint requires it for Desktop clients even with PKCE, so it is
// mandatory for the oauth2 auth type. We never accept it as a CLI flag — that
// would expose it in /proc/<pid>/cmdline and shell history. Sources, in order:
//
//  1. GOOGLE_CLIENT_SECRET env var (handy for scripted setup).
//  2. Interactive prompt via terminal (echo disabled), matching basic-auth UX.
func readGoogleClientSecret() (string, error) {
	if s := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET")); s != "" {
		return s, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("a Google OAuth client secret is required: set GOOGLE_CLIENT_SECRET or run interactively")
	}
	fmt.Print("Google OAuth client secret: ")
	secretBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read client secret: %w", err)
	}
	secret := strings.TrimSpace(string(secretBytes))
	if secret == "" {
		return "", fmt.Errorf("client secret is required")
	}
	return secret, nil
}
