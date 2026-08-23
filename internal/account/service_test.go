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
	"github.com/douglasdemoura/chroncal/internal/event"
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

func calendarNames(rows []storage.Calendar) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

type selectionFixture struct {
	svc       *Service
	q         *storage.Queries
	db        *sql.DB
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
	return selectionFixture{svc: svc, q: q, db: db, store: store, discovery: discovery}
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

// installAccountDeleteCommitFailure installs a deferred foreign-key trigger
// that makes the transaction's COMMIT (not DeleteAccount itself) fail. The
// commit-failure compensation leg then runs end to end against a real
// credential store. It mirrors the deferred-trigger technique used by the
// calendar Connect tests.
func installAccountDeleteCommitFailure(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE deferred_account_delete_failure (
			parent_id INTEGER REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER fail_account_delete
		AFTER DELETE ON accounts
		BEGIN
			INSERT INTO deferred_account_delete_failure(parent_id) VALUES (-1);
		END;
	`); err != nil {
		t.Fatalf("install deferred account delete failure: %v", err)
	}
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
	if calendar.AccountID != nil {
		t.Fatalf("preserved calendar still linked: %+v", calendar)
	}
	wantOrigin := remoteIdentityKey("/cal/work/", "https://cal.example.test/")
	if storage.NullableToString(calendar.RemoteUrl) != wantOrigin || calendar.RemoteName != "Work" {
		t.Fatalf("account remove should keep remote origin for later re-link: %+v", calendar)
	}
	if _, err := store.Get(account.ID, ""); err == nil {
		t.Fatal("credential should be removed with account")
	}
	resource, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "downloaded"})
	if err != nil {
		t.Fatalf("get preserved sync resource: %v", err)
	}
	if resource.RemoteUrl != "/cal/work/downloaded.ics" || resource.Etag != `"server"` || resource.Dirty != 0 {
		t.Fatalf("sync resource = %+v, want kept href/etag so a same-origin re-link does not PUT as a create", resource)
	}
	tombstones, err := q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("list tombstones: %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("tombstones = %+v, want kept for same-origin re-link", tombstones)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("list conflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want kept for same-origin re-link", conflicts)
	}
}

// Import must re-link the same local rows after account remove. Events stay
// on the original calendar IDs. The re-link writes a live discovery color
// and name onto the row. Account remove clears remote_color. Metadata sync
// then has a current remote baseline.
func TestServiceImportRelinksCalendarsAfterAccountRemove(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	remote := []caldav.RemoteCalendar{
		{Path: "/cal/second/", Name: "Second Remote", Color: "#112233", SupportedComponentSet: []string{"VEVENT"}},
		{Path: "/cal/seeded/", Name: "Seeded Calendar", Color: "#445566", SupportedComponentSet: []string{"VEVENT"}},
	}
	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return slices.Clone(remote), nil
	}

	account, err := svc.Create(ctx, CreateParams{
		Name: "dev", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	first, err := svc.Import(ctx, discovery, []string{"/cal/second/", "/cal/seeded/"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(first.CreatedIDs) != 2 {
		t.Fatalf("first import = %+v, want two created", first)
	}
	secondID, seededID := first.CreatedIDs[0], first.CreatedIDs[1]

	evtSvc := event.NewService(db, q)
	stay, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: seededID,
		Title:      "Stay here",
		StartTime:  time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	if err := svc.Delete(ctx, account.ID, store); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	unlinked, err := q.GetCalendar(ctx, seededID)
	if err != nil {
		t.Fatalf("get unlinked calendar: %v", err)
	}
	if storage.NullableToString(unlinked.RemoteColor) != "" {
		t.Fatalf("account remove should clear remote_color: %+v", unlinked)
	}
	if unlinked.RemoteName != "Seeded Calendar" || unlinked.Color != "#445566" {
		t.Fatalf("account remove should keep remote_name and local color: %+v", unlinked)
	}

	remote[0].Name = "Second Live"
	remote[0].Color = "#AABBCC"
	remote[1].Name = "Seeded Live"
	remote[1].Color = "#DDEEFF"

	again, err := svc.Create(ctx, CreateParams{
		Name: "dev", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	discovery, err = svc.Discover(ctx, again.ID, store)
	if err != nil {
		t.Fatalf("re-Discover: %v", err)
	}
	second, err := svc.Import(ctx, discovery, []string{"/cal/second/", "/cal/seeded/"})
	if err != nil {
		t.Fatalf("re-Import: %v", err)
	}
	if len(second.CreatedIDs) != 2 || second.CreatedIDs[0] != secondID || second.CreatedIDs[1] != seededID {
		t.Fatalf("re-import = %+v, want re-link of %d and %d in CreatedIDs", second, secondID, seededID)
	}
	if len(second.ExistingIDs) != 0 {
		t.Fatalf("re-import ExistingIDs = %+v, want empty", second.ExistingIDs)
	}

	calendars, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	if len(calendars) != 3 {
		t.Fatalf("calendar count = %d, want 3 (seeded Personal plus two remotes); names = %v", len(calendars), calendarNames(calendars))
	}
	linked, err := q.ListCalendarsByAccount(ctx, &again.ID)
	if err != nil {
		t.Fatalf("list re-linked calendars: %v", err)
	}
	if len(linked) != 2 {
		t.Fatalf("linked calendar count = %d, want 2", len(linked))
	}
	byID := map[int64]storage.Calendar{}
	for _, row := range linked {
		byID[row.ID] = row
	}
	gotSecond, ok := byID[secondID]
	if !ok {
		t.Fatalf("Second Live id %d was not re-linked; linked = %v", secondID, calendarNames(linked))
	}
	gotSeeded, ok := byID[seededID]
	if !ok {
		t.Fatalf("Seeded Live id %d was not re-linked; linked = %v", seededID, calendarNames(linked))
	}
	if gotSecond.Name != "Second Live" || strings.Contains(gotSecond.Name, "(2)") {
		t.Fatalf("Second Live name = %q, want live discovery name without suffix", gotSecond.Name)
	}
	if gotSeeded.Name != "Seeded Live" || strings.Contains(gotSeeded.Name, "(2)") {
		t.Fatalf("Seeded Live name = %q, want live discovery name without suffix", gotSeeded.Name)
	}
	if gotSecond.RemoteName != "Second Live" {
		t.Errorf("Second remote_name = %q, want live discovery name", gotSecond.RemoteName)
	}
	if gotSeeded.RemoteName != "Seeded Live" {
		t.Errorf("Seeded remote_name = %q, want live discovery name", gotSeeded.RemoteName)
	}
	if storage.NullableToString(gotSecond.RemoteColor) != "#AABBCC" {
		t.Errorf("Second remote_color = %q, want live discovery color", storage.NullableToString(gotSecond.RemoteColor))
	}
	if storage.NullableToString(gotSeeded.RemoteColor) != "#DDEEFF" {
		t.Errorf("Seeded remote_color = %q, want live discovery color", storage.NullableToString(gotSeeded.RemoteColor))
	}
	if gotSecond.Color != "#AABBCC" {
		t.Errorf("Second color = %q, want live discovery color", gotSecond.Color)
	}
	if gotSeeded.Color != "#DDEEFF" {
		t.Errorf("Seeded color = %q, want live discovery color", gotSeeded.Color)
	}
	if gotSecond.ColorDirty != 0 || gotSeeded.ColorDirty != 0 {
		t.Errorf("color_dirty = %d/%d, want 0/0", gotSecond.ColorDirty, gotSeeded.ColorDirty)
	}

	gotEvent, err := evtSvc.Get(ctx, stay.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if gotEvent.CalendarID != seededID {
		t.Fatalf("event calendar_id = %d, want original %d", gotEvent.CalendarID, seededID)
	}
}

// Two unlinked rows with the same remote identity are ambiguous. Import must
// create a new calendar. It must not pick one snapshot at random.
func TestServiceImportSkipsAmbiguousUnlinkedRemoteIdentity(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	store := newMemoryCredentialStore()
	svc := NewService(db, q)
	snapA, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Snap A", Color: "#111111"})
	if err != nil {
		t.Fatalf("create snapshot A: %v", err)
	}
	snapB, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Snap B", Color: "#222222"})
	if err != nil {
		t.Fatalf("create snapshot B: %v", err)
	}
	origin := remoteIdentityKey("/cal/work/", "https://cal.example.test/")
	if _, err := db.ExecContext(ctx,
		"UPDATE calendars SET remote_url = ?, remote_name = 'Work' WHERE id IN (?, ?)",
		origin, snapA.ID, snapB.ID,
	); err != nil {
		t.Fatalf("seed duplicate origins: %v", err)
	}

	svc.discover = func(context.Context, Account, auth.Credential, func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		return []caldav.RemoteCalendar{{
			Path: "/cal/work/", Name: "Work", Color: "#112233", SupportedComponentSet: []string{"VEVENT"},
		}}, nil
	}
	account, err := svc.Create(ctx, CreateParams{
		Name: "dev", ServerURL: "https://cal.example.test/", Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	discovery, err := svc.Discover(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	result, err := svc.Import(ctx, discovery, []string{"/cal/work/"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(result.CreatedIDs) != 1 {
		t.Fatalf("import = %+v, want one new calendar", result)
	}
	newID := result.CreatedIDs[0]
	if newID == snapA.ID || newID == snapB.ID {
		t.Fatalf("import re-linked snapshot %d, want a new row", newID)
	}

	for _, snap := range []storage.Calendar{snapA, snapB} {
		got, err := q.GetCalendar(ctx, snap.ID)
		if err != nil {
			t.Fatalf("get snapshot %d: %v", snap.ID, err)
		}
		if got.AccountID != nil {
			t.Fatalf("ambiguous snapshot %d was re-linked: %+v", snap.ID, got)
		}
	}
	created, err := q.GetCalendar(ctx, newID)
	if err != nil {
		t.Fatalf("get created calendar: %v", err)
	}
	if created.AccountID == nil || *created.AccountID != account.ID {
		t.Fatalf("new calendar account_id = %v, want %d", created.AccountID, account.ID)
	}
	if created.Name != "Work" {
		t.Fatalf("new calendar name = %q, want Work", created.Name)
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

func TestServiceLoadCredentialUsesAccountIdentity(t *testing.T) {
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

	cred, err := svc.LoadCredential(ctx, account.ID, store)
	if err != nil {
		t.Fatalf("LoadCredential: %v", err)
	}
	if cred.AccountID != account.ID {
		t.Fatalf("credential account ID = %d, want %d", cred.AccountID, account.ID)
	}
	if cred.AccountFingerprint != account.CredentialFingerprint() {
		t.Fatalf("credential fingerprint = %q, want %q", cred.AccountFingerprint, account.CredentialFingerprint())
	}
	if cred.Username != "alice" || cred.Password != "secret" {
		t.Fatalf("credential = %+v, want stored account credential", cred)
	}
}

func TestServiceLoadCredentialWaitsForAccountLifecycle(t *testing.T) {
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

	release, err := synclock.Account(ctx, db, account.ID)
	if err != nil {
		t.Fatalf("lock account lifecycle: %v", err)
	}
	type loadResult struct {
		cred auth.Credential
		err  error
	}
	done := make(chan loadResult, 1)
	go func() {
		cred, err := svc.LoadCredential(ctx, account.ID, store)
		done <- loadResult{cred: cred, err: err}
	}()
	select {
	case result := <-done:
		release()
		t.Fatalf("LoadCredential completed while lifecycle lock was held: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("LoadCredential after release: %v", result.err)
		}
		if result.cred.AccountID != account.ID || result.cred.Password != "secret" {
			t.Fatalf("credential after release = %+v", result.cred)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LoadCredential did not resume after lifecycle release")
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
