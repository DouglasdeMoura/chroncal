package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

type memoryCredentialStore struct {
	credentials map[int64]auth.Credential
	getErr      error
	setErr      error
	deleteErr   error
	deleteCalls int
}

func newMemoryCredentialStore() *memoryCredentialStore {
	return &memoryCredentialStore{credentials: make(map[int64]auth.Credential)}
}

func (s *memoryCredentialStore) Get(accountID int64, _ string) (auth.Credential, error) {
	if s.getErr != nil {
		return auth.Credential{}, s.getErr
	}
	cred, ok := s.credentials[accountID]
	if !ok {
		return auth.Credential{}, fmt.Errorf("credential %d not found", accountID)
	}
	return cred, nil
}

func (s *memoryCredentialStore) Set(cred auth.Credential) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.credentials[cred.AccountID] = cred
	return nil
}

func (s *memoryCredentialStore) Delete(accountID int64) error {
	s.deleteCalls++
	delete(s.credentials, accountID)
	return s.deleteErr
}

func TestServiceCreateRollsBackWhenCredentialStorageFails(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := newMemoryCredentialStore()
	store.setErr = errors.New("keyring unavailable")
	svc := NewService(db, q)

	_, err = svc.Create(context.Background(), CreateParams{
		Name:          "Work",
		ServerURL:     "https://cal.example.test/dav/",
		Username:      "alice",
		AuthType:      "basic",
		AllowInsecure: false,
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err == nil || !errors.Is(err, store.setErr) {
		t.Fatalf("Create error = %v, want credential-store failure", err)
	}

	accounts, err := q.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("account count = %d, want rollback to zero", len(accounts))
	}
}

func TestServiceDiscoversAndImportsSelectedCalendars(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	account, err := svc.Create(ctx, CreateParams{
		Name: "Google", ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
		Username: "me@example.test", AuthType: "oauth2",
	}, auth.Credential{Username: "me@example.test", AccessToken: "token"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc.discover = func(_ context.Context, got Account, cred auth.Credential, persist func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		if got.ID != account.ID || got.ServerURL != "https://apidata.googleusercontent.com/caldav/v2/" {
			t.Fatalf("discovery account = %+v", got)
		}
		if cred.AccountID != account.ID || cred.AccessToken != "token" {
			t.Fatalf("discovery credential = %+v", cred)
		}
		return []caldav.RemoteCalendar{
			{Path: "/cal/me/primary/", Name: "Personal", Color: "#112233", Access: caldav.CalendarAccessOwner, SupportedComponentSet: []string{"VEVENT"}},
			{Path: "/cal/me/family/", Name: "Família", Description: "Shared", Color: "#445566", Access: caldav.CalendarAccessWrite, SupportedComponentSet: []string{"VEVENT", "VTODO"}},
			{Path: "/cal/me/freebusy/", Name: "Availability", SupportedComponentSet: []string{"VFREEBUSY"}},
		}, nil
	}

	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovery.Calendars) != 3 {
		t.Fatalf("calendar count = %d, want 3", len(discovery.Calendars))
	}
	if !discovery.Calendars[0].Importable || !discovery.Calendars[1].Importable || discovery.Calendars[2].Importable {
		t.Fatalf("importable flags = %#v", discovery.Calendars)
	}

	result, err := svc.Import(ctx, discovery, []string{"/cal/me/primary/", "/cal/me/family/"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(result.CreatedIDs) != 2 || len(result.ExistingIDs) != 0 {
		t.Fatalf("first import = %+v, want two created", result)
	}

	calendars, err := q.ListCalendarsByAccount(ctx, &account.ID)
	if err != nil {
		t.Fatalf("list imported calendars: %v", err)
	}
	if len(calendars) != 2 {
		t.Fatalf("imported calendar count = %d, want 2", len(calendars))
	}
	family := calendars[1]
	if family.Name != "Família" || family.RemoteName != "Família" || family.RemoteAccess != "write" || family.RemoteComponents != "VEVENT,VTODO" {
		t.Fatalf("imported family calendar = %+v", family)
	}
	if family.OwnerEmail != account.Username {
		t.Errorf("imported owner_email = %q, want account username %q", family.OwnerEmail, account.Username)
	}
	// The first discovered calendar is named "Personal", which collides with the
	// calendar seeded by migration 001; the UNIQUE name constraint must be
	// honored by appending a suffix while the pristine name is kept in remote_name.
	primary := calendars[0]
	if primary.Name != "Personal (2)" || primary.RemoteName != "Personal" {
		t.Errorf("collision handling: name=%q remote_name=%q, want %q/%q", primary.Name, primary.RemoteName, "Personal (2)", "Personal")
	}

	result, err = svc.Import(ctx, discovery, []string{"/cal/me/family/"})
	if err != nil {
		t.Fatalf("repeat Import: %v", err)
	}
	if len(result.CreatedIDs) != 0 || len(result.ExistingIDs) != 1 {
		t.Fatalf("repeat import = %+v, want one existing", result)
	}
}

func TestServiceDiscoverWithCredentialReplacesOnlyAfterSuccessfulDiscovery(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	configured, err := svc.Create(ctx, CreateParams{
		Name: "Google", ServerURL: "https://apidata.googleusercontent.com/caldav/v2/", Username: "alice", AuthType: "oauth2",
	}, auth.Credential{Username: "alice", AccessToken: "old", RefreshToken: "refresh"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc.discover = func(_ context.Context, _ Account, cred auth.Credential, _ func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		if cred.AccessToken != "new" || cred.RefreshToken != "refresh" {
			t.Fatalf("discovery credential = %+v, want replacement access token and preserved refresh token", cred)
		}
		return []caldav.RemoteCalendar{{
			Path: "/work/", Name: "Work", SupportedComponentSet: []string{"VEVENT"},
		}}, nil
	}

	if _, err := svc.DiscoverWithCredential(ctx, configured.ID, auth.Credential{Username: "alice", AccessToken: "new"}, store); err != nil {
		t.Fatalf("DiscoverWithCredential: %v", err)
	}
	if got := store.credentials[configured.ID]; got.AccessToken != "new" || got.RefreshToken != "refresh" {
		t.Fatalf("stored credential = %+v, want replacement access token and preserved refresh token", got)
	}
}

func TestServiceDiscoverWithCredentialRestoresPreviousCredentialOnFailure(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	configured, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "old"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	discoveryErr := errors.New("authentication failed")
	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return nil, discoveryErr
	}

	if _, err := svc.DiscoverWithCredential(ctx, configured.ID, auth.Credential{Username: "alice", Password: "wrong"}, store); !errors.Is(err, discoveryErr) {
		t.Fatalf("DiscoverWithCredential error = %v, want %v", err, discoveryErr)
	}
	if got := store.credentials[configured.ID].Password; got != "old" {
		t.Fatalf("stored password after failure = %q, want previous credential", got)
	}
}

func TestServiceRefreshMarksMissingOnlyAfterCompleteDiscovery(t *testing.T) {
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

	all := []caldav.RemoteCalendar{
		{Path: "/cal/one/", Name: "One", SupportedComponentSet: []string{"VEVENT"}},
		{Path: "/cal/two/", Name: "Two", SupportedComponentSet: []string{"VEVENT"}},
	}
	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return slices.Clone(all), nil
	}
	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("initial Discover: %v", err)
	}
	if _, err := svc.Import(ctx, discovery, []string{"/cal/one/", "/cal/two/"}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		refreshed := slices.Clone(all[1:])
		refreshed[0].Path = "/cal/two" // Equivalent collection URL without a trailing slash.
		return refreshed, nil
	}
	refreshed, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("refresh Discover: %v", err)
	}
	if len(refreshed.Calendars) != 2 {
		t.Fatalf("refreshed discovery count = %d, want found and missing imported calendars", len(refreshed.Calendars))
	}
	var missing DiscoveredCalendar
	for _, item := range refreshed.Calendars {
		if item.Path == "/cal/one/" {
			missing = item
			break
		}
	}
	if !missing.Imported || !missing.Missing || missing.CalendarID == 0 || missing.Name != "One" {
		t.Fatalf("missing imported calendar = %+v", missing)
	}
	calendars, err := q.ListCalendarsByAccount(ctx, &account.ID)
	if err != nil {
		t.Fatalf("list after refresh: %v", err)
	}
	if calendars[0].RemoteMissing != 1 || calendars[1].RemoteMissing != 0 {
		t.Fatalf("missing flags = %d, %d; want 1, 0", calendars[0].RemoteMissing, calendars[1].RemoteMissing)
	}

	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return nil, errors.New("temporary server failure")
	}
	if _, err := svc.Discover(ctx, account.ID, store); err == nil {
		t.Fatal("failed discovery should return an error")
	}
	calendars, err = q.ListCalendarsByAccount(ctx, &account.ID)
	if err != nil {
		t.Fatalf("list after failed refresh: %v", err)
	}
	if calendars[0].RemoteMissing != 1 || calendars[1].RemoteMissing != 0 {
		t.Fatalf("failed refresh changed missing flags to %d, %d", calendars[0].RemoteMissing, calendars[1].RemoteMissing)
	}
}

func TestServiceDeletePreservesCalendarsAsLocal(t *testing.T) {
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
		return []caldav.RemoteCalendar{{Path: "/cal/work/", Name: "Work", SupportedComponentSet: []string{"VEVENT"}}}, nil
	}
	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	result, err := svc.Import(ctx, discovery, []string{"/cal/work/"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	calendarID := result.CreatedIDs[0]
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "downloaded", OwnerType: "event",
		RemoteUrl: "/cal/work/downloaded.ics", Etag: `"server"`, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("seed sync resource: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID, Uid: "deleted", RemoteUrl: "/cal/work/deleted.ics",
	}); err != nil {
		t.Fatalf("seed tombstone: %v", err)
	}
	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calendarID, OwnerType: "event", Uid: "conflict",
		LocalIcal: "local", ServerIcal: "server", ServerEtag: `"conflict"`,
	}); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}

	if err := svc.Delete(ctx, account.ID, store); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	calendar, err := q.GetCalendar(ctx, result.CreatedIDs[0])
	if err != nil {
		t.Fatalf("get preserved calendar: %v", err)
	}
	if calendar.AccountID != nil || storage.NullableToString(calendar.RemoteUrl) != "" || calendar.RemoteName != "" {
		t.Fatalf("preserved calendar still linked: %+v", calendar)
	}
	if _, err := store.Get(account.ID, ""); err == nil {
		t.Fatal("credential should be removed with account")
	}
	resource, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "downloaded"})
	if err != nil {
		t.Fatalf("get detached sync resource: %v", err)
	}
	if resource.RemoteUrl != "" || resource.Etag != "" || resource.Dirty != 1 {
		t.Fatalf("detached sync resource = %+v, want blank identity and dirty local state", resource)
	}
	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("list tombstones: %v", err)
	}
	if len(tombstones) != 0 {
		t.Fatalf("stale tombstones survived account removal: %+v", tombstones)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("list conflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("stale conflicts survived account removal: %+v", conflicts)
	}
}

