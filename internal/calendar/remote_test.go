package calendar

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// panicCredStore panics from Set to simulate a panic raised inside Connect
// after BeginTx but before Commit.
type panicCredStore struct{}

func (panicCredStore) Get(int64, string) (auth.Credential, error) { return auth.Credential{}, nil }
func (panicCredStore) Set(auth.Credential) error                  { panic("simulated credential-store panic") }
func (panicCredStore) Delete(int64) error                         { return nil }

// failGetAccountDB wraps a DBTX and turns the GetAccount read into a
// non-ErrNoRows failure (a "no such column" scan error). That simulates a
// transient DB error. Every other query stays untouched.
type failGetAccountDB struct {
	storage.DBTX
}

func (d failGetAccountDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	if strings.Contains(query, "FROM accounts WHERE id = ?") {
		return d.DBTX.QueryRowContext(ctx, "SELECT no_such_column FROM accounts WHERE id = ?", args...)
	}
	return d.DBTX.QueryRowContext(ctx, query, args...)
}

type memCredStore struct {
	creds    map[int64]auth.Credential
	getErr   error
	setCalls int
	setErrAt int
	setErr   error
}

func (s *memCredStore) Get(id int64, _ string) (auth.Credential, error) {
	if s.getErr != nil {
		return auth.Credential{}, s.getErr
	}
	c, ok := s.creds[id]
	if !ok {
		return auth.Credential{}, nil
	}
	return c, nil
}

func (s *memCredStore) Set(c auth.Credential) error {
	s.setCalls++
	if s.setErrAt != 0 && s.setCalls == s.setErrAt {
		return s.setErr
	}
	if s.creds == nil {
		s.creds = make(map[int64]auth.Credential)
	}
	s.creds[c.AccountID] = c
	return nil
}

func (s *memCredStore) Delete(id int64) error {
	delete(s.creds, id)
	return nil
}

func TestConnect_SeedsRemoteColor(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cal.Color == "#9FE1E7" {
		t.Fatal("seed precondition: default color must differ from remote color")
	}

	link := RemoteLink{
		RemoteURL:        "https://example.com/dav/calendars/work/",
		Username:         "user",
		AuthType:         "basic",
		AllowInsecure:    false,
		RemoteColor:      "#9FE1E7",
		RemoteAccess:     "read",
		RemoteComponents: []string{"VTODO", "VEVENT"},
	}
	cred := auth.Credential{Username: "user", Password: "pass"}

	if err := svc.Connect(ctx, cal, link, cred, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after Connect: %v", err)
	}
	if got.Color != "#9FE1E7" {
		t.Errorf("Color = %q, want #9FE1E7 (adopted from remote at link time)", got.Color)
	}
	if got.RemoteColor != "#9FE1E7" {
		t.Errorf("RemoteColor = %q, want #9FE1E7", got.RemoteColor)
	}
	if got.ColorDirty {
		t.Error("ColorDirty must stay false right after seeding from the server")
	}
	if got.RemoteAccess != "read" || got.RemoteComponents != "VEVENT,VTODO" {
		t.Errorf("remote capabilities = %q/%q, want read/VEVENT,VTODO", got.RemoteAccess, got.RemoteComponents)
	}
}

