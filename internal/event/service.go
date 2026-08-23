package event

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

type Service struct {
	db *sql.DB
	q  *storage.Queries
	// tx is non-nil when the service runs inside a caller-managed
	// transaction (see WithTx). When set, q is already bound to tx and the
	// per-method write helpers join the outer transaction. They do not open
	// their own. A multi-step sequence then commits or rolls back atomically.
	tx *sql.Tx
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
}

// WithTx returns a copy of the service whose writes run inside tx. The caller
// owns tx (commit/rollback). The returned service's write methods neither
// begin nor commit their own transaction. Several calls can then compose into
// a single atomic unit.
func (s *Service) WithTx(tx *sql.Tx) *Service {
	return &Service{db: s.db, q: s.q.WithTx(tx), tx: tx}
}

// txscope returns a transaction-scoped Queries plus commit and rollback
// helpers. When the service already runs inside a caller-managed transaction
// (see WithTx), the work joins that transaction. Commit is a no-op. Rollback
// is left to the outer owner. Otherwise it opens and owns a fresh transaction.
func (s *Service) txscope(ctx context.Context) (qtx *storage.Queries, commit func() error, rollback func(), err error) {
	if s.tx != nil {
		return s.q, func() error { return nil }, func() {}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	return s.q.WithTx(tx), tx.Commit, func() { _ = tx.Rollback() }, nil
}

// dirtyExec returns the DBTX the dirty-mark side effect must use. That is the
// outer transaction when one is active, so the write joins it and cannot
// deadlock against the held write lock. Otherwise it is the pooled *sql.DB.
func (s *Service) dirtyExec() storage.DBTX {
	if s.tx != nil {
		return s.tx
	}
	return s.db
}

type CreateParams struct {
	CalendarID     int64
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
	Timezone       string
	Status         string
	Transp         string
	Sequence       int64
	Priority       int64
	Class          string
	URL            string
	ConferenceURI  string
	Categories     string
	ExDates        string
	RDates         string
	RecurrenceID   string
	Geo            string
	DurationValue  string
	DtStamp        string
}

type UpdateParams struct {
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
	CalendarID     int64
	Timezone       string
	Status         string
	Transp         string
	Priority       int64
	Class          string
	URL            string
	ConferenceURI  string
	Categories     string
	ExDates        string
	RDates         string
	Geo            string
	DurationValue  string
	DtStamp        string
}

type UpsertParams struct {
	UID            string
	CalendarID     int64
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
	Timezone       string
	Status         string
	Transp         string
	Sequence       int64
	Priority       int64
	Class          string
	URL            string
	ConferenceURI  string
	Categories     string
	ExDates        string
	RDates         string
	RecurrenceID   string
	Geo            string
	DurationValue  string
	DtStamp        string
}

func applyEventDefaults(status, transp, class *string) {
	*status = strings.ToUpper(*status)
	*transp = strings.ToUpper(*transp)
	*class = strings.ToUpper(*class)
	if *status == "" {
		*status = "CONFIRMED"
	}
	if *transp == "" {
		*transp = "OPAQUE"
	}
	if *class == "" {
		*class = "PUBLIC"
	}
}

// normalizeEventTimes coerces start/end to the storage invariant that all
// database times are RFC 3339 in UTC (see AGENTS.md). Without it, times built
// in a non-UTC zone (for example the CLI time.Local or a --timezone value)
// persist with an offset. They then sort incorrectly against the UTC bounds
// used by date-range queries. The event drops from list views (#254).
//
// Timed events keep their absolute instant. All-day events are pinned to UTC
// midnight on their wall-clock date. They then occupy exactly one calendar
// day regardless of the zone they were built in. This matches the TUI and
// iCal import. A plain .UTC() would shift local midnight onto the previous
// or next day and make the event surface on two days.
func normalizeEventTimes(start, end *time.Time, allDay bool) {
	*start = toStorageTime(*start, allDay)
	*end = toStorageTime(*end, allDay)
}

func toStorageTime(t time.Time, allDay bool) time.Time {
	if allDay {
		// Take the wall-clock date in t's own location before the zone is
		// dropped. Then pin to UTC midnight.
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return t.UTC()
}

func (p *CreateParams) applyDefaults() {
	applyEventDefaults(&p.Status, &p.Transp, &p.Class)
	normalizeEventTimes(&p.StartTime, &p.EndTime, p.AllDay)
}

func (p *UpsertParams) applyDefaults() {
	applyEventDefaults(&p.Status, &p.Transp, &p.Class)
	normalizeEventTimes(&p.StartTime, &p.EndTime, p.AllDay)
}

func (p *UpdateParams) applyDefaults() {
	applyEventDefaults(&p.Status, &p.Transp, &p.Class)
	normalizeEventTimes(&p.StartTime, &p.EndTime, p.AllDay)
}

// markDirtyTx marks the sync resource of the event with the given ID dirty
// inside the caller's transaction. A failed mark aborts the mutation per the
// issue #107 contract: a change that is never pushed must not commit.
func (s *Service) markDirtyByID(ctx context.Context, eventID int64) error {
	r, err := s.q.GetEvent(ctx, eventID)
	if err != nil {
		return fmt.Errorf("get event for dirty mark: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.dirtyExec(), r.CalendarID, r.Uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return nil
}

// ensureEventWritable resolves an event by ID and enforces remote
// access/component policy for a user-originated edit. For a cross-calendar
// move (destCalID set and different from the event's current calendar) the
// destination calendar is validated too. A move into a read-only or
// VEVENT-less collection then fails before any row is written.
func (s *Service) ensureEventWritable(ctx context.Context, eventID, destCalID int64) error {
	r, err := s.q.GetEvent(ctx, eventID)
	if err != nil {
		return err
	}
	if err := calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, "VEVENT"); err != nil {
		return err
	}
	if destCalID != 0 && destCalID != r.CalendarID {
		if err := calendaraccess.EnsureWritable(ctx, s.q, destCalID, "VEVENT"); err != nil {
			return err
		}
	}
	return nil
}

// distinctCalendarIDsByUID returns every calendar that any event row with the
// given UID lives on: master, overrides, or series-tail leftovers. A recurring
// series can leave orphaned rows behind after the master is purged on its own
// (the trash view supports series tails). A UID-keyed mutation must then
// enforce policy on each of these calendars, not only the master's.
func (s *Service) distinctCalendarIDsByUID(ctx context.Context, uid string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT calendar_id FROM events WHERE uid = ?`, uid)
	if err != nil {
		return nil, fmt.Errorf("distinct calendar ids: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan calendar id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate calendar ids: %w", err)
	}
	return ids, nil
}

// ensureSeriesWritable enforces remote access/component policy on every
// calendar the UID touches. A series delete or restore is then blocked even
// when only orphaned overrides or series-tail rows remain after a master purge.
func (s *Service) ensureSeriesWritable(ctx context.Context, uid string) error {
	ids, err := s.distinctCalendarIDsByUID(ctx, uid)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := calendaraccess.EnsureWritable(ctx, s.q, id, "VEVENT"); err != nil {
			return err
		}
	}
	return nil
}

// validateDurationValue applies the span rule that the todo service
// applies in validateTiming. The import path drops a bad value with a
// warning instead, so sync never reaches this error. The start time
// anchors the storability check. A span that carries the end past the
// storable range would write a time the database cannot read back
// (see timeutil.Storable).
func validateDurationValue(start time.Time, v string) error {
	if err := duration.ValidateOptionalSpan(v); err != nil {
		return err
	}
	if v == "" || start.IsZero() {
		return nil
	}
	if end := duration.Add(start, v); !timeutil.Storable(end) {
		return fmt.Errorf("invalid duration %q: the end time is past year %d", v, timeutil.MaxStorableYear)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Event, error) {
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, err
	}
	p.applyDefaults()

	if err := calendaraccess.EnsureWritable(ctx, s.q, p.CalendarID, "VEVENT"); err != nil {
		return Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	r, err := qtx.CreateEvent(ctx, storage.CreateEventParams{
		Uid:            uuid.New().String(),
		CalendarID:     p.CalendarID,
		Title:          p.Title,
		Description:    storage.StringToNullable(p.Description),
		Location:       storage.StringToNullable(p.Location),
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         storage.BoolToInt(p.AllDay),
		RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
		Timezone:       storage.StringToNullable(p.Timezone),
		Status:         p.Status,
		Transp:         p.Transp,
		Sequence:       p.Sequence,
		Priority:       p.Priority,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		Exdates:        storage.StringToNullable(p.ExDates),
		Rdates:         storage.StringToNullable(p.RDates),
		RecurrenceID:   p.RecurrenceID,
		Geo:            storage.StringToNullable(p.Geo),
		Duration:       storage.StringToNullable(p.DurationValue),
		Dtstamp:        storage.StringToNullable(p.DtStamp),
		ConferenceUri:  p.ConferenceURI,
	})
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	if cats := ParseCategoryList(p.Categories); len(cats) > 0 {
		if err := replaceCategoriesTx(ctx, qtx, e.ID, cats); err != nil {
			return Event{}, fmt.Errorf("replace categories: %w", err)
		}
	}
	// Mark dirty inside the transaction so a failed sync-tracking write rolls
	// the new event back rather than committing a row that can never be pushed
	// (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, e.CalendarID, e.UID, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit create event: %w", err)
	}
	e.Categories = p.Categories
	return e, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Event, error) {
	p.applyDefaults()

	if err := s.ensureEventWritable(ctx, id, p.CalendarID); err != nil {
		return Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, err := updateEventTx(ctx, qtx, id, p)
	if err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit update event: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, e.CalendarID, e.UID, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return e, nil
}

// UpdateWithRelations updates an event row together with its attendees and
// alarms in a single transaction. A failure in any child write then rolls the
// whole edit back (issue #87). The TUI edit path uses this instead of Update
// plus ReplaceAttendees/ReplaceAlarms in separate transactions. Those separate
// writes could leave a half-updated row when a later child write failed.
func (s *Service) UpdateWithRelations(ctx context.Context, id int64, p UpdateParams, attendees []model.Attendee, alarms []model.Alarm) (Event, error) {
	p.applyDefaults()

	if err := s.ensureEventWritable(ctx, id, p.CalendarID); err != nil {
		return Event{}, err
	}
	// Reject a bad attendee or alarm before the transaction opens. See
	// model.PrepareAttendeesForWrite and model.PrepareAlarmsForWrite.
	attendees, err := model.PrepareAttendeesForWrite(model.EventAttendee, attendees)
	if err != nil {
		return Event{}, err
	}
	alarms, err = model.PrepareAlarmsForWrite(alarms)
	if err != nil {
		return Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, err := updateEventTx(ctx, qtx, id, p)
	if err != nil {
		return Event{}, err
	}
	if err := replaceRelationsTx(ctx, qtx, e.ID, attendees, alarms); err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit update event: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, e.CalendarID, e.UID, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return e, nil
}

// updateEventTx writes the event row and its categories using a tx-bound
// Queries. It opens no transaction and does not commit or mark the resource
// dirty, so callers can compose it with attendee/alarm writes inside one
// transaction.
func updateEventTx(ctx context.Context, qtx *storage.Queries, id int64, p UpdateParams) (Event, error) {
	// Every update entry point reaches this function, so the span rule
	// lives here rather than at each caller.
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, err
	}
	old, err := qtx.GetEvent(ctx, id)
	if err != nil {
		return Event{}, err
	}
	r, err := qtx.UpdateEvent(ctx, storage.UpdateEventParams{
		ID:             id,
		Title:          p.Title,
		Description:    storage.StringToNullable(p.Description),
		Location:       storage.StringToNullable(p.Location),
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         storage.BoolToInt(p.AllDay),
		RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
		CalendarID:     p.CalendarID,
		Timezone:       storage.StringToNullable(p.Timezone),
		Status:         p.Status,
		Transp:         p.Transp,
		Priority:       p.Priority,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		Exdates:        storage.StringToNullable(p.ExDates),
		Rdates:         storage.StringToNullable(p.RDates),
		Geo:            storage.StringToNullable(p.Geo),
		Duration:       storage.StringToNullable(p.DurationValue),
		Dtstamp:        storage.StringToNullable(p.DtStamp),
		ConferenceUri:  p.ConferenceURI,
	})
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	if err := replaceCategoriesTx(ctx, qtx, e.ID, ParseCategoryList(p.Categories)); err != nil {
		return Event{}, fmt.Errorf("replace categories: %w", err)
	}
	if localSpanEdit(old, p) {
		if err := clearPreservedDTENDTx(ctx, qtx, e.ID); err != nil {
			return Event{}, err
		}
	}
	e.Categories = p.Categories
	return e, nil
}

// localSpanEdit reports whether an update replaces the stored end time or
// duration. Only a local edit goes through UpdateParams. A sync upsert
// bypasses this rule, because a fresh server body sets the slot again
// (issue #649).
func localSpanEdit(old storage.Event, p UpdateParams) bool {
	return old.EndTime != p.EndTime.Format(time.RFC3339) ||
		storage.NullableToString(old.Duration) != p.DurationValue
}

// clearPreservedDTENDTx removes the X-CHRONCAL-ORIGINAL-DTEND slot from the
// event's x-properties. It runs inside the caller's transaction and keeps
// every other x-property. It does nothing when the slot is absent.
//
// A local edit that changes the span invalidates the preserved server DTEND.
// An export after the edit must emit the edited span, not the stale server
// string (issue #649).
func clearPreservedDTENDTx(ctx context.Context, qtx *storage.Queries, eventID int64) error {
	rows, err := qtx.ListXPropertiesByOwner(ctx, storage.ListXPropertiesByOwnerParams{
		OwnerType: "event", OwnerID: eventID,
	})
	if err != nil {
		return fmt.Errorf("list x-properties: %w", err)
	}
	found := false
	for _, r := range rows {
		if r.Name == model.XPropOriginalDTEND {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if err := qtx.DeleteXPropertiesByOwner(ctx, storage.DeleteXPropertiesByOwnerParams{
		OwnerType: "event", OwnerID: eventID,
	}); err != nil {
		return fmt.Errorf("delete x-properties: %w", err)
	}
	for _, r := range rows {
		if r.Name == model.XPropOriginalDTEND {
			continue
		}
		if err := qtx.InsertXProperty(ctx, storage.InsertXPropertyParams{
			OwnerType: "event", OwnerID: eventID,
			Name: r.Name, Value: r.Value, Params: r.Params,
		}); err != nil {
			return fmt.Errorf("insert x-property: %w", err)
		}
	}
	return nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Event, error) {
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, err
	}
	p.applyDefaults()

	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return Event{}, err
	}
	defer rollback()

	r, err := qtx.UpsertEventByUID(ctx, storage.UpsertEventByUIDParams{
		Uid:            p.UID,
		CalendarID:     p.CalendarID,
		Title:          p.Title,
		Description:    storage.StringToNullable(p.Description),
		Location:       storage.StringToNullable(p.Location),
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         storage.BoolToInt(p.AllDay),
		RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
		Timezone:       storage.StringToNullable(p.Timezone),
		Status:         p.Status,
		Transp:         p.Transp,
		Sequence:       p.Sequence,
		Priority:       p.Priority,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		Exdates:        storage.StringToNullable(p.ExDates),
		Rdates:         storage.StringToNullable(p.RDates),
		RecurrenceID:   p.RecurrenceID,
		Geo:            storage.StringToNullable(p.Geo),
		Duration:       storage.StringToNullable(p.DurationValue),
		Dtstamp:        storage.StringToNullable(p.DtStamp),
		ConferenceUri:  p.ConferenceURI,
	})
	if err != nil {
		return Event{}, err
	}
	e := FromStorage(r)
	if err := replaceCategoriesTx(ctx, qtx, e.ID, ParseCategoryList(p.Categories)); err != nil {
		return Event{}, fmt.Errorf("replace categories: %w", err)
	}
	if err := commit(); err != nil {
		return Event{}, fmt.Errorf("commit upsert event: %w", err)
	}
	e.Categories = p.Categories
	return e, nil
}

// UpdateInstance creates or updates a per-occurrence override of a recurring
// event. The override is stored as a separate row with the same UID as the
// master. RecurrenceID matches the original (un-edited) instance start in UTC.
// The master row is not modified. The recurrence rule and every other
// instance then keep their previous behavior.
//
// instanceTime is the original occurrence time used as the override key (its
// RECURRENCE-ID). The new StartTime/EndTime in p reflect the user's edits and
// may differ. For example, a move of Wednesday's standup from 9:00 to 9:30
// sets RecurrenceID=2026-05-20T09:00:00Z but StartTime=2026-05-20T09:30:00Z.
func (s *Service) UpdateInstance(ctx context.Context, uid string, instanceTime time.Time, p UpdateParams) (Event, error) {
	p.applyDefaults()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, calendarID, err := updateInstanceTx(ctx, qtx, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit override: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, calendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return e, nil
}

// UpdateInstanceWithRelations is UpdateInstance plus an attendee/alarm write in
// the same transaction, so the override row and its children commit atomically
// (issue #87).
func (s *Service) UpdateInstanceWithRelations(ctx context.Context, uid string, instanceTime time.Time, p UpdateParams, attendees []model.Attendee, alarms []model.Alarm) (Event, error) {
	p.applyDefaults()

	// Reject a bad attendee or alarm before the transaction opens. See
	// model.PrepareAttendeesForWrite and model.PrepareAlarmsForWrite.
	attendees, err := model.PrepareAttendeesForWrite(model.EventAttendee, attendees)
	if err != nil {
		return Event{}, err
	}
	alarms, err = model.PrepareAlarmsForWrite(alarms)
	if err != nil {
		return Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, calendarID, err := updateInstanceTx(ctx, qtx, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	if err := replaceRelationsTx(ctx, qtx, e.ID, attendees, alarms); err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit override: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, calendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return e, nil
}

// updateInstanceTx creates or updates a per-occurrence override row and its
// categories using a tx-bound Queries. It returns the event and the master's
// calendar ID (for a dirty mark after commit). It opens no transaction, so
// callers can compose it with attendee/alarm writes.
func updateInstanceTx(ctx context.Context, qtx *storage.Queries, uid string, instanceTime time.Time, p UpdateParams) (Event, int64, error) {
	// Every update entry point reaches this function, so the span rule
	// lives here rather than at each caller.
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, 0, err
	}
	master, err := qtx.GetEventByUID(ctx, uid)
	if err != nil {
		return Event{}, 0, fmt.Errorf("get master: %w", err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return Event{}, 0, err
	}
	if p.CalendarID != 0 && p.CalendarID != master.CalendarID {
		if err := calendaraccess.EnsureWritable(ctx, qtx, p.CalendarID, "VEVENT"); err != nil {
			return Event{}, 0, err
		}
	}
	recID := instanceTime.UTC().Format(time.RFC3339)

	// Caller is the source of truth for categories. An empty p.Categories
	// means the user explicitly cleared the tags on this override.
	carriedCats := ParseCategoryList(p.Categories)

	var r storage.Event
	if existing, gErr := qtx.GetEventByUIDAndRecurrenceID(ctx, storage.GetEventByUIDAndRecurrenceIDParams{
		Uid:          uid,
		RecurrenceID: recID,
	}); gErr == nil {
		r, err = updateOverrideTx(ctx, qtx, existing, p)
		if err != nil {
			return Event{}, 0, fmt.Errorf("update override: %w", err)
		}
	} else {
		r, err = qtx.CreateEvent(ctx, overrideCreateParams(uid, recID, master.Sequence+1, p))
		if err != nil {
			// Concurrent override creation race: the UNIQUE(uid, recurrence_id)
			// constraint protects against duplicate rows, so retry as update.
			if isUniqueViolationOnRecurrenceID(err) {
				existing, eErr := qtx.GetEventByUIDAndRecurrenceID(ctx, storage.GetEventByUIDAndRecurrenceIDParams{
					Uid:          uid,
					RecurrenceID: recID,
				})
				if eErr != nil {
					return Event{}, 0, fmt.Errorf("retry get override: %w", eErr)
				}
				r, err = updateOverrideTx(ctx, qtx, existing, p)
				if err != nil {
					return Event{}, 0, fmt.Errorf("retry update override: %w", err)
				}
			} else {
				return Event{}, 0, fmt.Errorf("create override: %w", err)
			}
		}
	}

	if err := replaceCategoriesTx(ctx, qtx, r.ID, carriedCats); err != nil {
		return Event{}, 0, err
	}

	e := FromStorage(r)
	e.Categories = timeutil.JoinCategoryList(carriedCats)
	return e, master.CalendarID, nil
}

// updateOverrideTx updates a stored override row. A span change clears the
// preserved server DTEND slot on that row (issue #649). A fresh override row
// never carries the slot, so the create path needs no clear.
func updateOverrideTx(ctx context.Context, qtx *storage.Queries, existing storage.Event, p UpdateParams) (storage.Event, error) {
	r, err := qtx.UpdateEvent(ctx, overrideUpdateParams(existing.ID, p))
	if err != nil {
		return storage.Event{}, err
	}
	if localSpanEdit(existing, p) {
		if err := clearPreservedDTENDTx(ctx, qtx, existing.ID); err != nil {
			return storage.Event{}, err
		}
	}
	return r, nil
}

// overrideUpdateParams builds the storage params for an update of a stored
// override row. Recurrence-related fields are pinned to empty because an
// override never owns its own rule.
func overrideUpdateParams(id int64, p UpdateParams) storage.UpdateEventParams {
	return storage.UpdateEventParams{
		ID:             id,
		Title:          p.Title,
		Description:    storage.StringToNullable(p.Description),
		Location:       storage.StringToNullable(p.Location),
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         storage.BoolToInt(p.AllDay),
		RecurrenceRule: storage.StringToNullable(""),
		CalendarID:     p.CalendarID,
		Timezone:       storage.StringToNullable(p.Timezone),
		Status:         p.Status,
		Transp:         p.Transp,
		Priority:       p.Priority,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		Exdates:        storage.StringToNullable(""),
		Rdates:         storage.StringToNullable(""),
		Geo:            storage.StringToNullable(p.Geo),
		Duration:       storage.StringToNullable(p.DurationValue),
		Dtstamp:        storage.StringToNullable(p.DtStamp),
		ConferenceUri:  p.ConferenceURI,
	}
}

// overrideCreateParams builds the storage params for a fresh override row.
// seq should be the master's sequence + 1 so this override shows up as a
// later revision in iCal SEQUENCE terms.
func overrideCreateParams(uid, recID string, seq int64, p UpdateParams) storage.CreateEventParams {
	return storage.CreateEventParams{
		Uid:            uid,
		CalendarID:     p.CalendarID,
		Title:          p.Title,
		Description:    storage.StringToNullable(p.Description),
		Location:       storage.StringToNullable(p.Location),
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         storage.BoolToInt(p.AllDay),
		RecurrenceRule: storage.StringToNullable(""),
		Timezone:       storage.StringToNullable(p.Timezone),
		Status:         p.Status,
		Transp:         p.Transp,
		Sequence:       seq,
		Priority:       p.Priority,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		Exdates:        storage.StringToNullable(""),
		Rdates:         storage.StringToNullable(""),
		RecurrenceID:   recID,
		Geo:            storage.StringToNullable(p.Geo),
		Duration:       storage.StringToNullable(p.DurationValue),
		Dtstamp:        storage.StringToNullable(p.DtStamp),
		ConferenceUri:  p.ConferenceURI,
	}
}

// isUniqueViolationOnRecurrenceID returns true when err is a SQLite UNIQUE
// constraint violation on the (uid, recurrence_id) index — i.e. a concurrent
// override creation lost a race.
func isUniqueViolationOnRecurrenceID(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") &&
		strings.Contains(msg, "recurrence_id")
}

// UpdateFromInstance splits a recurring series at instanceTime. The past stays
// intact. The user's edits apply to a new series that starts at instanceTime.
// Internally it:
//
//  1. Truncates the master's RRULE with UNTIL=instanceTime-1s.
//  2. Soft-deletes any overrides at or after the cutoff. Those instances will
//     never expand again, so an override there would be unreachable.
//  3. Creates a brand-new event (fresh UID) with p's field values plus the
//     RecurrenceRule the caller passes in. That is typically the same rule
//     the user had, possibly edited.
//
// Both rows are marked dirty so CalDAV sync ships the truncation and the new
// series together. Pre-truncation state is recorded in event_truncate_deletes
// so the trash view can offer an atomic restore later.
func (s *Service) UpdateFromInstance(ctx context.Context, uid string, instanceTime time.Time, p UpdateParams) (Event, error) {
	p.applyDefaults()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, masterCalendarID, err := updateFromInstanceTx(ctx, qtx, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit split: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, masterCalendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark master resource dirty: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, e.CalendarID, e.UID, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return e, nil
}

// UpdateFromInstanceWithRelations is UpdateFromInstance plus an attendee/alarm
// write on the new split series in the same transaction. The truncation, the
// new master, and its children then commit atomically (issue #87).
func (s *Service) UpdateFromInstanceWithRelations(ctx context.Context, uid string, instanceTime time.Time, p UpdateParams, attendees []model.Attendee, alarms []model.Alarm) (Event, error) {
	p.applyDefaults()

	// Reject a bad attendee or alarm before the transaction opens. See
	// model.PrepareAttendeesForWrite and model.PrepareAlarmsForWrite.
	attendees, err := model.PrepareAttendeesForWrite(model.EventAttendee, attendees)
	if err != nil {
		return Event{}, err
	}
	alarms, err = model.PrepareAlarmsForWrite(alarms)
	if err != nil {
		return Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, masterCalendarID, err := updateFromInstanceTx(ctx, qtx, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	if err := replaceRelationsTx(ctx, qtx, e.ID, attendees, alarms); err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit split: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, masterCalendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark master resource dirty: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.db, e.CalendarID, e.UID, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return e, nil
}

// updateFromInstanceTx truncates the master series. It soft-deletes future
// overrides. It records the pre-truncation state. It creates the new
// split-series master with its categories. All of that uses a tx-bound
// Queries. It returns the new event and the master's calendar ID. It opens
// no transaction. Callers can then compose it with attendee/alarm writes.
func updateFromInstanceTx(ctx context.Context, qtx *storage.Queries, uid string, instanceTime time.Time, p UpdateParams) (Event, int64, error) {
	// Every update entry point reaches this function, so the span rule
	// lives here rather than at each caller.
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, 0, err
	}
	master, err := qtx.GetEventByUID(ctx, uid)
	if err != nil {
		return Event{}, 0, fmt.Errorf("get master: %w", err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return Event{}, 0, err
	}
	if p.CalendarID != 0 && p.CalendarID != master.CalendarID {
		if err := calendaraccess.EnsureWritable(ctx, qtx, p.CalendarID, "VEVENT"); err != nil {
			return Event{}, 0, err
		}
	}

	// An RDATE-only master (no RRULE) has no recurrence rule to truncate;
	// synthesizing an "UNTIL=..." would corrupt it into an unparseable RRULE
	// (issue #414). Leave the rule NULL when there is nothing to truncate.
	prevRRule := storage.NullableToString(master.RecurrenceRule)
	if prevRRule != "" {
		until := instanceTime.UTC().Add(-time.Second)
		truncatedRule := setRRuleUntil(prevRRule, until, master.AllDay == 1)
		if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
			RecurrenceRule: storage.StringToNullable(truncatedRule),
			ID:             master.ID,
		}); err != nil {
			return Event{}, 0, fmt.Errorf("truncate master rrule: %w", err)
		}
	}

	if err := softDeleteOverridesAndRecordTruncation(ctx, qtx, master, instanceTime, prevRRule); err != nil {
		return Event{}, 0, err
	}

	// Caller is the source of truth for categories. An empty p.Categories
	// means the new split series starts with no tags.
	carriedCats := ParseCategoryList(p.Categories)

	newUID := uuid.New().String()
	r, err := qtx.CreateEvent(ctx, storage.CreateEventParams{
		Uid:            newUID,
		CalendarID:     p.CalendarID,
		Title:          p.Title,
		Description:    storage.StringToNullable(p.Description),
		Location:       storage.StringToNullable(p.Location),
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         storage.BoolToInt(p.AllDay),
		RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
		Timezone:       storage.StringToNullable(p.Timezone),
		Status:         p.Status,
		Transp:         p.Transp,
		Sequence:       0,
		Priority:       p.Priority,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		Exdates:        storage.StringToNullable(""),
		Rdates:         storage.StringToNullable(""),
		RecurrenceID:   "",
		Geo:            storage.StringToNullable(p.Geo),
		Duration:       storage.StringToNullable(p.DurationValue),
		Dtstamp:        storage.StringToNullable(p.DtStamp),
		ConferenceUri:  p.ConferenceURI,
	})
	if err != nil {
		return Event{}, 0, fmt.Errorf("create split series: %w", err)
	}

	if err := replaceCategoriesTx(ctx, qtx, r.ID, carriedCats); err != nil {
		return Event{}, 0, err
	}

	e := FromStorage(r)
	e.Categories = timeutil.JoinCategoryList(carriedCats)
	return e, master.CalendarID, nil
}

// setRRuleUntil adds or replaces the UNTIL parameter in an RRULE string.
//
// RFC 5545 requires UNTIL's value type to match DTSTART. A DATE-valued
// (all-day) series must use a DATE UNTIL (YYYYMMDD). A DATE-TIME series
// uses a UTC DATE-TIME (YYYYMMDDTHHMMSSZ). A DATE-TIME UNTIL on an
// all-day series produces a type-mismatched RRULE that strict CalDAV servers
// reject.
func setRRuleUntil(rule string, until time.Time, allDay bool) string {
	layout := "20060102T150405Z"
	if allDay {
		layout = "20060102"
	}
	untilStr := "UNTIL=" + until.UTC().Format(layout)
	parts := strings.Split(rule, ";")
	out := parts[:0]
	for _, p := range parts {
		// strings.Split("", ";") yields [""]; that empty element must not
		// survive, or it would join into a leading ";" separator and a bogus
		// ";UNTIL=..." RRULE (issue #414).
		if p == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(p), "UNTIL=") && !strings.HasPrefix(strings.ToUpper(p), "COUNT=") {
			out = append(out, p)
		}
	}
	out = append(out, untilStr)
	return strings.Join(out, ";")
}