func TestServiceDeleteRollsBackWhenCredentialRemovalFails(t *testing.T) {
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
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "downloaded", OwnerType: "event",
		RemoteUrl: "/work/downloaded.ics", Etag: `"server"`, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("seed sync resource: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calendarID, Uid: "deleted", RemoteUrl: "/work/deleted.ics",
	}); err != nil {
		t.Fatalf("seed tombstone: %v", err)
	}
	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calendarID, OwnerType: "event", Uid: "conflict",
		LocalIcal: "local", ServerIcal: "server", ServerEtag: `"conflict"`,
	}); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}

	deleteErr := errors.New("keyring delete failed")
	store.deleteErr = deleteErr
	if err := svc.Delete(ctx, account.ID, store); !errors.Is(err, deleteErr) {
		t.Fatalf("Delete error = %v, want credential failure", err)
	}
	if _, err := q.GetAccount(ctx, account.ID); err != nil {
		t.Fatalf("account was not rolled back: %v", err)
	}
	calendar, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if calendar.AccountID == nil || *calendar.AccountID != account.ID || storage.NullableToString(calendar.RemoteUrl) != remoteURL {
		t.Fatalf("calendar link was not rolled back: %+v", calendar)
	}
	if _, err := store.Get(account.ID, ""); err != nil {
		t.Fatalf("credential was not restored after partial delete: %v", err)
	}
	resource, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "downloaded"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if resource.RemoteUrl != "/work/downloaded.ics" || resource.Etag != `"server"` || resource.Dirty != 0 {
		t.Fatalf("sync resource cleanup was not rolled back: %+v", resource)
	}
	if tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID); err != nil || len(tombstones) != 1 {
		t.Fatalf("tombstone rollback = (%+v, %v), want one row", tombstones, err)
	}
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID); err != nil || len(conflicts) != 1 {
		t.Fatalf("conflict rollback = (%+v, %v), want one row", conflicts, err)
	}
}

