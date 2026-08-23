package account

import (
	"context"
	"database/sql"
	"errors"

	"slices"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
)

func TestServiceStoreCredentialWaitsForAccountLifecycle(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "old"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	release, err := synclock.Account(ctx, db, account.ID)
	if err != nil {
		t.Fatalf("lock account lifecycle: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- svc.StoreCredential(ctx, account.ID, account.CredentialFingerprint(), auth.Credential{Password: "new"}, store)
	}()
	select {
	case err := <-done:
		release()
		t.Fatalf("StoreCredential completed while lifecycle lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StoreCredential after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StoreCredential did not resume after lifecycle release")
	}
	if cred, err := store.Get(account.ID, ""); err != nil || cred.Password != "new" {
		t.Fatalf("stored credential = (%+v, %v), want new password", cred, err)
	}
}

func TestServiceStoreCredentialRejectsStaleConnectionIdentity(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://old.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "old"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oldFingerprint := account.CredentialFingerprint()
	if err := q.UpdateAccount(ctx, storage.UpdateAccountParams{
		ID: account.ID, Name: account.Name, ServerUrl: "https://new.example.test/", AuthType: "basic", Username: "alice",
	}); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	newFingerprint := auth.AccountFingerprint("https://new.example.test/", "basic", "alice")
	if err := store.Set(auth.Credential{
		AccountID: account.ID, AccountFingerprint: newFingerprint, Username: "alice", Password: "replacement",
	}); err != nil {
		t.Fatalf("seed replacement credential: %v", err)
	}

	err = svc.StoreCredential(ctx, account.ID, oldFingerprint, auth.Credential{Password: "stale-oauth"}, store)
	if !errors.Is(err, auth.ErrCredentialIdentityMismatch) {
		t.Fatalf("StoreCredential error = %v, want identity mismatch", err)
	}
	got, err := store.Get(account.ID, "")
	if err != nil {
		t.Fatalf("Get replacement credential: %v", err)
	}
	if got.Password != "replacement" {
		t.Fatalf("stale OAuth completion overwrote replacement credential: %+v", got)
	}
}

func TestServiceDeleteWaitsForAccountLifecycle(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	calendars, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := calendars[0].ID
	remoteURL := "https://cal.example.test/work/"
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: calendarID, AccountID: &account.ID, RemoteUrl: &remoteURL,
	}); err != nil {
		t.Fatalf("link calendar: %v", err)
	}

	release, err := synclock.Account(ctx, db, account.ID)
	if err != nil {
		t.Fatalf("lock account lifecycle: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.Delete(ctx, account.ID, store) }()
	select {
	case err := <-done:
		release()
		t.Fatalf("Delete completed while account lifecycle lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Delete after lifecycle release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Delete did not resume after lifecycle lock was released")
	}
}

func TestServiceDeleteWaitsForDiscoveryCredentialRefresh(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://apidata.googleusercontent.com/caldav", Username: "alice", AuthType: "oauth2",
	}, auth.Credential{Username: "alice", RefreshToken: "old"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	svc.discover = func(_ context.Context, _ Account, _ auth.Credential, persist func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		close(refreshStarted)
		<-releaseRefresh
		if err := persist(auth.Credential{RefreshToken: "new"}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	discovered := make(chan error, 1)
	go func() {
		_, err := svc.Discover(ctx, account.ID, store)
		discovered <- err
	}()
	<-refreshStarted

	deleted := make(chan error, 1)
	go func() { deleted <- svc.Delete(ctx, account.ID, store) }()
	select {
	case err := <-deleted:
		close(releaseRefresh)
		t.Fatalf("Delete completed during credential refresh: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRefresh)
	if err := <-discovered; err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("Delete after discovery: %v", err)
	}
	if _, err := store.Get(account.ID, ""); err == nil {
		t.Fatal("credential was recreated after account removal")
	}
}

func TestRemoteIdentityKeyNormalizesEquivalentCollectionURLs(t *testing.T) {
	t.Parallel()

	// Absolute Google URLs collapse regardless of trailing slash or %40 encoding.
	googleBase := "https://apidata.googleusercontent.com/caldav/v2"
	want := remoteIdentityKey("https://apidata.googleusercontent.com/caldav/v2/user@example.com/events", googleBase)
	for _, raw := range []string{
		"https://apidata.googleusercontent.com/caldav/v2/user@example.com/events/",
		"https://apidata.googleusercontent.com/caldav/v2/user%40example.com/events/",
	} {
		if got := remoteIdentityKey(raw, googleBase); got != want {
			t.Errorf("remoteIdentityKey(%q) = %q, want %q", raw, got, want)
		}
	}

	// A legacy absolute direct link and the server-relative path discovery
	// returns must reconcile to the same key so the row is reused, not duplicated.
	server := "https://cal.example.test/"
	abs := remoteIdentityKey("https://cal.example.test/cal/work", server)
	if got := remoteIdentityKey("/cal/work/", server); got != abs {
		t.Errorf("relative key %q != absolute key %q; legacy links would duplicate", got, abs)
	}
}

// validateServerURL guards the safety of the stored server URL. HTTPS by
// default. HTTP only behind an explicit opt-in. No query/fragment. A
// misconfigured endpoint then cannot smuggle parameters into discovery requests.
func TestValidateServerURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		raw           string
		allowInsecure bool
		wantErr       bool
	}{
		{"https accepted", "https://cal.example.test/dav/", false, false},
		{"http rejected without allow-insecure", "http://cal.example.test/dav/", false, true},
		{"http accepted with allow-insecure", "http://localhost:8080/dav/", true, false},
		{"query rejected", "https://cal.example.test/dav/?token=x", false, true},
		{"fragment rejected", "https://cal.example.test/dav/#section", false, true},
		{"missing scheme rejected", "cal.example.test/dav/", false, true},
		{"missing host rejected", "https://", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateServerURL(tc.raw, tc.allowInsecure)
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("validateServerURL(%q, %v) = nil, want error", tc.raw, tc.allowInsecure)
			case !tc.wantErr && err != nil:
				t.Fatalf("validateServerURL(%q, %v) = %v, want nil", tc.raw, tc.allowInsecure, err)
			}
		})
	}
}

