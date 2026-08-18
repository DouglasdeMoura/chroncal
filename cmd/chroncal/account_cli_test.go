package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

func TestAccountAddAndList(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	srv := newAccountDiscoveryServer(t)

	stdout, _, err := runChroncalCommand(t,
		"account", "add", "Personal Google",
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("account add: %v", err)
	}
	if !strings.Contains(stdout, `Account "Personal Google" added with 2 calendar(s)`) {
		t.Fatalf("account add output = %q", stdout)
	}

	stdout, _, err = runChroncalCommand(t, "account", "list", "--output", "json")
	if err != nil {
		t.Fatalf("account list: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("decode account list: %v\n%s", err, stdout)
	}
	if len(rows) != 1 || rows[0]["name"] != "Personal Google" || rows[0]["username"] != "me@example.test" {
		t.Fatalf("account list = %#v", rows)
	}
}

func TestAccountAddImportsAndSyncsAllUsableCalendars(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	srv := newAccountDiscoveryServer(t)

	stdout, _, err := runChroncalCommand(t,
		"account", "add", "Test account",
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("account add: %v", err)
	}
	var result jsonDiscovery
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode account add: %v\n%s", err, stdout)
	}
	if result.Account.ID == 0 || len(result.Calendars) != 3 ||
		len(result.CreatedIDs) != 2 || len(result.SyncedIDs) != 2 {
		t.Fatalf("account add result = %+v", result)
	}
	for _, remote := range result.Calendars {
		if remote.Name == "Availability" {
			if remote.Imported {
				t.Fatalf("unsupported calendar was imported: %+v", remote)
			}
			continue
		}
		if !remote.Imported || remote.CalendarID == 0 {
			t.Fatalf("usable calendar missing post-import state: %+v", remote)
		}
	}
	assertLinkedCalendarNames(t, dbPath, "Holidays in Brazil", "Personal (2)")
	assertLinkedCalendarsSynced(t, dbPath)
}

func TestAccountAddAttemptsInitialSyncForEveryImportedCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	var holidaySync atomic.Bool
	srv := newAccountDiscoveryServerWithInterceptor(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "REPORT" {
			return false
		}
		switch r.URL.Path {
		case "/calendars/me/personal/":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte("<malformed"))
			return true
		case "/calendars/me/holidays/":
			holidaySync.Store(true)
		}
		return false
	})

	_, _, err := runChroncalCommand(t,
		"account", "add", "Test account",
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
	)
	if err == nil || !strings.Contains(err.Error(), "initial sync failed") {
		t.Fatalf("account add sync error = %v", err)
	}
	if !holidaySync.Load() {
		t.Fatal("second imported calendar was not synced after the first failed")
	}
	assertLinkedCalendarNames(t, dbPath, "Holidays in Brazil", "Personal (2)")
}

