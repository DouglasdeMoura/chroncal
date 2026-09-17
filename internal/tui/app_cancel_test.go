package tui

import (
	"context"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/auth"
)

// esc stops a running sync. The spinner gates the calendar list and the
// account manager, so without the key the user waits for the whole budget
// against a server that never answers.
func TestEscCancelsARunningSync(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m, ctx := m.beginCancellableOp()

	next, cmd, handled := m.interceptGlobalKeys(keyPressMsg("esc"))
	if !handled {
		t.Fatal("esc was not handled while a sync ran")
	}
	if cmd != nil {
		t.Errorf("esc returned a command %v, want none", cmd)
	}
	if err := ctx.Err(); err != context.Canceled {
		t.Errorf("context error = %v, want context.Canceled", err)
	}
	if !next.opCancelled {
		t.Error("opCancelled = false, want true")
	}
	if next.syncStatus != "Cancelling…" {
		t.Errorf("syncStatus = %q, want %q", next.syncStatus, "Cancelling…")
	}
}

// With no cancellable operation in flight, esc keeps its old meaning. The
// overlays then still close on it.
func TestEscIsNotCaughtWithNoRunningOperation(t *testing.T) {
	m := Model{syncSpinner: spinner.New()}
	if _, _, handled := m.interceptGlobalKeys(keyPressMsg("esc")); handled {
		t.Error("esc was caught with no operation in flight")
	}

	// A non-cancellable operation (an account rename) also leaves esc alone.
	m.syncing = true
	if _, _, handled := m.interceptGlobalKeys(keyPressMsg("esc")); handled {
		t.Error("esc was caught while a non-cancellable operation ran")
	}
}

// The cancelled run reports "Sync cancelled". The cancelled context names
// nothing the user must act on, so the error text stays off the footer.
func TestFinishSyncReportsACancelledRun(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, _ := m.finishSync(syncFinishedMsg{err: context.Canceled})
	if next.syncStatus != "Sync cancelled" {
		t.Errorf("syncStatus = %q, want %q", next.syncStatus, "Sync cancelled")
	}
	if next.syncing {
		t.Error("syncing = true after a cancelled run, want false")
	}
	if next.opCancel != nil || next.opCancelled {
		t.Error("the cancel state survived the finished run")
	}
}

// A cancel drops the queued calendar too. Starting it would put the spinner
// back on the screen the user just escaped from.
func TestCancelDropsTheQueuedCalendar(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m.pendingSyncCalendar = syncTarget{ID: 7, Name: "Work"}
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, cmd := m.finishSync(syncFinishedMsg{err: context.Canceled})
	if next.pendingSyncCalendar.ID != 0 {
		t.Errorf("pendingSyncCalendar = %+v, want empty", next.pendingSyncCalendar)
	}
	if cmd == nil {
		t.Fatal("finishSync returned no command")
	}
	if batchEmits(cmd, func(msg tea.Msg) bool {
		_, ok := msg.(SyncCalendarRequestedMsg)
		return ok
	}) {
		t.Error("the cancelled run still asked for the queued calendar")
	}
}

// A sync of every calendar stops between two calendars as well as inside
// one. The finished calendars keep their results.
func TestCancelStopsTheSyncAllChain(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m.syncTargets = []syncTarget{{ID: 1, Name: "One"}, {ID: 2, Name: "Two"}}
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, cmd := m.handleSyncCalendarFinished(syncCalendarFinishedMsg{index: 0, total: 2, name: "One"})
	if cmd == nil {
		t.Fatal("handleSyncCalendarFinished returned no command")
	}
	if !batchEmits(cmd, func(msg tea.Msg) bool {
		_, ok := msg.(syncFinishedMsg)
		return ok
	}) {
		t.Error("the cancelled chain started the next calendar")
	}
	model, ok := next.(Model)
	if !ok {
		t.Fatalf("handleSyncCalendarFinished returned %T, want Model", next)
	}
	if model.syncTargets != nil {
		t.Errorf("syncTargets = %+v, want nil", model.syncTargets)
	}
}

// memoryCredentialStore keeps credentials in memory. The cleanup test needs
// a store that Accounts.Delete can read and write, and it must not reach the
// OS keyring: a real keyring call opens a session bus connection that the
// secretless tests cannot take back, and it would write to the keyring of
// the user who runs the tests.
type memoryCredentialStore struct {
	creds map[int64]auth.Credential
}

func newMemoryCredentialStore() *memoryCredentialStore {
	return &memoryCredentialStore{creds: map[int64]auth.Credential{}}
}

func (s *memoryCredentialStore) Get(accountID int64, _ string) (auth.Credential, error) {
	cred, ok := s.creds[accountID]
	if !ok {
		return auth.Credential{}, auth.ErrCredentialNotFound
	}
	return cred, nil
}

func (s *memoryCredentialStore) Set(cred auth.Credential) error {
	s.creds[cred.AccountID] = cred
	return nil
}

func (s *memoryCredentialStore) Delete(accountID int64) error {
	delete(s.creds, accountID)
	return nil
}

