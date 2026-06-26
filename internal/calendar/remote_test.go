package calendar

import (
	"context"
	"database/sql"
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

func (panicCredStore) Get(int64) (auth.Credential, error) { return auth.Credential{}, nil }
func (panicCredStore) Set(auth.Credential) error          { panic("simulated credential-store panic") }
func (panicCredStore) Delete(int64) error                 { return nil }

// failGetAccountDB wraps a DBTX and turns the GetAccount read into a
// non-ErrNoRows failure (a "no such column" scan error), simulating a
// transient DB error while leaving every other query untouched.
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
	creds map[int64]auth.Credential
}

func (s *memCredStore) Get(id int64) (auth.Credential, error) {
	c, ok := s.creds[id]
	if !ok {
		return auth.Credential{}, nil
	}
	return c, nil
}

func (s *memCredStore) Set(c auth.Credential) error {
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
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
		RemoteColor:   "#9FE1E7",
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

func TestConnect_RelinkRestoresCredentialOnTxFailure(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	// Calendar 1 is linked to a hidden account whose name differs from the
	// name Connect will rename it to (hiddenAccountName(1) == "__calendar_1").
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

	// A second hidden account already owns the name Connect will try to rename
	// the first account to, so UpdateAccount hits a UNIQUE(name) violation and
	// the re-link transaction fails after credStore.Set would have run.
	if _, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__calendar_1",
		ServerUrl: "https://other.example.com",
		AuthType:  "basic",
		Username:  "user",
	}); err != nil {
		t.Fatalf("CreateAccount (collision): %v", err)
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
		t.Fatal("Connect: expected error from UNIQUE(name) collision, got nil")
	}

	got, err := store.Get(account.ID)
	if err != nil {
		t.Fatalf("Get credential after failed Connect: %v", err)
	}
	if got.Password != "old-pass" {
		t.Errorf("stored password = %q, want %q: a failed re-link must not leave the keyring holding the new credential", got.Password, "old-pass")
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
// BeginTx and Commit in Connect does not leak the transaction. The in-memory
// test pool is pinned to a single connection (storage.Open sets
// SetMaxOpenConns(1) for ":memory:"), so a leaked transaction never returns its
// connection to the pool and the next query blocks until its context expires.
// A deferred rollback releases the connection, so the follow-up read returns
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
// that disconnecting one calendar from a shared hidden account does not delete
// the stored credential when the account still has other calendars linked.
func TestDisconnect_HiddenAccountWithMultipleCalendars_PreservesCredential(t *testing.T) {
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
	for _, calID := range []int64{1, cal2.ID} {
		id := calID
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID:        id,
			AccountID: &account.ID,
			RemoteUrl: storage.StringToNullable("https://example.com/dav/"),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount(%d): %v", id, err)
		}
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

	got, err := store.Get(account.ID)
	if err != nil {
		t.Fatalf("Get credential after Disconnect: %v", err)
	}
	if got.Password != "secret" {
		t.Errorf("Disconnect: credential wrongly deleted when account survived; password = %q, want %q", got.Password, "secret")
	}
}

// TestDeleteWithRemoteCleanup_HiddenAccountWithMultipleCalendars_PreservesCredential
// verifies that deleting a calendar whose hidden account still serves another
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
	for _, calID := range []int64{1, cal2.ID} {
		id := calID
		if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID:        id,
			AccountID: &account.ID,
			RemoteUrl: storage.StringToNullable("https://example.com/dav/"),
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

	got, err := store.Get(account.ID)
	if err != nil {
		t.Fatalf("Get credential after DeleteWithRemoteCleanup: %v", err)
	}
	if got.Password != "secret" {
		t.Errorf("DeleteWithRemoteCleanup: credential wrongly deleted when account survived; password = %q, want %q", got.Password, "secret")
	}
}