func TestAccountAddRollsBackWhenDiscoveryHasNoUsableCalendars(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	srv := newAccountDiscoveryServerWithInterceptor(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/calendars/me/" {
			return false
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response><d:href>/calendars/me/freebusy/</d:href><d:propstat><d:prop>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Availability</d:displayname>
    <c:supported-calendar-component-set><c:comp name="VFREEBUSY"/></c:supported-calendar-component-set>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`))
		return true
	})

	_, _, err := runChroncalCommand(t,
		"account", "add", "Test account",
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
	)
	if err == nil || !strings.Contains(err.Error(), "exposes no usable") {
		t.Fatalf("account add error = %v", err)
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
		t.Fatalf("incomplete account was retained: %+v", accounts)
	}
	assertLinkedCalendarNames(t, dbPath)
}

func TestAccountGetReturnsResolvedAccount(t *testing.T) {
	_, serverURL := setupDiscoveredAccountCLI(t, "Personal Google")

	stdout, _, err := runChroncalCommand(t,
		"account", "get", "Personal Google", "--output", "json",
	)
	if err != nil {
		t.Fatalf("account get: %v", err)
	}
	var got jsonAccount
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode account get: %v\n%s", err, stdout)
	}
	if got.ID == 0 || got.DisplayName != "Personal Google" ||
		got.ServerURL != serverURL {
		t.Fatalf("account get = %+v", got)
	}
}

func TestAccountUpdateRenamesDisplayName(t *testing.T) {
	setupDiscoveredAccountCLI(t, "Personal Google")

	stdout, _, err := runChroncalCommand(t,
		"account", "update", "Personal Google",
		"--name", "Home Google",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("account update: %v", err)
	}
	var got jsonAccount
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode account update: %v\n%s", err, stdout)
	}
	if got.DisplayName != "Home Google" || got.Name != "Home Google" {
		t.Fatalf("renamed account = %+v", got)
	}
}

func TestAccountCalendarsAddAllUsableCollectionsIdempotently(t *testing.T) {
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

	stdout, _, err := runChroncalCommand(t,
		"account", "calendars", "add", "Test account", "--all", "--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("account calendars add --all: %v", err)
	}
	if !strings.Contains(stdout, "Added and synced 0 calendars") ||
		!strings.Contains(stdout, "2 were already selected") ||
		!strings.Contains(stdout, "Availability") {
		t.Fatalf("calendar add output = %q", stdout)
	}

	// Repeating the same complete discovery must reuse both linked rows.
	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "add", "Test account", "--all", "--allow-plaintext",
	); err != nil {
		t.Fatalf("repeat account calendars add --all: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	var imported int
	for _, calendar := range calendars {
		if calendar.AccountID != 0 {
			imported++
		}
	}
	if imported != 2 {
		t.Fatalf("linked calendar count = %d, want 2 after repeat discovery", imported)
	}
}

func TestAccountCalendarsAddSelectsCalendarByName(t *testing.T) {
	dbPath, _ := setupDiscoveredAccountCLI(t, "Test account")
	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "add", "Test account",
		"--calendar", "Holidays in Brazil",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("account calendars add --calendar: %v", err)
	}

	assertLinkedCalendarNames(t, dbPath, "Holidays in Brazil", "Personal (2)")
}

func TestAccountCalendarsListAndAdd(t *testing.T) {
	dbPath := setupAccountCalendarSelectionTest(t)

	stdout, _, err := runChroncalCommand(t,
		"account", "calendars", "list", "Test account",
		"--allow-plaintext", "--output", "json",
	)
	if err != nil {
		t.Fatalf("account calendars list: %v", err)
	}
	var discovery jsonDiscovery
	if err := json.Unmarshal([]byte(stdout), &discovery); err != nil {
		t.Fatalf("decode calendar inventory: %v\n%s", err, stdout)
	}
	if len(discovery.Calendars) != 3 {
		t.Fatalf("discovered calendars = %d, want 3", len(discovery.Calendars))
	}

	stdout, _, err = runChroncalCommand(t,
		"account", "calendars", "add", "Test account",
		"--calendar", "Holidays in Brazil",
		"--allow-plaintext", "--output", "json",
	)
	if err != nil {
		t.Fatalf("account calendars add: %v", err)
	}
	if err := json.Unmarshal([]byte(stdout), &discovery); err != nil {
		t.Fatalf("decode add result: %v\n%s", err, stdout)
	}
	if len(discovery.CreatedIDs) != 1 || len(discovery.SyncedIDs) != 1 || len(discovery.ExistingIDs) != 0 {
		t.Fatalf("add result = %+v", discovery)
	}
	var added *jsonDiscoveredCalendar
	for i := range discovery.Calendars {
		if discovery.Calendars[i].Name == "Holidays in Brazil" {
			added = &discovery.Calendars[i]
		}
	}
	if added == nil || !added.Imported || added.CalendarID != discovery.CreatedIDs[0] {
		t.Fatalf("added calendar state = %+v; result = %+v", added, discovery)
	}
	assertLinkedCalendarNames(t, dbPath, "Holidays in Brazil", "Personal (2)")
	assertLinkedCalendarsSynced(t, dbPath)
}

func TestAccountCalendarsSetReconcilesExactSelection(t *testing.T) {
	dbPath := setupAccountCalendarSelectionTest(t)
	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "add", "Test account",
		"--all", "--allow-plaintext",
	); err != nil {
		t.Fatalf("account calendars add --all: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"account", "calendars", "set", "Test account",
		"--calendar", "Holidays in Brazil",
		"--yes", "--allow-plaintext", "--output", "json",
	)
	if err != nil {
		t.Fatalf("account calendars set: %v", err)
	}
	var result struct {
		AccountID      int64    `json:"account_id"`
		SelectedPaths  []string `json:"selected_paths"`
		CreatedIDs     []int64  `json:"created_ids"`
		RemovedIDs     []int64  `json:"removed_ids"`
		AccountRemoved bool     `json:"account_removed"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode set result: %v\n%s", err, stdout)
	}
	if result.AccountID == 0 || len(result.SelectedPaths) != 1 ||
		len(result.CreatedIDs) != 0 || len(result.RemovedIDs) != 1 ||
		result.AccountRemoved {
		t.Fatalf("set result = %+v", result)
	}
	assertLinkedCalendarNames(t, dbPath, "Holidays in Brazil")
}