// Create validates connection params before a touch of the database. A bad
// request then leaves no row behind.
func TestCreateRejectsInvalidConnectionParams(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)

	for _, tc := range []struct {
		name   string
		params CreateParams
		want   string
	}{
		{"empty name", CreateParams{ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic"}, "account name is required"},
		{"reserved legacy prefix", CreateParams{Name: "__calendar_work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic"}, "reserved prefix"},
		{"empty username", CreateParams{Name: "Work", ServerURL: "https://cal.example.test/", AuthType: "basic"}, "username is required"},
		{"invalid auth type", CreateParams{Name: "Work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "digest"}, "invalid auth type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), tc.params, auth.Credential{Username: "alice", Password: "secret"}, store)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Create %s err = %v, want containing %q", tc.name, err, tc.want)
			}
		})
	}

	accounts, err := q.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %d, want 0 after every rejection", len(accounts))
	}
}

// Discovery reconciliation must not clobber local edits. A user's rename and
// color change (with the dirty flag set) survive a refresh that reports the
// original remote metadata. The remote_* mirror columns still update. A
// collection that reappears then clears its gone flag.
func TestDiscoverReconciliationPreservesLocalColorAndNameEdits(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return []caldav.RemoteCalendar{{
			Path: "/cal/work/", Name: "Work", Color: "#112233", SupportedComponentSet: []string{"VEVENT"},
		}}, nil
	}
	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	result, err := svc.Import(ctx, discovery, []string{"/cal/work/"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	calID := result.CreatedIDs[0]

	// Simulate the user renaming the calendar and choosing a custom color.
	if _, err := db.ExecContext(ctx,
		"UPDATE calendars SET name = 'My Work', color = '#FF0000', color_dirty = 1 WHERE id = ?", calID,
	); err != nil {
		t.Fatalf("seed local edits: %v", err)
	}

	if _, err := svc.Discover(ctx, account.ID, store); err != nil {
		t.Fatalf("refresh Discover: %v", err)
	}

	cal, err := q.GetCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Name != "My Work" {
		t.Errorf("local rename clobbered by discovery: name = %q, want %q", cal.Name, "My Work")
	}
	if cal.Color != "#FF0000" {
		t.Errorf("local color clobbered by discovery: color = %q, want %q", cal.Color, "#FF0000")
	}
	if storage.NullableToString(cal.RemoteColor) != "#112233" {
		t.Errorf("remote color mirror not refreshed: %q, want #112233", storage.NullableToString(cal.RemoteColor))
	}
	if cal.RemoteMissing != 0 {
		t.Errorf("reappearing collection still marked missing: %d", cal.RemoteMissing)
	}
}

func TestDiscoverRemoteRenameCollisionPreservesLocalName(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	if _, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Taken", Color: "#111111"}); err != nil {
		t.Fatalf("create colliding local calendar: %v", err)
	}
	account, err := svc.Create(ctx, CreateParams{
		Name: "Remote", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	remoteName := "Original"
	remoteAccess := caldav.CalendarAccessWrite
	remoteComponents := []string{"VEVENT"}
	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return []caldav.RemoteCalendar{{
			Path: "/cal/work/", Name: remoteName, Access: remoteAccess, SupportedComponentSet: remoteComponents,
		}}, nil
	}
	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("initial Discover: %v", err)
	}
	result, err := svc.Import(ctx, discovery, []string{"/cal/work/"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	calID := result.CreatedIDs[0]

	remoteName = "Taken"
	remoteAccess = caldav.CalendarAccessRead
	remoteComponents = []string{"VTODO"}
	if _, err := svc.Discover(ctx, account.ID, store); err != nil {
		t.Fatalf("rename Discover: %v", err)
	}
	cal, err := q.GetCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if cal.Name != "Original" {
		t.Errorf("local name = %q, want collision-preserving %q", cal.Name, "Original")
	}
	if cal.RemoteName != "Taken" || cal.RemoteAccess != "read" || cal.RemoteComponents != "VTODO" {
		t.Errorf("remote metadata did not reconcile after rename collision: %+v", cal)
	}
}

// Import defends its contract for callers that did not pre-filter. A path that
// was never discovered and a collection without a usable component type both
// fail. They persist no row.
func TestImportRejectsUnknownPathAndUnsupportedComponents(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	discovery := Discovery{Account: account, Calendars: []DiscoveredCalendar{
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/work/", Name: "Work", SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/avail/", Name: "Availability", SupportedComponentSet: []string{"VFREEBUSY"}}, Importable: false},
	}}

	if _, err := svc.Import(ctx, discovery, []string{"/cal/missing/"}); err == nil || !strings.Contains(err.Error(), "was not part of this discovery") {
		t.Fatalf("unknown path import err = %v", err)
	}
	if _, err := svc.Import(ctx, discovery, []string{"/cal/avail/"}); err == nil || !strings.Contains(err.Error(), "no supported event, todo, or journal components") {
		t.Fatalf("unsupported import err = %v", err)
	}

	cals, err := q.ListCalendarsByAccount(ctx, &account.ID)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	if len(cals) != 0 {
		t.Fatalf("calendars = %d, want 0 after failed imports", len(cals))
	}
}

