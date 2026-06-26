package todo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
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
	// tx is non-nil when the service runs inside a caller-managed
	// transaction (see WithTx). When set, q is already bound to tx and the
	// per-method write helpers join the outer transaction instead of opening
	// their own, so a multi-step sequence commits or rolls back atomically.
	tx *sql.Tx
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
}

// WithTx returns a copy of the service whose writes run inside tx. The caller
// owns tx (commit/rollback); the returned service's mutating methods neither
// begin nor commit their own transaction, letting several calls compose into a
// single atomic unit.
func (s *Service) WithTx(tx *sql.Tx) *Service {
	return &Service{db: s.db, q: s.q.WithTx(tx), tx: tx}
}

// txscope returns a transaction-scoped Queries plus commit and rollback
// helpers. When the service already runs inside a caller-managed transaction
// (see WithTx), the work joins that transaction: commit is a no-op and rollback
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

// dirtyExec returns the DBTX the dirty-marking side effect must use: the outer
// transaction when one is active (so the write joins it and cannot deadlock
// against the held write lock), otherwise the pooled *sql.DB.
func (s *Service) dirtyExec() storage.DBTX {
	if s.tx != nil {
		return s.tx
	}
	return s.db
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

const (
	defaultStatus = "NEEDS-ACTION"
	defaultClass  = "PUBLIC"
	alarmAction   = "DISPLAY"
	alarmRelated  = "START"
)

var ErrInvalidTiming = errors.New("invalid todo timing")

func defaults(status, class string) (string, string) {
	if status == "" {
		status = defaultStatus
	}
	if class == "" {
		class = defaultClass
	}
	return status, class
}

// completedAtFor reconciles the completed_at timestamp with the status: a
// COMPLETED todo gets a timestamp (preserving any existing one, else now),
// and any other status clears it so reopened todos don't keep a stale value.
func completedAtFor(status, completedAt string) string {
	if status != "COMPLETED" {
		return ""
	}
	if completedAt == "" {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return completedAt
}

func validateTiming(dueDate, startDate, dur string) error {
	if dur == "" {
		return nil
	}
	if startDate == "" {
		return fmt.Errorf("%w: duration requires start date", ErrInvalidTiming)
	}
	if dueDate != "" {
		return fmt.Errorf("%w: due date and duration are mutually exclusive", ErrInvalidTiming)
	}
	return nil
}

func (p *CreateParams) applyDefaults() {
	p.Status, p.Class = defaults(p.Status, p.Class)
	if p.Status == "COMPLETED" {
		p.PercentComplete = 100
	}
}

func (p *UpsertParams) applyDefaults() {
	p.Status, p.Class = defaults(p.Status, p.Class)
	p.CompletedAt = completedAtFor(p.Status, p.CompletedAt)
	p.PercentComplete = percentCompleteFor(p.Status, p.PercentComplete)
}

// percentCompleteFor reconciles percent-complete with the status: a COMPLETED
// todo is forced to 100, and a stale 100 left over from completion is reset to
// 0 when the todo is reopened to a non-completed status.
func percentCompleteFor(status string, percent int64) int64 {
	if status == "COMPLETED" {
		return 100
	}
	if percent == 100 {
		return 0
	}
	return percent
}

func (s *Service) Search(ctx context.Context, p SearchParams) ([]Todo, error) {
	ftsQuery := storage.FTSQuery(p.Query)
	if ftsQuery == "" {
		return nil, nil
	}
	rows, err := s.q.SearchTodosFTS(ctx, ftsQuery, p.CalendarID, p.Status, int64(p.Completed))
	if err != nil {
		return nil, fmt.Errorf("search todos: %w", err)
	}
	todos := fromStorageSlice(rows)
	s.populateCategories(ctx, todos)
	return todos, nil
}

func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Todo, error) {
	rows, err := s.q.ListTodosForExport(ctx, storage.ListTodosForExportParams{
		CalendarID:      p.CalendarID,
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
	fromStr := from.Format("2006-01-02")
	toStr := to.Format("2006-01-02")
	rows, err := s.q.ListTodosByDueDateRange(ctx, storage.ListTodosByDueDateRangeParams{
		DueDate:   &fromStr,
		DueDate_2: &toStr,
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

// markDirtyByID looks up a todo by ID and marks its sync resource as dirty.
func (s *Service) markDirtyByID(ctx context.Context, todoID int64) {
	r, err := s.q.GetTodo(ctx, todoID)
	if err != nil {
		return
	}
	_ = storage.MarkResourceDirty(ctx, s.dirtyExec(), r.CalendarID, r.Uid, "todo")
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Todo, error) {
	p.applyDefaults()
	if err := validateTiming(p.DueDate, p.StartDate, p.Duration); err != nil {
		return Todo{}, err
	}
	completedAt := ""
	if p.Status == "COMPLETED" {
		completedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r, err := s.q.CreateTodo(ctx, storage.CreateTodoParams{
		Uid:             uuid.New().String(),
		CalendarID:      p.CalendarID,
		Summary:         p.Summary,
		Description:     storage.StringToNullable(p.Description),
		Location:        storage.StringToNullable(p.Location),
		DueDate:         storage.StringToNullable(p.DueDate),
		StartDate:       storage.StringToNullable(p.StartDate),
		Duration:        storage.StringToNullable(p.Duration),
		CompletedAt:     storage.StringToNullable(completedAt),
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             storage.StringToNullable(p.URL),
		RecurrenceRule:  storage.StringToNullable(p.RecurrenceRule),
		Timezone:        storage.StringToNullable(p.Timezone),
		Sequence:        p.Sequence,
		Exdates:         storage.StringToNullable(p.ExDates),
		Rdates:          storage.StringToNullable(p.RDates),
		RecurrenceID:    p.RecurrenceID,
		Geo:             storage.StringToNullable(p.Geo),
		Dtstamp:         storage.StringToNullable(p.DtStamp),
	})
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	if err := s.ReplaceCategories(ctx, t.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	t.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.db, t.CalendarID, t.UID, "todo")
	return t, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Todo, error) {
	p.Status, p.Class = defaults(p.Status, p.Class)
	p.CompletedAt = completedAtFor(p.Status, p.CompletedAt)
	p.PercentComplete = percentCompleteFor(p.Status, p.PercentComplete)
	if err := validateTiming(p.DueDate, p.StartDate, p.Duration); err != nil {
		return Todo{}, err
	}
	r, err := s.q.UpdateTodo(ctx, storage.UpdateTodoParams{
		ID:              id,
		Summary:         p.Summary,
		Description:     storage.StringToNullable(p.Description),
		Location:        storage.StringToNullable(p.Location),
		DueDate:         storage.StringToNullable(p.DueDate),
		StartDate:       storage.StringToNullable(p.StartDate),
		Duration:        storage.StringToNullable(p.Duration),
		CompletedAt:     storage.StringToNullable(p.CompletedAt),
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		CalendarID:      p.CalendarID,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             storage.StringToNullable(p.URL),
		RecurrenceRule:  storage.StringToNullable(p.RecurrenceRule),
		Timezone:        storage.StringToNullable(p.Timezone),
		Exdates:         storage.StringToNullable(p.ExDates),
		Rdates:          storage.StringToNullable(p.RDates),
		Geo:             storage.StringToNullable(p.Geo),
		Dtstamp:         storage.StringToNullable(p.DtStamp),
	})
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	if err := s.ReplaceCategories(ctx, t.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	t.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.db, t.CalendarID, t.UID, "todo")
	return t, nil
}

func (s *Service) Complete(ctx context.Context, id int64) (Todo, error) {
	r, err := s.q.CompleteTodo(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	_ = storage.MarkResourceDirty(ctx, s.db, t.CalendarID, t.UID, "todo")
	return t, nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Todo, error) {
	p.applyDefaults()
	if err := validateTiming(p.DueDate, p.StartDate, p.Duration); err != nil {
		return Todo{}, err
	}
	r, err := s.q.UpsertTodoByUID(ctx, storage.UpsertTodoByUIDParams{
		Uid:             p.UID,
		CalendarID:      p.CalendarID,
		Summary:         p.Summary,
		Description:     storage.StringToNullable(p.Description),
		Location:        storage.StringToNullable(p.Location),
		DueDate:         storage.StringToNullable(p.DueDate),
		StartDate:       storage.StringToNullable(p.StartDate),
		Duration:        storage.StringToNullable(p.Duration),
		CompletedAt:     storage.StringToNullable(p.CompletedAt),
		PercentComplete: p.PercentComplete,
		Status:          p.Status,
		Priority:        p.Priority,
		Class:           p.Class,
		Url:             storage.StringToNullable(p.URL),
		RecurrenceRule:  storage.StringToNullable(p.RecurrenceRule),
		Timezone:        storage.StringToNullable(p.Timezone),
		Sequence:        p.Sequence,
		Exdates:         storage.StringToNullable(p.ExDates),
		Rdates:          storage.StringToNullable(p.RDates),
		RecurrenceID:    p.RecurrenceID,
		Geo:             storage.StringToNullable(p.Geo),
		Dtstamp:         storage.StringToNullable(p.DtStamp),
	})
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	if err := s.ReplaceCategories(ctx, t.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	t.Categories = p.Categories
	return t, nil
}

// ErrHasOverrides is returned when attempting to delete a recurring master
// todo that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("todo has overrides: use DeleteSeries to delete the entire series")

// Delete soft-deletes a todo by ID. For a standalone todo it flips
// deleted_at; for an override it adds EXDATE to the master and soft-
// deletes the override in the same transaction so undo can reverse both
// sides. A recurring master with live overrides is rejected — callers
// must use DeleteSeries.
func (s *Service) Delete(ctx context.Context, id int64) error {
	td, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if td.RecurrenceRule != "" && td.RecurrenceID == "" {
		overrides, err := s.q.ListTodoOverridesByUID(ctx, td.UID)
		if err != nil {
			return fmt.Errorf("check overrides: %w", err)
		}
		if len(overrides) > 0 {
			return ErrHasOverrides
		}
	}

	if td.RecurrenceID == "" {
		// Tombstone + soft-delete commit together so a failed tombstone write
		// can't leave a soft-deleted row whose next sync DELETEs a still-live
		// server resource (issue #107).
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, td.CalendarID, td.UID); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
		if err := qtx.SoftDeleteTodo(ctx, id); err != nil {
			return fmt.Errorf("soft-delete todo: %w", err)
		}
		if err := storage.MarkResourceDirty(ctx, tx, td.CalendarID, td.UID, "todo"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	if td.RecurrenceID != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)

		master, err := qtx.GetTodoByUID(ctx, td.UID)
		if err == nil {
			existing := timeutil.ParseTimeList(storage.NullableToString(master.Exdates))
			recIDTime, parseErr := timeutil.ParseRecurrenceID(td.RecurrenceID)
			if parseErr != nil {
				// A malformed recurrence_id can't be excluded from the
				// master, so soft-deleting the override would resurrect the
				// occurrence via series expansion. Fail loudly instead — the
				// restore path treats the same parse failure as fatal.
				return fmt.Errorf("parse recurrence_id %q: %w", td.RecurrenceID, parseErr)
			}
			existing = append(existing, recIDTime)
			if err := qtx.UpdateTodoExdates(ctx, storage.UpdateTodoExdatesParams{
				Exdates: storage.StringToNullable(timeutil.SerializeTimeList(existing)),
				ID:      master.ID,
			}); err != nil {
				return fmt.Errorf("update exdates: %w", err)
			}
			// Record provenance so restore knows this EXDATE was
			// delete-added (and may be stripped) rather than imported.
			if err := qtx.RecordTodoExdateDelete(ctx, storage.RecordTodoExdateDeleteParams{
				CalendarID:   master.CalendarID,
				Uid:          td.UID,
				RecurrenceID: td.RecurrenceID,
			}); err != nil {
				return fmt.Errorf("record exdate delete: %w", err)
			}
		}

		if err := qtx.SoftDeleteTodo(ctx, id); err != nil {
			return fmt.Errorf("soft-delete todo: %w", err)
		}
		// Mark the master dirty — its EXDATE was modified — inside the same
		// transaction so a failed mark rolls the EXDATE change back rather than
		// committing a change that is never pushed (issue #107).
		if err := storage.MarkResourceDirty(ctx, tx, td.CalendarID, td.UID, "todo"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	// Unreachable: RecurrenceID is either "" (handled above) or non-empty.
	if err := s.q.SoftDeleteTodo(ctx, id); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, td.CalendarID, td.UID, "todo")
	return nil
}

// DeleteSeries soft-deletes a recurring master todo and every override
// sharing its UID. A tombstone is created when the master is synced so
// the next push sends DELETE to the server; the local rows stay in
// place until purge so the user can restore them.
func (s *Service) DeleteSeries(ctx context.Context, uid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Tombstone, dirty-mark, and soft-delete commit together so a failed
	// sync-tracking write can't leave a tombstone for a still-live series
	// whose next sync would DELETE it from the server (issue #107). A missing
	// master means there is nothing to track.
	master, mErr := qtx.GetTodoByUID(ctx, uid)
	haveMaster := mErr == nil
	if haveMaster {
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, master.CalendarID, uid); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
	}

	if err := qtx.SoftDeleteTodosByUID(ctx, uid); err != nil {
		return fmt.Errorf("soft-delete series: %w", err)
	}
	if haveMaster {
		if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "todo"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
	}
	return tx.Commit()
}

// ListOverridesByUID returns all override instances for a given UID.
func (s *Service) ListOverridesByUID(ctx context.Context, uid string) ([]Todo, error) {
	rows, err := s.q.ListTodoOverridesByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

// Alarm CRUD

func (s *Service) ListAlarms(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	return s.listAlarms(ctx, todoID, true)
}

// ListAlarmsLean returns a todo's alarms with attendees (needed to fire
// EMAIL alarms) but without X-properties, which are round-trip-only and
// never read at fire time. The alarm check loop calls this per todo every
// tick, so it skips the per-todo x_properties query that export/sync need.
func (s *Service) ListAlarmsLean(ctx context.Context, todoID int64) ([]model.Alarm, error) {
	return s.listAlarms(ctx, todoID, false)
}

func (s *Service) listAlarms(ctx context.Context, todoID int64, withXProps bool) ([]model.Alarm, error) {
	rows, err := s.q.ListTodoAlarmsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	alarmIDs := make([]int64, len(rows))
	for i, r := range rows {
		alarmIDs[i] = r.ID
	}
	// Load failures propagate: attendees feed content matching in
	// ReplaceAlarms and X-properties feed export/sync pushes, so a silently
	// degraded alarm set would corrupt merges or rewrite the server copy.
	attRows, err := s.q.ListTodoAlarmAttendeesByAlarmIDs(ctx, alarmIDs)
	if err != nil {
		return nil, fmt.Errorf("load alarm attendees: %w", err)
	}
	attMap := make(map[int64][]model.AlarmAttendee, len(rows))
	for _, ar := range attRows {
		attMap[ar.AlarmID] = append(attMap[ar.AlarmID], model.AlarmAttendee{
			ID: ar.ID, Email: ar.Email, Name: storage.NullableToString(ar.Name),
		})
	}
	alarms := make([]model.Alarm, len(rows))
	for i, r := range rows {
		alarms[i] = fromStorageTodoAlarm(r)
		alarms[i].Attendees = attMap[r.ID]
	}
	if withXProps {
		if err := storage.AttachAlarmXProperties(ctx, s.q, storage.OwnerTypeTodoAlarm, alarms); err != nil {
			return nil, err
		}
	}
	return alarms, nil
}

// fromStorageTodoAlarm maps a todo_alarms row to the shared alarm model.
func fromStorageTodoAlarm(r storage.TodoAlarm) model.Alarm {
	return model.Alarm{
		ID: r.ID, EventID: r.TodoID,
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

func (s *Service) ReplaceAlarms(ctx context.Context, todoID int64, alarms []model.Alarm) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	// Merge instead of wipe-and-recreate: deleting a todo alarm row cascades
	// away its todo_alarm_state, which would resurrect dismissed firings on
	// every sync rewrite. Mirrors event.Service.ReplaceAlarms.
	existing, err := loadExistingTodoAlarms(ctx, qtx, todoID)
	if err != nil {
		return err
	}

	// Copy before applying defaults — the caller's slice must not be mutated.
	alarms = append([]model.Alarm(nil), alarms...)
	for i := range alarms {
		if alarms[i].Action == "" {
			alarms[i].Action = alarmAction
		}
		if alarms[i].Related == "" {
			alarms[i].Related = alarmRelated
		}
	}

	matched := make([]bool, len(existing))
	var unmatched []model.Alarm
	for _, a := range alarms {
		if j, found := matchTodoAlarm(existing, matched, a); found {
			matched[j] = true
			if err := syncMatchedTodoAlarm(ctx, qtx, a, existing[j]); err != nil {
				return err
			}
		} else {
			unmatched = append(unmatched, a)
		}
	}

	// Second pass: content changed but the RFC 9074 UID is stable — the same
	// alarm edited. Update in place so the row ID (and its state) survives.
	for _, a := range unmatched {
		if j, found := matchTodoAlarmByUID(existing, matched, a); found {
			matched[j] = true
			if err := updateTodoAlarmInPlace(ctx, qtx, todoID, a, existing[j]); err != nil {
				return err
			}
		} else {
			if err := createNewTodoAlarm(ctx, qtx, todoID, a); err != nil {
				return err
			}
		}
	}

	for j, ex := range existing {
		if !matched[j] {
			if err := qtx.DeleteTodoAlarmByID(ctx, ex.ID); err != nil {
				return fmt.Errorf("delete unmatched alarm: %w", err)
			}
		}
	}

	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// loadExistingTodoAlarms loads a todo's alarms with attendees and
// X-properties inside the transaction for merge matching.
func loadExistingTodoAlarms(ctx context.Context, qtx *storage.Queries, todoID int64) ([]model.Alarm, error) {
	rows, err := qtx.ListTodoAlarmsByTodoID(ctx, todoID)
	if err != nil {
		return nil, fmt.Errorf("list existing alarms: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	alarmIDs := make([]int64, len(rows))
	for i, r := range rows {
		alarmIDs[i] = r.ID
	}
	attRows, err := qtx.ListTodoAlarmAttendeesByAlarmIDs(ctx, alarmIDs)
	if err != nil {
		return nil, fmt.Errorf("list alarm attendees: %w", err)
	}
	attMap := make(map[int64][]model.AlarmAttendee, len(rows))
	for _, ar := range attRows {
		attMap[ar.AlarmID] = append(attMap[ar.AlarmID], model.AlarmAttendee{
			ID: ar.ID, Email: ar.Email, Name: storage.NullableToString(ar.Name),
		})
	}
	alarms := make([]model.Alarm, len(rows))
	for i, r := range rows {
		alarms[i] = fromStorageTodoAlarm(r)
		alarms[i].Attendees = attMap[r.ID]
	}
	if err := storage.AttachAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, alarms); err != nil {
		return nil, err
	}
	return alarms, nil
}

// matchTodoAlarm tries to match an incoming alarm with existing ones by
// content. Rows whose non-empty RFC 9074 UIDs differ are never paired —
// see event.matchAlarm for the rationale.
func matchTodoAlarm(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
	for j, ex := range existing {
		if matched[j] {
			continue
		}
		if a.UID != "" && ex.UID != "" && a.UID != ex.UID {
			continue
		}
		if a.ContentEqual(ex) {
			return j, true
		}
	}
	return 0, false
}

// matchTodoAlarmByUID matches an incoming alarm against unmatched existing
// ones by RFC 9074 UID.
func matchTodoAlarmByUID(existing []model.Alarm, matched []bool, a model.Alarm) (int, bool) {
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

// syncMatchedTodoAlarm syncs a content-matched alarm's UID and ACKNOWLEDGED state.
func syncMatchedTodoAlarm(ctx context.Context, qtx *storage.Queries, a model.Alarm, ex model.Alarm) error {
	if ex.UID == "" {
		uid := a.UID
		if uid == "" {
			uid = uuid.New().String()
		}
		if err := qtx.UpdateTodoAlarmUID(ctx, storage.UpdateTodoAlarmUIDParams{
			Uid: storage.StringToNullable(uid),
			ID:  ex.ID,
		}); err != nil {
			return fmt.Errorf("backfill alarm uid: %w", err)
		}
	}
	if a.Acknowledged != ex.Acknowledged && model.ValidateAcknowledged(a.Acknowledged) {
		if err := qtx.UpdateTodoAlarmAcknowledged(ctx, storage.UpdateTodoAlarmAcknowledgedParams{
			Acknowledged: storage.StringToNullable(a.Acknowledged),
			ID:           ex.ID,
		}); err != nil {
			return fmt.Errorf("update alarm acknowledged: %w", err)
		}
	}
	// X-properties are excluded from content matching; refresh them so a
	// remote X-prop change still lands. nil means the caller has no X-prop
	// knowledge — keep the stored rows; only a non-nil slice is authoritative.
	if a.XProperties == nil || model.XPropsContentEqual(a.XProperties, ex.XProperties) {
		return nil
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, ex.ID, a.XProperties)
}

// updateTodoAlarmInPlace rewrites a UID-matched alarm's content on its
// existing row, preserving the row ID so todo_alarm_state entries survive.
func updateTodoAlarmInPlace(ctx context.Context, qtx *storage.Queries, todoID int64, a model.Alarm, ex model.Alarm) error {
	// Same ACKNOWLEDGED policy as syncMatchedTodoAlarm: a malformed
	// incoming value must not clobber valid stored state.
	ack := a.Acknowledged
	if !model.ValidateAcknowledged(ack) {
		ack = ex.Acknowledged
	}
	if err := qtx.UpdateTodoAlarmContentByID(ctx, storage.UpdateTodoAlarmContentByIDParams{
		Action:        a.Action,
		TriggerValue:  a.TriggerValue,
		Description:   storage.StringToNullable(a.Description),
		Summary:       storage.StringToNullable(a.Summary),
		Repeat:        int64(a.Repeat),
		Duration:      storage.StringToNullable(a.Duration),
		Related:       a.Related,
		Acknowledged:  storage.StringToNullable(ack),
		AttachUri:     storage.StringToNullable(a.AttachURI),
		AttachFmttype: storage.StringToNullable(a.AttachFmtType),
		ID:            ex.ID,
		TodoID:        todoID,
	}); err != nil {
		return fmt.Errorf("update alarm content: %w", err)
	}
	if err := qtx.DeleteTodoAlarmAttendeesByAlarmID(ctx, ex.ID); err != nil {
		return fmt.Errorf("delete alarm attendees: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateTodoAlarmAttendee(ctx, storage.CreateTodoAlarmAttendeeParams{
			AlarmID: ex.ID,
			Email:   att.Email,
			Name:    storage.StringToNullable(att.Name),
		})
		if err != nil {
			return fmt.Errorf("create alarm attendee: %w", err)
		}
	}
	if a.XProperties == nil || model.XPropsContentEqual(a.XProperties, ex.XProperties) {
		return nil
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, ex.ID, a.XProperties)
}

// isUniqueUIDViolation reports whether an insert failed on the global
// alarm-UID unique index.
func isUniqueUIDViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") && strings.Contains(msg, ".uid")
}

// createNewTodoAlarm creates a new todo alarm and its attendees. On a global
// UID collision (e.g. a server duplicating a todo with its VALARM UIDs),
// mint a fresh local UID instead of failing the sync forever.
func createNewTodoAlarm(ctx context.Context, qtx *storage.Queries, todoID int64, a model.Alarm) error {
	uid := a.UID
	if uid == "" {
		uid = uuid.New().String()
	}
	params := storage.CreateTodoAlarmParams{
		TodoID:        todoID,
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
	}
	row, err := qtx.CreateTodoAlarm(ctx, params)
	if isUniqueUIDViolation(err) {
		params.Uid = storage.StringToNullable(uuid.New().String())
		row, err = qtx.CreateTodoAlarm(ctx, params)
	}
	if err != nil {
		return fmt.Errorf("create alarm: %w", err)
	}
	for _, att := range a.Attendees {
		_, err := qtx.CreateTodoAlarmAttendee(ctx, storage.CreateTodoAlarmAttendeeParams{
			AlarmID: row.ID,
			Email:   att.Email,
			Name:    storage.StringToNullable(att.Name),
		})
		if err != nil {
			return fmt.Errorf("create alarm attendee: %w", err)
		}
	}
	if len(a.XProperties) == 0 {
		return nil
	}
	return storage.ReplaceAlarmXProperties(ctx, qtx, storage.OwnerTypeTodoAlarm, row.ID, a.XProperties)
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
			Email: r.Email, Name: storage.NullableToString(r.Name),
			RSVPStatus: r.RsvpStatus, Role: r.Role,
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
	return attendees, nil
}

func (s *Service) ReplaceAttendees(ctx context.Context, todoID int64, attendees []model.Attendee) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := qtx.DeleteTodoAttendeesByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete attendees: %w", err)
	}
	for _, a := range attendees {
		org := storage.BoolToInt(a.Organizer)
		rsvp := ""
		if a.RSVPRequested {
			rsvp = "TRUE"
		}
		_, err := qtx.CreateTodoAttendee(ctx, storage.CreateTodoAttendeeParams{
			TodoID:        todoID,
			Email:         a.Email,
			Name:          storage.StringToNullable(a.Name),
			RsvpStatus:    a.RSVPStatus,
			Role:          a.Role,
			Organizer:     org,
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
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
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
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

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
	if err := commit(); err != nil {
		return fmt.Errorf("commit replace categories: %w", err)
	}
	return nil
}

// Attachment CRUD

func (s *Service) ListAttachments(ctx context.Context, todoID int64) ([]model.Attachment, error) {
	rows, err := s.q.ListTodoAttachmentsByTodoID(ctx, todoID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, len(rows))
	for i, r := range rows {
		out[i] = model.Attachment{ID: r.ID, URI: storage.NullableToString(r.Uri), FmtType: storage.NullableToString(r.Fmttype), Data: r.Data, Filename: storage.NullableToString(r.Filename)}
	}
	return out, nil
}

func (s *Service) ReplaceAttachments(ctx context.Context, todoID int64, attachments []model.Attachment) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteTodoAttachmentsByTodoID(ctx, todoID); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	for _, a := range attachments {
		_, err := qtx.CreateTodoAttachment(ctx, storage.CreateTodoAttachmentParams{
			TodoID: todoID, Uri: storage.StringToNullable(a.URI), Fmttype: storage.StringToNullable(a.FmtType), Data: a.Data, Filename: storage.StringToNullable(a.Filename),
		})
		if err != nil {
			return fmt.Errorf("create attachment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
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
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
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
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
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
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
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
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
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
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
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
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
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
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
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
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

// Converters

func fromStorage(r storage.Todo) Todo {
	var deletedAt *time.Time
	if r.DeletedAt != nil && *r.DeletedAt != "" {
		t := timeutil.ParseDateTime(*r.DeletedAt)
		deletedAt = &t
	}
	return Todo{
		ID:              r.ID,
		UID:             r.Uid,
		CalendarID:      r.CalendarID,
		Summary:         r.Summary,
		Description:     storage.NullableToString(r.Description),
		Location:        storage.NullableToString(r.Location),
		DueDate:         storage.NullableToString(r.DueDate),
		StartDate:       storage.NullableToString(r.StartDate),
		Duration:        storage.NullableToString(r.Duration),
		CompletedAt:     storage.NullableToString(r.CompletedAt),
		PercentComplete: r.PercentComplete,
		Status:          r.Status,
		Priority:        r.Priority,
		Class:           r.Class,
		URL:             storage.NullableToString(r.Url),
		RecurrenceRule:  storage.NullableToString(r.RecurrenceRule),
		Timezone:        storage.NullableToString(r.Timezone),
		Sequence:        r.Sequence,
		ExDates:         storage.NullableToString(r.Exdates),
		RDates:          storage.NullableToString(r.Rdates),
		RecurrenceID:    r.RecurrenceID,
		Geo:             storage.NullableToString(r.Geo),
		DtStamp:         storage.NullableToString(r.Dtstamp),
		CreatedAt:       timeutil.ParseDateTime(r.CreatedAt),
		UpdatedAt:       timeutil.ParseDateTime(r.UpdatedAt),
		DeletedAt:       deletedAt,
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
	t.Categories = timeutil.JoinCategoryList(cats)
}

func (s *Service) populateCategories(ctx context.Context, todos []Todo) {
	if len(todos) == 0 {
		return
	}
	ids := make([]int64, len(todos))
	for i := range todos {
		ids[i] = todos[i].ID
	}
	rows, err := s.q.ListCategoriesByTodoIDs(ctx, ids)
	if err != nil {
		return
	}
	catMap := make(map[int64][]string, len(todos))
	for _, r := range rows {
		catMap[r.TodoID] = append(catMap[r.TodoID], r.Category)
	}
	for i := range todos {
		if cats, ok := catMap[todos[i].ID]; ok {
			todos[i].Categories = timeutil.JoinCategoryList(cats)
		}
	}
}

// X-Property CRUD

func (s *Service) ListXProperties(ctx context.Context, todoID int64) ([]model.XProperty, error) {
	rows, err := s.q.ListXPropertiesByOwner(ctx, storage.ListXPropertiesByOwnerParams{
		OwnerType: "todo", OwnerID: todoID,
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

func (s *Service) ReplaceXProperties(ctx context.Context, todoID int64, xprops []model.XProperty) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteXPropertiesByOwner(ctx, storage.DeleteXPropertiesByOwnerParams{
		OwnerType: "todo", OwnerID: todoID,
	}); err != nil {
		return fmt.Errorf("delete x-properties: %w", err)
	}
	for _, xp := range xprops {
		if err := qtx.InsertXProperty(ctx, storage.InsertXPropertyParams{
			OwnerType: "todo", OwnerID: todoID,
			Name: xp.Name, Value: xp.Value, Params: xp.Params,
		}); err != nil {
			return fmt.Errorf("insert x-property: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, todoID)
	return nil
}

func fromStorageSlice(rows []storage.Todo) []Todo {
	todos := make([]Todo, len(rows))
	for i, r := range rows {
		todos[i] = fromStorage(r)
	}
	return todos
}
