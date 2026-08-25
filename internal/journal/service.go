package journal

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
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
	CalendarID     int64
	Summary        string
	Description    string
	StartDate      string
	Status         string
	Class          string
	URL            string
	Categories     string
	RecurrenceRule string
	Timezone       string
	Sequence       int64
	ExDates        string
	RDates         string
	RecurrenceID   string
	DtStamp        string
}

type UpdateParams struct {
	Summary        string
	Description    string
	StartDate      string
	Status         string
	CalendarID     int64
	Class          string
	URL            string
	Categories     string
	RecurrenceRule string
	Timezone       string
	ExDates        string
	RDates         string
	DtStamp        string
}

type UpsertParams struct {
	UID            string
	CalendarID     int64
	Summary        string
	Description    string
	StartDate      string
	Status         string
	Class          string
	URL            string
	Categories     string
	RecurrenceRule string
	Timezone       string
	Sequence       int64
	ExDates        string
	RDates         string
	RecurrenceID   string
	DtStamp        string
}

const (
	defaultStatus = "FINAL"
	defaultClass  = "PUBLIC"
)

func defaults(status, class string) (string, string) {
	if status == "" {
		status = defaultStatus
	}
	if class == "" {
		class = defaultClass
	}
	return status, class
}

func (p *CreateParams) applyDefaults() {
	p.Status, p.Class = defaults(p.Status, p.Class)
}

func (p *UpsertParams) applyDefaults() {
	p.Status, p.Class = defaults(p.Status, p.Class)
}