func TestServiceDeleteAbortsOnCredentialReadFailure(t *testing.T) {
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

	readErr := errors.New("keyring temporarily unavailable")
	store.getErr = readErr
	if err := svc.Delete(ctx, account.ID, store); !errors.Is(err, readErr) {
		t.Fatalf("Delete error = %v, want credential read failure", err)
	}
	if store.deleteCalls != 0 {
		t.Fatalf("credential Delete called %d times after failed read", store.deleteCalls)
	}
	if _, err := q.GetAccount(ctx, account.ID); err != nil {
		t.Fatalf("account changed after failed credential read: %v", err)
	}
	store.getErr = nil
	if cred, err := store.Get(account.ID, ""); err != nil || cred.Password != "secret" {
		t.Fatalf("credential changed after failed read: (%+v, %v)", cred, err)
	}
}

func TestServiceDeleteTreatsCredentialIdentityMismatchAsNoPreviousCredential(t *testing.T) {
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
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store.getErr = auth.ErrCredentialIdentityMismatch

	if err := svc.Delete(ctx, account.ID, store); err != nil {
		t.Fatalf("Delete after credential identity mismatch: %v", err)
	}
	if _, err := q.GetAccount(ctx, account.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetAccount error = %v, want deleted account", err)
	}
	if store.deleteCalls != 1 {
		t.Fatalf("credential Delete calls = %d, want 1", store.deleteCalls)
	}
}

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

