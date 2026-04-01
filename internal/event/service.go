package event

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
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

func (p *CreateParams) applyDefaults() {
	p.Status = strings.ToUpper(p.Status)
	p.Transp = strings.ToUpper(p.Transp)
	p.Class = strings.ToUpper(p.Class)
	if p.Status == "" {
		p.Status = "CONFIRMED"
	}
	if p.Transp == "" {
		p.Transp = "OPAQUE"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
}

func (p *UpsertParams) applyDefaults() {
	p.Status = strings.ToUpper(p.Status)
	p.Transp = strings.ToUpper(p.Transp)
	p.Class = strings.ToUpper(p.Class)
	if p.Status == "" {
		p.Status = "CONFIRMED"
	}
	if p.Transp == "" {
		p.Transp = "OPAQUE"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
}

func (s *Service) ListByDateRange(ctx context.Context, from, to time.Time) ([]Event, error) {
	rows, err := s.q.ListEventsByDateRange(ctx, storage.ListEventsByDateRangeParams{
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) ListByStatusAndDateRange(ctx context.Context, status string, from, to time.Time) ([]Event, error) {
	rows, err := s.q.ListEventsByStatusAndDateRange(ctx, storage.ListEventsByStatusAndDateRangeParams{
		Status:      status,
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
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
		CalendarID:  calID,
		StartTime:   from.Format(time.RFC3339),
		StartTime_2: to.Format(time.RFC3339),
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
		return nil, nil
	}
	rows, err := s.q.SearchEventsFTS(ctx, ftsQuery, p.CalendarID, p.From, p.To, p.Status)
	if err != nil {
		return nil, fmt.Errorf("search events: %w", err)
	}
	events := fromStorageSlice(rows)
	s.populateCategories(ctx, events)
	return events, nil
}

func (s *Service) syncFTS(ctx context.Context, e Event) {
	cats := strings.ReplaceAll(e.Categories, ",", " ")
	_ = s.q.UpsertEventFTS(ctx, e.ID, e.Title, e.Description, e.Location, cats)
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
		AllDay:         boolToInt(p.AllDay),
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
	s.syncFTS(ctx, e)
	return e, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Event, error) {
	p.Status = strings.ToUpper(p.Status)
	p.Transp = strings.ToUpper(p.Transp)
	p.Class = strings.ToUpper(p.Class)
	if p.Status == "" {
		p.Status = "CONFIRMED"
	}
	if p.Transp == "" {
		p.Transp = "OPAQUE"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
	r, err := s.q.UpdateEvent(ctx, storage.UpdateEventParams{
		ID:             id,
		Title:          p.Title,
		Description:    storage.StringToNullable(p.Description),
		Location:       storage.StringToNullable(p.Location),
		StartTime:      p.StartTime.Format(time.RFC3339),
		EndTime:        p.EndTime.Format(time.RFC3339),
		AllDay:         boolToInt(p.AllDay),
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
	s.syncFTS(ctx, e)
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
		AllDay:         boolToInt(p.AllDay),
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
	s.syncFTS(ctx, e)
	return e, nil
}

// ErrHasOverrides is returned when attempting to delete a recurring master
// event that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("event has overrides: use DeleteSeries to delete the entire series")

func (s *Service) Delete(ctx context.Context, id int64) error {
	evt, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

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
			recIDTime, parseErr := time.Parse(time.RFC3339, evt.RecurrenceID)
			if parseErr != nil {
				// Try date-only format for all-day events.
				recIDTime, parseErr = time.Parse("2006-01-02", evt.RecurrenceID)
				if parseErr == nil {
					recIDTime = time.Date(recIDTime.Year(), recIDTime.Month(), recIDTime.Day(), 0, 0, 0, 0, time.Local)
				}
			}
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

		_ = qtx.DeleteEventFTS(ctx, id)
		if err := qtx.DeleteEvent(ctx, id); err != nil {
			return fmt.Errorf("delete event: %w", err)
		}
		return tx.Commit()
	}

	_ = s.q.DeleteEventFTS(ctx, id)
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

	_ = qtx.DeleteEventsFTSByUID(ctx, uid)
	if err := qtx.DeleteEventsByUID(ctx, uid); err != nil {
		return fmt.Errorf("delete series: %w", err)
	}
	return tx.Commit()
}

// Alarm CRUD

func (s *Service) ListAlarms(ctx context.Context, eventID int64) ([]model.Alarm, error) {
	rows, err := s.q.ListAlarmsByEventID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	alarms := make([]model.Alarm, len(rows))
	for i, r := range rows {
		alarms[i] = fromStorageAlarm(r)
		attRows, err := s.q.ListAlarmAttendeesByAlarmID(ctx, r.ID)
		if err == nil {
			for _, ar := range attRows {
				alarms[i].Attendees = append(alarms[i].Attendees, model.AlarmAttendee{
					ID: ar.ID, Email: ar.Email, Name: storage.NullableToString(ar.Name),
				})
			}
		}
	}
	return alarms, nil
}

func (s *Service) ReplaceAlarms(ctx context.Context, eventID int64, alarms []model.Alarm) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)

	// Load existing alarms with attendees for content matching.
	existingRows, err := qtx.ListAlarmsByEventID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("list existing alarms: %w", err)
	}
	existing := make([]model.Alarm, len(existingRows))
	for i, r := range existingRows {
		existing[i] = fromStorageAlarm(r)
		attRows, err := qtx.ListAlarmAttendeesByAlarmID(ctx, r.ID)
		if err == nil {
			for _, ar := range attRows {
				existing[i].Attendees = append(existing[i].Attendees, model.AlarmAttendee{
					ID: ar.ID, Email: ar.Email, Name: storage.NullableToString(ar.Name),
				})
			}
		}
	}

	// Match incoming alarms against existing by content.
	// Slice-based matching: each existing alarm can only match once (supports duplicates).
	matched := make([]bool, len(existing))
	for _, a := range alarms {
		found := false
		for j, ex := range existing {
			if matched[j] {
				continue
			}
			if a.ContentEqual(ex) {
				matched[j] = true
				found = true
				// If existing alarm has no UID, backfill it now.
				if ex.UID == "" {
					uid := a.UID
					if uid == "" {
						uid = uuid.New().String()
					}
					if err := qtx.UpdateAlarmUID(ctx, storage.UpdateAlarmUIDParams{
						Uid: storage.StringToNullable(uid),
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
				break
			}
		}
		if !found {
			// New alarm: assign UID and insert.
			uid := a.UID
			if uid == "" {
				uid = uuid.New().String()
			}
			row, err := qtx.CreateAlarm(ctx, storage.CreateAlarmParams{
				EventID:       eventID,
				Uid:           storage.StringToNullable(uid),
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
		}
	}

	// Delete existing alarms that were not matched (they were removed).
	for j, ex := range existing {
		if !matched[j] {
			if err := qtx.DeleteAlarmByID(ctx, ex.ID); err != nil {
				return fmt.Errorf("delete unmatched alarm: %w", err)
			}
		}
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
			Organizer:     boolToInt(a.Organizer),
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
		out[i] = model.Attachment{ID: r.ID, URI: r.Uri, FmtType: storage.NullableToString(r.Fmttype), Data: r.Data, Filename: storage.NullableToString(r.Filename)}
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
			EventID: eventID, Uri: a.URI, Fmttype: storage.StringToNullable(a.FmtType), Data: a.Data, Filename: storage.StringToNullable(a.Filename),
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
		out[i] = model.Relation{ID: r.ID, RelType: r.RelType, RelUID: storage.NullableToString(r.RelUid)}
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
			EventID: eventID, RelType: r.RelType, RelUid: storage.StringToNullable(r.RelUID),
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
		StartTime:      parseTime(r.StartTime),
		EndTime:        parseTime(r.EndTime),
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
		CreatedAt:      parseTime(r.CreatedAt),
		UpdatedAt:      parseTime(r.UpdatedAt),
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
	rows, err := s.q.ListCategoriesByEventID(ctx, e.ID)
	if err != nil {
		return
	}
	cats := make([]string, len(rows))
	for j, r := range rows {
		cats[j] = r.Category
	}
	e.Categories = strings.Join(cats, ",")
}

func (s *Service) populateCategories(ctx context.Context, events []Event) {
	for i := range events {
		rows, err := s.q.ListCategoriesByEventID(ctx, events[i].ID)
		if err != nil {
			continue
		}
		cats := make([]string, len(rows))
		for j, r := range rows {
			cats[j] = r.Category
		}
		events[i].Categories = strings.Join(cats, ",")
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

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