func (s *Service) List(ctx context.Context) ([]Journal, error) {
	rows, err := s.q.ListJournals(ctx)
	if err != nil {
		return nil, err
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}

func (s *Service) ListAll(ctx context.Context) ([]Journal, error) {
	rows, err := s.q.ListAllJournals(ctx)
	if err != nil {
		return nil, err
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}

func (s *Service) ListByCalendar(ctx context.Context, calID int64) ([]Journal, error) {
	rows, err := s.q.ListJournalsByCalendar(ctx, calID)
	if err != nil {
		return nil, err
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}

func (s *Service) ListByStatus(ctx context.Context, status string) ([]Journal, error) {
	rows, err := s.q.ListJournalsByStatus(ctx, status)
	if err != nil {
		return nil, err
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}

func (s *Service) ListByDateRange(ctx context.Context, from, to time.Time) ([]Journal, error) {
	fromStr := from.Format("2006-01-02")
	toStr := to.Format("2006-01-02")
	rows, err := s.q.ListJournalsByStartDateRange(ctx, storage.ListJournalsByStartDateRangeParams{
		StartDate:   &fromStr,
		StartDate_2: &toStr,
	})
	if err != nil {
		return nil, err
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Journal, error) {
	r, err := s.q.GetJournal(ctx, id)
	if err != nil {
		return Journal{}, err
	}
	j := fromStorage(r)
	s.populateSingleCategories(ctx, &j)
	return j, nil
}

func (s *Service) GetByUID(ctx context.Context, uid string) (Journal, error) {
	r, err := s.q.GetJournalByUID(ctx, uid)
	if err != nil {
		return Journal{}, err
	}
	j := fromStorage(r)
	s.populateSingleCategories(ctx, &j)
	return j, nil
}

func (s *Service) GetByUIDAndRecurrenceID(ctx context.Context, uid, recurrenceID string) (Journal, error) {
	r, err := s.q.GetJournalByUIDAndRecurrenceID(ctx, storage.GetJournalByUIDAndRecurrenceIDParams{
		Uid:          uid,
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		return Journal{}, err
	}
	j := fromStorage(r)
	s.populateSingleCategories(ctx, &j)
	return j, nil
}

// markDirtyByID looks up a journal by ID and marks its sync resource as dirty.
// The error is surfaced so a dropped dirty flag is never silent.
func (s *Service) markDirtyByID(ctx context.Context, journalID int64) error {
	r, err := s.q.GetJournal(ctx, journalID)
	if err != nil {
		return fmt.Errorf("get journal for dirty mark: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, s.dirtyExec(), r.CalendarID, r.Uid, "journal"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return nil
}

// ensureWritable guards a user-originated mutation against a read-only or
// VJOURNAL-unsupported remote collection. Empty capability metadata keeps
// legacy and direct-linked calendars writable. It is the single chokepoint for
// user-facing writes. The sync Upsert/import paths bypass it because they
// replay server-originated data.
func (s *Service) ensureWritable(ctx context.Context, calendarID int64) error {
	return calendaraccess.EnsureWritable(ctx, s.q, calendarID, "VJOURNAL")
}

// calendarIDsForUID returns the distinct calendar IDs of every journal row
// with uid: live, soft-deleted, master, and override alike. UID-keyed
// mutations (delete series, restore series) resolve this widest set. The
// access guard then still covers orphaned override and series-tail rows that
// survive a purged master. The per-UID queries (recurrence_id = "") would
// otherwise miss them.
func (s *Service) calendarIDsForUID(ctx context.Context, uid string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT DISTINCT calendar_id FROM journals WHERE uid = ?", uid)
	if err != nil {
		return nil, fmt.Errorf("resolve calendars for uid: %w", err)
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
		return nil, fmt.Errorf("iterate calendars for uid: %w", err)
	}
	return ids, nil
}

// ensureAllWritable guards every calendar in ids. Used by UID-keyed mutations
// whose rows may span calendars and whose master may be absent.
func (s *Service) ensureAllWritable(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		if err := s.ensureWritable(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Journal, error) {
	if err := s.ensureWritable(ctx, p.CalendarID); err != nil {
		return Journal{}, err
	}
	p.applyDefaults()

	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return Journal{}, err
	}
	defer rollback()

	r, err := qtx.CreateJournal(ctx, storage.CreateJournalParams{
		Uid:            uuid.New().String(),
		CalendarID:     p.CalendarID,
		Summary:        p.Summary,
		Description:    storage.StringToNullable(p.Description),
		StartDate:      storage.StringToNullable(p.StartDate),
		Status:         p.Status,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
		Timezone:       storage.StringToNullable(p.Timezone),
		Sequence:       p.Sequence,
		Exdates:        storage.StringToNullable(p.ExDates),
		Rdates:         storage.StringToNullable(p.RDates),
		RecurrenceID:   p.RecurrenceID,
		Dtstamp:        storage.StringToNullable(p.DtStamp),
	})
	if err != nil {
		return Journal{}, err
	}
	j := fromStorage(r)
	if err := replaceCategoriesTx(ctx, qtx, j.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Journal{}, fmt.Errorf("replace categories: %w", err)
	}
	if err := commit(); err != nil {
		return Journal{}, fmt.Errorf("commit create journal: %w", err)
	}
	j.Categories = p.Categories
	if err := storage.MarkResourceDirty(ctx, s.dirtyExec(), j.CalendarID, j.UID, "journal"); err != nil {
		return Journal{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return j, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Journal, error) {
	existing, err := s.q.GetJournal(ctx, id)
	if err != nil {
		return Journal{}, err
	}
	if err := s.ensureWritable(ctx, existing.CalendarID); err != nil {
		return Journal{}, err
	}
	if p.CalendarID != existing.CalendarID {
		if err := s.ensureWritable(ctx, p.CalendarID); err != nil {
			return Journal{}, err
		}
	}
	p.Status, p.Class = defaults(p.Status, p.Class)

	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return Journal{}, err
	}
	defer rollback()

	r, err := qtx.UpdateJournal(ctx, storage.UpdateJournalParams{
		ID:             id,
		Summary:        p.Summary,
		Description:    storage.StringToNullable(p.Description),
		StartDate:      storage.StringToNullable(p.StartDate),
		Status:         p.Status,
		CalendarID:     p.CalendarID,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
		Timezone:       storage.StringToNullable(p.Timezone),
		Exdates:        storage.StringToNullable(p.ExDates),
		Rdates:         storage.StringToNullable(p.RDates),
		Dtstamp:        storage.StringToNullable(p.DtStamp),
	})
	if err != nil {
		return Journal{}, err
	}
	j := fromStorage(r)
	if err := replaceCategoriesTx(ctx, qtx, j.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Journal{}, fmt.Errorf("replace categories: %w", err)
	}
	if err := commit(); err != nil {
		return Journal{}, fmt.Errorf("commit update journal: %w", err)
	}
	j.Categories = p.Categories
	if err := storage.MarkResourceDirty(ctx, s.dirtyExec(), j.CalendarID, j.UID, "journal"); err != nil {
		return Journal{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	return j, nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Journal, error) {
	p.applyDefaults()

	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return Journal{}, err
	}
	defer rollback()

	r, err := qtx.UpsertJournalByUID(ctx, storage.UpsertJournalByUIDParams{
		Uid:            p.UID,
		CalendarID:     p.CalendarID,
		Summary:        p.Summary,
		Description:    storage.StringToNullable(p.Description),
		StartDate:      storage.StringToNullable(p.StartDate),
		Status:         p.Status,
		Class:          p.Class,
		Url:            storage.StringToNullable(p.URL),
		RecurrenceRule: storage.StringToNullable(p.RecurrenceRule),
		Timezone:       storage.StringToNullable(p.Timezone),
		Sequence:       p.Sequence,
		Exdates:        storage.StringToNullable(p.ExDates),
		Rdates:         storage.StringToNullable(p.RDates),
		RecurrenceID:   p.RecurrenceID,
		Dtstamp:        storage.StringToNullable(p.DtStamp),
	})
	if err != nil {
		return Journal{}, err
	}
	j := fromStorage(r)
	if err := replaceCategoriesTx(ctx, qtx, j.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Journal{}, fmt.Errorf("replace categories: %w", err)
	}
	if err := commit(); err != nil {
		return Journal{}, fmt.Errorf("commit upsert journal: %w", err)
	}
	j.Categories = p.Categories
	return j, nil
}

// ListOverridesByUID returns all override instances for a given UID.
func (s *Service) ListOverridesByUID(ctx context.Context, uid string) ([]Journal, error) {
	rows, err := s.q.ListJournalOverridesByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

// Category CRUD

func (s *Service) ListCategories(ctx context.Context, journalID int64) ([]string, error) {
	rows, err := s.q.ListCategoriesByJournalID(ctx, journalID)
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
	return s.q.ListAllJournalCategories(ctx)
}

func (s *Service) ReplaceCategories(ctx context.Context, journalID int64, categories []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := replaceCategoriesTx(ctx, qtx, journalID, categories); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return fmt.Errorf("commit replace categories: %w", err)
	}
	return nil
}

// replaceCategoriesTx replaces a journal's categories using a tx-bound Queries.
// It does not open or commit a transaction, so callers can compose it with the
// journal row write inside a single transaction.
func replaceCategoriesTx(ctx context.Context, qtx *storage.Queries, journalID int64, categories []string) error {
	if err := qtx.DeleteCategoriesByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete categories: %w", err)
	}
	for i, c := range categories {
		_, err := qtx.CreateJournalCategory(ctx, storage.CreateJournalCategoryParams{
			JournalID: journalID,
			Category:  c,
			Position:  int64(i),
		})
		if err != nil {
			return fmt.Errorf("create category: %w", err)
		}
	}
	return nil
}

// Comment CRUD

func (s *Service) ListComments(ctx context.Context, journalID int64) ([]string, error) {
	rows, err := s.q.ListJournalCommentsByJournalID(ctx, journalID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceComments(ctx context.Context, journalID int64, comments []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteJournalCommentsByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete comments: %w", err)
	}
	for _, c := range comments {
		_, err := qtx.CreateJournalComment(ctx, storage.CreateJournalCommentParams{
			JournalID: journalID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create comment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, journalID)
}

// Contact CRUD

func (s *Service) ListContacts(ctx context.Context, journalID int64) ([]string, error) {
	rows, err := s.q.ListJournalContactsByJournalID(ctx, journalID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

func (s *Service) ReplaceContacts(ctx context.Context, journalID int64, contacts []string) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteJournalContactsByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete contacts: %w", err)
	}
	for _, c := range contacts {
		_, err := qtx.CreateJournalContact(ctx, storage.CreateJournalContactParams{
			JournalID: journalID, Text: c,
		})
		if err != nil {
			return fmt.Errorf("create contact: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, journalID)
}

// Relation CRUD

func (s *Service) ListRelations(ctx context.Context, journalID int64) ([]model.Relation, error) {
	rows, err := s.q.ListJournalRelationsByJournalID(ctx, journalID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Relation, len(rows))
	for i, r := range rows {
		out[i] = model.Relation{ID: r.ID, RelType: r.RelType, RelUID: r.RelUid}
	}
	return out, nil
}

func (s *Service) ReplaceRelations(ctx context.Context, journalID int64, relations []model.Relation) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteJournalRelationsByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete relations: %w", err)
	}
	for _, r := range relations {
		_, err := qtx.CreateJournalRelation(ctx, storage.CreateJournalRelationParams{
			JournalID: journalID, RelType: r.RelType, RelUid: r.RelUID,
		})
		if err != nil {
			return fmt.Errorf("create relation: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, journalID)
}

// Converters

func fromStorage(r storage.Journal) Journal {
	var deletedAt *time.Time
	if r.DeletedAt != nil && *r.DeletedAt != "" {
		t := timeutil.ParseDateTime(*r.DeletedAt)
		deletedAt = &t
	}
	return Journal{
		ID:             r.ID,
		UID:            r.Uid,
		CalendarID:     r.CalendarID,
		Summary:        r.Summary,
		Description:    storage.NullableToString(r.Description),
		StartDate:      storage.NullableToString(r.StartDate),
		Status:         r.Status,
		Class:          r.Class,
		URL:            storage.NullableToString(r.Url),
		RecurrenceRule: storage.NullableToString(r.RecurrenceRule),
		Timezone:       storage.NullableToString(r.Timezone),
		Sequence:       r.Sequence,
		ExDates:        storage.NullableToString(r.Exdates),
		RDates:         storage.NullableToString(r.Rdates),
		RecurrenceID:   r.RecurrenceID,
		DtStamp:        storage.NullableToString(r.Dtstamp),
		CreatedAt:      timeutil.ParseDateTime(r.CreatedAt),
		UpdatedAt:      timeutil.ParseDateTime(r.UpdatedAt),
		DeletedAt:      deletedAt,
	}
}

func (s *Service) populateSingleCategories(ctx context.Context, j *Journal) {
	rows, err := s.q.ListCategoriesByJournalID(ctx, j.ID)
	if err != nil {
		return
	}
	cats := make([]string, len(rows))
	for i, r := range rows {
		cats[i] = r.Category
	}
	j.Categories = timeutil.JoinCategoryList(cats)
}

func (s *Service) populateCategories(ctx context.Context, journals []Journal) {
	if len(journals) == 0 {
		return
	}
	ids := make([]int64, len(journals))
	for i := range journals {
		ids[i] = journals[i].ID
	}
	rows, err := s.q.ListCategoriesByJournalIDs(ctx, ids)
	if err != nil {
		return
	}
	catMap := make(map[int64][]string, len(journals))
	for _, r := range rows {
		catMap[r.JournalID] = append(catMap[r.JournalID], r.Category)
	}
	for i := range journals {
		if cats, ok := catMap[journals[i].ID]; ok {
			journals[i].Categories = timeutil.JoinCategoryList(cats)
		}
	}
}

// X-Property CRUD

func (s *Service) ListXProperties(ctx context.Context, journalID int64) ([]model.XProperty, error) {
	rows, err := s.q.ListXPropertiesByOwner(ctx, storage.ListXPropertiesByOwnerParams{
		OwnerType: "journal", OwnerID: journalID,
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

func (s *Service) ReplaceXProperties(ctx context.Context, journalID int64, xprops []model.XProperty) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteXPropertiesByOwner(ctx, storage.DeleteXPropertiesByOwnerParams{
		OwnerType: "journal", OwnerID: journalID,
	}); err != nil {
		return fmt.Errorf("delete x-properties: %w", err)
	}
	for _, xp := range xprops {
		if err := qtx.InsertXProperty(ctx, storage.InsertXPropertyParams{
			OwnerType: "journal", OwnerID: journalID,
			Name: xp.Name, Value: xp.Value, Params: xp.Params,
		}); err != nil {
			return fmt.Errorf("insert x-property: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	return s.markDirtyByID(ctx, journalID)
}

func fromStorageSlice(rows []storage.Journal) []Journal {
	journals := make([]Journal, len(rows))
	for i, r := range rows {
		journals[i] = fromStorage(r)
	}
	return journals
}