// validateServerURL guards the safety of the stored server URL: HTTPS by
// default, HTTP only behind an explicit opt-in, and no query/fragment so a
// misconfigured endpoint can't smuggle parameters into discovery requests.
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

// Create validates connection params before touching the database so a bad
// request leaves no row behind.
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

// Discovery reconciliation must not clobber local edits: a user's rename and
// color change (with the dirty flag set) survive a refresh that reports the
// original remote metadata, while the remote_* mirror columns still update and
// a reappearing collection clears its missing flag.
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

// Import defends its contract for callers that did not pre-filter: a path that
// was never discovered and a collection without a usable component type both
// fail without persisting anything.
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

func TestSuggestedNameUsesProviderOrAccountDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		username string
		want     string
	}{
		{"maildodouglas@gmail.com", "Google"},
		{"douglas.moura@jaya.tech", "Jaya"},
		{"douglas.ademoura@familywellhealth.com", "Familywellhealth"},
		{"person@calendar.example.co.uk", "Example"},
		{"plain-user", "plain-user"},
	} {
		if got := SuggestedName(tc.username); got != tc.want {
			t.Errorf("SuggestedName(%q) = %q, want %q", tc.username, got, tc.want)
		}
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
// share a remote display name: the second gets a suffixed local name while both
// rows preserve the pristine remote name (and the owner email comes from the
// account username).
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

func calendarNames(rows []storage.Calendar) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// A calendar linked to an account before discovery (legacy direct link) stores
// an absolute remote_url. Discovery returns a server-relative path for the same
// collection; they must reconcile to one row instead of duplicating, the
// user-customized local name survives the first refresh, and the remote_name
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

// Create guards the server-URL boundary: plain HTTP without the insecure
// opt-in, query/fragment, and embedded userinfo are all rejected before any
// account or credential is written.
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

type selectionFixture struct {
	svc       *Service
	q         *storage.Queries
	store     *memoryCredentialStore
	discovery Discovery
}

func newSelectionFixture(t *testing.T) selectionFixture {
	t.Helper()
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	configured, err := svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/dav/",
		Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return []caldav.RemoteCalendar{
			{Path: "/cal/a/", Name: "A", Color: "#112233", Access: caldav.CalendarAccessOwner, SupportedComponentSet: []string{"VEVENT"}},
			{Path: "/cal/b/", Name: "B", Color: "#445566", Access: caldav.CalendarAccessWrite, SupportedComponentSet: []string{"VEVENT"}},
		}, nil
	}
	discovery, err := svc.Discover(ctx, configured.ID, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return selectionFixture{svc: svc, q: q, store: store, discovery: discovery}
}

func (f selectionFixture) importAndRefresh(t *testing.T, paths ...string) (ImportResult, Discovery) {
	t.Helper()
	ctx := context.Background()
	result, err := f.svc.Import(ctx, f.discovery, paths)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	discovery, err := f.svc.Discover(ctx, f.discovery.Account.ID, f.store)
	if err != nil {
		t.Fatalf("refresh Discover: %v", err)
	}
	return result, discovery
}

func TestReconcileSelectionAddsAndRemovesInOneFinalState(t *testing.T) {
	f := newSelectionFixture(t)
	imported, discovery := f.importAndRefresh(t, "/cal/a/")

	result, err := f.svc.ReconcileSelection(context.Background(), discovery, SelectionParams{
		SelectedPaths: []string{"/cal/b/"},
	}, f.store)
	if err != nil {
		t.Fatalf("ReconcileSelection: %v", err)
	}
	if len(result.CreatedIDs) != 1 || !slices.Equal(result.RemovedIDs, imported.CreatedIDs) || result.AccountRemoved {
		t.Fatalf("selection result = %+v", result)
	}
	if _, err := f.q.GetCalendar(context.Background(), imported.CreatedIDs[0]); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed calendar lookup err = %v, want sql.ErrNoRows", err)
	}
	rows, err := f.q.ListCalendarsByAccount(context.Background(), &discovery.Account.ID)
	if err != nil {
		t.Fatalf("list account calendars: %v", err)
	}
	if len(rows) != 1 || storage.NullableToString(rows[0].RemoteUrl) != "/cal/b/" {
		t.Fatalf("final account calendars = %+v, want only /cal/b/", rows)
	}
	if _, ok := f.store.credentials[discovery.Account.ID]; !ok {
		t.Fatal("non-empty account credential was removed")
	}
}

