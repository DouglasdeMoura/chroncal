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

	// If this is an override, add EXDATE to the master so the instance
	// doesn't reappear on next expansion.
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

		if err := qtx.DeleteEvent(ctx, id); err != nil {
			return fmt.Errorf("delete event: %w", err)
		}
		return tx.Commit()
	}

	return s.q.DeleteEvent(ctx, id)
}

// DeleteSeries deletes a recurring master event and all its overrides.
func (s *Service) DeleteSeries(ctx context.Context, uid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.DeleteEventsByUID(ctx, uid); err != nil {
		return fmt.Errorf("delete series: %w", err)
	}
	return tx.Commit()
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
	return nil
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
	return nil
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
	for _, a := range alarms {
		if j, found := matchAlarm(existing, matched, a); found {
			matched[j] = true
			if err := syncMatchedAlarm(ctx, qtx, eventID, a, existing[j]); err != nil {
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

	return tx.Commit()
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
	return tx.Commit()
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
	return tx.Commit()
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
	return tx.Commit()
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
	return tx.Commit()
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
	return tx.Commit()
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
	return tx.Commit()
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
	return tx.Commit()
}

// Converters

func fromStorage(r storage.Event) Event {
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
		ExDates:        storage.NullableToString(r.Exdates),
		RDates:         storage.NullableToString(r.Rdates),
		RecurrenceID:   r.RecurrenceID,
		Geo:            storage.NullableToString(r.Geo),
		DurationValue:  storage.NullableToString(r.Duration),
		DtStamp:        storage.NullableToString(r.Dtstamp),
		CreatedAt:      timeutil.ParseDateTime(r.CreatedAt),
		UpdatedAt:      timeutil.ParseDateTime(r.UpdatedAt),
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
