package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/recurset"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/uidmove"
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

// ErrInstanceCalendarMove reports an edit of one occurrence that asks for
// another calendar. Move the whole series instead.
var ErrInstanceCalendarMove = errors.New("event: one occurrence cannot move to another calendar; move the whole series")

// ErrMoveTargetHasUID reports a calendar move to a calendar that already
// holds a row of the UID with the same recurrence ID. The row can be in
// the trash. Purge or restore that row first.
var ErrMoveTargetHasUID = errors.New("event: the target calendar already holds this UID")

// ErrAmbiguousUID reports an edit of one occurrence whose UID has a master
// in more than one calendar, and no rule can choose the series. The edit
// names no calendar, or it names a calendar with a legacy override of the
// occurrence and no master. The service refuses the edit.
var ErrAmbiguousUID = errors.New("event: the UID has a series in more than one calendar; name the calendar of the series")

// IsMoveRefusal reports whether err is a refusal of an edit that the user
// can fix in the edit form: a refused calendar move or a refused
// occurrence edit. A caller that shows a form keeps it open with the error,
// so the edits are not lost.
func IsMoveRefusal(err error) bool {
	for _, refusal := range []error{
		ErrInstanceCalendarMove,
		ErrMoveTargetHasUID,
		ErrAmbiguousUID,
		ErrOccurrenceOutsideSeries,
		storage.ErrMoveOpenConflict,
	} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// ErrOccurrenceOutsideSeries reports an edit of an occurrence whose stored
// override is in the trash, and the occurrence is not part of the series
// any more, for example after a "this and following" split that ended the
// series before it. A revive of that row would leave a live override
// outside the series. Restore the series tail from the trash first.
var ErrOccurrenceOutsideSeries = errors.New("event: the occurrence is outside the series; restore the series tail from the trash first")

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

// markDirtyByID marks the sync resource of the event with the given ID dirty
// through the service's current dirty executor. A failed mark returns an
// error so every caller can propagate it per the issue #107 contract: a
// change that is never pushed must not stay silent.
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
// access/component policy on its calendar for a user-originated edit of
// its child rows. An update of the row runs the same check in
// uidmove.Rows.Update, for the target calendar of a move too.
func (s *Service) ensureEventWritable(ctx context.Context, eventID int64) error {
	r, err := s.q.GetEvent(ctx, eventID)
	if err != nil {
		return err
	}
	return calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, "VEVENT")
}

// distinctCalendarIDsByUID returns every calendar that any event row with the
// given UID lives on: master, overrides, or series-tail leftovers. A recurring
// series can leave orphaned rows behind after the master is purged on its own
// (the trash view supports series tails). A UID-keyed mutation must then
// enforce policy on each of these calendars, not only the master's.
func (s *Service) distinctCalendarIDsByUID(ctx context.Context, uid string) ([]int64, error) {
	ids, err := s.q.ListEventCalendarIDsByUID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("distinct calendar ids: %w", err)
	}
	return ids, nil
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, err := updateEventTx(ctx, qtx, id, p, nil)
	if err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit update event: %w", err)
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

	e, err := updateEventTx(ctx, qtx, id, p, func(ctx context.Context, eventID int64) error {
		return replaceRelationsTx(ctx, qtx, eventID, attendees, alarms)
	})
	if err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit update event: %w", err)
	}
	return e, nil
}

