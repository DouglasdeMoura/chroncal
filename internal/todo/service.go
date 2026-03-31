package todo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

type SearchParams struct {
	Query      string
	CalendarID int64  // 0 = all
	Status     string // empty = all
	Completed  int    // 0 = all, 1 = completed only, 2 = incomplete only
}

type ExportParams struct {
	CalendarID int64  // 0 = all
	Category   string // empty = all
	Status     string // empty = all
	Completed  int    // 0 = all, 1 = completed, 2 = incomplete
}

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
	DtStamp         string
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
	DtStamp         string
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
	DtStamp         string
}

func (p *CreateParams) applyDefaults() {
	if p.Status == "" {
		p.Status = "NEEDS-ACTION"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
	if p.Status == "COMPLETED" {
		p.PercentComplete = 100
	}
}

func (p *UpsertParams) applyDefaults() {
	if p.Status == "" {
		p.Status = "NEEDS-ACTION"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
	if p.Status == "COMPLETED" && p.CompletedAt == "" {
		p.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		p.PercentComplete = 100
	}
}

func (s *Service) Search(ctx context.Context, p SearchParams) ([]Todo, error) {
	rows, err := s.q.SearchTodos(ctx, storage.SearchTodosParams{
		Query:           sql.NullString{String: p.Query, Valid: p.Query != ""},
		CalendarID:      int64(p.CalendarID),
		FilterStatus:    p.Status,
		CompletedFilter: int64(p.Completed),
	})
	if err != nil {
		return nil, fmt.Errorf("search todos: %w", err)
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Todo, error) {
	rows, err := s.q.ListTodosForExport(ctx, storage.ListTodosForExportParams{
		CalendarID:      int64(p.CalendarID),
		Category:        p.Category,
		FilterStatus:    p.Status,
		CompletedFilter: int64(p.Completed),
	})
	if err != nil {
		return nil, fmt.Errorf("export todos: %w", err)
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) List(ctx context.Context) ([]Todo, error) {
	rows, err := s.q.ListTodos(ctx)
	if err != nil {
		return nil, err
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) ListAll(ctx context.Context) ([]Todo, error) {
	rows, err := s.q.ListAllTodos(ctx)
	if err != nil {
		return nil, err
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) ListByCalendar(ctx context.Context, calID int64) ([]Todo, error) {
	rows, err := s.q.ListTodosByCalendar(ctx, calID)
	if err != nil {
		return nil, err
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) ListByStatus(ctx context.Context, status string) ([]Todo, error) {
	rows, err := s.q.ListTodosByStatus(ctx, status)
	if err != nil {
		return nil, err
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) ListByDueDateRange(ctx context.Context, from, to time.Time) ([]Todo, error) {
	// Use date-only format for bounds so that date-only DUE values
	// (stored as "YYYY-MM-DD") are correctly matched by string comparison.
	rows, err := s.q.ListTodosByDueDateRange(ctx, storage.ListTodosByDueDateRangeParams{
		DueDate:   from.Format("2006-01-02"),
		DueDate_2: to.Format("2006-01-02"),
	})
	if err != nil {
		return nil, err
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Todo, error) {
	r, err := s.q.GetTodo(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	s.populateSingleCategories(ctx, &t)
	return t, nil
}

func (s *Service) GetByUID(ctx context.Context, uid string) (Todo, error) {
	r, err := s.q.GetTodoByUID(ctx, uid)
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	s.populateSingleCategories(ctx, &t)
	return t, nil
}

func (s *Service) GetByUIDAndRecurrenceID(ctx context.Context, uid, recurrenceID string) (Todo, error) {
	r, err := s.q.GetTodoByUIDAndRecurrenceID(ctx, storage.GetTodoByUIDAndRecurrenceIDParams{
		Uid:          uid,
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	s.populateSingleCategories(ctx, &t)
	return t, nil
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Todo, error) {
	p.applyDefaults()
	completedAt := ""
	if p.Status == "COMPLETED" {
		completedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r, err := s.q.CreateTodo(ctx, storage.CreateTodoParams{
		Uid:             uuid.New().String(),
		CalendarID:      p.CalendarID,
		Summary:         p.Summary,
		Description:     p.Description,
		Location:        p.Location,
		DueDate:         p.DueDate,
		StartDate:       p.StartDate,
		Duration:        p.Duration,
		CompletedAt:     completedAt,
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             p.URL,
		RecurrenceRule:  p.RecurrenceRule,
		Timezone:        p.Timezone,
		Sequence:        p.Sequence,
		Exdates:         p.ExDates,
		Rdates:          p.RDates,
		RecurrenceID:    p.RecurrenceID,
		Geo:             p.Geo,
		Dtstamp:         p.DtStamp,
	})
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	if err := s.ReplaceCategories(ctx, t.ID, event.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	t.Categories = p.Categories
	return t, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Todo, error) {
	if p.Status == "" {
		p.Status = "NEEDS-ACTION"
	}
	if p.Class == "" {
		p.Class = "PUBLIC"
	}
	if p.Status == "COMPLETED" && p.CompletedAt == "" {
		p.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		p.PercentComplete = 100
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
		RecurrenceRule:  p.RecurrenceRule,
		Timezone:        p.Timezone,
		Exdates:         p.ExDates,
		Rdates:          p.RDates,
		Geo:             p.Geo,
		Dtstamp:         p.DtStamp,
	})
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	if err := s.ReplaceCategories(ctx, t.ID, event.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	t.Categories = p.Categories
	return t, nil
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
		RecurrenceRule:  p.RecurrenceRule,
		Timezone:        p.Timezone,
		Sequence:        p.Sequence,
		Exdates:         p.ExDates,
		Rdates:          p.RDates,
		RecurrenceID:    p.RecurrenceID,
		Geo:             p.Geo,
		Dtstamp:         p.DtStamp,
	})
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	if err := s.ReplaceCategories(ctx, t.ID, event.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	t.Categories = p.Categories
	return t, nil
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
			UID:           r.Uid,
			Action:        r.Action,
			TriggerValue:  r.TriggerValue,
			Description:   r.Description,
			Summary:       r.Summary,
			Repeat:        int(r.Repeat),
			Duration:      r.Duration,
			Related:       r.Related,
			Acknowledged:  r.Acknowledged,
			AttachURI:     r.AttachUri,
			AttachFmtType: r.AttachFmttype,
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
		uid := a.UID
		if uid == "" {
			uid = uuid.New().String()
		}
		row, err := qtx.CreateTodoAlarm(ctx, storage.CreateTodoAlarmParams{
			TodoID:        todoID,
			Uid:           uid,
			Action:        a.Action,
			TriggerValue:  a.TriggerValue,
			Description:   a.Description,
			Summary:       a.Summary,
			Repeat:        int64(a.Repeat),
			Duration:      a.Duration,
			Related:       a.Related,
			Acknowledged:  a.Acknowledged,
			AttachUri:     a.AttachURI,
			AttachFmttype: a.AttachFmtType,
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
			Organizer:     r.Organizer == 1,
			CUType:        r.Cutype,
			RSVPRequested: strings.EqualFold(r.Rsvp, "TRUE"),
			SentBy:        r.SentBy,
			DelegatedTo:   r.DelegatedTo,
			DelegatedFrom: r.DelegatedFrom,
			Member:        r.Member,
			Dir:           r.Dir,
			Language:      r.Language,
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
		rsvp := ""
		if a.RSVPRequested {
			rsvp = "TRUE"
		}
		_, err := qtx.CreateTodoAttendee(ctx, storage.CreateTodoAttendeeParams{
			TodoID:        todoID,
			Email:         a.Email,
			Name:          a.Name,
			RsvpStatus:    a.RSVPStatus,
			Role:          a.Role,
			Organizer:     org,
			Cutype:        a.CUType,
			Rsvp:          rsvp,
			SentBy:        a.SentBy,
			DelegatedTo:   a.DelegatedTo,
			DelegatedFrom: a.DelegatedFrom,
			Member:        a.Member,
			Dir:           a.Dir,
			Language:      a.Language,
		})
		if err != nil {
			return fmt.Errorf("create attendee: %w", err)
		}
	}
	return tx.Commit()
}

// Category CRUD

func (s *Service) ListCategories(ctx context.Context, todoID int64) ([]string, error) {
	rows, err := s.q.ListCategoriesByTodoID(ctx, todoID)
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
	return s.q.ListAllTodoCategories(ctx)
}

func (s *Service) ReplaceCategories(ctx context.Context, todoID int64, categories []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteCategoriesByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete categories: %w", err)
	}
	for _, c := range categories {
		_, err := qtx.CreateTodoCategory(ctx, storage.CreateTodoCategoryParams{
			TodoID:   todoID,
			Category: c,
		})
		if err != nil {
			return fmt.Errorf("create category: %w", err)
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
		RecurrenceRule:  r.RecurrenceRule,
		Timezone:        r.Timezone,
		Sequence:        r.Sequence,
		ExDates:         r.Exdates,
		RDates:          r.Rdates,
		RecurrenceID:    r.RecurrenceID,
		Geo:             r.Geo,
		DtStamp:         r.Dtstamp,
		CreatedAt:       parseTime(r.CreatedAt),
		UpdatedAt:       parseTime(r.UpdatedAt),
	}
}

func (s *Service) populateSingleCategories(ctx context.Context, t *Todo) {
	rows, err := s.q.ListCategoriesByTodoID(ctx, t.ID)
	if err != nil {
		return
	}
	cats := make([]string, len(rows))
	for j, r := range rows {
		cats[j] = r.Category
	}
	t.Categories = strings.Join(cats, ",")
}

func (s *Service) populateCategories(ctx context.Context, todos []Todo) {
	for i := range todos {
		rows, err := s.q.ListCategoriesByTodoID(ctx, todos[i].ID)
		if err != nil {
			continue
		}
		cats := make([]string, len(rows))
		for j, r := range rows {
			cats[j] = r.Category
		}
		todos[i].Categories = strings.Join(cats, ",")
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
