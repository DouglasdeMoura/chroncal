package todo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/duration"
	"github.com/douglasdemoura/chroncal/internal/hydrate"
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
	From       string // date-only ("YYYY-MM-DD") or empty
	To         string // date-only ("YYYY-MM-DD") or empty
	Category   string // empty = all
	Status     string // empty = all
	Completed  int    // 0 = all, 1 = completed, 2 = incomplete
}

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
// owns tx (commit/rollback). The returned service's methods that mutate
// neither begin nor commit their own transaction. Several calls can then
// compose into a single atomic unit.
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
	todoComponent = "VTODO"
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

// completedAtFor reconciles the completed_at timestamp with the status.
// A COMPLETED todo gets a timestamp. The function keeps a timestamp that
// already exists, or uses now. Any other status clears it. A reopened todo
// then does not keep a stale value.
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
	// The span rule closes every CLI and TUI write path at once (issue
	// #582 round 3). The import path screens the same three rules and
	// drops the DURATION with a warning, so a server cannot fail a
	// sync pull here (issue #582 round 5).
	if err := duration.ValidateSpan(dur); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTiming, err)
	}
	if startDate == "" {
		return fmt.Errorf("%w: duration requires start date", ErrInvalidTiming)
	}
	if dueDate != "" {
		return fmt.Errorf("%w: due date and duration are mutually exclusive", ErrInvalidTiming)
	}
	// The span must also land on a time the database can hold. See
	// timeutil.Storable.
	if start := timeutil.ParseDate(startDate); !start.IsZero() {
		if end := duration.Add(start, dur); !timeutil.Storable(end) {
			return fmt.Errorf("%w: the end time is past year %d", ErrInvalidTiming, timeutil.MaxStorableYear)
		}
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

// percentCompleteFor reconciles percent-complete with the status. A COMPLETED
// todo is forced to 100. A stale 100 left over from completion is reset to
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
	rows, err := s.q.ListTodosForExport(ctx, storage.TodoFilterParams{
		CalendarID:      p.CalendarID,
		FromDate:        p.From,
		ToDate:          p.To,
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

// todoCalendarIDsByUID returns the distinct calendar IDs of every row with
// uid, live or soft-deleted. A series-wide guard needs this so orphaned
// overrides and series-tail rows stay protected after the master is purged
// on its own. GetTodoByUIDIncludingDeleted returns no row then. A
// master-only lookup would skip the guard.
//
// This is a read-only guard helper written as raw SQL rather than a sqlc
// query. It stays local to the todo domain. A storage regenerate would
// churn shared generated files.
func (s *Service) todoCalendarIDsByUID(ctx context.Context, uid string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT calendar_id FROM todos WHERE uid = ?`, uid)
	if err != nil {
		return nil, fmt.Errorf("list todo calendar ids: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan todo calendar id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("todo calendar ids: %w", err)
	}
	return ids, nil
}

// ensureSeriesWritable guards every calendar a UID spans before a series-wide
// mutation (DeleteSeries, RestoreByUID). Distinct calendar IDs across all
// rows with the UID, not just the master, cover overrides and series-tail
// rows when the master row has been purged.
func (s *Service) ensureSeriesWritable(ctx context.Context, uid string) error {
	ids, err := s.todoCalendarIDsByUID(ctx, uid)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := calendaraccess.EnsureWritable(ctx, s.q, id, todoComponent); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Todo, error) {
	if err := calendaraccess.EnsureWritable(ctx, s.q, p.CalendarID, todoComponent); err != nil {
		return Todo{}, err
	}
	p.applyDefaults()
	if err := validateTiming(p.DueDate, p.StartDate, p.Duration); err != nil {
		return Todo{}, err
	}
	completedAt := ""
	if p.Status == "COMPLETED" {
		completedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// One transaction wraps the todo row and its categories. A category write
	// that fails then rolls the row back. The path does not commit an orphan
	// whose MarkResourceDirty never ran (issue #222, mirror of event issue #73).
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return Todo{}, err
	}
	defer rollback()

	r, err := qtx.CreateTodo(ctx, storage.CreateTodoParams{
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
	if cats := timeutil.ParseCategoryList(p.Categories); len(cats) > 0 {
		// A fresh row has no categories yet, so the DELETE in
		// replaceCategoriesTx is pure overhead when there's nothing to add.
		if err := replaceCategoriesTx(ctx, qtx, t.ID, cats); err != nil {
			return Todo{}, fmt.Errorf("replace categories: %w", err)
		}
	}
	if err := commit(); err != nil {
		return Todo{}, fmt.Errorf("commit create todo: %w", err)
	}
	t.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.dirtyExec(), t.CalendarID, t.UID, "todo")
	return t, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Todo, error) {
	existing, err := s.q.GetTodo(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	// Reject writes the linked remote collection cannot accept before any
	// side effect. Guard the source calendar (where the todo lives). On a
	// move, guard the destination calendar too.
	if err := calendaraccess.EnsureWritable(ctx, s.q, existing.CalendarID, todoComponent); err != nil {
		return Todo{}, err
	}
	if p.CalendarID != existing.CalendarID {
		if err := calendaraccess.EnsureWritable(ctx, s.q, p.CalendarID, todoComponent); err != nil {
			return Todo{}, err
		}
	}
	p.Status, p.Class = defaults(p.Status, p.Class)
	p.CompletedAt = completedAtFor(p.Status, p.CompletedAt)
	p.PercentComplete = percentCompleteFor(p.Status, p.PercentComplete)
	if err := validateTiming(p.DueDate, p.StartDate, p.Duration); err != nil {
		return Todo{}, err
	}

	// One transaction wraps the todo row and its categories. A category write
	// that fails then rolls the row update back. The path does not commit a
	// half-updated row whose MarkResourceDirty never ran (issue #222).
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return Todo{}, err
	}
	defer rollback()

	r, err := qtx.UpdateTodo(ctx, storage.UpdateTodoParams{
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
	if err := replaceCategoriesTx(ctx, qtx, t.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	if err := commit(); err != nil {
		return Todo{}, fmt.Errorf("commit update todo: %w", err)
	}
	t.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.dirtyExec(), t.CalendarID, t.UID, "todo")
	return t, nil
}

func (s *Service) Complete(ctx context.Context, id int64) (Todo, error) {
	existing, err := s.q.GetTodo(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	if err := calendaraccess.EnsureWritable(ctx, s.q, existing.CalendarID, todoComponent); err != nil {
		return Todo{}, err
	}
	r, err := s.q.CompleteTodo(ctx, id)
	if err != nil {
		return Todo{}, err
	}
	t := fromStorage(r)
	_ = storage.MarkResourceDirty(ctx, s.dirtyExec(), t.CalendarID, t.UID, "todo")
	return t, nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Todo, error) {
	p.applyDefaults()
	if err := validateTiming(p.DueDate, p.StartDate, p.Duration); err != nil {
		return Todo{}, err
	}

	// One transaction wraps the todo row and its categories. A category write
	// that fails then rolls the upsert back. The path does not leave an orphan
	// row (issue #222). txscope joins the sync engine's outer transaction when
	// UpsertByUID is called via WithTx.
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return Todo{}, err
	}
	defer rollback()

	r, err := qtx.UpsertTodoByUID(ctx, storage.UpsertTodoByUIDParams{
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
	if err := replaceCategoriesTx(ctx, qtx, t.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Todo{}, fmt.Errorf("replace categories: %w", err)
	}
	if err := commit(); err != nil {
		return Todo{}, fmt.Errorf("commit upsert todo: %w", err)
	}
	t.Categories = p.Categories
	return t, nil
}

// ListOverridesByUID returns all override instances for a given UID.
func (s *Service) ListOverridesByUID(ctx context.Context, uid string) ([]Todo, error) {
	rows, err := s.q.ListTodoOverridesByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
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

func fromStorageSlice(rows []storage.Todo) []Todo {
	todos := make([]Todo, len(rows))
	for i, r := range rows {
		todos[i] = fromStorage(r)
	}
	return todos
}

// Hydrate loads the transient relation slices onto t. See event.Service.Hydrate
// for the contract. That is the single definition of a fully populated record.
// It fails fast so no caller pushes an amputated one.
func (s *Service) Hydrate(ctx context.Context, t *Todo) error {
	return s.hydrate(ctx, t, true)
}

// HydrateBestEffort populates every relation it can and returns the joined
// errors for the ones it could not. It does not stop at the first failure.
//
// This is for read-only display paths. One unreadable relation should degrade
// that field alone. An early stop would leave every relation after it nil.
// A caller that renders JSON cannot tell that apart from "there are none".
// Anything that writes iCal must use Hydrate. A partial record pushed to a
// server overwrites the complete copy there.
func (s *Service) HydrateBestEffort(ctx context.Context, t *Todo) error {
	return s.hydrate(ctx, t, false)
}

// HydrateSkipUnreadable populates every relation it can and returns the names
// of the relations it could not load. It never fails. See
// event.Service.HydrateSkipUnreadable for the contract.
func (s *Service) HydrateSkipUnreadable(ctx context.Context, t *Todo) []string {
	return s.collect(ctx, t, false).Failed()
}

// collect loads every relation onto t through one Collector. Hydrate,
// HydrateBestEffort, and HydrateSkipUnreadable share it, so the relation set
// stays a single definition.
func (s *Service) collect(ctx context.Context, t *Todo, failFast bool) *hydrate.Collector {
	c := &hydrate.Collector{Kind: "todo", ID: t.ID, FailFast: failFast}
	hydrate.Rel(ctx, c, &t.Alarms, "alarms", s.ListAlarms)
	hydrate.Rel(ctx, c, &t.Attendees, "attendees", s.ListAttendees)
	hydrate.Rel(ctx, c, &t.Attachments, "attachments", s.ListAttachments)
	hydrate.Rel(ctx, c, &t.Comments, "comments", s.ListComments)
	hydrate.Rel(ctx, c, &t.Contacts, "contacts", s.ListContacts)
	hydrate.Rel(ctx, c, &t.Resources, "resources", s.ListResources)
	hydrate.Rel(ctx, c, &t.Relations, "relations", s.ListRelations)
	hydrate.Rel(ctx, c, &t.XProperties, "x-properties", s.ListXProperties)
	return c
}

func (s *Service) hydrate(ctx context.Context, t *Todo, failFast bool) error {
	return s.collect(ctx, t, failFast).Err()
}