// updateEventTx writes the event row, its categories, and the child rows
// of children (nil for none) with a tx-bound Queries. It runs the steps of
// uidmove.Rows.Update, so it also records a calendar move and marks the
// resource dirty inside the transaction (issue #107). It opens no
// transaction and does not commit.
func updateEventTx(ctx context.Context, qtx *storage.Queries, id int64, p UpdateParams, children func(ctx context.Context, eventID int64) error) (Event, error) {
	// Every update entry point reaches this function, so the span rule
	// lives here rather than at each caller.
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, err
	}
	old, err := qtx.GetEvent(ctx, id)
	if err != nil {
		return Event{}, err
	}
	// The access checks, the move rules, the move record, and the dirty
	// mark run in the transaction, in the order of uidmove.Rows.Update.
	var e Event
	err = uidMoveRows(qtx, old.Uid).Update(ctx, "VEVENT", "event", old.RecurrenceID, old.CalendarID, p.CalendarID, func(ctx context.Context, calendarID int64) error {
		r, err := qtx.UpdateEvent(ctx, storage.UpdateEventParams{
			ID:             id,
			Title:          p.Title,
			Description:    storage.StringToNullable(p.Description),
			Location:       storage.StringToNullable(p.Location),
			StartTime:      p.StartTime.Format(time.RFC3339),
			EndTime:        p.EndTime.Format(time.RFC3339),
			AllDay:         storage.BoolToInt(p.AllDay),
			RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
			CalendarID:     calendarID,
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
			return err
		}
		e = FromStorage(r)
		if err := replaceCategoriesTx(ctx, qtx, e.ID, ParseCategoryList(p.Categories)); err != nil {
			return fmt.Errorf("replace categories: %w", err)
		}
		if localSpanEdit(old, p) {
			if err := clearPreservedDTENDTx(ctx, qtx, e.ID); err != nil {
				return err
			}
		}
		if children != nil {
			return children(ctx, e.ID)
		}
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	e.Categories = p.Categories
	return e, nil
}

// uidMasters converts master rows to the form of the uidmove package.
func uidMasters(rows []storage.Event) []uidmove.Master {
	out := make([]uidmove.Master, len(rows))
	for i, m := range rows {
		out[i] = uidmove.Master{ID: m.ID, CalendarID: m.CalendarID, Trashed: m.DeletedAt != nil}
	}
	return out
}

// uidMoveRows binds the event queries of the transaction to the shared
// calendar-move rules for uid. Two calendars can hold the same UID, so each
// lookup stays inside one calendar.
func uidMoveRows(qtx *storage.Queries, uid string) uidmove.Rows {
	return uidmove.Rows{
		Q:               qtx,
		UID:             uid,
		ErrInstanceMove: ErrInstanceCalendarMove,
		ErrTargetHasUID: ErrMoveTargetHasUID,
		Masters: func(ctx context.Context) ([]uidmove.Master, error) {
			masters, err := qtx.ListEventMastersByUIDIncludingDeleted(ctx, uid)
			if err != nil {
				return nil, err
			}
			return uidMasters(masters), nil
		},
		CountCollisions: func(ctx context.Context, fromCalendarID, toCalendarID int64) (int64, int64, error) {
			n, err := qtx.CountEventUIDMoveCollisions(ctx, storage.CountEventUIDMoveCollisionsParams{
				ToCalendarID: toCalendarID, Uid: uid, FromCalendarID: fromCalendarID,
			})
			return n.Live, n.Trashed, err
		},
		MoveRows: func(ctx context.Context, fromCalendarID, toCalendarID int64) error {
			return moveUIDRowsTx(ctx, qtx, storage.MoveEventUIDToCalendarParams{
				FromCalendarID: fromCalendarID, ToCalendarID: toCalendarID, Uid: uid,
			})
		},
	}
}

// findInstanceOverrideTx finds the stored override of recID for master. A
// row in the calendar of the master wins. Else it takes a legacy override
// (see storage.LegacyOverride) that the series of master owns. The view
// event_series_rows holds the rule. More than one legacy row gives
// ErrAmbiguousUID. No row gives sql.ErrNoRows.
func findInstanceOverrideTx(ctx context.Context, qtx *storage.Queries, master storage.Event, recID string) (storage.Event, error) {
	rows, err := qtx.ListSeriesOverridesAtRecurrenceID(ctx, storage.ListSeriesOverridesAtRecurrenceIDParams{
		CalendarID:   master.CalendarID,
		Uid:          master.Uid,
		RecurrenceID: recID,
	})
	if err != nil {
		return storage.Event{}, err
	}
	for _, r := range rows {
		if r.CalendarID == master.CalendarID {
			return r, nil
		}
	}
	switch len(rows) {
	case 0:
		return storage.Event{}, sql.ErrNoRows
	case 1:
		return rows[0], nil
	default:
		return storage.Event{}, ErrAmbiguousUID
	}
}

// moveUIDRowsTx moves the event rows and the delete-log rows of one UID to
// another calendar. uidmove.Rows.Record calls it. The log keys are per
// calendar (migration 057). The target calendar holds no event row of the
// UID (uidmove.Rows.CheckTarget), so a log row of the UID in it is a
// stale row, for example one that a purge of the master left. The move
// drops those rows first, so the moved rows take their keys.
func moveUIDRowsTx(ctx context.Context, qtx *storage.Queries, arg storage.MoveEventUIDToCalendarParams) error {
	if err := qtx.MoveEventUIDToCalendar(ctx, arg); err != nil {
		return fmt.Errorf("move events: %w", err)
	}
	if err := deleteLogRowsTx(ctx, qtx, arg.ToCalendarID, arg.Uid); err != nil {
		return err
	}
	if err := qtx.MoveEventExdateDeletesUIDToCalendar(ctx, storage.MoveEventExdateDeletesUIDToCalendarParams(arg)); err != nil {
		return fmt.Errorf("move event_exdate_deletes: %w", err)
	}
	if err := qtx.MoveEventTruncateDeletesUIDToCalendar(ctx, storage.MoveEventTruncateDeletesUIDToCalendarParams(arg)); err != nil {
		return fmt.Errorf("move event_truncate_deletes: %w", err)
	}
	if err := qtx.MoveEventSeriesDeletesUIDToCalendar(ctx, storage.MoveEventSeriesDeletesUIDToCalendarParams(arg)); err != nil {
		return fmt.Errorf("move event_series_deletes: %w", err)
	}
	return nil
}

// deleteLogRowsTx drops the delete-log rows of one UID in one calendar:
// the occurrence log, the truncation log, and the series log.
func deleteLogRowsTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) error {
	if err := qtx.DeleteEventExdateDeletesByCalendarUID(ctx, storage.DeleteEventExdateDeletesByCalendarUIDParams{CalendarID: calendarID, Uid: uid}); err != nil {
		return fmt.Errorf("delete event_exdate_deletes: %w", err)
	}
	if err := qtx.DeleteEventTruncateDeletesByCalendarUID(ctx, storage.DeleteEventTruncateDeletesByCalendarUIDParams{CalendarID: calendarID, Uid: uid}); err != nil {
		return fmt.Errorf("delete event_truncate_deletes: %w", err)
	}
	if err := qtx.DeleteEventSeriesDeletesByCalendarUID(ctx, storage.DeleteEventSeriesDeletesByCalendarUIDParams{CalendarID: calendarID, Uid: uid}); err != nil {
		return fmt.Errorf("delete event_series_deletes: %w", err)
	}
	return nil
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
func (s *Service) UpdateInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time, p UpdateParams) (Event, error) {
	p.applyDefaults()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, err := updateInstanceTx(ctx, qtx, calendarID, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	// Mark the override dirty inside the transaction so a failed sync-tracking
	// write rolls the override back rather than committing a change that is
	// never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, e.CalendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit override: %w", err)
	}
	return e, nil
}

// UpdateInstanceWithRelations is UpdateInstance plus an attendee/alarm write in
// the same transaction, so the override row and its children commit atomically
// (issue #87).
func (s *Service) UpdateInstanceWithRelations(ctx context.Context, calendarID int64, uid string, instanceTime time.Time, p UpdateParams, attendees []model.Attendee, alarms []model.Alarm) (Event, error) {
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

	e, err := updateInstanceTx(ctx, qtx, calendarID, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	if err := replaceRelationsTx(ctx, qtx, e.ID, attendees, alarms); err != nil {
		return Event{}, err
	}
	// Mark the override dirty inside the transaction so a failed sync-tracking
	// write rolls the override and its children back rather than committing a
	// change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, e.CalendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit override: %w", err)
	}
	return e, nil
}

// updateInstanceTx creates or updates a per-occurrence override row and its
// categories with a tx-bound Queries. The caller marks the calendar of the
// returned event dirty inside its transaction. storage.MarkResourceDirty
// sends the mark of a legacy override to the calendar of its master. It
// opens no transaction, so callers can compose it with attendee/alarm
// writes.
func updateInstanceTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, instanceTime time.Time, p UpdateParams) (Event, error) {
	// Every update entry point reaches this function, so the span rule
	// lives here rather than at each caller.
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, err
	}
	recID := instanceTime.UTC().Format(time.RFC3339)
	move := uidMoveRows(qtx, uid)
	// The series is the one of the calendar of the occurrence: the
	// calendar of its row, or of its master (findSeriesMasterTx). The
	// three edit scopes share that rule. p.CalendarID is the target of
	// the edit only, and the move rule below refuses a move of one
	// occurrence.
	master, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return Event{}, wrapMasterLookup(err)
	}
	masters, err := move.Masters(ctx)
	if err != nil {
		return Event{}, fmt.Errorf("list masters: %w", err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return Event{}, err
	}
	existing, existErr := findInstanceOverrideTx(ctx, qtx, master, recID)
	if existErr != nil && !errors.Is(existErr, sql.ErrNoRows) {
		if errors.Is(existErr, ErrAmbiguousUID) {
			return Event{}, existErr
		}
		return Event{}, fmt.Errorf("get override: %w", existErr)
	}
	// The occurrence lives in the calendar of its stored override, or in
	// the calendar of the master when no override exists. The shared rule
	// refuses a move of one occurrence (issue #828). It permits a legacy
	// override to stay in its calendar or to go back to a master. A move
	// back takes the rows of the UID along, so the rule also refuses a
	// target that already holds one of those rows.
	fromCalendarID := master.CalendarID
	if existErr == nil {
		fromCalendarID = existing.CalendarID
	}
	// A legacy override lives in another calendar than its master. The
	// edit writes that calendar (an update in place, or a move out of it),
	// so that calendar must take the write too, as in an update by row ID.
	if fromCalendarID != master.CalendarID {
		if err := calendaraccess.EnsureWritable(ctx, qtx, fromCalendarID, "VEVENT"); err != nil {
			return Event{}, err
		}
	}
	// CalendarID 0 keeps the calendar of the occurrence, as in an update by
	// row ID (uidmove.Rows.Update).
	if p.CalendarID == 0 {
		p.CalendarID = fromCalendarID
	}
	if err := move.CheckOf(ctx, recID, fromCalendarID, p.CalendarID, masters); err != nil {
		return Event{}, err
	}
	if p.CalendarID != master.CalendarID && p.CalendarID != fromCalendarID {
		if err := calendaraccess.EnsureWritable(ctx, qtx, p.CalendarID, "VEVENT"); err != nil {
			return Event{}, err
		}
	}

	// Caller is the source of truth for categories. An empty p.Categories
	// means the user explicitly cleared the tags on this override.
	carriedCats := ParseCategoryList(p.Categories)

	var r storage.Event
	// storedCalendarID is the calendar of a stored override before the
	// edit, or 0 for a new override.
	var storedCalendarID int64
	if existErr == nil {
		storedCalendarID = existing.CalendarID
		r, err = updateOverrideTx(ctx, qtx, existing, p)
		if err != nil {
			return Event{}, fmt.Errorf("update override: %w", err)
		}
	} else {
		r, err = qtx.CreateEvent(ctx, overrideCreateParams(uid, recID, master.Sequence+1, p))
		if err != nil {
			// The UNIQUE(calendar_id, uid, recurrence_id) key holds a row:
			// a concurrent create, or an override that a delete of this
			// occurrence soft-deleted. Retry as an update of that row.
			if isUniqueViolationOnRecurrenceID(err) {
				existing, eErr := qtx.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
					CalendarID:   p.CalendarID,
					Uid:          uid,
					RecurrenceID: recID,
				})
				if eErr != nil {
					return Event{}, fmt.Errorf("retry get override: %w", eErr)
				}
				if existing.DeletedAt != nil && *existing.DeletedAt != "" {
					if err := reviveOverrideTx(ctx, qtx, master, existing); err != nil {
						return Event{}, err
					}
				}
				storedCalendarID = existing.CalendarID
				r, err = updateOverrideTx(ctx, qtx, existing, p)
				if err != nil {
					return Event{}, fmt.Errorf("retry update override: %w", err)
				}
			} else {
				return Event{}, fmt.Errorf("create override: %w", err)
			}
		}
	}

	// An override that an older version saved in another calendar can move
	// back to the calendar of the master. Record that move. The other rows
	// of the UID then move too, and the server object in the old calendar
	// gets a tombstone. Without it that stale object keeps the UID, and
	// iCloud refuses the later PUT (issue #828).
	if storedCalendarID != 0 {
		if err := move.Record(ctx, storedCalendarID, r.CalendarID); err != nil {
			return Event{}, fmt.Errorf("record override calendar move: %w", err)
		}
	}

	if err := replaceCategoriesTx(ctx, qtx, r.ID, carriedCats); err != nil {
		return Event{}, err
	}

	e := FromStorage(r)
	e.Categories = timeutil.JoinCategoryList(carriedCats)
	return e, nil
}