func TestAccountCalendarsSetCanPromoteNewDefault(t *testing.T) {
	dbPath := setupAccountCalendarSelectionTest(t)
	addPersonalAndMakeDefault(t, dbPath)

	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "set", "Test account",
		"--calendar", "Holidays in Brazil",
		"--default", "Holidays in Brazil",
		"--yes", "--allow-plaintext",
	); err != nil {
		t.Fatalf("replace selected default: %v", err)
	}
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New after reconcile: %v", err)
	}
	defer a.Close()
	def, err := a.Calendars.GetDefault(context.Background())
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if def.Name != "Holidays in Brazil" {
		t.Fatalf("default calendar = %q", def.Name)
	}
}

func TestAccountCalendarsSetRequiresDefaultReplacement(t *testing.T) {
	dbPath := setupAccountCalendarSelectionTest(t)
	addPersonalAndMakeDefault(t, dbPath)

	_, _, err := runChroncalCommand(t,
		"account", "calendars", "set", "Test account",
		"--calendar", "Holidays in Brazil",
		"--yes", "--allow-plaintext",
	)
	if err == nil || !strings.Contains(err.Error(), "--default") {
		t.Fatalf("set without replacement error = %v, want --default guidance", err)
	}
}

func addPersonalAndMakeDefault(t *testing.T, dbPath string) {
	t.Helper()
	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "add", "Test account",
		"--calendar", "Personal",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("add Personal: %v", err)
	}
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		a.Close()
		t.Fatalf("list calendars: %v", err)
	}
	var personalID int64
	for _, item := range calendars {
		if item.AccountID != 0 {
			personalID = item.ID
		}
	}
	a.Close()
	if personalID == 0 {
		t.Fatal("Personal calendar was not imported")
	}
	if _, _, err := runChroncalCommand(t,
		"calendar", "set-default", strconv.FormatInt(personalID, 10),
	); err != nil {
		t.Fatalf("set Personal default: %v", err)
	}
}