func TestConnect_RelinkDoesNotClobberLocalColorEdit(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()

	// Simulate a calendar that was previously linked to a hidden account.
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__calendar_1",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        1,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://example.com/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	// User just changed the local color in the dialog: Update set color_dirty=1
	// and persisted the new color. Re-saving the dialog falls into the
	// existing-account branch of Connect.
	if _, err := db.ExecContext(ctx, "UPDATE calendars SET color = ?, color_dirty = 1 WHERE id = 1", "#FF0000"); err != nil {
		t.Fatalf("seed local color edit: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
		RemoteColor:   "#0000FF",
	}
	if err := svc.Connect(ctx, cal, link, auth.Credential{Username: "user", Password: "pass"}, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after Connect: %v", err)
	}
	if got.Color != "#FF0000" {
		t.Errorf("Color = %q, want #FF0000 (re-link must not clobber the user's local edit)", got.Color)
	}
	if !got.ColorDirty {
		t.Error("ColorDirty must stay true so the next sync pushes the local edit to the server")
	}
}

func TestConnect_RelinkSplitsCalendarFromSharedHiddenAccount(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	second, err := svc.Create(ctx, "Second", "", "")
	if err != nil {
		t.Fatalf("create second calendar: %v", err)
	}
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__calendar_1",
		ServerUrl: "https://old.example.com",
		AuthType:  "basic",
		Username:  "old-user",
	})
	if err != nil {
		t.Fatalf("create shared hidden account: %v", err)
	}
	for _, calendarID := range []int64{1, second.ID} {
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID:        calendarID,
			AccountID: &account.ID,
			RemoteUrl: storage.StringToNullable(fmt.Sprintf("https://old.example.com/cal/%d", calendarID)),
		}); err != nil {
			t.Fatalf("link calendar %d: %v", calendarID, err)
		}
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: 1, Uid: "old-deletion", RemoteUrl: "/cal/1/old-deletion.ics",
	}); err != nil {
		t.Fatalf("seed old tombstone: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get calendar: %v", err)
	}
	store := &memCredStore{}
	if err := store.Set(auth.Credential{AccountID: account.ID, Username: "old-user", Password: "old-pass"}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if err := svc.Connect(ctx, cal, RemoteLink{
		RemoteURL: "https://new.example.com/dav/work",
		Username:  "new-user",
		AuthType:  "basic",
	}, auth.Credential{Username: "new-user", Password: "new-pass"}, store); err != nil {
		t.Fatalf("relink shared calendar: %v", err)
	}

	firstAfter, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get relinked calendar: %v", err)
	}
	secondAfter, err := svc.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("get untouched calendar: %v", err)
	}
	if firstAfter.AccountID == account.ID {
		t.Fatal("relinked calendar still shares the legacy account")
	}
	if secondAfter.AccountID != account.ID {
		t.Fatalf("untouched calendar account = %d, want %d", secondAfter.AccountID, account.ID)
	}
	oldAccount, err := q.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("get old account: %v", err)
	}
	if oldAccount.ServerUrl != "https://old.example.com" || oldAccount.Username != "old-user" {
		t.Fatalf("old shared account was mutated: %+v", oldAccount)
	}
	if tombstones, err := q.ListTombstonesByCalendar(ctx, 1); err != nil || len(tombstones) != 0 {
		t.Fatalf("old tombstones survived endpoint relink: (%+v, %v)", tombstones, err)
	}
}

