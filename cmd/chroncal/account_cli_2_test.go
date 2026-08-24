package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"strings"

	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

func TestAccountReauthStoresFreshTokens(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Personal Google", ServerURL: "https://apidata.googleusercontent.com/caldav",
		Username: "me@gmail.com", AuthType: "oauth2",
	}, auth.Credential{
		Username: "me@gmail.com", AccessToken: "old-access", RefreshToken: "old-refresh",
		OAuthClientID: "stored-client-id", OAuthClientSecret: "stored-secret",
	}, store)
	if err != nil {
		t.Fatalf("create oauth account: %v", err)
	}
	a.Close()
	t.Setenv("GOOGLE_CLIENT_SECRET", "")

	expiry := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	calls := stubGoogleOAuthFlow(t, &auth.GoogleOAuthResult{
		AccessToken: "new-access", RefreshToken: "new-refresh", Expiry: expiry,
	})
	stdout, _, err := runAccountCommandInProcess(t,
		"account", "reauth", "Personal Google",
		"--output", "json", "--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("account reauth: %v", err)
	}
	assertAccountJSONWithoutSecrets(t, stdout, "Personal Google", "oauth2")
	for _, secret := range []string{
		"new-access", "old-access", "new-refresh", "old-refresh", "stored-secret",
	} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("json leaked secret %q: %s", secret, stdout)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("oauth flow ran %d time(s), want 1", len(*calls))
	}
	if got := (*calls)[0]; got.clientID != "stored-client-id" || got.clientSecret != "stored-secret" {
		t.Fatalf("oauth flow client config = %+v, want the stored values", got)
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Accounts.LoadCredential(ctx, created.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" {
		t.Fatalf("tokens = %+v, want fresh access and refresh tokens", got)
	}
	if want := expiry.Format(time.RFC3339); got.TokenExpiry != want {
		t.Fatalf("token expiry = %q, want %q", got.TokenExpiry, want)
	}
	if got.Username != "me@gmail.com" ||
		got.OAuthClientID != "stored-client-id" || got.OAuthClientSecret != "stored-secret" {
		t.Fatalf("stored identity changed: %+v", got)
	}
}

func TestAccountReauthKeepsRefreshTokenWhenGoogleOmitsIt(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Personal Google", ServerURL: "https://apidata.googleusercontent.com/caldav",
		Username: "me@gmail.com", AuthType: "oauth2",
	}, auth.Credential{
		Username: "me@gmail.com", AccessToken: "old-access", RefreshToken: "old-refresh",
		OAuthClientID: "stored-client-id", OAuthClientSecret: "stored-secret",
	}, store)
	if err != nil {
		t.Fatalf("create oauth account: %v", err)
	}
	a.Close()
	t.Setenv("GOOGLE_CLIENT_SECRET", "")

	stubGoogleOAuthFlow(t, &auth.GoogleOAuthResult{
		AccessToken: "new-access", Expiry: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	})
	if _, _, err := runAccountCommandInProcess(t,
		"account", "reauth", "Personal Google",
		"--output", "json", "--allow-plaintext",
	); err != nil {
		t.Fatalf("account reauth: %v", err)
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Accounts.LoadCredential(ctx, created.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if got.AccessToken != "new-access" {
		t.Fatalf("access token = %q, want new-access", got.AccessToken)
	}
	if got.RefreshToken != "old-refresh" {
		t.Fatalf("refresh token = %q, want old-refresh kept", got.RefreshToken)
	}
}

func TestAccountReauthRefusesBasicAndBearer(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	basic, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/dav/",
		Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "old-password"}, store)
	if err != nil {
		t.Fatalf("create basic account: %v", err)
	}
	bearer, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "API Token", ServerURL: "https://cal.example.test/dav/",
		Username: "bob", AuthType: "bearer",
	}, auth.Credential{Username: "bob", AccessToken: "old-token"}, store)
	if err != nil {
		t.Fatalf("create bearer account: %v", err)
	}
	a.Close()

	for _, ref := range []string{"Work", "API Token"} {
		_, stderr, err := runChroncalCommand(t,
			"account", "reauth", ref,
			"--output", "json", "--allow-plaintext",
		)
		if err == nil {
			t.Fatalf("account reauth should refuse %s accounts", ref)
		}
		var payload struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
			t.Fatalf("decode error %q: %v", stderr, jerr)
		}
		if payload.Code != "invalid_input" {
			t.Fatalf("code = %q, want invalid_input", payload.Code)
		}
		if !strings.Contains(strings.ToLower(payload.Error), "account credentials") {
			t.Fatalf("error = %q, want account credentials guidance", payload.Error)
		}
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	gotBasic, err := a.Accounts.LoadCredential(ctx, basic.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load basic credential: %v", err)
	}
	if gotBasic.Password != "old-password" {
		t.Fatalf("basic password = %q, want old-password unchanged", gotBasic.Password)
	}
	gotBearer, err := a.Accounts.LoadCredential(ctx, bearer.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load bearer credential: %v", err)
	}
	if gotBearer.AccessToken != "old-token" {
		t.Fatalf("bearer token = %q, want old-token unchanged", gotBearer.AccessToken)
	}
}