func TestReconcileSelectionRemovesEmptyAccountAndCredential(t *testing.T) {
	f := newSelectionFixture(t)
	imported, discovery := f.importAndRefresh(t, "/cal/a/")

	result, err := f.svc.ReconcileSelection(context.Background(), discovery, SelectionParams{}, f.store)
	if err != nil {
		t.Fatalf("ReconcileSelection: %v", err)
	}
	if !result.AccountRemoved || !slices.Equal(result.RemovedIDs, imported.CreatedIDs) {
		t.Fatalf("selection result = %+v", result)
	}
	if _, err := f.q.GetAccount(context.Background(), discovery.Account.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed account lookup err = %v, want sql.ErrNoRows", err)
	}
	if _, ok := f.store.credentials[discovery.Account.ID]; ok {
		t.Fatal("empty account credential remains stored")
	}
}

func TestReconcileSelectionCanPromoteNewlyAddedDefault(t *testing.T) {
	f := newSelectionFixture(t)
	imported, discovery := f.importAndRefresh(t, "/cal/a/")
	ctx := context.Background()
	if err := f.q.ClearDefaultCalendar(ctx); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	if err := f.q.SetCalendarAsDefault(ctx, imported.CreatedIDs[0]); err != nil {
		t.Fatalf("set imported default: %v", err)
	}

	result, err := f.svc.ReconcileSelection(ctx, discovery, SelectionParams{
		SelectedPaths:  []string{"/cal/b/"},
		NewDefaultPath: "/cal/b/",
	}, f.store)
	if err != nil {
		t.Fatalf("ReconcileSelection: %v", err)
	}
	if len(result.CreatedIDs) != 1 {
		t.Fatalf("created IDs = %v, want one", result.CreatedIDs)
	}
	replacement, err := f.q.GetCalendar(ctx, result.CreatedIDs[0])
	if err != nil {
		t.Fatalf("get replacement default: %v", err)
	}
	if replacement.IsDefault != 1 {
		t.Fatalf("replacement IsDefault = %d, want 1", replacement.IsDefault)
	}
}

