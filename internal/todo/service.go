package todo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/tcal/internal/model"
	"github.com/douglasdemoura/tcal/internal/storage"
)

type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
}

type CreateParams struct {
	CalendarID      int64
	Summary         string
	Description     string
	Location        string
	DueDate         string
	StartDate       string
	Duration        string
	PercentComplete int64
	Status          string
	Priority        int64
	Class           string
	URL             string
	Categories      string
	RecurrenceRule  string
	Timezone        string
	Sequence        int64
	ExDates         string
	RDates          string
	RecurrenceID    string
	Geo             string
}

type UpdateParams struct {
	Summary         string
	Description     string
	Location        string
	DueDate         string
	StartDate       string
	Duration        string
	CompletedAt     string
	PercentComplete int64
	Status          string
	CalendarID      int64
	Priority        int64
	Class           string
	URL             string
	Categories      string
	RecurrenceRule  string
	Timezone        string
	ExDates         string
	RDates          string
	Geo             string
}

type UpsertParams struct {
	UID             string
	CalendarID      int64
	Summary         string
	Description     string
	Location        string
	DueDate         string
	StartDate       string
	Duration        string
	CompletedAt     string
	PercentComplete int64
	Status          string
	Priority        int64
	Class           string
	URL             string
	Categories      string
	RecurrenceRule  string
	Timezone        string
	Sequence        int64
	ExDates         string
	RDates          string
	RecurrenceID    string
	Geo             string
}

