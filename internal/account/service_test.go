package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

type memoryCredentialStore struct {
	credentials map[int64]auth.Credential
	setErr      error
}

func newMemoryCredentialStore() *memoryCredentialStore {
	return &memoryCredentialStore{credentials: make(map[int64]auth.Credential)}
}

func (s *memoryCredentialStore) Get(accountID int64) (auth.Credential, error) {
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
	delete(s.credentials, accountID)
	return nil
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
		Name: "Google", ServerURL: "https://calendar.example.test/caldav/",
		Username: "me@example.test", AuthType: "oauth2",
	}, auth.Credential{Username: "me@example.test", AccessToken: "token"}, store)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc.discover = func(_ context.Context, got Account, cred auth.Credential, persist func(auth.Credential) error) ([]caldav.RemoteCalendar, error) {
		if got.ID != account.ID || got.ServerURL != "https://calendar.example.test/caldav/" {
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

	result, err = svc.Import(ctx, discovery, []string{"/cal/me/family/"})
	if err != nil {
		t.Fatalf("repeat Import: %v", err)
	}
	if len(result.CreatedIDs) != 0 || len(result.ExistingIDs) != 1 {
		t.Fatalf("repeat import = %+v, want one existing", result)
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
	if _, err := svc.Discover(ctx, account.ID, store); err != nil {
		t.Fatalf("refresh Discover: %v", err)
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
	if _, err := store.Get(account.ID); err == nil {
		t.Fatal("credential should be removed with account")
	}
}

func TestRemoteIdentityKeyNormalizesEquivalentCollectionURLs(t *testing.T) {
	t.Parallel()

	want := remoteIdentityKey("https://apidata.googleusercontent.com/caldav/v2/user@example.com/events")
	for _, raw := range []string{
		"https://apidata.googleusercontent.com/caldav/v2/user@example.com/events/",
		"https://apidata.googleusercontent.com/caldav/v2/user%40example.com/events/",
	} {
		if got := remoteIdentityKey(raw); got != want {
			t.Errorf("remoteIdentityKey(%q) = %q, want %q", raw, got, want)
		}
	}
}