func TestRenameUpdatesOnlyAccountDescription(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	created, err := svc.Create(ctx, CreateParams{
		Name: "Google", ServerURL: "https://cal.example.test/dav/", AuthType: "basic", Username: "alice@example.test",
	}, auth.Credential{Username: "alice@example.test", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fingerprint := created.CredentialFingerprint()

	renamed, err := svc.Rename(ctx, created.ID, "  Personal Google  ")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != "Personal Google" || renamed.DisplayName != "Personal Google" {
		t.Fatalf("renamed account = %+v", renamed)
	}
	if renamed.CredentialFingerprint() != fingerprint {
		t.Fatalf("rename changed credential identity: %q != %q", renamed.CredentialFingerprint(), fingerprint)
	}
	if _, err := store.Get(created.ID, fingerprint); err != nil {
		t.Fatalf("rename lost credential: %v", err)
	}
}

func TestRenameRejectsEmptyAndDuplicateDescriptions(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	first, err := svc.Create(ctx, CreateParams{
		Name: "First", ServerURL: "https://one.example.test/dav/", AuthType: "basic", Username: "first",
	}, auth.Credential{Username: "first", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if _, err := svc.Create(ctx, CreateParams{
		Name: "Second", ServerURL: "https://two.example.test/dav/", AuthType: "basic", Username: "second",
	}, auth.Credential{Username: "second", Password: "secret"}, store); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	if _, err := svc.Rename(ctx, first.ID, " "); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty Rename error = %v", err)
	}
	if _, err := svc.Rename(ctx, first.ID, "Second"); err == nil {
		t.Fatal("duplicate Rename succeeded")
	}
	got, err := svc.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get after rejected rename: %v", err)
	}
	if got.Name != "First" {
		t.Fatalf("rejected rename changed account to %q", got.Name)
	}
}

func TestSetOrderPersistsAccountSectionOrder(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	first, err := svc.Create(ctx, CreateParams{
		Name: "Alpha", ServerURL: "https://one.example.test/dav/", AuthType: "basic", Username: "first",
	}, auth.Credential{Username: "first", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := svc.Create(ctx, CreateParams{
		Name: "Zulu", ServerURL: "https://two.example.test/dav/", AuthType: "basic", Username: "second",
	}, auth.Credential{Username: "second", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	if err := svc.SetOrder(ctx, []int64{second.ID, first.ID}); err != nil {
		t.Fatalf("SetOrder: %v", err)
	}
	got, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Fatalf("account order = %+v, want IDs [%d %d]", got, second.ID, first.ID)
	}
}

func TestUserFacingNameHidesCredentialIdentifiers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		username string
		id       int64
		want     string
	}{
		{"__calendar_42", "alice@example.com", 42, "Example"},
		{"__calendar_42", "  ", 42, "Remote account 42"},
		{"alice@example.com", "alice@example.com", 42, "Example"},
		{"Google", "alice@example.com", 42, "Google"},
	} {
		if got := UserFacingName(tc.name, tc.username, tc.id); got != tc.want {
			t.Errorf("UserFacingName(%q, %q, %d) = %q, want %q", tc.name, tc.username, tc.id, got, tc.want)
		}
	}
}

// Import must keep calendars.name UNIQUE even when two discovered collections
// share a remote display name. The second gets a suffixed local name. Both
// rows preserve the pristine remote name. The owner email comes from the
// account username.
func TestImportGeneratesUniqueLocalNamesForCollisions(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
		Username: "owner@example.test", AuthType: "oauth2",
	}, auth.Credential{Username: "owner@example.test", AccessToken: "tok"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	discovery := Discovery{Account: account, Calendars: []DiscoveredCalendar{
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/a/", Name: "Shared", SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/b/", Name: "Shared", SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/c/", Name: "Shared", SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
	}}

	if _, err := svc.Import(ctx, discovery, []string{"/cal/a/", "/cal/b/", "/cal/c/"}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	calendars, err := q.ListCalendarsByAccount(ctx, &account.ID)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	wantNames := map[string]bool{"Shared": false, "Shared (2)": false, "Shared (3)": false}
	for _, cal := range calendars {
		if cal.RemoteName != "Shared" {
			t.Errorf("calendar %q has remote_name %q, want pristine %q", cal.Name, cal.RemoteName, "Shared")
		}
		if cal.OwnerEmail != account.Username {
			t.Errorf("calendar %q owner_email = %q, want %q", cal.Name, cal.OwnerEmail, account.Username)
		}
		wantNames[cal.Name] = true
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("expected a calendar named %q; got names %#v", name, calendarNames(calendars))
		}
	}
}

// A calendar linked to an account before discovery (legacy direct link) stores
// an absolute remote_url. Discovery returns a server-relative path for the same
// collection. They must reconcile to one row instead of a duplicate. The
// user-customized local name survives the first refresh. The remote_name
// mirror is seeded from discovery.
func TestDiscoverReconcilesLegacyDirectLinkAndPreservesLocalName(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Legacy calendar: absolute remote URL, empty remote_name, user-chosen name.
	legacyName := "My Cal"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO calendars (name, account_id, remote_url, remote_name) VALUES (?, ?, ?, '')`,
		legacyName, account.ID, "https://cal.example.test/cal/work"); err != nil {
		t.Fatalf("seed legacy calendar: %v", err)
	}
	legacyID := int64(2) // seeded 'Personal' is id 1

	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return []caldav.RemoteCalendar{{
			Path: "/cal/work/", Name: "Work", SupportedComponentSet: []string{"VEVENT"},
		}}, nil
	}
	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovery.Calendars) != 1 {
		t.Fatalf("discovered = %d, want 1", len(discovery.Calendars))
	}
	if !discovery.Calendars[0].Imported || discovery.Calendars[0].CalendarID != legacyID {
		t.Fatalf("legacy calendar not reconciled: %+v", discovery.Calendars[0])
	}

	calendars, err := q.ListCalendarsByAccount(ctx, &account.ID)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	if len(calendars) != 1 {
		t.Fatalf("calendars = %d, want 1 (absolute link must not duplicate)", len(calendars))
	}
	cal := calendars[0]
	if cal.ID != legacyID {
		t.Errorf("calendar id = %d, want legacy id %d (row must be reused)", cal.ID, legacyID)
	}
	if cal.Name != legacyName {
		t.Errorf("legacy local name clobbered on first refresh: %q, want %q", cal.Name, legacyName)
	}
	if cal.RemoteName != "Work" {
		t.Errorf("remote_name mirror not seeded: %q, want %q", cal.RemoteName, "Work")
	}
	if cal.RemoteMissing != 0 {
		t.Errorf("reconciled calendar still marked missing: %d", cal.RemoteMissing)
	}
}

// Create guards the oauth2 security boundary: oauth2 is only valid against
// Google's CalDAV host. A non-Google server must be rejected before any account
// or credential is written.
func TestCreateRejectsNonGoogleOAuth2WithoutPersisting(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)

	_, err = svc.Create(ctx, CreateParams{
		Name: "Not Google", ServerURL: "https://cal.example.test/dav/",
		Username: "alice", AuthType: "oauth2",
	}, auth.Credential{Username: "alice", AccessToken: "tok"}, store)
	if err == nil || !strings.Contains(err.Error(), "Google") {
		t.Fatalf("Create non-Google oauth2 err = %v, want a Google-only rejection", err)
	}
	if accounts, _ := q.ListAccounts(ctx); len(accounts) != 0 {
		t.Fatalf("rejected Create persisted %d accounts, want 0", len(accounts))
	}
	if len(store.credentials) != 0 {
		t.Fatalf("rejected Create persisted %d credentials, want 0", len(store.credentials))
	}
}

// Create guards the server-URL boundary. Plain HTTP without the insecure
// opt-in, query/fragment, and embedded userinfo are all rejected. That happens
// before any account or credential is written.
func TestCreateRejectsUnsafeServerURLWithoutPersisting(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{"plain http without allow-insecure", "http://cal.example.test/dav/", "must use HTTPS"},
		{"query string", "https://cal.example.test/dav/?token=x", "query"},
		{"fragment", "https://cal.example.test/dav/#section", "fragment"},
		{"embedded userinfo", "https://alice:secret@cal.example.test/dav/", "must not include credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryCredentialStore()
			svc := NewService(db, q)
			_, err := svc.Create(ctx, CreateParams{
				Name: "Unsafe", ServerURL: tc.url, Username: "alice", AuthType: "basic",
			}, auth.Credential{Username: "alice", Password: "secret"}, store)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Create %s err = %v, want containing %q", tc.name, err, tc.want)
			}
			if accounts, _ := q.ListAccounts(ctx); len(accounts) != 0 {
				t.Fatalf("rejected Create persisted %d accounts, want 0", len(accounts))
			}
			if len(store.credentials) != 0 {
				t.Fatalf("rejected Create persisted %d credentials, want 0", len(store.credentials))
			}
		})
	}
}

func TestServiceRemoveWithCalendarsDeletesAccountCalendarsAndCredential(t *testing.T) {
	f := newSelectionFixture(t)
	imported, _ := f.importAndRefresh(t, "/cal/a/", "/cal/b/")

	result, err := f.svc.RemoveWithCalendars(
		context.Background(),
		f.discovery.Account.ID,
		RemoveParams{},
		f.store,
	)
	if err != nil {
		t.Fatalf("RemoveWithCalendars: %v", err)
	}
	if !slices.Equal(result.RemovedIDs, imported.CreatedIDs) {
		t.Fatalf("removed IDs = %v, want %v", result.RemovedIDs, imported.CreatedIDs)
	}
	if _, err := f.q.GetAccount(context.Background(), f.discovery.Account.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed account lookup err = %v, want sql.ErrNoRows", err)
	}
	rows, err := f.q.ListCalendarsByAccount(context.Background(), &f.discovery.Account.ID)
	if err != nil {
		t.Fatalf("list removed account calendars: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("account calendars remain: %+v", rows)
	}
	if _, ok := f.store.credentials[f.discovery.Account.ID]; ok {
		t.Fatal("removed account credential remains stored")
	}
	all, err := f.q.ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("list surviving calendars: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("unrelated local calendar was removed")
	}
}

func TestServiceRemoveWithCalendarsRequiresAndPromotesExternalDefault(t *testing.T) {
	f := newSelectionFixture(t)
	imported, _ := f.importAndRefresh(t, "/cal/a/")
	ctx := context.Background()
	all, err := f.q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	var replacementID int64
	for _, row := range all {
		if row.ID != imported.CreatedIDs[0] {
			replacementID = row.ID
			break
		}
	}
	if replacementID == 0 {
		t.Fatal("fixture has no external replacement calendar")
	}
	if err := f.q.ClearDefaultCalendar(ctx); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	if err := f.q.SetCalendarAsDefault(ctx, imported.CreatedIDs[0]); err != nil {
		t.Fatalf("set account calendar default: %v", err)
	}

	if _, err := f.svc.RemoveWithCalendars(ctx, f.discovery.Account.ID, RemoveParams{}, f.store); !errors.Is(err, calendar.ErrDefaultCalendarRequiresPromotion) {
		t.Fatalf("RemoveWithCalendars without replacement error = %v", err)
	}
	if _, err := f.svc.RemoveWithCalendars(ctx, f.discovery.Account.ID, RemoveParams{NewDefaultID: replacementID}, f.store); err != nil {
		t.Fatalf("RemoveWithCalendars with replacement: %v", err)
	}
	replacement, err := f.q.GetCalendar(ctx, replacementID)
	if err != nil {
		t.Fatalf("get replacement: %v", err)
	}
	if replacement.IsDefault != 1 {
		t.Fatalf("replacement IsDefault = %d, want 1", replacement.IsDefault)
	}
}

func TestServiceRemoveWithCalendarsRollsBackCredentialFailure(t *testing.T) {
	f := newSelectionFixture(t)
	imported, _ := f.importAndRefresh(t, "/cal/a/")
	f.store.deleteErr = errors.New("keyring delete failed")

	_, err := f.svc.RemoveWithCalendars(context.Background(), f.discovery.Account.ID, RemoveParams{}, f.store)
	if !errors.Is(err, f.store.deleteErr) {
		t.Fatalf("RemoveWithCalendars error = %v, want credential failure", err)
	}
	if _, err := f.q.GetAccount(context.Background(), f.discovery.Account.ID); err != nil {
		t.Fatalf("account was removed after rollback: %v", err)
	}
	if _, err := f.q.GetCalendar(context.Background(), imported.CreatedIDs[0]); err != nil {
		t.Fatalf("calendar was removed after rollback: %v", err)
	}
	if _, ok := f.store.credentials[f.discovery.Account.ID]; !ok {
		t.Fatal("credential was not restored after rollback")
	}
}

func TestServiceRemoveWithCalendarsRefusesLastCalendar(t *testing.T) {
	f := newSelectionFixture(t)
	imported, _ := f.importAndRefresh(t, "/cal/a/")
	ctx := context.Background()
	all, err := f.q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	for _, row := range all {
		if row.ID != imported.CreatedIDs[0] {
			if err := f.q.DeleteCalendar(ctx, row.ID); err != nil {
				t.Fatalf("delete fixture calendar: %v", err)
			}
		}
	}
	if err := f.q.SetCalendarAsDefault(ctx, imported.CreatedIDs[0]); err != nil {
		t.Fatalf("set sole default: %v", err)
	}

	_, err = f.svc.RemoveWithCalendars(ctx, f.discovery.Account.ID, RemoveParams{}, f.store)
	if !errors.Is(err, calendar.ErrLastCalendar) {
		t.Fatalf("RemoveWithCalendars error = %v, want calendar.ErrLastCalendar", err)
	}
	if _, err := f.q.GetCalendar(ctx, imported.CreatedIDs[0]); err != nil {
		t.Fatalf("last calendar was removed: %v", err)
	}
}
