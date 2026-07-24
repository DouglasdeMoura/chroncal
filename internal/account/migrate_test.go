package account

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// newMigrationFixture opens a fresh DB, creates a destination account, and
// returns the service plus the seeded local "Personal" calendar (id 1). Tests
// add events/todos/journals to a local source and assert migration outcomes.
func newMigrationFixture(t *testing.T) (*Service, *storage.Queries, *sql.DB, Account) {
	t.Helper()
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, q)
	ctx := context.Background()
	acct, err := svc.Create(ctx, CreateParams{
		Name:      "Google",
		ServerURL: "https://apidata.googleusercontent.com/caldav/v2/",
		Username:  "me@example.test",
		AuthType:  "oauth2",
	}, auth.Credential{Username: "me@example.test", AccessToken: "token"}, newMemoryCredentialStore())
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return svc, q, db, acct
}

// discoveryFor builds a Discovery whose single importable collection is the
// chosen destination path, owned by acct.
func discoveryFor(acct Account, path, name string) Discovery {
	return Discovery{
		Account: acct,
		Calendars: []DiscoveredCalendar{{
			RemoteCalendar: caldav.RemoteCalendar{
				Path:                  path,
				Name:                  name,
				Color:                 "#9e69af",
				Access:                caldav.CalendarAccessOwner,
				SupportedComponentSet: []string{"VEVENT", "VTODO", "VJOURNAL"},
			},
			Importable: true,
		}},
	}
}

func insertEvent(t *testing.T, db *sql.DB, calendarID int64, uid, title string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO events (uid, calendar_id, title, start_time, end_time)
		VALUES (?, ?, ?, '2026-05-01T10:00:00Z', '2026-05-01T11:00:00Z')`,
		uid, calendarID, title)
	if err != nil {
		t.Fatalf("insert event %q: %v", uid, err)
	}
}

func insertTodo(t *testing.T, db *sql.DB, calendarID int64, uid, summary string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO todos (uid, calendar_id, summary, due_date)
		VALUES (?, ?, ?, '2026-05-01T17:00:00Z')`, uid, calendarID, summary)
	if err != nil {
		t.Fatalf("insert todo %q: %v", uid, err)
	}
}

func insertJournal(t *testing.T, db *sql.DB, calendarID int64, uid, summary string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO journals (uid, calendar_id, summary, start_date)
		VALUES (?, ?, ?, '2026-05-01T00:00:00Z')`, uid, calendarID, summary)
	if err != nil {
		t.Fatalf("insert journal %q: %v", uid, err)
	}
}

func insertSoftDeletedEvent(t *testing.T, db *sql.DB, calendarID int64, uid string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `INSERT INTO events (uid, calendar_id, title, start_time, end_time, deleted_at)
		VALUES (?, ?, 'gone', '2026-05-01T10:00:00Z', '2026-05-01T11:00:00Z', '2026-04-01T00:00:00Z')`,
		uid, calendarID)
	if err != nil {
		t.Fatalf("insert soft-deleted event %q: %v", uid, err)
	}
}

func calendarExists(t *testing.T, q *storage.Queries, id int64) bool {
	t.Helper()
	_, err := q.GetCalendar(context.Background(), id)
	if err == nil {
		return true
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	t.Fatalf("GetCalendar %d: %v", id, err)
	return false
}

func eventCalendarID(t *testing.T, q *storage.Queries, uid string) int64 {
	t.Helper()
	row, err := q.GetEventByUID(context.Background(), uid)
	if err != nil {
		t.Fatalf("GetEventByUID %q: %v", uid, err)
	}
	return row.CalendarID
}

func dirtyCount(t *testing.T, db *sql.DB, calendarID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sync_resources WHERE calendar_id = ? AND dirty = 1`, calendarID).Scan(&n); err != nil {
		t.Fatalf("count dirty: %v", err)
	}
	return n
}