// TestDiscoveryCleanupRemovesTheAccountAfterACancel pins the reason
// newDiscoveryCleanupContext exists. connectAndDiscoverCalendar writes the
// account row before it discovers, so a failed discovery removes the row
// again. esc cancels the discovery context, and Accounts.Delete uses the
// context for the account lock, for the queries, and for the transaction.
// The removal on the cancelled context therefore fails and leaves the
// incomplete account behind. The cleanup context must remove it.
func TestDiscoveryCleanupRemovesTheAccountAfterACancel(t *testing.T) {
	_, a := newDBBackedModel(t)
	ctx := context.Background()
	store := newMemoryCredentialStore()

	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name:      "Nextcloud",
		ServerURL: "https://cloud.example.com/remote.php/dav/",
		Username:  "scott",
		AuthType:  "basic",
	}, auth.Credential{Username: "scott", Password: "hunter2"}, store)
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}

	discoveryCtx, cancelDiscovery := context.WithCancel(ctx)
	cancelDiscovery() // the user presses esc while discovery waits

	// The cancelled discovery context cannot remove the row. This is the
	// finding that the cleanup context answers.
	if err := a.Accounts.Delete(discoveryCtx, created.ID, store); err == nil {
		t.Fatal("Delete on the cancelled discovery context removed the account")
	}

	cleanupCtx, endCleanup := newDiscoveryCleanupContext(discoveryCtx)
	defer endCleanup()
	if err := cleanupCtx.Err(); err != nil {
		t.Fatalf("cleanup context err = %v, want nil", err)
	}
	if err := a.Accounts.Delete(cleanupCtx, created.ID, store); err != nil {
		t.Fatalf("remove the incomplete account: %v", err)
	}

	accounts, err := a.Accounts.List(ctx)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after the cleanup = %d, want 0", len(accounts))
	}
	if _, err := store.Get(created.ID, created.CredentialFingerprint()); err == nil {
		t.Fatal("the credential of the incomplete account stayed in the store")
	}
}

// The plan of a Sync All run can finish before the cancel arrives. The
// cancelled context then stops nothing, so the first calendar must not
// start. Without the guard, beginCancellableOp clears the cancelled flag
// and the run continues on the screen the user just escaped from.
func TestCancelStopsTheSyncAllPlanBeforeTheFirstCalendar(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, _ := m.handleSyncAllPlanned(syncAllPlannedMsg{
		targets: []syncTarget{{ID: 1, Name: "One"}, {ID: 2, Name: "Two"}},
	})
	model, ok := next.(Model)
	if !ok {
		t.Fatalf("handleSyncAllPlanned returned %T, want Model", next)
	}
	if model.syncStatus != "Sync cancelled" {
		t.Errorf("syncStatus = %q, want %q", model.syncStatus, "Sync cancelled")
	}
	if model.syncing {
		t.Error("syncing = true after a cancelled plan, want false")
	}
	if model.opCancel != nil || model.opCancelled {
		t.Error("the cancelled plan armed a new cancellable operation")
	}
}

// A discovery can finish before the cancel arrives. The account row and the
// credential are then on disk while the user reads a cancelled discovery, so
// esc must take them away again.
func TestCancelDiscardsAnAccountThatDiscoveryCreated(t *testing.T) {
	secretlessEnv(t)
	m, a := newDBBackedModel(t)
	ctx := context.Background()

	store, err := m.openCredentialStore()
	if err != nil {
		t.Fatalf("open the credential store: %v", err)
	}
	// A password command carries no secret, so the account writes on a host
	// with no keyring.
	created, err := a.Accounts.Create(ctx, account.CreateParams{
		Name:      "Nextcloud",
		ServerURL: "https://cloud.example.com/remote.php/dav/",
		Username:  "scott",
		AuthType:  "basic",
	}, auth.Credential{Username: "scott", PasswordCommand: "pass show caldav/nextcloud"}, store)
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}

	m.syncing = true
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, cmd := m.handleAccountDiscoveryReady(accountDiscoveryReadyMsg{
		discovery:      account.Discovery{Account: account.Account{ID: created.ID}},
		createdAccount: true,
	})
	model, ok := next.(Model)
	if !ok {
		t.Fatalf("handleAccountDiscoveryReady returned %T, want Model", next)
	}
	if model.pendingDiscoveryAccountID != 0 {
		t.Errorf("pendingDiscoveryAccountID = %d, want 0", model.pendingDiscoveryAccountID)
	}
	if cmd == nil {
		t.Fatal("the cancelled discovery returned no command")
	}
	discarded, ok := cmd().(calendarDiscoveryDiscardedMsg)
	if !ok {
		t.Fatal("the cancelled discovery kept the account that it created")
	}
	if discarded.err != nil {
		t.Fatalf("discard the created account: %v", discarded.err)
	}

	accounts, err := a.Accounts.List(ctx)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after the cancel = %d, want 0", len(accounts))
	}
	if _, err := store.Get(created.ID, created.CredentialFingerprint()); err == nil {
		t.Fatal("the credential of the discarded account stayed in the store")
	}
}