func TestConnect_RelinkRestoresCredentialOnTxFailure(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()

	// Calendar 1 is linked to a hidden account that Connect updates in place.
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__calendar_99",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        1,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://example.com/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	// Force the transaction's COMMIT (not UpdateAccount itself) to fail after
	// Connect has written the replacement credential. The deferred foreign-key
	// violation exercises the keyring rollback path directly.
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE deferred_account_failure (
			parent_id INTEGER REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER fail_account_update
		AFTER UPDATE ON accounts
		BEGIN
			INSERT INTO deferred_account_failure(parent_id) VALUES (-1);
		END;
	`); err != nil {
		t.Fatalf("install deferred commit failure: %v", err)
	}

	// The keyring already holds the old credential for the linked account.
	store := &memCredStore{}
	oldCred := auth.Credential{AccountID: account.ID, Username: "user", Password: "old-pass"}
	if err := store.Set(oldCred); err != nil {
		t.Fatalf("seed old credential: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
	}
	newCred := auth.Credential{Username: "user", Password: "new-pass"}

	if err := svc.Connect(ctx, cal, link, newCred, store); err == nil {
		t.Fatal("Connect: expected deferred commit error, got nil")
	}

	got, err := store.Get(account.ID, "")
	if err != nil {
		t.Fatalf("Get credential after failed Connect: %v", err)
	}
	if got.Password != "old-pass" {
		t.Errorf("stored password = %q, want %q: a failed re-link must not leave the keyring holding the new credential", got.Password, "old-pass")
	}
}

func TestConnect_RelinkSurfacesCredentialRestoreFailure(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "__calendar_99", ServerUrl: "https://old.example.test", AuthType: "basic", Username: "alice",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: 1, AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://old.example.test/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE deferred_account_failure (
			parent_id INTEGER REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER fail_account_update
		AFTER UPDATE ON accounts
		BEGIN
			INSERT INTO deferred_account_failure(parent_id) VALUES (-1);
		END;
	`); err != nil {
		t.Fatalf("install deferred commit failure: %v", err)
	}
	restoreErr := errors.New("keyring restore failed")
	store := &memCredStore{setErrAt: 3, setErr: restoreErr}
	if err := store.Set(auth.Credential{
		AccountID:          account.ID,
		AccountFingerprint: auth.AccountFingerprint(account.ServerUrl, account.AuthType, account.Username),
		Username:           "alice", Password: "old",
	}); err != nil {
		t.Fatalf("seed old credential: %v", err)
	}
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get calendar: %v", err)
	}
	err = svc.Connect(ctx, cal, RemoteLink{
		RemoteURL: "https://new.example.test/dav/calendars/work/",
		Username:  "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "new"}, store)
	if !errors.Is(err, restoreErr) && !strings.Contains(err.Error(), restoreErr.Error()) {
		t.Fatalf("Connect error = %v, want credential restore failure", err)
	}
	got, err := store.Get(account.ID, "")
	if err != nil {
		t.Fatalf("Get credential: %v", err)
	}
	oldFingerprint := auth.AccountFingerprint(account.ServerUrl, account.AuthType, account.Username)
	if got.AccountFingerprint == "" || got.AccountFingerprint == oldFingerprint {
		t.Fatalf("replacement credential lacks blocking identity after failed restore: %+v", got)
	}
}

func TestConnect_RelinkAbortsOnCredentialPreflightFailure(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "__calendar_99", ServerUrl: "https://old.example.test", AuthType: "basic", Username: "alice",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	oldRemoteURL := "https://old.example.test/dav/calendars/work/"
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: 1, AccountID: &account.ID, RemoteUrl: storage.StringToNullable(oldRemoteURL),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get calendar: %v", err)
	}
	storeErr := errors.New("keyring unavailable")
	store := &memCredStore{getErr: storeErr}
	err = svc.Connect(ctx, cal, RemoteLink{
		RemoteURL: "https://new.example.test/dav/calendars/work/",
		Username:  "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "replacement"}, store)
	if !errors.Is(err, storeErr) {
		t.Fatalf("Connect error = %v, want credential preflight failure", err)
	}
	gotAccount, err := q.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if gotAccount.ServerUrl != "https://old.example.test" {
		t.Fatalf("account server changed after failed credential preflight: %+v", gotAccount)
	}
	gotCalendar, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get calendar after Connect: %v", err)
	}
	if gotCalendar.RemoteURL != oldRemoteURL {
		t.Fatalf("calendar remote URL = %q, want %q", gotCalendar.RemoteURL, oldRemoteURL)
	}
}

func TestConnect_RelinkTreatsCredentialIdentityMismatchAsNoPreviousCredential(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "__calendar_99", ServerUrl: "https://old.example.test", AuthType: "basic", Username: "alice",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: 1, AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://old.example.test/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get calendar: %v", err)
	}
	store := &memCredStore{getErr: auth.ErrCredentialIdentityMismatch}
	if err := svc.Connect(ctx, cal, RemoteLink{
		RemoteURL: "https://new.example.test/dav/calendars/work/",
		Username:  "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "replacement"}, store); err != nil {
		t.Fatalf("Connect after credential identity mismatch: %v", err)
	}
	gotAccount, err := q.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if gotAccount.ServerUrl == account.ServerUrl {
		t.Fatalf("account was not relinked after identity mismatch: %+v", gotAccount)
	}
	if store.setCalls != 1 {
		t.Fatalf("credential Set calls = %d, want replacement write", store.setCalls)
	}
}

func TestConnect_RelinkPropagatesTransientGetAccountError(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	// Calendar 1 is already linked to a hidden account whose name differs from
	// hiddenAccountName(1), so the buggy create-new path would NOT collide on
	// the UNIQUE(name) constraint and would happily spawn a duplicate account.
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__calendar_99",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        1,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://example.com/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	// The keyring already holds the credential for the linked account.
	store := &memCredStore{}
	if err := store.Set(auth.Credential{AccountID: account.ID, Username: "user", Password: "old-pass"}); err != nil {
		t.Fatalf("seed old credential: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// A Service whose GetAccount read fails transiently (a non-ErrNoRows
	// error). Transactional writes still go through the real *sql.Tx, so the
	// create-new path can succeed if the code reaches it.
	faulty := NewService(db, storage.New(failGetAccountDB{db}))

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
	}
	newCred := auth.Credential{Username: "user", Password: "new-pass"}

	if err := faulty.Connect(ctx, cal, link, newCred, store); err == nil {
		t.Fatal("Connect: expected a transient GetAccount error to propagate, got nil (fell through to create a duplicate hidden account)")
	}

	accounts, err := q.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Errorf("account count = %d, want 1: a transient read error must not spawn a duplicate hidden account", len(accounts))
	}
	if len(store.creds) != 1 {
		t.Errorf("credential count = %d, want 1: a transient read error must not orphan the old credential behind a new one", len(store.creds))
	}
}

// TestConnect_PanicDoesNotLeakTransaction verifies that a panic raised between
// BeginTx and Commit in Connect does not leak the transaction. The test pool
// is pinned to a single connection (testutil.NewTestDB sets
// SetMaxOpenConns(1)). A leaked transaction then never returns
// its connection to the pool. The next query blocks until its context expires.
// A deferred rollback releases the connection. The follow-up read then returns
// promptly.
func TestConnect_PanicDoesNotLeakTransaction(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Connect: expected the credential-store panic to propagate, got none")
			}
		}()
		_ = svc.Connect(ctx, cal, link, auth.Credential{Username: "user"}, panicCredStore{})
	}()

	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := svc.Get(deadline, 1); err != nil {
		t.Fatalf("read after panicked Connect failed (transaction leaked, connection not released): %v", err)
	}
}