func TestReconcileSelectionRejectsStaleImportedInventory(t *testing.T) {
	f := newSelectionFixture(t)
	_, discovery := f.importAndRefresh(t, "/cal/a/")
	if _, err := f.svc.Import(context.Background(), discovery, []string{"/cal/b/"}); err != nil {
		t.Fatalf("concurrent Import: %v", err)
	}

	_, err := f.svc.ReconcileSelection(context.Background(), discovery, SelectionParams{
		SelectedPaths: []string{"/cal/a/"},
	}, f.store)
	if !errors.Is(err, ErrSelectionStale) {
		t.Fatalf("ReconcileSelection stale err = %v, want ErrSelectionStale", err)
	}
	rows, listErr := f.q.ListCalendarsByAccount(context.Background(), &discovery.Account.ID)
	if listErr != nil {
		t.Fatalf("list account calendars: %v", listErr)
	}
	if len(rows) != 2 {
		t.Fatalf("stale reconciliation changed account calendars: %+v", rows)
	}
}

func TestReconcileSelectionRollsBackWhenCredentialRemovalFails(t *testing.T) {
	f := newSelectionFixture(t)
	imported, discovery := f.importAndRefresh(t, "/cal/a/")
	f.store.deleteErr = errors.New("keyring delete failed")

	_, err := f.svc.ReconcileSelection(context.Background(), discovery, SelectionParams{}, f.store)
	if !errors.Is(err, f.store.deleteErr) {
		t.Fatalf("ReconcileSelection err = %v, want credential delete failure", err)
	}
	if _, err := f.q.GetAccount(context.Background(), discovery.Account.ID); err != nil {
		t.Fatalf("account was removed after rollback: %v", err)
	}
	if _, err := f.q.GetCalendar(context.Background(), imported.CreatedIDs[0]); err != nil {
		t.Fatalf("calendar was removed after rollback: %v", err)
	}
	if _, ok := f.store.credentials[discovery.Account.ID]; !ok {
		t.Fatal("credential was not restored after rollback")
	}
}

func TestReconcileSelectionRefusesLastApplicationCalendar(t *testing.T) {
	f := newSelectionFixture(t)
	imported, discovery := f.importAndRefresh(t, "/cal/a/")
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

	_, err = f.svc.ReconcileSelection(ctx, discovery, SelectionParams{}, f.store)
	if !errors.Is(err, calendar.ErrLastCalendar) {
		t.Fatalf("ReconcileSelection err = %v, want calendar.ErrLastCalendar", err)
	}
	if _, err := f.q.GetCalendar(ctx, imported.CreatedIDs[0]); err != nil {
		t.Fatalf("last calendar was removed: %v", err)
	}
}