func (p *CreateParams) applyDefaults() {
	if p.Status == "" {
		p.Status = "NEEDS-ACTION"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
}

func (p *UpsertParams) applyDefaults() {
	if p.Status == "" {
		p.Status = "NEEDS-ACTION"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
}

func (s *Service) List(ctx context.Context) ([]Todo, error) {
	rows, err := s.q.ListTodos(ctx)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

func (s *Service) ListAll(ctx context.Context) ([]Todo, error) {
	rows, err := s.q.ListAllTodos(ctx)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

func (s *Service) ListByCalendar(ctx context.Context, calID int64) ([]Todo, error) {
	rows, err := s.q.ListTodosByCalendar(ctx, calID)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

func (s *Service) ListByStatus(ctx context.Context, status string) ([]Todo, error) {
	rows, err := s.q.ListTodosByStatus(ctx, status)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

func (s *Service) ListByDueDateRange(ctx context.Context, from, to time.Time) ([]Todo, error) {
	rows, err := s.q.ListTodosByDueDateRange(ctx, storage.ListTodosByDueDateRangeParams{
		DueDate:   from.Format(time.RFC3339),
		DueDate_2: to.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

func (s *Service) Get(ctx context.Context, id int64) (Todo, error) {
	r, err := s.q.GetTodo(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) GetByUID(ctx context.Context, uid string) (Todo, error) {
	r, err := s.q.GetTodoByUID(ctx, uid)
	if err != nil {
		return Todo{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Todo, error) {
	p.applyDefaults()
	r, err := s.q.CreateTodo(ctx, storage.CreateTodoParams{
		Uid:             uuid.New().String(),
		CalendarID:      p.CalendarID,
		Summary:         p.Summary,
		Description:     p.Description,
		Location:        p.Location,
		DueDate:         p.DueDate,
		StartDate:       p.StartDate,
		Duration:        p.Duration,
		CompletedAt:     "",
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             p.URL,
		Categories:      p.Categories,
		RecurrenceRule:  p.RecurrenceRule,
		Timezone:        p.Timezone,
		Sequence:        p.Sequence,
		Exdates:         p.ExDates,
		Rdates:          p.RDates,
		RecurrenceID:    p.RecurrenceID,
		Geo:             p.Geo,
	})
	if err != nil {
		return Todo{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Todo, error) {
	if p.Status == "" {
		p.Status = "NEEDS-ACTION"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
	r, err := s.q.UpdateTodo(ctx, storage.UpdateTodoParams{
		ID:              id,
		Summary:         p.Summary,
		Description:     p.Description,
		Location:        p.Location,
		DueDate:         p.DueDate,
		StartDate:       p.StartDate,
		Duration:        p.Duration,
		CompletedAt:     p.CompletedAt,
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		CalendarID:      p.CalendarID,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             p.URL,
		Categories:      p.Categories,
		RecurrenceRule:  p.RecurrenceRule,
		Timezone:        p.Timezone,
		Exdates:         p.ExDates,
		Rdates:          p.RDates,
		Geo:             p.Geo,
	})
	if err != nil {
		return Todo{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Complete(ctx context.Context, id int64) (Todo, error) {
	r, err := s.q.CompleteTodo(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Todo, error) {
	p.applyDefaults()
	r, err := s.q.UpsertTodoByUID(ctx, storage.UpsertTodoByUIDParams{
		Uid:             p.UID,
		CalendarID:      p.CalendarID,
		Summary:         p.Summary,
		Description:     p.Description,
		Location:        p.Location,
		DueDate:         p.DueDate,
		StartDate:       p.StartDate,
		Duration:        p.Duration,
		CompletedAt:     p.CompletedAt,
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             p.URL,
		Categories:      p.Categories,
		RecurrenceRule:  p.RecurrenceRule,
		Timezone:        p.Timezone,
		Sequence:        p.Sequence,
		Exdates:         p.ExDates,
		Rdates:          p.RDates,
		RecurrenceID:    p.RecurrenceID,
		Geo:             p.Geo,
	})
	if err != nil {
		return Todo{}, err
	}
	return fromStorage(r), nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteTodo(ctx, id)
}

// Alarm CRUD

func (s *Service) ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	rows, err := s.q.ListTodoAlarmsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	alarms := make([]model.Alarm, len(rows))
	for i, r := range rows {
		alarms[i] = model.Alarm{
			ID: r.ID, EventID: r.TodoID,
			Action: r.Action, TriggerValue: r.TriggerValue,
			Description: r.Description,
			Repeat:      int(r.Repeat),
			Duration:    r.Duration,
			Related:     r.Related,
		}
		attRows, err := s.q.ListTodoAlarmAttendeesByAlarmID(ctx, r.ID)
		if err == nil {
			for _, ar := range attRows {
				alarms[i].Attendees = append(alarms[i].Attendees, model.AlarmAttendee{
					ID: ar.ID, Email: ar.Email, Name: ar.Name,
				})
			}
		}
	}
	return alarms, nil
}

func (s *Service) ReplaceAlarms(ctx context.Context, todoID int64, alarms []model.Alarm) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteTodoAlarmsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete alarms: %w", err)
	}
	for _, a := range alarms {
		row, err := qtx.CreateTodoAlarm(ctx, storage.CreateTodoAlarmParams{
			TodoID:       todoID,
			Action:       a.Action,
			TriggerValue: a.TriggerValue,
			Description:  a.Description,
			Repeat:       int64(a.Repeat),
			Duration:     a.Duration,
			Related:      a.Related,
		})
		if err != nil {
			return fmt.Errorf("create alarm: %w", err)
		}
		for _, att := range a.Attendees {
			_, err := qtx.CreateTodoAlarmAttendee(ctx, storage.CreateTodoAlarmAttendeeParams{
				AlarmID: row.ID,
				Email:   att.Email,
				Name:    att.Name,
			})
			if err != nil {
				return fmt.Errorf("create alarm attendee: %w", err)
			}
		}
	}
	return tx.Commit()
}

// Attendee CRUD

func (s *Service) ListAttendees(ctx context.Context, todoID int64) ([]model.Attendee, error) {
	rows, err := s.q.ListTodoAttendeesByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	attendees := make([]model.Attendee, len(rows))
	for i, r := range rows {
		attendees[i] = model.Attendee{
			ID: r.ID, EventID: r.TodoID,
			Email: r.Email, Name: r.Name,
			RSVPStatus: r.RsvpStatus, Role: r.Role,
			Organizer: r.Organizer == 1,
		}
	}
	return attendees, nil
}

func (s *Service) ReplaceAttendees(ctx context.Context, todoID int64, attendees []model.Attendee) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteTodoAttendeesByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete attendees: %w", err)
	}
	for _, a := range attendees {
		org := int64(0)
		if a.Organizer {
			org = 1
		}
		_, err := qtx.CreateTodoAttendee(ctx, storage.CreateTodoAttendeeParams{
			TodoID:     todoID,
			Email:      a.Email,
			Name:       a.Name,
			RsvpStatus: a.RSVPStatus,
			Role:       a.Role,
			Organizer:  org,
		})
		if err != nil {
			return fmt.Errorf("create attendee: %w", err)
		}
	}
	return tx.Commit()
}

// Attachment CRUD

func (s *Service) ListAttachments(ctx context.Context, todoID int64) ([]model.Attachment, error) {
	rows, err := s.q.ListTodoAttachmentsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, len(rows))
	for i, r := range rows {
		out[i] = model.Attachment{ID: r.ID, URI: r.Uri, FmtType: r.Fmttype, Data: r.Data, Filename: r.Filename}
	}
	return out, nil
}

func (s *Service) ReplaceAttachments(ctx context.Context, todoID int64, attachments []model.Attachment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteTodoAttachmentsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	for _, a := range attachments {
		_, err := qtx.CreateTodoAttachment(ctx, storage.CreateTodoAttachmentParams{
			TodoID: todoID, Uri: a.URI, Fmttype: a.FmtType, Data: a.Data, Filename: a.Filename,
		})
		if err != nil {
			return fmt.Errorf("create attachment: %w", err)
		}
	}
	return tx.Commit()
}

// Comment CRUD

func (s *Service) ListComments(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListTodoCommentsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceComments(ctx context.Context, todoID int64, comments []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteTodoCommentsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete comments: %w", err)
	}
	for _, c := range comments {
		_, err := qtx.CreateTodoComment(ctx, storage.CreateTodoCommentParams{
			TodoID: todoID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create comment: %w", err)
		}
	}
	return tx.Commit()
}

// Contact CRUD

func (s *Service) ListContacts(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListTodoContactsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceContacts(ctx context.Context, todoID int64, contacts []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteTodoContactsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete contacts: %w", err)
	}
	for _, c := range contacts {
		_, err := qtx.CreateTodoContact(ctx, storage.CreateTodoContactParams{
			TodoID: todoID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create contact: %w", err)
		}
	}
	return tx.Commit()
}

// Resource CRUD

func (s *Service) ListResources(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListTodoResourcesByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceResources(ctx context.Context, todoID int64, resources []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteTodoResourcesByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete resources: %w", err)
	}
	for _, r := range resources {
		_, err := qtx.CreateTodoResource(ctx, storage.CreateTodoResourceParams{
			TodoID: todoID, Text: r,
		})
		if err != nil {
			return fmt.Errorf("create resource: %w", err)
		}
	}
	return tx.Commit()
}

// Relation CRUD

func (s *Service) ListRelations(ctx context.Context, todoID int64) ([]model.Relation, error) {
	rows, err := s.q.ListTodoRelationsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Relation, len(rows))
	for i, r := range rows {
		out[i] = model.Relation{ID: r.ID, RelType: r.RelType, RelUID: r.RelUid}
	}
	return out, nil
}

func (s *Service) ReplaceRelations(ctx context.Context, todoID int64, relations []model.Relation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteTodoRelationsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete relations: %w", err)
	}
	for _, r := range relations {
		_, err := qtx.CreateTodoRelation(ctx, storage.CreateTodoRelationParams{
			TodoID: todoID, RelType: r.RelType, RelUid: r.RelUID,
		})
		if err != nil {
			return fmt.Errorf("create relation: %w", err)
		}
	}
	return tx.Commit()
}

// Converters

func fromStorage(r storage.Todo) Todo {
	return Todo{
		ID:              r.ID,
		UID:             r.Uid,
		CalendarID:      r.CalendarID,
		Summary:         r.Summary,
		Description:     r.Description,
		Location:        r.Location,
		DueDate:         r.DueDate,
		StartDate:       r.StartDate,
		Duration:        r.Duration,
		CompletedAt:     r.CompletedAt,
		PercentComplete: r.PercentComplete,
		Status:          r.Status,
		Priority:        r.Priority,
		Class:           r.Class,
		URL:             r.Url,
		Categories:      r.Categories,
		RecurrenceRule:  r.RecurrenceRule,
		Timezone:        r.Timezone,
		Sequence:        r.Sequence,
		ExDates:         r.Exdates,
		RDates:          r.Rdates,
		RecurrenceID:    r.RecurrenceID,
		Geo:             r.Geo,
		CreatedAt:       parseTime(r.CreatedAt),
		UpdatedAt:       parseTime(r.UpdatedAt),
	}
}

func fromStorageSlice(rows []storage.Todo) []Todo {
	todos := make([]Todo, len(rows))
	for i, r := range rows {
		todos[i] = fromStorage(r)
	}
	return todos
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