func TestAccountCalendarsSetNoneRemovesAccount(t *testing.T) {
	dbPath := setupAccountCalendarSelectionTest(t)
	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "add", "Test account",
		"--calendar", "Personal",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("add Personal: %v", err)
	}
	stdout, _, err := runChroncalCommand(t,
		"account", "calendars", "set", "Test account",
		"--none", "--yes", "--allow-plaintext", "--output", "json",
	)
	if err != nil {
		t.Fatalf("remove every selected calendar: %v", err)
	}
	var result struct {
		AccountRemoved bool `json:"account_removed"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode empty selection result: %v\n%s", err, stdout)
	}
	if !result.AccountRemoved {
		t.Fatalf("empty selection result = %+v", result)
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
		t.Fatalf("accounts after empty selection = %+v", accounts)
	}
	assertLinkedCalendarNames(t, dbPath)
}

func setupAccountCalendarSelectionTest(t *testing.T) string {
	t.Helper()
	dbPath, _ := setupDiscoveredAccountCLI(t, "Test account")
	if _, _, err := runChroncalCommand(t,
		"account", "calendars", "set", "Test account",
		"--calendar", "Personal",
		"--yes", "--allow-plaintext",
	); err != nil {
		t.Fatalf("reduce initial account selection: %v", err)
	}
	return dbPath
}

func TestAccountCredentialsRotatesBearerAndBasic(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)

	bearer, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "API Token", ServerURL: "https://cal.example.test/dav/",
		Username: "alice", AuthType: "bearer",
	}, auth.Credential{AccessToken: "old-token"}, store)
	if err != nil {
		t.Fatalf("create bearer account: %v", err)
	}
	basic, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/dav/",
		Username: "bob", AuthType: "basic",
	}, auth.Credential{Password: "old-password"}, store)
	if err != nil {
		t.Fatalf("create basic account: %v", err)
	}
	a.Close()

	t.Setenv("CHRONCAL_BEARER_TOKEN", "new-token")
	stdout, _, err := runChroncalCommand(t,
		"account", "credentials", "API Token",
		"--output", "json", "--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("account credentials bearer: %v", err)
	}
	assertAccountJSONWithoutSecrets(t, stdout, "API Token", "bearer")
	if strings.Contains(stdout, "new-token") || strings.Contains(stdout, "old-token") {
		t.Fatalf("json leaked a bearer token: %s", stdout)
	}

	t.Setenv("CHRONCAL_PASSWORD", "new-password")
	stdout, _, err = runChroncalCommand(t,
		"account", "credentials", "Work",
		"--output", "json", "--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("account credentials basic: %v", err)
	}
	assertAccountJSONWithoutSecrets(t, stdout, "Work", "basic")
	if strings.Contains(stdout, "new-password") || strings.Contains(stdout, "old-password") {
		t.Fatalf("json leaked a password: %s", stdout)
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	store = openPlaintextStore(t, a)
	gotBearer, err := a.Accounts.LoadCredential(ctx, bearer.ID, store)
	if err != nil {
		t.Fatalf("load bearer credential: %v", err)
	}
	if gotBearer.AccessToken != "new-token" || gotBearer.Password != "" {
		t.Fatalf("bearer credential = %+v, want access_token new-token", gotBearer)
	}
	gotBasic, err := a.Accounts.LoadCredential(ctx, basic.ID, store)
	if err != nil {
		t.Fatalf("load basic credential: %v", err)
	}
	if gotBasic.Password != "new-password" || gotBasic.AccessToken != "" {
		t.Fatalf("basic credential = %+v, want password new-password", gotBasic)
	}
	if gotBearer.Username != "alice" || gotBasic.Username != "bob" {
		t.Fatalf("usernames changed: bearer=%q basic=%q", gotBearer.Username, gotBasic.Username)
	}
}

func TestAccountCredentialsRefusesOAuth(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Personal Google", ServerURL: "https://apidata.googleusercontent.com/caldav",
		Username: "me@gmail.com", AuthType: "oauth2",
	}, auth.Credential{AccessToken: "access", RefreshToken: "refresh"}, store)
	if err != nil {
		t.Fatalf("create oauth account: %v", err)
	}
	a.Close()

	t.Setenv("CHRONCAL_PASSWORD", "should-not-apply")
	_, stderr, err := runChroncalCommand(t,
		"account", "credentials", "Personal Google",
		"--output", "json", "--allow-plaintext",
	)
	if err == nil {
		t.Fatal("account credentials should refuse oauth2")
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
	lower := strings.ToLower(payload.Error)
	oauthHint := strings.Contains(lower, "oauth")
	reauthHint := strings.Contains(lower, "reauth")
	if !oauthHint && !reauthHint {
		t.Fatalf("error = %q, want oauth/reauth guidance", payload.Error)
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Accounts.LoadCredential(ctx, created.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load oauth credential: %v", err)
	}
	if got.AccessToken != "access" || got.RefreshToken != "refresh" {
		t.Fatalf("oauth credential mutated: %+v", got)
	}
}

func TestAccountCredentialsMissingEnvLeavesPreviousSecret(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/dav/",
		Username: "alice", AuthType: "basic",
	}, auth.Credential{Password: "old-password"}, store)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	a.Close()

	t.Setenv("CHRONCAL_PASSWORD", "")
	_, stderr, err := runChroncalCommand(t,
		"account", "credentials", "Work",
		"--output", "json", "--allow-plaintext",
	)
	if err == nil {
		t.Fatal("credentials without CHRONCAL_PASSWORD should fail")
	}
	if !strings.Contains(stderr, "CHRONCAL_PASSWORD") {
		t.Fatalf("missing-env error = %q, want CHRONCAL_PASSWORD guidance", stderr)
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Accounts.LoadCredential(ctx, created.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if got.Password != "old-password" {
		t.Fatalf("password = %q, want old-password left unchanged", got.Password)
	}
}

func TestAccountCredentialsRepairsBrokenKeyring(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	ctx := context.Background()
	a := openPlaintextApp(t, dbPath)
	store := openPlaintextStore(t, a)
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/dav/",
		Username: "alice", AuthType: "bearer",
	}, auth.Credential{AccessToken: "old-token"}, store)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := store.Delete(created.ID); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	a.Close()

	t.Setenv("CHRONCAL_BEARER_TOKEN", "repaired-token")
	if _, _, err := runChroncalCommand(t,
		"account", "credentials", "Work",
		"--output", "json", "--allow-plaintext",
	); err != nil {
		t.Fatalf("credentials with missing keyring entry: %v", err)
	}

	a = openPlaintextApp(t, dbPath)
	defer a.Close()
	got, err := a.Accounts.LoadCredential(ctx, created.ID, openPlaintextStore(t, a))
	if err != nil {
		t.Fatalf("load repaired credential: %v", err)
	}
	if got.AccessToken != "repaired-token" {
		t.Fatalf("access token = %q, want repaired-token", got.AccessToken)
	}
}

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

// oauthFlowCall records the client config one stubbed OAuth flow run used.
type oauthFlowCall struct {
	clientID     string
	clientSecret string
}

// stubGoogleOAuthFlow swaps the OAuth flow seam for a canned result so tests
// exercise reauth without binding a loopback listener or opening a browser.
// The returned slice pointer records the client config every run used.
func stubGoogleOAuthFlow(t *testing.T, result *auth.GoogleOAuthResult) *[]oauthFlowCall {
	t.Helper()
	var calls []oauthFlowCall
	prev := runGoogleOAuthFlow
	runGoogleOAuthFlow = func(_ context.Context, clientID, clientSecret string) (*auth.GoogleOAuthResult, error) {
		calls = append(calls, oauthFlowCall{clientID: clientID, clientSecret: clientSecret})
		return result, nil
	}
	t.Cleanup(func() { runGoogleOAuthFlow = prev })
	return &calls
}

// runAccountCommandInProcess executes one account command tree in this
// process so package-var seams (the stubbed OAuth flow) apply. It mirrors
// TestHelperProcess: a fresh root whose PersistentPreRunE reloads cfg from
// CHRONCAL_DB, with the real --output/--allow-plaintext flag closures.
func runAccountCommandInProcess(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	prevFmt, prevPlaintext := outputFmt, allowPlaintext
	t.Cleanup(func() { outputFmt, allowPlaintext = prevFmt, prevPlaintext })

	root := &cobra.Command{
		Use: "chroncal-test",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			cfg, err = config.Load()
			return err
		},
	}
	root.PersistentFlags().StringVarP(&outputFmt, "output", "o", "text", "output format (text, json)")
	root.PersistentFlags().BoolVar(&allowPlaintext, "allow-plaintext", false, "permit storing credentials in plaintext when no OS keyring is available")
	root.AddCommand(accountCmd())
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func openPlaintextApp(t *testing.T, dbPath string) *app.App {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	a.AllowPlaintext = true
	return a
}

func openPlaintextStore(t *testing.T, a *app.App) auth.CredentialStore {
	t.Helper()
	store, err := auth.NewCredentialStore(
		a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, true,
	)
	if err != nil {
		t.Fatalf("credential store: %v", err)
	}
	return store
}

func assertAccountJSONWithoutSecrets(t *testing.T, stdout, displayName, authType string) {
	t.Helper()
	var got jsonAccount
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode account json: %v\n%s", err, stdout)
	}
	if got.DisplayName != displayName || got.AuthType != authType || got.ID == 0 {
		t.Fatalf("account json = %+v, want %s %s", got, displayName, authType)
	}
	if strings.Contains(stdout, `"password"`) || strings.Contains(stdout, `"access_token"`) ||
		strings.Contains(stdout, `"refresh_token"`) {
		t.Fatalf("account json includes secret keys: %s", stdout)
	}
}

func setupDiscoveredAccountCLI(t *testing.T, name string) (string, string) {
	t.Helper()
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	srv := newAccountDiscoveryServer(t)
	if _, _, err := runChroncalCommand(t,
		"account", "add", name,
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("account add: %v", err)
	}
	return dbPath, srv.URL + "/"
}

func assertLinkedCalendarNames(t *testing.T, dbPath string, want ...string) {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	var got []string
	for _, item := range calendars {
		if item.AccountID != 0 {
			got = append(got, item.Name)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("linked calendars = %v, want %v", got, want)
	}
}

func assertLinkedCalendarsSynced(t *testing.T, dbPath string) {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	for _, item := range calendars {
		if item.AccountID != 0 && item.LastSyncAt == "" {
			t.Fatalf("linked calendar %q has no successful initial sync", item.Name)
		}
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
	for _, calendar := range calendars {
		if calendar.AccountID != 0 || calendar.RemoteURL != "" {
			t.Fatalf("calendar still linked after account removal: %+v", calendar)
		}
	}
}

func newAccountDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newAccountDiscoveryServerWithInterceptor(t, nil)
}

type accountDiscoveryInterceptor func(http.ResponseWriter, *http.Request) bool

func newAccountDiscoveryServerWithInterceptor(
	t *testing.T,
	intercept accountDiscoveryInterceptor,
) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if intercept != nil && intercept(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		var body string
		switch r.URL.Path {
		case "/":
			body = accountMultistatus(`/`, `<d:current-user-principal><d:href>/principals/me/</d:href></d:current-user-principal>`)
		case "/principals/me/":
			body = accountMultistatus(`/principals/me/`, `<c:calendar-home-set><d:href>/calendars/me/</d:href></c:calendar-home-set>`)
		case "/calendars/me/":
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response><d:href>/calendars/me/personal/</d:href><d:propstat><d:prop>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Personal</d:displayname>
    <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/calendars/me/holidays/</d:href><d:propstat><d:prop>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Holidays in Brazil</d:displayname>
    <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/calendars/me/freebusy/</d:href><d:propstat><d:prop>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Availability</d:displayname>
    <c:supported-calendar-component-set><c:comp name="VFREEBUSY"/></c:supported-calendar-component-set>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`
		case "/calendars/me/personal/":
			body = accountCalendarMetadata("Personal", "#112233", "write")
		case "/calendars/me/holidays/":
			body = accountCalendarMetadata("Holidays in Brazil", "#445566", "read")
		case "/calendars/me/freebusy/":
			body = accountCalendarMetadata("Availability", "#778899", "read")
		default:
			t.Errorf("unexpected discovery request path %q", r.URL.Path)
			body = accountMultistatus(r.URL.Path, "")
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func accountMultistatus(href, prop string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop>
  <d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, href, prop)
}