func TestConnect_NoRemoteColor_LeavesLocalColor(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	original, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
	}
	cred := auth.Credential{Username: "user", Password: "pass"}

	if err := svc.Connect(ctx, original, link, cred, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after Connect: %v", err)
	}
	if got.Color != original.Color {
		t.Errorf("Color = %q, want %q (no remote color → keep local)", got.Color, original.Color)
	}
	if got.RemoteColor != "" {
		t.Errorf("RemoteColor = %q, want empty when fetch yielded nothing", got.RemoteColor)
	}
}

// TestDisconnect_HiddenAccountWithMultipleCalendars_PreservesCredential verifies
// that a disconnect of one calendar from a shared hidden account does not
// delete the stored credential. That holds when the account still has other
// calendars linked.
func TestDisconnect_HiddenAccountWithMultipleCalendars_PreservesCredential(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()

	// Create a second calendar.
	cal2, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{
		Name:  "Work",
		Color: "#0000ff",
	})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	// Create a hidden account shared by both calendars.
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      hiddenAccountPrefix + "shared",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Link both calendars to the same hidden account.
	for i, calID := range []int64{1, cal2.ID} {
		id := calID
		remoteURL := []string{
			"https://example.com/dav/personal/",
			"https://example.com/dav/work/",
		}[i]
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID:        id,
			AccountID: &account.ID,
			RemoteUrl: storage.StringToNullable(remoteURL),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount(%d): %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE calendars
		SET ctag = 'old-ctag', sync_token = 'old-token',
		    remote_access = 'read', remote_components = 'VEVENT'
		WHERE id = 1`); err != nil {
		t.Fatalf("seed calendar remote state: %v", err)
	}
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: 1, Uid: "downloaded", OwnerType: "event",
		RemoteUrl: "/dav/personal/downloaded.ics", Etag: `"old"`, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("seed sync resource: %v", err)
	}
	if err := q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: 1, Uid: "deleted", RemoteUrl: "/dav/personal/deleted.ics",
	}); err != nil {
		t.Fatalf("seed tombstone: %v", err)
	}
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: 1, OwnerType: "event", OwnerID: 1, Uid: "conflict",
		LocalIcal: "local", ServerIcal: "server", ServerEtag: `"old"`,
	}); err != nil {
		t.Fatalf("seed sync conflict: %v", err)
	}
	if _, err := q.BumpSyncPendingHref(ctx, storage.BumpSyncPendingHrefParams{
		CalendarID: 1, Href: "/dav/personal/phantom-invite.ics",
	}); err != nil {
		t.Fatalf("seed pending href: %v", err)
	}

	// Seed the credential.
	store := &memCredStore{}
	if err := store.Set(auth.Credential{AccountID: account.ID, Username: "user", Password: "secret"}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	// Disconnect calendar 1 only. The account still has calendar 2 linked so
	// the account row is not deleted, and the credential must survive.
	cal1, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get cal1: %v", err)
	}
	if err := svc.Disconnect(ctx, cal1, store); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	got, err := store.Get(account.ID, "")
	if err != nil {
		t.Fatalf("Get credential after Disconnect: %v", err)
	}
	if got.Password != "secret" {
		t.Errorf("Disconnect: credential wrongly deleted when account survived; password = %q, want %q", got.Password, "secret")
	}
	local, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get disconnected calendar: %v", err)
	}
	if local.AccountID != 0 || local.RemoteURL != "" || local.CTag != "" || local.SyncToken != "" ||
		local.RemoteAccess != "unknown" || local.RemoteComponents != "" {
		t.Errorf("disconnected calendar retained remote state: %+v", local)
	}
	resource, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: 1, Uid: "downloaded"})
	if err != nil {
		t.Fatalf("Get detached sync resource: %v", err)
	}
	if resource.RemoteUrl != "" || resource.Etag != "" || resource.Dirty != 1 {
		t.Errorf("sync resource not detached: %+v", resource)
	}
	if tombstones, err := q.ListTombstonesByCalendar(ctx, 1); err != nil || len(tombstones) != 0 {
		t.Errorf("tombstones after disconnect = (%+v, %v), want none", tombstones, err)
	}
	if conflicts, err := q.ListSyncConflictsByCalendar(ctx, 1); err != nil || len(conflicts) != 0 {
		t.Errorf("conflicts after disconnect = (%+v, %v), want none", conflicts, err)
	}
	if pending, err := q.ListSyncPendingHrefsByCalendar(ctx, 1); err != nil || len(pending) != 0 {
		t.Errorf("pending hrefs after disconnect = (%+v, %v), want none", pending, err)
	}
}

// TestDeleteWithRemoteCleanup_HiddenAccountWithMultipleCalendars_PreservesCredential
// verifies that a delete of a calendar whose hidden account still serves another
// calendar does not delete the stored credential.
func TestDeleteWithRemoteCleanup_HiddenAccountWithMultipleCalendars_PreservesCredential(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	// Create a second calendar.
	cal2, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{
		Name:  "Work",
		Color: "#0000ff",
	})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	// Create a hidden account shared by both calendars.
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      hiddenAccountPrefix + "shared",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Link both calendars to the same hidden account.
	for i, calID := range []int64{1, cal2.ID} {
		id := calID
		remoteURL := []string{
			"https://example.com/dav/personal/",
			"https://example.com/dav/work/",
		}[i]
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID:        id,
			AccountID: &account.ID,
			RemoteUrl: storage.StringToNullable(remoteURL),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount(%d): %v", id, err)
		}
	}

	// Seed the credential.
	store := &memCredStore{}
	if err := store.Set(auth.Credential{AccountID: account.ID, Username: "user", Password: "secret"}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	// Delete cal2 (non-default). The account still has calendar 1 linked so
	// the account row is not deleted, and the credential must survive.
	if err := svc.DeleteWithRemoteCleanup(ctx, cal2.ID, 0, store); err != nil {
		t.Fatalf("DeleteWithRemoteCleanup: %v", err)
	}

	got, err := store.Get(account.ID, "")
	if err != nil {
		t.Fatalf("Get credential after DeleteWithRemoteCleanup: %v", err)
	}
	if got.Password != "secret" {
		t.Errorf("DeleteWithRemoteCleanup: credential wrongly deleted when account survived; password = %q, want %q", got.Password, "secret")
	}
}

// TestDeleteWithRemoteCleanup_PromotesDefault exercises the default-promotion
// path. A delete of the current default must refuse without a replacement (and
// roll back). It then succeeds by a promote of a calendar that remains, atomically.
func TestDeleteWithRemoteCleanup_PromotesDefault(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	personal, _ := svc.GetDefault(ctx) // the seeded default calendar
	work, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{
		Name:  "Work",
		Color: "#0000ff",
	})
	if err != nil {
		t.Fatalf("CreateCalendar Work: %v", err)
	}

	// Deleting the default without a replacement must refuse, leaving the
	// default intact (the delete is rolled back inside the same transaction).
	if err := svc.DeleteWithRemoteCleanup(ctx, personal.ID, 0, &memCredStore{}); !errors.Is(err, ErrDefaultCalendarRequiresPromotion) {
		t.Fatalf("delete default without replacement: err = %v, want ErrDefaultCalendarRequiresPromotion", err)
	}
	if def, err := svc.GetDefault(ctx); err != nil || def.ID != personal.ID {
		t.Fatalf("default = (%+v, %v) after refused delete, want id=%d", def, err, personal.ID)
	}
	if _, err := svc.Get(ctx, personal.ID); err != nil {
		t.Fatalf("personal calendar should still exist after refused delete: %v", err)
	}

	// Promoting a surviving calendar moves the default atomically.
	if err := svc.DeleteWithRemoteCleanup(ctx, personal.ID, work.ID, &memCredStore{}); err != nil {
		t.Fatalf("DeleteWithRemoteCleanup with promotion: %v", err)
	}
	if _, err := svc.Get(ctx, personal.ID); err == nil {
		t.Fatal("personal calendar should be deleted after promotion")
	}
	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault after promotion: %v", err)
	}
	if def.ID != work.ID || !def.IsDefault {
		t.Fatalf("default = %+v, want id=%d IsDefault=true", def, work.ID)
	}
}

// TestConnectTargetNamesTheAccountConnectWrites pins the pre-flight seam that
// the CLI uses before it prompts for a secret. Connect keeps the hidden
// account of a calendar only when that account links to this calendar alone.
// Every other link writes a brand-new account, which holds no credential.
func TestConnectTargetNamesTheAccountConnectWrites(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	link := RemoteLink{
		RemoteURL: "https://example.com/dav/calendars/work/",
		Username:  "user",
		AuthType:  "basic",
	}
	serverURL, err := DeriveServerURL(link.RemoteURL, link.AllowInsecure)
	if err != nil {
		t.Fatalf("DeriveServerURL: %v", err)
	}
	wantFingerprint := auth.AccountFingerprint(serverURL, "basic", "user")

	// A calendar with no account: Connect creates one.
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get calendar: %v", err)
	}
	id, fingerprint, err := svc.ConnectTarget(ctx, cal, link)
	if err != nil {
		t.Fatalf("ConnectTarget on an unlinked calendar: %v", err)
	}
	if id != 0 {
		t.Fatalf("account ID = %d, want 0", id)
	}
	if fingerprint != wantFingerprint {
		t.Fatalf("fingerprint = %q, want %q", fingerprint, wantFingerprint)
	}

	// A single-calendar hidden account: Connect keeps it.
	hidden, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "__calendar_1", ServerUrl: "https://old.example.com",
		AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: 1, AccountID: &hidden.ID,
		RemoteUrl: storage.StringToNullable("https://old.example.com/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	cal, err = svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get the linked calendar: %v", err)
	}
	id, _, err = svc.ConnectTarget(ctx, cal, link)
	if err != nil {
		t.Fatalf("ConnectTarget on a hidden account: %v", err)
	}
	if id != hidden.ID {
		t.Fatalf("account ID = %d, want %d", id, hidden.ID)
	}

	// A named account: Connect does not keep it, so the target is a new one.
	named, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "Work", ServerUrl: "https://old.example.com",
		AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: 1, AccountID: &named.ID,
		RemoteUrl: storage.StringToNullable("https://old.example.com/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	cal, err = svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get the relinked calendar: %v", err)
	}
	id, _, err = svc.ConnectTarget(ctx, cal, link)
	if err != nil {
		t.Fatalf("ConnectTarget on a named account: %v", err)
	}
	if id != 0 {
		t.Fatalf("account ID = %d, want 0 for a named account", id)
	}
}

// TestDisconnect_DetachesTheMovesFromTheCalendar records a move conflict
// whose source is calendar 1 and unlinks calendar 1. The unlink deletes
// the source tombstones, so it must end the move conflict in the target
// calendar as detached.
func TestDisconnect_DetachesTheMovesFromTheCalendar(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	target, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Target", Color: "#0000ff"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: hiddenAccountPrefix + "moves", ServerUrl: "https://example.com", AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for i, id := range []int64{1, target.ID} {
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID: id, AccountID: &account.ID, RemoteUrl: storage.StringToNullable([]string{"https://example.com/dav/a/", "https://example.com/dav/b/"}[i]),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount(%d): %v", id, err)
		}
	}
	source := int64(1)
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: target.ID, OwnerType: "event", OwnerID: 1, Uid: "moved",
		LocalIcal: "local", ServerIcal: "server", ServerEtag: "e2", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	// A failed cancel of the same source ends too: its replay cannot reach
	// a server after the unlink.
	if _, err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: target.ID, OwnerType: "event", OwnerID: 2, Uid: "failed",
		LocalIcal: "local", ServerIcal: "server", ServerEtag: "e2", MoveSourceCalendarID: &source,
	}); err != nil {
		t.Fatalf("CreateSyncConflict failed: %v", err)
	}
	if _, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{MoveState: "cancel-failed", CalendarID: target.ID, Uid: "failed", MoveSourceCalendarID: &source}); err != nil {
		t.Fatalf("SetMoveConflictState: %v", err)
	}
	cal1, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get cal1: %v", err)
	}
	if err := svc.Disconnect(ctx, cal1, &memCredStore{}); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	for _, uid := range []string{"moved", "failed"} {
		row, err := q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: target.ID, Uid: uid})
		if err != nil || row.MoveState != "detached" || row.ResolvedAt == nil {
			t.Fatalf("move conflict %s = %+v, %v; want it detached", uid, row, err)
		}
	}
}

// TestConnect_MarksAMovedCopyOfALocalCalendarDirty moves an item into a
// local calendar, so the copy has no sync row there. The link of that
// calendar must give the copy a dirty row, so the copy pushes and counts as
// live (issue #841).
func TestConnect_MarksAMovedCopyOfALocalCalendarDirty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	source, err := svc.q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "source"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class) VALUES ('moved', 1, 'Moved', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z', 'CONFIRMED', 'OPAQUE', 'PUBLIC')`,
		fmt.Sprintf(`INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (%d, 'moved', '/s/moved.ics', 1)`, source.ID),
	} {
		if _, err := svc.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	link := RemoteLink{RemoteURL: "https://example.com/dav/calendars/work/", Username: "user", AuthType: "basic"}
	if err := svc.Connect(ctx, cal, link, auth.Credential{Username: "user", Password: "pass"}, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	sr, err := svc.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: 1, Uid: "moved"})
	if err != nil || sr.Dirty != 1 || sr.RemoteUrl != "" {
		t.Fatalf("sync row of the copy = %+v, %v; want a dirty row", sr, err)
	}
}