// reviveOverrideTx un-hides a soft-deleted override row that an edit of the
// same occurrence takes again. It revives the row only when its recurrence
// ID is an occurrence of the current recurrence set of master (RRULE and
// RDATE, with the EXDATEs ignored, see recurset.Checker). A split or a
// truncation that ended the series before the occurrence gives
// ErrOccurrenceOutsideSeries. A row that a series delete hid, or that an
// UNTIL hid until the user removed it, is an occurrence again, so it
// revives. masterCalendarID is the calendar of the
// master of the edit. Two calendars can hold the same UID, so the EXDATE
// rule reads that calendar only. It uses the restore rule of the trash:
// the master loses the EXDATE that the delete recorded, and an imported
// EXDATE stays (issue #86, see softdelete.ClearMasterEXDATE).
//
// The edit makes a fresh override, so the old child rows of the row go:
// the attendees, the alarms, the attachments, the comments, the contacts,
// the resources, the relations, and the x-properties. A *WithRelations
// caller then writes its own attendees and alarms.
func reviveOverrideTx(ctx context.Context, qtx *storage.Queries, master storage.Event, row storage.Event) error {
	m := FromStorage(master)
	if !recurset.NewChecker(m.RecurrenceRule, m.Timezone, m.StartTime, m.ParseRDates()).OccursAt(row.RecurrenceID) {
		return ErrOccurrenceOutsideSeries
	}
	masterCalendarID := master.CalendarID
	if err := qtx.RestoreEvent(ctx, row.ID); err != nil {
		return fmt.Errorf("revive override: %w", err)
	}
	if err := clearMasterEXDATE(ctx, qtx, masterCalendarID, row.Uid, row.RecurrenceID); err != nil {
		return err
	}
	if err := replaceRelationsTx(ctx, qtx, row.ID, nil, nil); err != nil {
		return err
	}
	for _, del := range []func(context.Context, int64) error{
		qtx.DeleteEventAttachmentsByEventID,
		qtx.DeleteEventCommentsByEventID,
		qtx.DeleteEventContactsByEventID,
		qtx.DeleteEventResourcesByEventID,
		qtx.DeleteEventRelationsByEventID,
	} {
		if err := del(ctx, row.ID); err != nil {
			return fmt.Errorf("clear revived override: %w", err)
		}
	}
	if err := qtx.DeleteXPropertiesByOwner(ctx, storage.DeleteXPropertiesByOwnerParams{OwnerType: "event", OwnerID: row.ID}); err != nil {
		return fmt.Errorf("clear revived override: %w", err)
	}
	return nil
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
//
// calendarID names the calendar of the series. Two calendars can hold the
// same UID (issue #842). With 0, one live master is the only choice, and
// more give ErrAmbiguousUID. See findSeriesMasterTx. p.CalendarID names the
// calendar of the new series, and 0 keeps the calendar of the master.
func (s *Service) UpdateFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time, p UpdateParams) (Event, error) {
	p.applyDefaults()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	e, masterCalendarID, err := updateFromInstanceTx(ctx, qtx, calendarID, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	// Mark both rows dirty inside the transaction so a failed sync-tracking
	// write rolls the split back rather than committing a change that is
	// never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, masterCalendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, tx, e.CalendarID, e.UID, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit split: %w", err)
	}
	return e, nil
}

