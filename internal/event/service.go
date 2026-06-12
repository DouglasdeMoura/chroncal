package event

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

type SearchParams struct {
	Query      string
	CalendarID int64  // 0 = all calendars
	From       string // RFC3339 or empty
	To         string // RFC3339 or empty
	Status     string // empty = all
}

type ExportParams struct {
	CalendarID int64  // 0 = all
	From       string // RFC3339 or empty
	To         string // RFC3339 or empty
	Category   string // empty = all
	Status     string // empty = all
}

type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
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

func (p *CreateParams) applyDefaults() {
	applyEventDefaults(&p.Status, &p.Transp, &p.Class)
}

func (p *UpsertParams) applyDefaults() {
	applyEventDefaults(&p.Status, &p.Transp, &p.Class)
}

func (p *UpdateParams) applyDefaults() {
	applyEventDefaults(&p.Status, &p.Transp, &p.Class)
}

func (s *Service) CountByCalendar(ctx context.Context, calendarID int64) (int64, error) {
	return s.q.CountEventsByCalendar(ctx, calendarID)
}

func (s *Service) ListByDateRange(ctx context.Context, from, to time.Time) ([]Event, error) {
	rows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime: to.Format(time.RFC3339),   // start_time < to
		EndTime:   from.Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ListByCalendarAndDateRange(ctx context.Context, calID int64, from, to time.Time) ([]Event, error) {
	rows, err := s.q.ListEventsByCalendarAndDateRange(ctx, storage.ListEventsByCalendarAndDateRangeParams{
		CalendarID: calID,
		StartTime:  to.Format(time.RFC3339),   // start_time < to
		EndTime:    from.Format(time.RFC3339), // end_time > from
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) Search(ctx context.Context, p SearchParams) ([]Event, error) {
	ftsQuery := storage.FTSQuery(p.Query)
	if ftsQuery == "" {
		return []Event{}, nil
	}
	rows, err := s.q.SearchEventsFTS(ctx, ftsQuery, p.CalendarID, p.From, p.To, p.Status)
	if err != nil {
		return nil, fmt.Errorf("search events: %w", err)
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Event, error) {
	rows, err := s.q.ListEventsForExport(ctx, storage.EventFilterParams{
		CalendarID:   p.CalendarID,
		FromTime:     p.From,
		ToTime:       p.To,
		Category:     p.Category,
		FilterStatus: p.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("export events: %w", err)
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ListOverridesByUID(ctx context.Context, uid string) ([]Event, error) {
	rows, err := s.q.ListOverridesByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Event, error) {
	r, err := s.q.GetEvent(ctx, id)
	if err != nil {
		return Event{}, err
	}
	e := fromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

func (s *Service) GetByUID(ctx context.Context, uid string) (Event, error) {
	r, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return Event{}, err
	}
	e := fromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

func (s *Service) GetByUIDAndRecurrenceID(ctx context.Context, uid, recurrenceID string) (Event, error) {
	r, err := s.q.GetEventByUIDAndRecurrenceID(ctx, storage.GetEventByUIDAndRecurrenceIDParams{
		Uid:          uid,
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		return Event{}, err
	}
	e := fromStorage(r)
	s.populateSingleCategories(ctx, &e)
	return e, nil
}

// markDirtyByID looks up an event by ID and marks its sync resource as dirty.
func (s *Service) markDirtyByID(ctx context.Context, eventID int64) {
	r, err := s.q.GetEvent(ctx, eventID)
	if err != nil {
		return
	}
	_ = storage.MarkResourceDirty(ctx, s.db, r.CalendarID, r.Uid, "event")
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Event, error) {
	p.applyDefaults()
	r, err := s.q.CreateEvent(ctx, storage.CreateEventParams{
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
	e := fromStorage(r)
	if cats := ParseCategoryList(p.Categories); len(cats) > 0 {
		if err := s.ReplaceCategories(ctx, e.ID, cats); err != nil {
			return Event{}, fmt.Errorf("replace categories: %w", err)
		}
	}
	e.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.db, e.CalendarID, e.UID, "event")
	return e, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Event, error) {
	p.applyDefaults()
	r, err := s.q.UpdateEvent(ctx, storage.UpdateEventParams{
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
	e := fromStorage(r)
	if err := s.ReplaceCategories(ctx, e.ID, ParseCategoryList(p.Categories)); err != nil {
		return Event{}, fmt.Errorf("replace categories: %w", err)
	}
	e.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.db, e.CalendarID, e.UID, "event")
	return e, nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Event, error) {
	p.applyDefaults()
	r, err := s.q.UpsertEventByUID(ctx, storage.UpsertEventByUIDParams{
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
	e := fromStorage(r)
	if err := s.ReplaceCategories(ctx, e.ID, ParseCategoryList(p.Categories)); err != nil {
		return Event{}, fmt.Errorf("replace categories: %w", err)
	}
	e.Categories = p.Categories
	return e, nil
}

// ErrHasOverrides is returned when attempting to delete a recurring master
// event that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("event has overrides: use DeleteSeries to delete the entire series")

func (s *Service) Delete(ctx context.Context, id int64) error {
	r, err := s.q.GetEvent(ctx, id)
	if err != nil {
		return err
	}
	evt := fromStorage(r)

	// If this is a recurring master, check for overrides.
	if evt.RecurrenceRule != "" && evt.RecurrenceID == "" {
		overrides, err := s.q.ListOverridesByUID(ctx, evt.UID)
		if err != nil {
			return fmt.Errorf("check overrides: %w", err)
		}
		if len(overrides) > 0 {
			return ErrHasOverrides
		}
	}

	// If this is a standalone event (no recurrence or a solo master), create
	// a tombstone so the sync engine can send a DELETE to the server.
	if evt.RecurrenceID == "" {
		_, _ = storage.CreateTombstoneIfSynced(ctx, s.db, evt.CalendarID, evt.UID)
	}

	// If this is an override, add EXDATE to the master so the instance
	// doesn't reappear on next expansion. The master's sync resource
	// becomes dirty (modified EXDATE), not the override.
	if evt.RecurrenceID != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)

		master, err := qtx.GetEventByUID(ctx, evt.UID)
		if err == nil {
			existing := ParseTimeList(storage.NullableToString(master.Exdates))
			recIDTime, parseErr := timeutil.ParseRecurrenceID(evt.RecurrenceID)
			if parseErr == nil {
				existing = append(existing, recIDTime)
				if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
					Exdates: storage.StringToNullable(SerializeTimeList(existing)),
					ID:      master.ID,
				}); err != nil {
					return fmt.Errorf("update exdates: %w", err)
				}
			}
		}

		if err := qtx.SoftDeleteEvent(ctx, id); err != nil {
			return fmt.Errorf("soft-delete event: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		// Mark the master dirty — its EXDATE was modified.
		_ = storage.MarkResourceDirty(ctx, s.db, evt.CalendarID, evt.UID, "event")
		return nil
	}

	return s.q.SoftDeleteEvent(ctx, id)
}

// DeleteInstance excludes a single occurrence of a recurring event by adding
// an EXDATE to the master. instanceTime is the StartTime of the occurrence.
func (s *Service) DeleteInstance(ctx context.Context, uid string, instanceTime time.Time) error {
	master, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	existing := ParseTimeList(storage.NullableToString(master.Exdates))
	existing = append(existing, instanceTime.UTC())
	if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
		Exdates: storage.StringToNullable(SerializeTimeList(existing)),
		ID:      master.ID,
	}); err != nil {
		return fmt.Errorf("update exdates: %w", err)
	}

	recID := instanceTime.UTC().Format(time.RFC3339)
	override, oErr := qtx.GetEventByUIDAndRecurrenceID(ctx, storage.GetEventByUIDAndRecurrenceIDParams{
		Uid:          uid,
		RecurrenceID: recID,
	})
	if oErr == nil {
		if err := qtx.SoftDeleteEvent(ctx, override.ID); err != nil {
			return fmt.Errorf("soft-delete override: %w", err)
		}
	}

	// Log the EXDATE-based delete so the trash view can surface it.
	// ON CONFLICT upserts deleted_at, so deleting the same instance twice
	// keeps exactly one log row with the latest timestamp.
	if err := qtx.RecordEventExdateDelete(ctx, storage.RecordEventExdateDeleteParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: recID,
	}); err != nil {
		return fmt.Errorf("record exdate delete: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, master.CalendarID, uid, "event")
	return nil
}

// DeleteFromInstance truncates a recurring series so that instances at or
// after instanceTime are removed. It sets UNTIL on the RRULE, soft-deletes
// any overrides at or after the cutoff, and records the pre-truncation
// RRULE in event_truncate_deletes so the trash view can restore it atomically.
func (s *Service) DeleteFromInstance(ctx context.Context, uid string, instanceTime time.Time) error {
	master, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}

	prevRRule := storage.NullableToString(master.RecurrenceRule)
	until := instanceTime.UTC().Add(-time.Second)
	rule := setRRuleUntil(prevRRule, until)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
		RecurrenceRule: storage.StringToNullable(rule),
		ID:             master.ID,
	}); err != nil {
		return fmt.Errorf("update rrule: %w", err)
	}

	cutoff := instanceTime.UTC().Format(time.RFC3339)
	if err := qtx.SoftDeleteOverridesAtOrAfter(ctx, storage.SoftDeleteOverridesAtOrAfterParams{
		Uid:          uid,
		RecurrenceID: cutoff,
	}); err != nil {
		return fmt.Errorf("soft-delete future overrides: %w", err)
	}

	if err := qtx.RecordEventTruncateDelete(ctx, storage.RecordEventTruncateDeleteParams{
		CalendarID:    master.CalendarID,
		Uid:           uid,
		CutoffTime:    cutoff,
		PreviousRrule: prevRRule,
	}); err != nil {
		return fmt.Errorf("record truncate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, master.CalendarID, uid, "event")
	return nil
}

// UpdateInstance creates or updates a per-occurrence override of a recurring
// event. The override is stored as a separate row with the same UID as the
// master and a RecurrenceID matching the original (un-edited) instance start
// in UTC. The master row is not modified, so the recurrence rule and every
// other instance keep working unchanged.
//
// instanceTime is the original occurrence time used as the override key (its
// RECURRENCE-ID). The new StartTime/EndTime in p reflect the user's edits and
// may differ — e.g. moving Wednesday's standup from 9:00 to 9:30 sets
// RecurrenceID=2026-05-20T09:00:00Z but StartTime=2026-05-20T09:30:00Z.
func (s *Service) UpdateInstance(ctx context.Context, uid string, instanceTime time.Time, p UpdateParams) (Event, error) {
	p.applyDefaults()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	master, err := qtx.GetEventByUID(ctx, uid)
	if err != nil {
		return Event{}, fmt.Errorf("get master: %w", err)
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
		r, err = qtx.UpdateEvent(ctx, overrideUpdateParams(existing.ID, p))
		if err != nil {
			return Event{}, fmt.Errorf("update override: %w", err)
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
					return Event{}, fmt.Errorf("retry get override: %w", eErr)
				}
				r, err = qtx.UpdateEvent(ctx, overrideUpdateParams(existing.ID, p))
				if err != nil {
					return Event{}, fmt.Errorf("retry update override: %w", err)
				}
			} else {
				return Event{}, fmt.Errorf("create override: %w", err)
			}
		}
	}

	if err := qtx.DeleteCategoriesByEventID(ctx, r.ID); err != nil {
		return Event{}, fmt.Errorf("clear override categories: %w", err)
	}
	for _, c := range carriedCats {
		if _, err := qtx.CreateEventCategory(ctx, storage.CreateEventCategoryParams{
			EventID:  r.ID,
			Category: c,
		}); err != nil {
			return Event{}, fmt.Errorf("create override category: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit override: %w", err)
	}

	e := fromStorage(r)
	e.Categories = strings.Join(carriedCats, ",")
	_ = storage.MarkResourceDirty(ctx, s.db, master.CalendarID, uid, "event")
	return e, nil
}

// overrideUpdateParams builds the storage params for updating an existing
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

// overrideCreateParams builds the storage params for inserting a fresh
// override row. seq should be the master's sequence + 1 so this override
// shows up as a later revision in iCal SEQUENCE terms.
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

// UpdateFromInstance splits a recurring series at instanceTime, leaving the
// past intact and applying the user's edits to a new series that starts at
// instanceTime. Internally it:
//
//  1. Truncates the master's RRULE with UNTIL=instanceTime-1s.
//  2. Soft-deletes any overrides at or after the cutoff (those instances will
//     never expand again, so an override there would be unreachable).
//  3. Creates a brand-new event (fresh UID) carrying p's field values plus the
//     RecurrenceRule the caller passes in — typically the same rule the user
//     had, possibly edited.
//
// Both rows are marked dirty so CalDAV sync ships the truncation and the new
// series together. Pre-truncation state is recorded in event_truncate_deletes
// so the trash view can offer an atomic restore later.
func (s *Service) UpdateFromInstance(ctx context.Context, uid string, instanceTime time.Time, p UpdateParams) (Event, error) {
	p.applyDefaults()
	master, err := s.q.GetEventByUID(ctx, uid)
	if err != nil {
		return Event{}, fmt.Errorf("get master: %w", err)
	}

	prevRRule := storage.NullableToString(master.RecurrenceRule)
	until := instanceTime.UTC().Add(-time.Second)
	truncatedRule := setRRuleUntil(prevRRule, until)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
		RecurrenceRule: storage.StringToNullable(truncatedRule),
		ID:             master.ID,
	}); err != nil {
		return Event{}, fmt.Errorf("truncate master rrule: %w", err)
	}

	cutoff := instanceTime.UTC().Format(time.RFC3339)
	if err := qtx.SoftDeleteOverridesAtOrAfter(ctx, storage.SoftDeleteOverridesAtOrAfterParams{
		Uid:          uid,
		RecurrenceID: cutoff,
	}); err != nil {
		return Event{}, fmt.Errorf("soft-delete future overrides: %w", err)
	}

	if err := qtx.RecordEventTruncateDelete(ctx, storage.RecordEventTruncateDeleteParams{
		CalendarID:    master.CalendarID,
		Uid:           uid,
		CutoffTime:    cutoff,
		PreviousRrule: prevRRule,
	}); err != nil {
		return Event{}, fmt.Errorf("record truncate: %w", err)
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
		return Event{}, fmt.Errorf("create split series: %w", err)
	}

	for _, c := range carriedCats {
		if _, err := qtx.CreateEventCategory(ctx, storage.CreateEventCategoryParams{
			EventID:  r.ID,
			Category: c,
		}); err != nil {
			return Event{}, fmt.Errorf("create split category: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit split: %w", err)
	}

	e := fromStorage(r)
	e.Categories = strings.Join(carriedCats, ",")
	_ = storage.MarkResourceDirty(ctx, s.db, master.CalendarID, uid, "event")
	_ = storage.MarkResourceDirty(ctx, s.db, p.CalendarID, newUID, "event")
	return e, nil
}

// setRRuleUntil adds or replaces the UNTIL parameter in an RRULE string.
func setRRuleUntil(rule string, until time.Time) string {
	untilStr := "UNTIL=" + until.UTC().Format("20060102T150405Z")
	parts := strings.Split(rule, ";")
	out := parts[:0]
	for _, p := range parts {
		if !strings.HasPrefix(strings.ToUpper(p), "UNTIL=") && !strings.HasPrefix(strings.ToUpper(p), "COUNT=") {
			out = append(out, p)
		}
	}
	out = append(out, untilStr)
	return strings.Join(out, ";")
}

// DeleteSeries deletes a recurring master event and all its overrides.
func (s *Service) DeleteSeries(ctx context.Context, uid string) error {
	// Look up the master to get calendarID for tombstone creation.
	master, err := s.q.GetEventByUID(ctx, uid)
	if err == nil {
		_, _ = storage.CreateTombstoneIfSynced(ctx, s.db, master.CalendarID, uid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.SoftDeleteEventsByUID(ctx, uid); err != nil {
		return fmt.Errorf("soft-delete series: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete series: %w", err)
	}
	return nil
}

// Alarm CRUD

// buildAlarmsWithAttendees converts storage alarm rows into model.Alarm
// values with attendees batch-loaded.
func buildAlarmsWithAttendees(ctx context.Context, q *storage.Queries, rows []storage.EventAlarm) []model.Alarm {
	if len(rows) == 0 {
		return nil
	}
	alarmIDs := make([]int64, len(rows))
	for i, r := range rows {
		alarmIDs[i] = r.ID
	}
	attRows, err := q.ListAlarmAttendeesByAlarmIDs(ctx, alarmIDs)
	if err != nil {
		log.Printf("buildAlarmsWithAttendees: failed to load attendees for %d alarms: %v", len(alarmIDs), err)
	}
	attMap := make(map[int64][]model.AlarmAttendee, len(rows))
	for _, ar := range attRows {
		attMap[ar.AlarmID] = append(attMap[ar.AlarmID], model.AlarmAttendee{
			ID: ar.ID, Email: ar.Email, Name: storage.NullableToString(ar.Name),
		})
	}
	alarms := make([]model.Alarm, len(rows))
	for i, r := range rows {
		alarms[i] = fromStorageAlarm(r)
		alarms[i].Attendees = attMap[r.ID]
	}
	storage.AttachAlarmXProperties(ctx, q, storage.OwnerTypeEventAlarm, alarms)
	return alarms
}

func (s *Service) ListAlarms(ctx context.Context, eventID int64) ([]model.Alarm, error) {
	rows, err := s.q.ListAlarmsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	return buildAlarmsWithAttendees(ctx, s.q, rows), nil
}

// ListAlarmsByEventIDs fetches alarms for multiple event IDs in a single batch query.
// Returns a map of event ID to its list of alarms.
func (s *Service) ListAlarmsByEventIDs(ctx context.Context, eventIDs []int64) (map[int64][]model.Alarm, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	alarmRows, err := s.q.ListAlarmsByEventIDs(ctx, eventIDs)
	if err != nil {
		return nil, err
	}
	alarms := buildAlarmsWithAttendees(ctx, s.q, alarmRows)
	if len(alarms) == 0 {
		return nil, nil
	}
	alarmMap := make(map[int64][]model.Alarm, len(eventIDs))
	for _, a := range alarms {
		alarmMap[a.EventID] = append(alarmMap[a.EventID], a)
	}
	return alarmMap, nil
}

// loadExistingAlarms loads existing alarms with their attendees for the given event.
func loadExistingAlarms(ctx context.Context, qtx *storage.Queries, eventID int64) ([]model.Alarm, error) {
	rows, err := qtx.ListAlarmsByEventID(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("list existing alarms: %w", err)
	}
	return buildAlarmsWithAttendees(ctx, qtx, rows), nil
}

// applyAlarmDefaults sets default values for alarm fields.
func applyAlarmDefaults(a *model.Alarm) {
	if a.Action == "" {
		a.Action = "DISPLAY"
	}
	if a.Related == "" {
		a.Related = "START"
	}
}

// matchAlarm tries to match an incoming alarm with existing ones by content.
// Returns true and the index if matched, false otherwise.
func matchAlarm(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
	for j, ex := range existing {
		if matched[j] {
			continue
		}
		if a.ContentEqual(ex) {
			return j, true
		}
	}
	return 0, false
}

func alarmUID(a model.Alarm) string {
	if a.UID != "" {
		return a.UID
	}
	return uuid.New().String()
}

// matchAlarmByUID tries to match an incoming alarm with existing ones by
// RFC 9074 UID. Used as a fallback when content matching fails so an edited
// alarm (e.g. a changed trigger) updates its row in place instead of being
// deleted and re-created, which would cascade away its alarm_state and
// resurrect dismissed firings.
func matchAlarmByUID(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
	if a.UID == "" {
		return 0, false
	}
	for j, ex := range existing {
		if matched[j] || ex.UID == "" {
			continue
		}
		if ex.UID == a.UID {
			return j, true
		}
	}
	return 0, false
}

// updateAlarmInPlace rewrites a UID-matched alarm's content on its existing
// row, preserving the row ID so alarm_state entries keyed to it survive.
func updateAlarmInPlace(ctx context.Context, qtx *storage.Queries, eventID int64, a model.Alarm, ex model.Alarm) error {
	if err := qtx.UpdateAlarmContentByID(ctx, storage.UpdateAlarmContentByIDParams{
		Action:        a.Action,
		TriggerValue:  a.TriggerValue,
		Description:   storage.StringToNullable(a.Description),
		Summary:       storage.StringToNullable(a.Summary),
		Repeat:        int64(a.Repeat),
		Duration:      storage.StringToNullable(a.Duration),
		Related:       a.Related,
		Acknowledged:  storage.StringToNullable(a.Acknowledged),
		AttachUri:     storage.StringToNullable(a.AttachURI),
		AttachFmttype: storage.StringToNullable(a.AttachFmtType),
		ID:            ex.ID,
		EventID:       eventID,
	}); err != nil {
		return fmt.Errorf("update alarm content: %w", err)
	}
	if err := qtx.DeleteAlarmAttendeesByAlarmID(ctx, ex.ID); err != nil {
		return fmt.Errorf("delete alarm attendees: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateAlarmAttendee(ctx, storage.CreateAlarmAttendeeParams{
			AlarmID: ex.ID,
			Email:   att.Email,
			Name:    storage.StringToNullable(att.Name),
		})
		if err != nil {
			return fmt.Errorf("create alarm attendee: %w", err)
		}
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeEventAlarm, ex.ID, a.XProperties)
}

// syncMatchedAlarm syncs a matched alarm's UID and ACKNOWLEDGED state.
func syncMatchedAlarm(ctx context.Context, qtx *storage.Queries, eventID int64, a model.Alarm, ex model.Alarm) error {
	// If existing alarm has no UID, backfill it now.
	if ex.UID == "" {
		if err := qtx.UpdateAlarmUID(ctx, storage.UpdateAlarmUIDParams{
			Uid: storage.StringToNullable(alarmUID(a)),
			ID:  ex.ID,
		}); err != nil {
			return fmt.Errorf("backfill alarm uid: %w", err)
		}
	}
	// Sync ACKNOWLEDGED if the incoming value differs (including clearing).
	if a.Acknowledged != ex.Acknowledged && model.ValidateAcknowledged(a.Acknowledged) {
		if err := qtx.UpdateAlarmAcknowledged(ctx, storage.UpdateAlarmAcknowledgedParams{
			Acknowledged: storage.StringToNullable(a.Acknowledged),
			ID:           ex.ID,
			EventID:      eventID,
		}); err != nil {
			return fmt.Errorf("update alarm acknowledged: %w", err)
		}
	}
	// X-properties are excluded from content matching; refresh them so a
	// remote X-prop change still lands.
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeEventAlarm, ex.ID, a.XProperties)
}

// createNewAlarm creates a new alarm and its attendees.
func createNewAlarm(ctx context.Context, qtx *storage.Queries, eventID int64, a model.Alarm) error {
	row, err := qtx.CreateAlarm(ctx, storage.CreateAlarmParams{
		EventID:       eventID,
		Uid:           storage.StringToNullable(alarmUID(a)),
		Action:        a.Action,
		TriggerValue:  a.TriggerValue,
		Description:   storage.StringToNullable(a.Description),
		Summary:       storage.StringToNullable(a.Summary),
		Repeat:        int64(a.Repeat),
		Duration:      storage.StringToNullable(a.Duration),
		Related:       a.Related,
		Acknowledged:  storage.StringToNullable(a.Acknowledged),
		AttachUri:     storage.StringToNullable(a.AttachURI),
		AttachFmttype: storage.StringToNullable(a.AttachFmtType),
	})
	if err != nil {
		return fmt.Errorf("create alarm: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateAlarmAttendee(ctx, storage.CreateAlarmAttendeeParams{
			AlarmID: row.ID,
			Email:   att.Email,
			Name:    storage.StringToNullable(att.Name),
		})
		if err != nil {
			return fmt.Errorf("create alarm attendee: %w", err)
		}
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeEventAlarm, row.ID, a.XProperties)
}

// deleteUnmatchedAlarms deletes existing alarms that were not matched.
func deleteUnmatchedAlarms(ctx context.Context, qtx *storage.Queries, existing []model.Alarm, matched []bool) error {
	for j, ex := range existing {
		if !matched[j] {
			if err := qtx.DeleteAlarmByID(ctx, ex.ID); err != nil {
				return fmt.Errorf("delete unmatched alarm: %w", err)
			}
		}
	}
	return nil
}

func (s *Service) ReplaceAlarms(ctx context.Context, eventID int64, alarms []model.Alarm) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)

	// Load existing alarms with attendees for content matching.
	existing, err := loadExistingAlarms(ctx, qtx, eventID)
	if err != nil {
		return err
	}

	// Match incoming alarms against existing by content.
	// Slice-based matching: each existing alarm can only match once (supports duplicates).
	matched := make([]bool, len(existing))
	for i := range alarms {
		applyAlarmDefaults(&alarms[i])
	}
	var unmatched []model.Alarm
	for _, a := range alarms {
		if j, found := matchAlarm(existing, matched, a); found {
			matched[j] = true
			if err := syncMatchedAlarm(ctx, qtx, eventID, a, existing[j]); err != nil {
				return err
			}
		} else {
			unmatched = append(unmatched, a)
		}
	}

	// Second pass: alarms whose content changed but whose RFC 9074 UID is
	// stable are the same alarm edited, not a new one. Update in place so
	// the row ID — and the alarm_state rows hanging off it — survive.
	for _, a := range unmatched {
		if j, found := matchAlarmByUID(existing, matched, a); found {
			matched[j] = true
			if err := updateAlarmInPlace(ctx, qtx, eventID, a, existing[j]); err != nil {
				return err
			}
		} else {
			if err := createNewAlarm(ctx, qtx, eventID, a); err != nil {
				return err
			}
		}
	}

	// Delete existing alarms that were not matched (they were removed).
	if err := deleteUnmatchedAlarms(ctx, qtx, existing, matched); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// Attendee CRUD

func (s *Service) ListAttendees(ctx context.Context, eventID int64) ([]model.Attendee, error) {
	rows, err := s.q.ListAttendeesByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	attendees := make([]model.Attendee, len(rows))
	for i, r := range rows {
		attendees[i] = fromStorageAttendee(r)
	}
	return attendees, nil
}

func (s *Service) ReplaceAttendees(ctx context.Context, eventID int64, attendees []model.Attendee) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteAttendeesByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete attendees: %w", err)
	}
	for _, a := range attendees {
		rsvp := ""
		if a.RSVPRequested {
			rsvp = "TRUE"
		}
		_, err := qtx.CreateAttendee(ctx, storage.CreateAttendeeParams{
			EventID:       eventID,
			Email:         a.Email,
			Name:          storage.StringToNullable(a.Name),
			RsvpStatus:    a.RSVPStatus,
			Role:          a.Role,
			Organizer:     storage.BoolToInt(a.Organizer),
			Cutype:        storage.StringToNullable(a.CUType),
			Rsvp:          storage.StringToNullable(rsvp),
			SentBy:        storage.StringToNullable(a.SentBy),
			DelegatedTo:   storage.StringToNullable(a.DelegatedTo),
			DelegatedFrom: storage.StringToNullable(a.DelegatedFrom),
			Member:        storage.StringToNullable(a.Member),
			Dir:           storage.StringToNullable(a.Dir),
			Language:      storage.StringToNullable(a.Language),
		})
		if err != nil {
			return fmt.Errorf("create attendee: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// Category CRUD

func (s *Service) ListCategories(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListCategoriesByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Category
	}
	return out, nil
}

func (s *Service) ListAllCategories(ctx context.Context) ([]string, error) {
	return s.q.ListAllEventCategories(ctx)
}

func (s *Service) ReplaceCategories(ctx context.Context, eventID int64, categories []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteCategoriesByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete categories: %w", err)
	}
	for _, c := range categories {
		_, err := qtx.CreateEventCategory(ctx, storage.CreateEventCategoryParams{
			EventID:  eventID,
			Category: c,
		})
		if err != nil {
			return fmt.Errorf("create category: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace categories: %w", err)
	}
	return nil
}

// Attachment CRUD

func (s *Service) ListAttachments(ctx context.Context, eventID int64) ([]model.Attachment, error) {
	rows, err := s.q.ListEventAttachmentsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, len(rows))
	for i, r := range rows {
		out[i] = model.Attachment{ID: r.ID, URI: storage.NullableToString(r.Uri), FmtType: storage.NullableToString(r.Fmttype), Data: r.Data, Filename: storage.NullableToString(r.Filename)}
	}
	return out, nil
}

func (s *Service) ReplaceAttachments(ctx context.Context, eventID int64, attachments []model.Attachment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteEventAttachmentsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	for _, a := range attachments {
		_, err := qtx.CreateEventAttachment(ctx, storage.CreateEventAttachmentParams{
			EventID: eventID, Uri: storage.StringToNullable(a.URI), Fmttype: storage.StringToNullable(a.FmtType), Data: a.Data, Filename: storage.StringToNullable(a.Filename),
		})
		if err != nil {
			return fmt.Errorf("create attachment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// Comment CRUD

func (s *Service) ListComments(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListEventCommentsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceComments(ctx context.Context, eventID int64, comments []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteEventCommentsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete comments: %w", err)
	}
	for _, c := range comments {
		_, err := qtx.CreateEventComment(ctx, storage.CreateEventCommentParams{
			EventID: eventID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create comment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// Contact CRUD

func (s *Service) ListContacts(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListEventContactsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceContacts(ctx context.Context, eventID int64, contacts []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteEventContactsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete contacts: %w", err)
	}
	for _, c := range contacts {
		_, err := qtx.CreateEventContact(ctx, storage.CreateEventContactParams{
			EventID: eventID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create contact: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// Resource CRUD

func (s *Service) ListResources(ctx context.Context, eventID int64) ([]string, error) {
	rows, err := s.q.ListEventResourcesByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceResources(ctx context.Context, eventID int64, resources []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteEventResourcesByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete resources: %w", err)
	}
	for _, r := range resources {
		_, err := qtx.CreateEventResource(ctx, storage.CreateEventResourceParams{
			EventID: eventID, Text: r,
		})
		if err != nil {
			return fmt.Errorf("create resource: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// Relation CRUD

func (s *Service) ListRelations(ctx context.Context, eventID int64) ([]model.Relation, error) {
	rows, err := s.q.ListEventRelationsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Relation, len(rows))
	for i, r := range rows {
		out[i] = model.Relation{ID: r.ID, RelType: r.RelType, RelUID: r.RelUid}
	}
	return out, nil
}

func (s *Service) ReplaceRelations(ctx context.Context, eventID int64, relations []model.Relation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteEventRelationsByEventID(ctx, eventID); err != nil {
		return fmt.Errorf("delete relations: %w", err)
	}
	for _, r := range relations {
		_, err := qtx.CreateEventRelation(ctx, storage.CreateEventRelationParams{
			EventID: eventID, RelType: r.RelType, RelUid: r.RelUID,
		})
		if err != nil {
			return fmt.Errorf("create relation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

// Converters

func fromStorage(r storage.Event) Event {
	var deletedAt *time.Time
	if r.DeletedAt != nil && *r.DeletedAt != "" {
		t := timeutil.ParseDateTime(*r.DeletedAt)
		deletedAt = &t
	}
	return Event{
		ID:             r.ID,
		UID:            r.Uid,
		CalendarID:     r.CalendarID,
		Title:          r.Title,
		Description:    storage.NullableToString(r.Description),
		Location:       storage.NullableToString(r.Location),
		StartTime:      timeutil.ParseDateTime(r.StartTime),
		EndTime:        timeutil.ParseDateTime(r.EndTime),
		AllDay:         r.AllDay == 1,
		RecurrenceRule: storage.NullableToString(r.RecurrenceRule),
		Timezone:       storage.NullableToString(r.Timezone),
		Status:         r.Status,
		Transp:         r.Transp,
		Sequence:       r.Sequence,
		Priority:       r.Priority,
		Class:          r.Class,
		URL:            storage.NullableToString(r.Url),
		ConferenceURI:  r.ConferenceUri,
		ExDates:        storage.NullableToString(r.Exdates),
		RDates:         storage.NullableToString(r.Rdates),
		RecurrenceID:   r.RecurrenceID,
		Geo:            storage.NullableToString(r.Geo),
		DurationValue:  storage.NullableToString(r.Duration),
		DtStamp:        storage.NullableToString(r.Dtstamp),
		CreatedAt:      timeutil.ParseDateTime(r.CreatedAt),
		UpdatedAt:      timeutil.ParseDateTime(r.UpdatedAt),
		DeletedAt:      deletedAt,
	}
}

func fromStorageSlice(rows []storage.Event) []Event {
	events := make([]Event, len(rows))
	for i, r := range rows {
		events[i] = fromStorage(r)
	}
	return events
}

func (s *Service) populateSingleCategories(ctx context.Context, e *Event) {
	cats, err := s.ListCategories(ctx, e.ID)
	if err != nil {
		log.Printf("populateSingleCategories failed for event %d: %v", e.ID, err)
		return
	}
	e.Categories = strings.Join(cats, ",")
}

func (s *Service) populateCategories(ctx context.Context, events []Event) {
	if len(events) == 0 {
		return
	}
	ids := make([]int64, len(events))
	for i := range events {
		ids[i] = events[i].ID
	}
	rows, err := s.q.ListCategoriesByEventIDs(ctx, ids)
	if err != nil {
		log.Printf("populateCategories failed for %d events: %v", len(events), err)
		return
	}
	catMap := make(map[int64][]string, len(events))
	for _, r := range rows {
		catMap[r.EventID] = append(catMap[r.EventID], r.Category)
	}
	for i := range events {
		if cats, ok := catMap[events[i].ID]; ok {
			events[i].Categories = strings.Join(cats, ",")
		}
	}
}

func fromStorageAlarm(r storage.EventAlarm) model.Alarm {
	return model.Alarm{
		ID:            r.ID,
		EventID:       r.EventID,
		UID:           storage.NullableToString(r.Uid),
		Action:        r.Action,
		TriggerValue:  r.TriggerValue,
		Description:   storage.NullableToString(r.Description),
		Summary:       storage.NullableToString(r.Summary),
		Repeat:        int(r.Repeat),
		Duration:      storage.NullableToString(r.Duration),
		Related:       r.Related,
		Acknowledged:  storage.NullableToString(r.Acknowledged),
		AttachURI:     storage.NullableToString(r.AttachUri),
		AttachFmtType: storage.NullableToString(r.AttachFmttype),
	}
}

// X-Property CRUD

func (s *Service) ListXProperties(ctx context.Context, eventID int64) ([]model.XProperty, error) {
	rows, err := s.q.ListXPropertiesByOwner(ctx, storage.ListXPropertiesByOwnerParams{
		OwnerType: "event", OwnerID: eventID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.XProperty, len(rows))
	for i, r := range rows {
		out[i] = model.XProperty{
			ID: r.ID, OwnerType: r.OwnerType, OwnerID: r.OwnerID,
			Name: r.Name, Value: r.Value, Params: r.Params,
		}
	}
	return out, nil
}

func (s *Service) ReplaceXProperties(ctx context.Context, eventID int64, xprops []model.XProperty) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteXPropertiesByOwner(ctx, storage.DeleteXPropertiesByOwnerParams{
		OwnerType: "event", OwnerID: eventID,
	}); err != nil {
		return fmt.Errorf("delete x-properties: %w", err)
	}
	for _, xp := range xprops {
		if err := qtx.InsertXProperty(ctx, storage.InsertXPropertyParams{
			OwnerType: "event", OwnerID: eventID,
			Name: xp.Name, Value: xp.Value, Params: xp.Params,
		}); err != nil {
			return fmt.Errorf("insert x-property: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, eventID)
	return nil
}

func fromStorageAttendee(r storage.EventAttendee) model.Attendee {
	return model.Attendee{
		ID:            r.ID,
		EventID:       r.EventID,
		Email:         r.Email,
		Name:          storage.NullableToString(r.Name),
		RSVPStatus:    r.RsvpStatus,
		Role:          r.Role,
		Organizer:     r.Organizer == 1,
		CUType:        storage.NullableToString(r.Cutype),
		RSVPRequested: strings.EqualFold(storage.NullableToString(r.Rsvp), "TRUE"),
		SentBy:        storage.NullableToString(r.SentBy),
		DelegatedTo:   storage.NullableToString(r.DelegatedTo),
		DelegatedFrom: storage.NullableToString(r.DelegatedFrom),
		Member:        storage.NullableToString(r.Member),
		Dir:           storage.NullableToString(r.Dir),
		Language:      storage.NullableToString(r.Language),
	}
}