// TestConnect_MarksTheLocalItemsDirty links a local calendar with a plain
// event. The link must give the event a dirty sync row without an href, so
// the next push uploads it (issue #866).
func TestConnect_MarksTheLocalItemsDirty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO events (uid, calendar_id, title, start_time, end_time) VALUES ('local', 1, 'Local', '2026-04-03T10:00:00Z', '2026-04-03T11:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	link := RemoteLink{RemoteURL: "https://example.com/dav/calendars/work/", Username: "user", AuthType: "basic"}
	if err := svc.Connect(ctx, cal, link, auth.Credential{Username: "user", Password: "pass"}, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	sr, err := svc.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: 1, Uid: "local"})
	if err != nil || sr.Dirty != 1 || sr.RemoteUrl != "" || sr.OwnerType != "event" {
		t.Fatalf("sync row = %+v, %v; want a dirty event row without an href", sr, err)
	}
}

// TestConnect_RefusesACollectionThatAnotherCalendarLinks links two local
// calendars to one collection. The second link must refuse, or each item of
// the collection shows twice (issue #874). The check matches the collection
// in another case of the host, with no trailing slash, and with the user
// in another case. A different user can link the collection.
func TestConnect_RefusesACollectionThatAnotherCalendarLinks(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	first, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	link := RemoteLink{RemoteURL: "https://example.com/dav/calendars/work/", Username: "user", AuthType: "basic"}
	if err := svc.Connect(ctx, first, link, auth.Credential{Username: "user", Password: "pass"}, &memCredStore{}); err != nil {
		t.Fatalf("Connect first: %v", err)
	}
	second, err := svc.Create(ctx, "Second", "#00ff00", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	again := RemoteLink{RemoteURL: "https://EXAMPLE.com/dav/calendars/work", Username: "User", AuthType: "basic"}
	if _, _, err := svc.ConnectTarget(ctx, second, again); !errors.Is(err, ErrCollectionLinked) {
		t.Fatalf("ConnectTarget = %v; want ErrCollectionLinked", err)
	}
	err = svc.Connect(ctx, second, again, auth.Credential{Username: "User", Password: "pass"}, &memCredStore{})
	if !errors.Is(err, ErrCollectionLinked) || !strings.Contains(err.Error(), `"Personal"`) {
		t.Fatalf("Connect second = %v; want ErrCollectionLinked that names the first calendar", err)
	}
	got, err := svc.Get(ctx, second.ID)
	if err != nil || got.AccountID != 0 || got.RemoteURL != "" {
		t.Fatalf("second calendar = %+v, %v; want no link", got, err)
	}
	// A relink of the first calendar to its own collection stays possible.
	if err := svc.Connect(ctx, first, link, auth.Credential{Username: "user", Password: "new"}, &memCredStore{}); err != nil {
		t.Fatalf("relink first: %v", err)
	}
	other := RemoteLink{RemoteURL: link.RemoteURL, Username: "other", AuthType: "basic"}
	if err := svc.Connect(ctx, second, other, auth.Credential{Username: "other", Password: "pass"}, &memCredStore{}); err != nil {
		t.Fatalf("Connect with another user: %v", err)
	}
}
