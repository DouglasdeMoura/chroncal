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

// runGoogleOAuthFlow is the seam over auth.GoogleOAuthFlow. Tests stub it so
// OAuth code paths run without binding a loopback listener or opening a
// browser.
var runGoogleOAuthFlow = auth.GoogleOAuthFlow

type calendarRemoteFlags struct {
	RemoteURL string
	Username  string
	AuthType  string
	// PasswordCommand holds the --password-cmd value. It is a shell command
	// that prints the basic-auth password. The command is not a secret, so a
	// flag can carry it.
	PasswordCommand string
	OAuthClientID   string
	AllowInsecure   bool
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
	credStore, err := newCalendarCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
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
	switch {
	case cred.AccessToken != "":
		metaPassword = cred.AccessToken
	case cred.HasPasswordCommand():
		// The metadata fetch stays best effort. A password command that
		// fails leaves the color unset. The next sync reports the failure.
		if resolved, resolveErr := cred.ResolvePassword(ctx); resolveErr == nil {
			metaPassword = resolved
		}
	}
	// The metadata fetch is best effort: it seeds the color, the access mode,
	// and the component set. A failure must not stop the link. The budget
	// stays short because the user waits at the shell, but it tracks the
	// configured request timeout so a slow server can still answer.
	metaCtx, metaCancel := context.WithTimeout(ctx, calendarMetadataBudget())
	meta, metaErr := caldav.FetchCalendarMetadata(metaCtx, flags.RemoteURL, flags.Username, metaPassword, flags.AuthType, flags.AllowInsecure)
	metaCancel()
	if metaErr != nil {
		// Tell the user why the color and the access mode stay unset. A
		// silent skip looks like a server with no color.
		fmt.Fprintf(os.Stderr, "chroncal: warning: could not read the calendar metadata (%v); the color and the access mode stay unset\n", metaErr)
	}

	return a.Calendars.Connect(ctx, cal, calendarpkg.RemoteLink{
		RemoteURL:        flags.RemoteURL,
		Username:         flags.Username,
		AuthType:         flags.AuthType,
		AllowInsecure:    flags.AllowInsecure,
		RemoteColor:      meta.Color,
		RemoteAccess:     string(meta.Access),
		RemoteComponents: meta.SupportedComponents,
	}, cred, credStore)
}

// calendarMetadataBudget bounds the metadata PROPFIND that runs when a
// calendar links to a server. The fetch is best effort and the user waits at
// the shell, so the budget stays far below a sync budget. It still tracks the
// configured request timeout, so a slow server can answer.
func calendarMetadataBudget() time.Duration {
	const ceiling = 30 * time.Second
	if d := caldav.HTTPTimeout(); d < ceiling {
		return d
	}
	return ceiling
}

func disconnectCalendarRemote(ctx context.Context, a *app.App, cal calendarpkg.Calendar) error {
	credStore, _ := newCalendarCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
	return a.Calendars.Disconnect(ctx, cal, credStore)
}

func deleteCalendarWithCleanup(ctx context.Context, a *app.App, id, newDefaultID int64) error {
	credStore, _ := newCalendarCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
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
		secret, err := readBasicSecret(flags.PasswordCommand)
		if err != nil {
			return auth.Credential{}, err
		}
		return auth.Credential{
			Username:        flags.Username,
			Password:        secret.Password,
			PasswordCommand: secret.Command,
		}, nil
	case "oauth2":
		clientSecret, err := readGoogleClientSecret()
		if err != nil {
			return auth.Credential{}, err
		}
		result, err := runGoogleOAuthFlow(ctx, flags.OAuthClientID, clientSecret)
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

// basicSecret carries one resolved source for a basic-auth secret. At most
// one field has a value. Command holds a shell command that prints the
// password. Password holds the password itself.
type basicSecret struct {
	Password string
	Command  string
}

// readBasicSecret obtains the basic-auth secret source. A password command is
// not a secret, so a flag can carry it. A password never comes from a flag.
// That keeps the password out of /proc/<pid>/cmdline and the shell history.
// Sources, in order:
//
//  1. The --password-cmd flag.
//  2. The CHRONCAL_PASSWORD_CMD env var.
//  3. The CHRONCAL_PASSWORD env var.
//  4. The interactive prompt.
//
// A command source plus CHRONCAL_PASSWORD is an error. Two sources hide which
// secret the program sends.
func readBasicSecret(passwordCommand string) (basicSecret, error) {
	command := strings.TrimSpace(passwordCommand)
	if command == "" {
		command = strings.TrimSpace(os.Getenv("CHRONCAL_PASSWORD_CMD"))
	}
	if command != "" {
		if os.Getenv("CHRONCAL_PASSWORD") != "" {
			return basicSecret{}, errInvalidInputf(
				"a password command and CHRONCAL_PASSWORD are mutually exclusive; set only one")
		}
		return basicSecret{Command: command}, nil
	}
	password, err := readBasicPassword()
	if err != nil {
		return basicSecret{}, err
	}
	return basicSecret{Password: password}, nil
}

// readBasicPassword obtains the password for --auth basic. We never accept it
// as a CLI flag. That keeps secrets out of /proc/<pid>/cmdline and shell
// history. Sources, in order:
//
//  1. CHRONCAL_PASSWORD env var (handy for scripted/CI setup).
//  2. Interactive prompt via terminal (echo disabled).
func readBasicPassword() (string, error) {
	if s := os.Getenv("CHRONCAL_PASSWORD"); s != "" {
		return s, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("a password is required: set --password-cmd, CHRONCAL_PASSWORD_CMD, or CHRONCAL_PASSWORD, or run interactively")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	password := string(passwordBytes)
	if password == "" {
		return "", fmt.Errorf("password is required")
	}
	return password, nil
}

// readBearerToken obtains the bearer token for --auth bearer. We never accept
// it as a CLI flag. That keeps secrets out of /proc/<pid>/cmdline and shell
// history. Sources, in order:
//
//  1. CHRONCAL_BEARER_TOKEN env var (handy for scripted/CI setup).
//  2. Interactive prompt via terminal (echo disabled). Same UX as basic-auth.
func readBearerToken() (string, error) {
	if s := strings.TrimSpace(os.Getenv("CHRONCAL_BEARER_TOKEN")); s != "" {
		return s, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("a bearer token is required: set CHRONCAL_BEARER_TOKEN or run interactively")
	}
	fmt.Fprint(os.Stderr, "Bearer token: ")
	tokenBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
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
// mandatory for the oauth2 auth type. We never accept it as a CLI flag. That
// would expose it in /proc/<pid>/cmdline and shell history. Sources, in order:
//
//  1. GOOGLE_CLIENT_SECRET env var (handy for scripted setup).
//  2. Interactive prompt via terminal (echo disabled). Same UX as basic-auth.
func readGoogleClientSecret() (string, error) {
	if s := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET")); s != "" {
		return s, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("a Google OAuth client secret is required: set GOOGLE_CLIENT_SECRET or run interactively")
	}
	fmt.Fprint(os.Stderr, "Google OAuth client secret: ")
	secretBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read client secret: %w", err)
	}
	secret := strings.TrimSpace(string(secretBytes))
	if secret == "" {
		return "", fmt.Errorf("client secret is required")
	}
	return secret, nil
}