// UpdateFromInstanceWithRelations is UpdateFromInstance plus an attendee/alarm
// write on the new split series in the same transaction. The truncation, the
// new master, and its children then commit atomically (issue #87).
func (s *Service) UpdateFromInstanceWithRelations(ctx context.Context, calendarID int64, uid string, instanceTime time.Time, p UpdateParams, attendees []model.Attendee, alarms []model.Alarm) (Event, error) {
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

	e, masterCalendarID, err := updateFromInstanceTx(ctx, qtx, calendarID, uid, instanceTime, p)
	if err != nil {
		return Event{}, err
	}
	if err := replaceRelationsTx(ctx, qtx, e.ID, attendees, alarms); err != nil {
		return Event{}, err
	}
	// Mark both rows dirty inside the transaction so a failed sync-tracking
	// write rolls the split and its children back rather than committing a
	// change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, masterCalendarID, uid, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, tx, e.CalendarID, e.UID, "event"); err != nil {
		return Event{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit split: %w", err)
	}
	return e, nil
}

// updateFromInstanceTx truncates the master series. It soft-deletes future
// overrides. It records the pre-truncation state. It creates the new
// split-series master with its categories. All of that uses a tx-bound
// Queries. It returns the new event and the master's calendar ID. It opens
// no transaction. Callers can then compose it with attendee/alarm writes.
// calendarID names the calendar of the series (see findSeriesMasterTx).
func updateFromInstanceTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, instanceTime time.Time, p UpdateParams) (Event, int64, error) {
	// Every update entry point reaches this function, so the span rule
	// lives here rather than at each caller.
	if err := validateDurationValue(p.StartTime, p.DurationValue); err != nil {
		return Event{}, 0, err
	}
	master, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return Event{}, 0, wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return Event{}, 0, err
	}
	// CalendarID 0 keeps the calendar of the series, as in an update by row
	// ID (uidmove.Rows.Update).
	if p.CalendarID == 0 {
		p.CalendarID = master.CalendarID
	}
	if p.CalendarID != master.CalendarID {
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
	// Return the calendar of the master. The truncation lives there. The
	// new series can live in another calendar, and the caller marks it
	// dirty by its own calendar.
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