func TestAccountReauthOverridesClientConfigFromFlagAndEnv(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Personal Google", ServerURL: "https://apidata.googleusercontent.com/caldav",
		Username: "me@gmail.com", AuthType: "oauth2",
	}, auth.Credential{
		Username: "me@gmail.com", AccessToken: "old-access", RefreshToken: "old-refresh",
		OAuthClientID: "stored-client-id", OAuthClientSecret: "stored-secret",
	}, store)
	if err != nil {
		t.Fatalf("create oauth account: %v", err)
	}
	a.Close()
	t.Setenv("GOOGLE_CLIENT_SECRET", "env-secret")

	calls := stubGoogleOAuthFlow(t, &auth.GoogleOAuthResult{
		AccessToken: "new-access", RefreshToken: "new-refresh",
		Expiry: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	})
	if _, _, err := runAccountCommandInProcess(t,
		"account", "reauth", "Personal Google",
		"--oauth-client-id", "flag-client-id",
		"--output", "json", "--allow-plaintext",
	); err != nil {
		t.Fatalf("account reauth: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("oauth flow ran %d time(s), want 1", len(*calls))
	}
	if got := (*calls)[0]; got.clientID != "flag-client-id" || got.clientSecret != "env-secret" {
		t.Fatalf("oauth flow client config = %+v, want flag client ID and env secret", got)
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Accounts.LoadCredential(ctx, created.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if got.OAuthClientID != "flag-client-id" || got.OAuthClientSecret != "env-secret" {
		t.Fatalf("client config = %+v, want overrides persisted", got)
	}
}

func TestAccountReauthRequiresOAuthClientID(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Personal Google", ServerURL: "https://apidata.googleusercontent.com/caldav",
		Username: "me@gmail.com", AuthType: "oauth2",
	}, auth.Credential{Username: "me@gmail.com", AccessToken: "access", RefreshToken: "refresh"}, store)
	if err != nil {
		t.Fatalf("create oauth account: %v", err)
	}
	a.Close()
	t.Setenv("GOOGLE_CLIENT_SECRET", "env-secret")

	calls := stubGoogleOAuthFlow(t, &auth.GoogleOAuthResult{
		AccessToken: "new-access", RefreshToken: "new-refresh",
		Expiry: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	})
	_, _, err = runAccountCommandInProcess(t,
		"account", "reauth", "Personal Google", "--allow-plaintext",
	)
	if err == nil {
		t.Fatal("reauth without a client ID should fail")
	}
	var cliErr *cliError
	if !errors.As(err, &cliErr) || cliErr.Code != "invalid_input" {
		t.Fatalf("error = %v, want invalid_input", err)
	}
	if !strings.Contains(cliErr.Msg, "--oauth-client-id") {
		t.Fatalf("error = %q, want --oauth-client-id guidance", cliErr.Msg)
	}
	if len(*calls) != 0 {
		t.Fatalf("oauth flow ran %d time(s), want 0", len(*calls))
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Accounts.LoadCredential(ctx, created.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if got.AccessToken != "access" || got.RefreshToken != "refresh" {
		t.Fatalf("oauth credential mutated: %+v", got)
	}
}

func TestAccountRemovePreservesDownloadedCalendarsAsLocal(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	srv := newAccountDiscoveryServer(t)

	if _, _, err := runChroncalCommand(t,
		"account", "add", "Test account",
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("account add: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "add", "Test account", "--all", "--allow-plaintext",
	); err != nil {
		t.Fatalf("account calendars add: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"account", "remove", "Test account", "--yes", "--allow-plaintext",
	); err != nil {
		t.Fatalf("account remove: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	accounts, err := a.Accounts.List(context.Background())
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after remove = %+v", accounts)
	}
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	if len(calendars) != 3 {
		t.Fatalf("calendar count after removal = %d, want original local calendar plus 2 downloads", len(calendars))
	}
	var keptOrigin int
	for _, calendar := range calendars {
		if calendar.AccountID != 0 {
			t.Fatalf("calendar still linked after account removal: %+v", calendar)
		}
		if calendar.RemoteURL != "" {
			keptOrigin++
		}
	}
	if keptOrigin != 2 {
		t.Fatalf("calendars keeping remote origin = %d, want 2 downloaded collections", keptOrigin)
	}
}

// TestResolveAccountRejectsAmbiguousName proves that two accounts whose
// case-insensitive names collide are never silently resolved to the first
// match. The caller must disambiguate with a numeric ID.
func TestResolveAccountRejectsAmbiguousName(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	svc := account.NewService(db, q)

	// "Work" and "WORK" are distinct under SQLite's BINARY collation so both
	// inserts succeed, but EqualFold treats them as the same name.
	accA, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "Work", ServerUrl: "https://a.test/", AuthType: "basic", Username: "alice",
	})
	if err != nil {
		t.Fatalf("create account A: %v", err)
	}
	_, err = q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "WORK", ServerUrl: "https://b.test/", AuthType: "basic", Username: "bob",
	})
	if err != nil {
		t.Fatalf("create account B: %v", err)
	}

	if _, err := resolveAccount(ctx, svc, "Work"); err == nil {
		t.Fatal("ambiguous account name should be rejected, not silently resolved to the first match")
	}
	if _, err := resolveAccount(ctx, svc, "work"); err == nil {
		t.Fatal("case-insensitive ambiguous name should be rejected")
	}

	// Numeric ID disambiguates.
	got, err := resolveAccount(ctx, svc, fmt.Sprintf("%d", accA.ID))
	if err != nil {
		t.Fatalf("resolveAccount by numeric ID: %v", err)
	}
	if got.ID != accA.ID {
		t.Fatalf("resolveAccount by ID = %d, want %d", got.ID, accA.ID)
	}
}