// TestMigrateCalendarToAccount_MovesContentsAndRetiresSource is the direct
// end-to-end smoke scenario: a local calendar's events and todos move onto a
// newly linked destination calendar, get flagged dirty for upload, and the
// source calendar is retired — all atomically.
func TestMigrateCalendarToAccount_MovesContentsAndRetiresSource(t *testing.T) {
	svc, q, db, acct := newMigrationFixture(t)
	ctx := context.Background()

	source, err := q.GetCalendar(ctx, 1) // seeded "Personal"
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	insertEvent(t, db, source.ID, "evt-1", "Standup")
	insertEvent(t, db, source.ID, "evt-2", "Review")
	insertTodo(t, db, source.ID, "todo-1", "Pay bill")
	insertJournal(t, db, source.ID, "journal-1", "Daily note")
	insertSoftDeletedEvent(t, db, source.ID, "evt-deleted")

	disc := discoveryFor(acct, "/cal/me/work/", "Work")

	res, err := svc.MigrateCalendarToAccount(ctx, source.ID, disc, "/cal/me/work/")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !res.CreatedDestination {
		t.Errorf("CreatedDestination = false, want true (no prior local row)")
	}
	if res.Events != 2 || res.Todos != 1 || res.Journals != 1 {
		t.Errorf("counts = events:%d todos:%d journals:%d, want 2/1/1", res.Events, res.Todos, res.Journals)
	}

	if calendarExists(t, q, source.ID) {
		t.Error("source calendar still exists; it must be retired")
	}
	dest, err := q.GetCalendar(ctx, res.DestinationID)
	if err != nil {
		t.Fatalf("get destination: %v", err)
	}
	if dest.AccountID == nil || *dest.AccountID != acct.ID {
		t.Errorf("destination account_id = %v, want %d", dest.AccountID, acct.ID)
	}
	if got := storage.NullableToString(dest.RemoteUrl); got != "/cal/me/work/" {
		t.Errorf("destination remote_url = %q, want /cal/me/work/", got)
	}

	// Live events + todo moved to destination; soft-deleted event moved too.
	for _, uid := range []string{"evt-1", "evt-2"} {
		if got := eventCalendarID(t, q, uid); got != res.DestinationID {
			t.Errorf("event %q calendar_id = %d, want %d", uid, got, res.DestinationID)
		}
	}
	deleted, err := q.GetEventByUIDIncludingDeleted(ctx, "evt-deleted")
	if err != nil {
		t.Fatalf("get soft-deleted event: %v", err)
	}
	if deleted.CalendarID != res.DestinationID {
		t.Errorf("soft-deleted event calendar_id = %d, want %d", deleted.CalendarID, res.DestinationID)
	}
	var todoCal int64
	if err := db.QueryRowContext(context.Background(), `SELECT calendar_id FROM todos WHERE uid = 'todo-1'`).Scan(&todoCal); err != nil {
		t.Fatalf("read todo: %v", err)
	}
	if todoCal != res.DestinationID {
		t.Errorf("todo calendar_id = %d, want %d", todoCal, res.DestinationID)
	}
	var journalCal int64
	if err := db.QueryRowContext(context.Background(), `SELECT calendar_id FROM journals WHERE uid = 'journal-1'`).Scan(&journalCal); err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if journalCal != res.DestinationID {
		t.Errorf("journal calendar_id = %d, want %d", journalCal, res.DestinationID)
	}

	// Every live resource must be flagged dirty for the first upload.
	if got := dirtyCount(t, db, res.DestinationID); got != 4 {
		t.Errorf("dirty sync_resources = %d, want 4 (2 events + 1 todo + 1 journal)", got)
	}
}

