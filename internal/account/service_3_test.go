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

	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// ReconcileSelection must re-link the same local rows after account remove.
// Events stay on the original calendar IDs.
func TestReconcileSelectionRelinksCalendarsAfterAccountRemove(t *testing.T) {
	f := newSelectionFixture(t)
	imported, _ := f.importAndRefresh(t, "/cal/a/", "/cal/b/")
	if len(imported.CreatedIDs) != 2 {
		t.Fatalf("first import = %+v, want two created", imported)
	}
	aID, bID := imported.CreatedIDs[0], imported.CreatedIDs[1]
	ctx := context.Background()

	evtSvc := event.NewService(f.db, f.q)
	stay, err := evtSvc.Create(ctx, event.CreateParams{
		CalendarID: aID,
		Title:      "Stay here",
		StartTime:  time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	if err := f.svc.Delete(ctx, f.discovery.Account.ID, f.store); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	again, err := f.svc.Create(ctx, CreateParams{
		Name: "Work", ServerURL: "https://cal.example.test/dav/",
		Username: "alice", AuthType: "basic",
	}, auth.Credential{Username: "alice", Password: "secret"}, f.store)
	if err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	discovery, err := f.svc.Discover(ctx, again.ID, f.store)
	if err != nil {
		t.Fatalf("re-Discover: %v", err)
	}
	result, err := f.svc.ReconcileSelection(ctx, discovery, SelectionParams{
		SelectedPaths: []string{"/cal/a/", "/cal/b/"},
	}, f.store)
	if err != nil {
		t.Fatalf("ReconcileSelection: %v", err)
	}
	if !slices.Equal(result.CreatedIDs, []int64{aID, bID}) || len(result.RemovedIDs) != 0 || result.AccountRemoved {
		t.Fatalf("selection result = %+v, want re-link of %d and %d", result, aID, bID)
	}

	linked, err := f.q.ListCalendarsByAccount(ctx, &again.ID)
	if err != nil {
		t.Fatalf("list re-linked calendars: %v", err)
	}
	if len(linked) != 2 {
		t.Fatalf("linked calendar count = %d, want 2; names = %v", len(linked), calendarNames(linked))
	}
	byID := map[int64]storage.Calendar{}
	for _, row := range linked {
		byID[row.ID] = row
	}
	gotA, ok := byID[aID]
	if !ok {
		t.Fatalf("A id %d was not re-linked; linked = %v", aID, calendarNames(linked))
	}
	gotB, ok := byID[bID]
	if !ok {
		t.Fatalf("B id %d was not re-linked; linked = %v", bID, calendarNames(linked))
	}
	if gotA.Name != "A" || strings.Contains(gotA.Name, "(2)") {
		t.Fatalf("A name = %q, want original without suffix", gotA.Name)
	}
	if gotB.Name != "B" || strings.Contains(gotB.Name, "(2)") {
		t.Fatalf("B name = %q, want original without suffix", gotB.Name)
	}
	if storage.NullableToString(gotA.RemoteColor) != "#112233" {
		t.Errorf("A remote_color = %q, want discovery color", storage.NullableToString(gotA.RemoteColor))
	}
	if storage.NullableToString(gotB.RemoteColor) != "#445566" {
		t.Errorf("B remote_color = %q, want discovery color", storage.NullableToString(gotB.RemoteColor))
	}

	gotEvent, err := evtSvc.Get(ctx, stay.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if gotEvent.CalendarID != aID {
		t.Fatalf("event calendar_id = %d, want original %d", gotEvent.CalendarID, aID)
	}
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

func TestServiceDeleteRestoresCredentialOnCommitFailure(t *testing.T) {
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
	installAccountDeleteCommitFailure(t, db)

	if err := svc.Delete(ctx, account.ID, store); err == nil {
		t.Fatal("Delete: expected deferred commit error, got nil")
	}
	// The account row rolls back because the transaction never committed.
	if _, err := q.GetAccount(ctx, account.ID); err != nil {
		t.Fatalf("account was not rolled back after commit failure: %v", err)
	}
	// The credential was deleted inside the transaction; the commit then failed,
	// so compensation must restore the prior credential the keyring held.
	if _, err := store.Get(account.ID, ""); err != nil {
		t.Fatalf("credential was not restored after commit failure: %v", err)
	}
}

func TestServiceRemoveWithCalendarsRestoresCredentialOnCommitFailure(t *testing.T) {
	f := newSelectionFixture(t)
	imported, discovery := f.importAndRefresh(t, "/cal/a/")
	installAccountDeleteCommitFailure(t, f.db)

	_, err := f.svc.RemoveWithCalendars(context.Background(), discovery.Account.ID, RemoveParams{}, f.store)
	if err == nil {
		t.Fatal("RemoveWithCalendars: expected deferred commit error, got nil")
	}
	if _, err := f.q.GetAccount(context.Background(), discovery.Account.ID); err != nil {
		t.Fatalf("account was not rolled back after commit failure: %v", err)
	}
	if _, err := f.q.GetCalendar(context.Background(), imported.CreatedIDs[0]); err != nil {
		t.Fatalf("calendar was not rolled back after commit failure: %v", err)
	}
	if _, err := f.store.Get(discovery.Account.ID, ""); err != nil {
		t.Fatalf("credential was not restored after commit failure: %v", err)
	}
}

func TestReconcileSelectionRestoresCredentialOnCommitFailure(t *testing.T) {
	f := newSelectionFixture(t)
	_, discovery := f.importAndRefresh(t, "/cal/a/")
	installAccountDeleteCommitFailure(t, f.db)

	// Selecting nothing removes the now-empty account, so the credential is
	// deleted inside the transaction and must be restored when commit fails.
	_, err := f.svc.ReconcileSelection(context.Background(), discovery, SelectionParams{}, f.store)
	if err == nil {
		t.Fatal("ReconcileSelection: expected deferred commit error, got nil")
	}
	if _, err := f.q.GetAccount(context.Background(), discovery.Account.ID); err != nil {
		t.Fatalf("account was not rolled back after commit failure: %v", err)
	}
	if _, err := f.store.Get(discovery.Account.ID, ""); err != nil {
		t.Fatalf("credential was not restored after commit failure: %v", err)
	}
}

// TestReconcileSelectionRejectsIDAndPathPromotionTarget locks the intentional
// difference that a replacement default may be specified by ID OR path, not
// both. A supply of both is ambiguous. It must be rejected before any work.
func TestReconcileSelectionRejectsIDAndPathPromotionTarget(t *testing.T) {
	f := newSelectionFixture(t)
	imported, discovery := f.importAndRefresh(t, "/cal/a/")
	ctx := context.Background()
	if err := f.q.ClearDefaultCalendar(ctx); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	if err := f.q.SetCalendarAsDefault(ctx, imported.CreatedIDs[0]); err != nil {
		t.Fatalf("set imported default: %v", err)
	}

	// /cal/a/ (the default) is deselected, so a promotion is required;
	// specifying the replacement by both ID and path is rejected.
	_, err := f.svc.ReconcileSelection(ctx, discovery, SelectionParams{
		SelectedPaths:  []string{"/cal/b/"},
		NewDefaultID:   imported.CreatedIDs[0],
		NewDefaultPath: "/cal/b/",
	}, f.store)
	if !errors.Is(err, calendar.ErrInvalidPromotionTarget) {
		t.Fatalf("err = %v, want calendar.ErrInvalidPromotionTarget", err)
	}
}

// TestServiceRemoveWithCalendarsRejectsReplacementWhenDefaultNotRemoved locks
// the intentional difference. RemoveWithCalendars rejects a replacement
// default when the removed account does not own the current default. No
// promotion is required in that case.
func TestServiceRemoveWithCalendarsRejectsReplacementWhenDefaultNotRemoved(t *testing.T) {
	f := newSelectionFixture(t)
	f.importAndRefresh(t, "/cal/a/")
	ctx := context.Background()

	// The application default is the seeded calendar, not the account's
	// imported /cal/a/, so removing the account needs no promotion.
	all, err := f.q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	var defaultID int64
	for _, row := range all {
		if row.IsDefault == 1 {
			defaultID = row.ID
			break
		}
	}
	if defaultID == 0 {
		t.Fatal("fixture has no default calendar")
	}

	_, err = f.svc.RemoveWithCalendars(ctx, f.discovery.Account.ID, RemoveParams{NewDefaultID: defaultID}, f.store)
	if !errors.Is(err, calendar.ErrInvalidPromotionTarget) {
		t.Fatalf("err = %v, want calendar.ErrInvalidPromotionTarget", err)
	}
}

func TestUniqueUnlinkedByRemoteIdentity_RequiresSameOrigin(t *testing.T) {
	t.Parallel()
	stored := remoteIdentityKey("/cal/work/", "https://cal.example.test/")
	rows := []storage.Calendar{{
		ID:        7,
		RemoteUrl: storage.StringToNullable(stored),
	}}
	lookup := remoteIdentityKey("/cal/work/", "https://other.example.test/")
	if _, ok := uniqueUnlinkedByRemoteIdentity(rows, "https://other.example.test/")[lookup]; ok {
		t.Fatalf("different origin matched lookup %q", lookup)
	}
	key := remoteIdentityKey("/cal/work/", "https://cal.example.test/")
	got := uniqueUnlinkedByRemoteIdentity(rows, "https://cal.example.test/")
	if got[key] != 7 {
		t.Fatalf("same origin = %v, want id 7 at %q", got, key)
	}
}

func TestUniqueUnlinkedByRemoteIdentity_DropsDuplicates(t *testing.T) {
	t.Parallel()
	server := "https://cal.example.test/"
	key := remoteIdentityKey("/cal/work/", server)
	other := remoteIdentityKey("/cal/other/", server)
	linkedID := int64(9)
	rows := []storage.Calendar{
		{ID: 1, RemoteUrl: storage.StringToNullable(key)},
		{ID: 2, RemoteUrl: storage.StringToNullable(key)},
		{ID: 3, RemoteUrl: storage.StringToNullable(other)},
		{ID: 4, RemoteUrl: storage.StringToNullable(key)},
		{ID: 5, AccountID: &linkedID, RemoteUrl: storage.StringToNullable(other)},
	}
	got := uniqueUnlinkedByRemoteIdentity(rows, server)
	if _, ok := got[key]; ok {
		t.Fatalf("ambiguous key present: %v", got)
	}
	if got[other] != 3 {
		t.Fatalf("unique key = %v, want id 3 at %q", got, other)
	}
}
