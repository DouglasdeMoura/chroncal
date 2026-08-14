package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
)

// ErrCannotMigrateRemoteCalendar is returned when the source calendar is
// already linked to a remote account. Only local-only calendars can be moved
// into an account; a calendar that already belongs to an account has nothing
// to migrate.
var ErrCannotMigrateRemoteCalendar = errors.New("only a local calendar can be moved to an account")

// MigrateResult summarizes a successful "Move to Account" migration.
type MigrateResult struct {
	// DestinationID is the local calendar row now linked to the chosen
	// remote collection. It is either a freshly created row or, when the
	// collection was already linked locally, that existing row.
	DestinationID int64

	// CreatedDestination is true when migration created a new local calendar
	// row for the collection, and false when it merged into one that already
	// existed.
	CreatedDestination bool

	// Events, Todos, and Journals count the distinct live (non-soft-deleted)
	// identities reassigned to the destination and marked dirty for the first
	// sync. A recurring master and its overrides share a UID, so a series
	// counts once.
	Events   int
	Todos    int
	Journals int
}

// MigrateCalendarToAccount moves a local calendar's contents into a stored
// remote collection on the destination account. It then retires the local
// calendar. That is chroncal's "Move to Account…" flow.
//
// The destination account and collection are described by discovery. The
// caller obtains that from [Service.Discover]. selectedPath must identify one
// of its collections. Migration never contacts the server. Discovery already
// proved the account is reachable. The first sync performs the upload. The
// caller starts that sync with the returned DestinationID.
//
// Within a single transaction the method:
//  1. Resolves or creates the destination calendar row linked to the chosen
//     collection. Idempotent: reuse a stored row when the collection is
//     already linked locally. That mirrors [Service.Import].
//  2. Flags every live identity on the source dirty on the destination (one
//     set-based statement per table) so the next sync uploads it.
//  3. Reassigns every event, todo, and journal — live and soft-deleted — from
//     the source to the destination.
//  4. Promotes the destination to default when the source was the default.
//  5. Deletes the source calendar.
//  6. Settles a freshly created destination on its preferred name, which the
//     source may have held until it was deleted.
//
// Every failure rolls the transaction back. The source calendar and
// its data stay untouched. A successful commit followed by a failed first
// sync is still non-destructive. The moved data lives on the destination
// calendar, dirty, and retries on the next sync.
func (s *Service) MigrateCalendarToAccount(
	ctx context.Context,
	sourceID int64,
	discovery Discovery,
	selectedPath string,
) (MigrateResult, error) {
	if discovery.Account.ID == 0 {
		return MigrateResult{}, errors.New("migration requires a discovered account")
	}
	chosen, ok := discovery.calendarByPath(selectedPath)
	if !ok {
		return MigrateResult{}, fmt.Errorf("calendar %q was not part of this discovery", selectedPath)
	}
	if !chosen.Importable {
		return MigrateResult{}, fmt.Errorf("calendar %q has no supported event, todo, or journal components", chosen.Name)
	}
	if chosen.Access == caldav.CalendarAccessRead {
		return MigrateResult{}, fmt.Errorf("calendar %q is read-only", chosen.Name)
	}

	release, err := synclock.Account(ctx, s.db, discovery.Account.ID)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("lock migration destination account: %w", err)
	}
	defer release()

	source, err := s.q.GetCalendar(ctx, sourceID)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("get source calendar: %w", err)
	}
	if source.AccountID != nil && *source.AccountID != 0 {
		return MigrateResult{}, ErrCannotMigrateRemoteCalendar
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	destinationID, created, err := s.resolveDestinationCalendar(ctx, qtx, discovery, chosen)
	if err != nil {
		return MigrateResult{}, err
	}

	// Count and dirty-flag the source's live identities before reassignment,
	// while the source rows still identify exactly what moves. Re-flagging an
	// identity the destination already tracks is a harmless idempotent
	// re-push of content the remote already holds.
	eventCount, err := qtx.CountEventIdentitiesByCalendar(ctx, sourceID)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("count source events: %w", err)
	}
	todoCount, err := qtx.CountTodoIdentitiesByCalendar(ctx, sourceID)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("count source todos: %w", err)
	}
	journalCount, err := qtx.CountJournalIdentitiesByCalendar(ctx, sourceID)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("count source journals: %w", err)
	}
	if err := qtx.MarkEventIdentitiesDirtyForMigration(ctx, storage.MarkEventIdentitiesDirtyForMigrationParams{
		CalendarID:   destinationID,
		CalendarID_2: sourceID,
	}); err != nil {
		return MigrateResult{}, fmt.Errorf("mark events dirty: %w", err)
	}
	if err := qtx.MarkTodoIdentitiesDirtyForMigration(ctx, storage.MarkTodoIdentitiesDirtyForMigrationParams{
		CalendarID:   destinationID,
		CalendarID_2: sourceID,
	}); err != nil {
		return MigrateResult{}, fmt.Errorf("mark todos dirty: %w", err)
	}
	if err := qtx.MarkJournalIdentitiesDirtyForMigration(ctx, storage.MarkJournalIdentitiesDirtyForMigrationParams{
		CalendarID:   destinationID,
		CalendarID_2: sourceID,
	}); err != nil {
		return MigrateResult{}, fmt.Errorf("mark journals dirty: %w", err)
	}

	if _, err := qtx.ReassignEventsCalendar(ctx, storage.ReassignEventsCalendarParams{
		CalendarID:   destinationID,
		CalendarID_2: sourceID,
	}); err != nil {
		return MigrateResult{}, fmt.Errorf("reassign events: %w", err)
	}
	if _, err := qtx.ReassignTodosCalendar(ctx, storage.ReassignTodosCalendarParams{
		CalendarID:   destinationID,
		CalendarID_2: sourceID,
	}); err != nil {
		return MigrateResult{}, fmt.Errorf("reassign todos: %w", err)
	}
	if _, err := qtx.ReassignJournalsCalendar(ctx, storage.ReassignJournalsCalendarParams{
		CalendarID:   destinationID,
		CalendarID_2: sourceID,
	}); err != nil {
		return MigrateResult{}, fmt.Errorf("reassign journals: %w", err)
	}

	if source.IsDefault != 0 {
		if err := calendar.PromoteDefault(ctx, qtx, map[int64]struct{}{sourceID: {}}, destinationID); err != nil {
			return MigrateResult{}, fmt.Errorf("promote destination default: %w", err)
		}
	}

	if err := qtx.DeleteCalendar(ctx, sourceID); err != nil {
		return MigrateResult{}, fmt.Errorf("retire source calendar: %w", err)
	}

	if created {
		if err := settleDestinationName(ctx, qtx, destinationID, remoteCalendarName(chosen.RemoteCalendar)); err != nil {
			return MigrateResult{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return MigrateResult{}, fmt.Errorf("commit migration: %w", err)
	}

	return MigrateResult{
		DestinationID:      destinationID,
		CreatedDestination: created,
		Events:             int(eventCount),
		Todos:              int(todoCount),
		Journals:           int(journalCount),
	}, nil
}

// settleDestinationName renames a freshly created destination calendar to the
// best available local name once the source row is gone. The destination was
// named while the source still held its own name. A same-name move then gets a
// collision suffix ("Personal (2)"). That suffix would otherwise persist after
// the only calendar that held the plain name was deleted.
func settleDestinationName(ctx context.Context, qtx *storage.Queries, destinationID int64, preferred string) error {
	all, err := qtx.ListCalendars(ctx)
	if err != nil {
		return fmt.Errorf("list calendar names: %w", err)
	}
	taken := make(map[string]struct{}, len(all))
	var current string
	for _, row := range all {
		if row.ID == destinationID {
			current = row.Name
			continue
		}
		taken[row.Name] = struct{}{}
	}
	best := uniqueLocalName(preferred, taken)
	if best == current {
		return nil
	}
	if err := qtx.RenameCalendar(ctx, storage.RenameCalendarParams{
		Name: best,
		ID:   destinationID,
	}); err != nil {
		return fmt.Errorf("rename destination calendar: %w", err)
	}
	return nil
}

// resolveDestinationCalendar returns the local calendar row linked to the
// chosen collection. It creates one (mirrors Import) when no such row exists
// yet. The returned created flag distinguishes the two cases for the result.
func (s *Service) resolveDestinationCalendar(
	ctx context.Context,
	qtx *storage.Queries,
	discovery Discovery,
	chosen DiscoveredCalendar,
) (int64, bool, error) {
	accountID := discovery.Account.ID
	existing, err := qtx.ListCalendarsByAccount(ctx, &accountID)
	if err != nil {
		return 0, false, fmt.Errorf("list destination account calendars: %w", err)
	}
	wantKey := remoteIdentityKey(chosen.Path, discovery.Account.ServerURL)
	for _, row := range existing {
		if remoteIdentityKey(storage.NullableToString(row.RemoteUrl), discovery.Account.ServerURL) == wantKey {
			return row.ID, false, nil
		}
	}

	// No local row yet: create one exactly as Import does, reserving a
	// non-colliding local name while the source still holds its own.
	all, err := qtx.ListCalendars(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("list calendar names: %w", err)
	}
	taken := make(map[string]struct{}, len(all))
	for _, row := range all {
		taken[row.Name] = struct{}{}
	}
	row, err := createDiscoveredCalendarRow(ctx, qtx, discovery.Account, chosen, taken)
	if err != nil {
		return 0, false, fmt.Errorf("create destination calendar: %w", err)
	}
	return row.ID, true, nil
}

// calendarByPath looks up a discovered collection by its remote path.
func (d Discovery) calendarByPath(path string) (DiscoveredCalendar, bool) {
	for _, c := range d.Calendars {
		if c.Path == path {
			return c, true
		}
	}
	return DiscoveredCalendar{}, false
}