// TestMigrateCalendarToAccount_ReusesExistingDestinationRow merges the source
// into a collection that is already linked locally instead of creating a
// duplicate row that would sync the same remote URL twice.
func TestMigrateCalendarToAccount_ReusesExistingDestinationRow(t *testing.T) {
	svc, q, db, acct := newMigrationFixture(t)
	ctx := context.Background()

	disc := discoveryFor(acct, "/cal/me/work/", "Work")
	existing, err := svc.Import(ctx, disc, []string{"/cal/me/work/"})
	if err != nil {
		t.Fatalf("Import destination: %v", err)
	}
	if len(existing.CreatedIDs) != 1 {
		t.Fatalf("import created = %+v, want one", existing)
	}
	destID := existing.CreatedIDs[0]

	source, _ := q.GetCalendar(ctx, 1)
	insertEvent(t, db, source.ID, "evt-merge", "Migrate me")

	res, err := svc.MigrateCalendarToAccount(ctx, source.ID, disc, "/cal/me/work/")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.CreatedDestination {
		t.Error("CreatedDestination = true, want false (row already existed)")
	}
	if res.DestinationID != destID {
		t.Errorf("DestinationID = %d, want existing %d", res.DestinationID, destID)
	}
	if calendarExists(t, q, source.ID) {
		t.Error("source still exists")
	}
	if got := eventCalendarID(t, q, "evt-merge"); got != destID {
		t.Errorf("moved event calendar_id = %d, want %d", got, destID)
	}
}

// TestMigrateCalendarToAccount_PromotesDefault transfers defaultness to the
// destination so the app never observes a missing default.
func TestMigrateCalendarToAccount_PromotesDefault(t *testing.T) {
	svc, q, db, acct := newMigrationFixture(t)
	ctx := context.Background()

	source, _ := q.GetCalendar(ctx, 1)
	if source.IsDefault == 0 {
		t.Fatalf("seed calendar is not default: %+v", source)
	}
	insertEvent(t, db, source.ID, "evt-def", "Default event")

	disc := discoveryFor(acct, "/cal/me/work/", "Work")
	res, err := svc.MigrateCalendarToAccount(ctx, source.ID, disc, "/cal/me/work/")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	def, err := q.GetDefaultCalendar(ctx)
	if err != nil {
		t.Fatalf("GetDefaultCalendar: %v", err)
	}
	if def.ID != res.DestinationID {
		t.Errorf("default = %d, want destination %d", def.ID, res.DestinationID)
	}
}

// TestMigrateCalendarToAccount_RejectsRemoteSource enforces that only local
// calendars can enter the flow.
func TestMigrateCalendarToAccount_RejectsRemoteSource(t *testing.T) {
	svc, q, _, acct := newMigrationFixture(t)
	ctx := context.Background()

	disc := discoveryFor(acct, "/cal/me/work/", "Work")
	imported, err := svc.Import(ctx, disc, []string{"/cal/me/work/"})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	remoteID := imported.CreatedIDs[0]

	_, err = svc.MigrateCalendarToAccount(ctx, remoteID, disc, "/cal/me/work/")
	if !errors.Is(err, ErrCannotMigrateRemoteCalendar) {
		t.Errorf("migrating a remote calendar: err = %v, want ErrCannotMigrateRemoteCalendar", err)
	}
	// Remote calendar must be untouched.
	if !calendarExists(t, q, remoteID) {
		t.Error("remote calendar was removed by a rejected migration")
	}
}

func TestMigrateCalendarToAccount_RejectsReadOnlyDestination(t *testing.T) {
	svc, q, _, acct := newMigrationFixture(t)
	ctx := context.Background()
	source, _ := q.GetCalendar(ctx, 1)
	disc := discoveryFor(acct, "/cal/me/read-only/", "Read only")
	disc.Calendars[0].Access = caldav.CalendarAccessRead

	if _, err := svc.MigrateCalendarToAccount(ctx, source.ID, disc, "/cal/me/read-only/"); err == nil {
		t.Fatal("migration into a read-only collection succeeded")
	}
	if !calendarExists(t, q, source.ID) {
		t.Fatal("read-only rejection retired the source calendar")
	}
}

// TestMigrateCalendarToAccount_RejectsUnknownPath guards the contract that the
// selected path must come from the supplied discovery, and that a rejected
// migration leaves the source intact.
func TestMigrateCalendarToAccount_RejectsUnknownPath(t *testing.T) {
	svc, q, _, acct := newMigrationFixture(t)
	ctx := context.Background()
	source, _ := q.GetCalendar(ctx, 1)
	disc := discoveryFor(acct, "/cal/me/work/", "Work")
	if _, err := svc.MigrateCalendarToAccount(ctx, source.ID, disc, "/cal/me/elsewhere/"); err == nil {
		t.Error("migrating to an unknown path succeeded; want error")
	}
	if !calendarExists(t, q, source.ID) {
		t.Error("source removed by a rejected migration")
	}
}