func accountCalendarMetadata(name, color, privilege string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ic="http://apple.com/ns/ical/">
  <d:response><d:href>/</d:href><d:propstat><d:prop>
    <d:displayname>%s</d:displayname><d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
    <d:current-user-privilege-set><d:privilege><d:%s/></d:privilege></d:current-user-privilege-set>
    <ic:calendar-color>%s</ic:calendar-color>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, name, privilege, color)
}

// resolveDiscoveredSelections turns user-facing names/paths into collection
// paths. Ambiguous names, unknown references, and unsupported component types
// are rejected up front so Import never receives an invalid selection.
func TestResolveDiscoveredSelectionsRejectsAmbiguousAndUnknown(t *testing.T) {
	t.Parallel()
	discovery := account.Discovery{Calendars: []account.DiscoveredCalendar{
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/a/", Name: "Shared"}, Importable: true},
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/b/", Name: "Shared"}, Importable: true},
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/c/", Name: "Availability", SupportedComponentSet: []string{"VFREEBUSY"}}, Importable: false},
	}}

	if _, err := resolveDiscoveredSelections(discovery, []string{"Shared"}); err == nil {
		t.Fatal("ambiguous calendar name should be rejected")
	}
	if _, err := resolveDiscoveredSelections(discovery, []string{"Missing"}); err == nil {
		t.Fatal("unknown calendar reference should be rejected")
	}
	if _, err := resolveDiscoveredSelections(discovery, []string{"Availability"}); err == nil {
		t.Fatal("unsupported component calendar should be rejected")
	}

	paths, err := resolveDiscoveredSelections(discovery, []string{"/cal/a/"})
	if err != nil {
		t.Fatalf("select by remote path: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/cal/a/" {
		t.Fatalf("resolved paths = %v, want [/cal/a/]", paths)
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