// TestMigrateCalendarToAccount_RollbackOnFailure confirms a migration against
// a since-deleted account fails cleanly and leaves the source and its data
// untouched — no partial state leaks.
func TestMigrateCalendarToAccount_RollbackOnFailure(t *testing.T) {
	svc, q, db, acct := newMigrationFixture(t)
	ctx := context.Background()
	source, _ := q.GetCalendar(ctx, 1)
	insertEvent(t, db, source.ID, "evt-rb", "Keep me")

	disc := discoveryFor(acct, "/cal/me/work/", "Work")
	if err := q.DeleteAccount(ctx, acct.ID); err != nil {
		t.Fatalf("delete account row: %v", err)
	}
	_, err := svc.MigrateCalendarToAccount(ctx, source.ID, disc, "/cal/me/work/")
	if err == nil {
		t.Error("migration against a deleted account succeeded; want error")
	}
	if !calendarExists(t, q, source.ID) {
		t.Error("source calendar removed by a failed migration")
	}
	if got := eventCalendarID(t, q, "evt-rb"); got != source.ID {
		t.Errorf("event moved during a failed migration: calendar_id = %d, want %d", got, source.ID)
	}
}

// TestMigrateCalendarToAccount_SettlesCollidingDestinationName covers the
// common same-name move: the destination is created while the source still
// holds the name, so it gets a collision suffix mid-transaction — but once the
// source is deleted the plain name is free and must be settled on before
// commit.
func TestMigrateCalendarToAccount_SettlesCollidingDestinationName(t *testing.T) {
	svc, q, _, acct := newMigrationFixture(t)
	ctx := context.Background()
	source, err := q.GetCalendar(ctx, 1) // seeded "Personal"
	if err != nil {
		t.Fatalf("get source: %v", err)
	}

	disc := discoveryFor(acct, "/cal/me/personal/", source.Name)
	res, err := svc.MigrateCalendarToAccount(ctx, source.ID, disc, "/cal/me/personal/")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	dest, err := q.GetCalendar(ctx, res.DestinationID)
	if err != nil {
		t.Fatalf("get destination: %v", err)
	}
	if dest.Name != source.Name {
		t.Errorf("destination name = %q, want %q (collision suffix must not outlive the source)", dest.Name, source.Name)
	}
}

// TestMigrateCalendarToAccount_CountsSeriesOnce locks the MigrateResult
// contract: a recurring master and its overrides share one UID, so the series
// counts once and yields a single dirty sync resource — not one per row.
func TestMigrateCalendarToAccount_CountsSeriesOnce(t *testing.T) {
	svc, q, db, acct := newMigrationFixture(t)
	ctx := context.Background()
	source, err := q.GetCalendar(ctx, 1)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_rule)
		VALUES ('rec-1', ?, 'Standup', '2026-05-01T10:00:00Z', '2026-05-01T11:00:00Z', 'FREQ=DAILY')`, source.ID); err != nil {
		t.Fatalf("insert recurring master: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id)
		VALUES ('rec-1', ?, 'Standup (moved)', '2026-05-02T12:00:00Z', '2026-05-02T13:00:00Z', '2026-05-02T10:00:00Z')`, source.ID); err != nil {
		t.Fatalf("insert override: %v", err)
	}

	res, err := svc.MigrateCalendarToAccount(ctx, source.ID, discoveryFor(acct, "/cal/me/work/", "Work"), "/cal/me/work/")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Events != 1 {
		t.Errorf("Events = %d, want 1 (master + override share a uid)", res.Events)
	}
	if got := dirtyCount(t, db, res.DestinationID); got != 1 {
		t.Errorf("dirty sync_resources = %d, want 1 for the shared uid", got)
	}
}
