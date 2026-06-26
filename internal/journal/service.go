package journal

import (
	"context"
	"database/sql"
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
}

type ExportParams struct {
	CalendarID int64  // 0 = all
	Category   string // empty = all
	Status     string // empty = all
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

func (s *Service) Search(ctx context.Context, p SearchParams) ([]Journal, error) {
	ftsQuery := storage.FTSQuery(p.Query)
	if ftsQuery == "" {
		return nil, nil
	}
	rows, err := s.q.SearchJournalsFTS(ctx, ftsQuery, p.CalendarID, p.Status)
	if err != nil {
		return nil, fmt.Errorf("search journals: %w", err)
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
}

func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Journal, error) {
	rows, err := s.q.ListJournalsForExport(ctx, storage.ListJournalsForExportParams{
		CalendarID:   p.CalendarID,
		Category:     p.Category,
		FilterStatus: p.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("export journals: %w", err)
	}
	journals := fromStorageSlice(rows)
	s.populateCategories(ctx, journals)
	return journals, nil
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
func (s *Service) markDirtyByID(ctx context.Context, journalID int64) {
	r, err := s.q.GetJournal(ctx, journalID)
	if err != nil {
		return
	}
	_ = storage.MarkResourceDirty(ctx, s.dirtyExec(), r.CalendarID, r.Uid, "journal")
}

func (s *Service) Create(ctx context.Context, p CreateParams) (Journal, error) {
	p.applyDefaults()
	r, err := s.q.CreateJournal(ctx, storage.CreateJournalParams{
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
	if err := s.ReplaceCategories(ctx, j.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Journal{}, fmt.Errorf("replace categories: %w", err)
	}
	j.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.db, j.CalendarID, j.UID, "journal")
	return j, nil
}

func (s *Service) Update(ctx context.Context, id int64, p UpdateParams) (Journal, error) {
	p.Status, p.Class = defaults(p.Status, p.Class)
	r, err := s.q.UpdateJournal(ctx, storage.UpdateJournalParams{
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
	if err := s.ReplaceCategories(ctx, j.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Journal{}, fmt.Errorf("replace categories: %w", err)
	}
	j.Categories = p.Categories
	_ = storage.MarkResourceDirty(ctx, s.db, j.CalendarID, j.UID, "journal")
	return j, nil
}

func (s *Service) UpsertByUID(ctx context.Context, p UpsertParams) (Journal, error) {
	p.applyDefaults()
	r, err := s.q.UpsertJournalByUID(ctx, storage.UpsertJournalByUIDParams{
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
	if err := s.ReplaceCategories(ctx, j.ID, timeutil.ParseCategoryList(p.Categories)); err != nil {
		return Journal{}, fmt.Errorf("replace categories: %w", err)
	}
	j.Categories = p.Categories
	return j, nil
}

// ErrHasOverrides is returned when attempting to delete a recurring master
// journal that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("journal has overrides: use DeleteSeries to delete the entire series")

// Delete soft-deletes a journal by ID. For a standalone journal it flips
// deleted_at; for an override it adds EXDATE to the master and soft-
// deletes the override in the same transaction. A recurring master with
// live overrides is rejected — callers must use DeleteSeries.
func (s *Service) Delete(ctx context.Context, id int64) error {
	j, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if j.RecurrenceRule != "" && j.RecurrenceID == "" {
		overrides, err := s.q.ListJournalOverridesByUID(ctx, j.UID)
		if err != nil {
			return fmt.Errorf("check overrides: %w", err)
		}
		if len(overrides) > 0 {
			return ErrHasOverrides
		}
	}

	if j.RecurrenceID == "" {
		// Tombstone + soft-delete commit together so a failed tombstone write
		// can't leave a soft-deleted row whose next sync DELETEs a still-live
		// server resource (issue #107).
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, j.CalendarID, j.UID); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
		if err := qtx.SoftDeleteJournal(ctx, id); err != nil {
			return fmt.Errorf("soft-delete journal: %w", err)
		}
		if err := storage.MarkResourceDirty(ctx, tx, j.CalendarID, j.UID, "journal"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	if j.RecurrenceID != "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		qtx := s.q.WithTx(tx)

		master, err := qtx.GetJournalByUID(ctx, j.UID)
		if err == nil {
			existing := timeutil.ParseTimeList(storage.NullableToString(master.Exdates))
			recIDTime, parseErr := timeutil.ParseRecurrenceID(j.RecurrenceID)
			if parseErr != nil {
				// A malformed recurrence_id can't be excluded from the
				// master, so soft-deleting the override would resurrect the
				// occurrence via series expansion. Fail loudly instead — the
				// restore path treats the same parse failure as fatal.
				return fmt.Errorf("parse recurrence_id %q: %w", j.RecurrenceID, parseErr)
			}
			existing = append(existing, recIDTime)
			if err := qtx.UpdateJournalExdates(ctx, storage.UpdateJournalExdatesParams{
				Exdates: storage.StringToNullable(timeutil.SerializeTimeList(existing)),
				ID:      master.ID,
			}); err != nil {
				return fmt.Errorf("update exdates: %w", err)
			}
			// Record provenance so restore knows this EXDATE was
			// delete-added (and may be stripped) rather than imported.
			if err := qtx.RecordJournalExdateDelete(ctx, storage.RecordJournalExdateDeleteParams{
				CalendarID:   master.CalendarID,
				Uid:          j.UID,
				RecurrenceID: j.RecurrenceID,
			}); err != nil {
				return fmt.Errorf("record exdate delete: %w", err)
			}
		}

		if err := qtx.SoftDeleteJournal(ctx, id); err != nil {
			return fmt.Errorf("soft-delete journal: %w", err)
		}
		// Mark the master dirty — its EXDATE was modified — inside the same
		// transaction so a failed mark rolls the EXDATE change back rather than
		// committing a change that is never pushed (issue #107).
		if err := storage.MarkResourceDirty(ctx, tx, j.CalendarID, j.UID, "journal"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		return tx.Commit()
	}

	// Unreachable: RecurrenceID is either "" (handled above) or non-empty.
	if err := s.q.SoftDeleteJournal(ctx, id); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, j.CalendarID, j.UID, "journal")
	return nil
}

// DeleteSeries soft-deletes a recurring master journal and every override
// sharing its UID. A tombstone is queued when the master is synced so the
// next push sends DELETE to the server; the local rows stay in place
// until purge so the user can restore them.
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
	master, mErr := qtx.GetJournalByUID(ctx, uid)
	haveMaster := mErr == nil
	if haveMaster {
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, master.CalendarID, uid); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
	}

	if err := qtx.SoftDeleteJournalsByUID(ctx, uid); err != nil {
		return fmt.Errorf("soft-delete series: %w", err)
	}
	if haveMaster {
		if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "journal"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
	}
	return tx.Commit()
}

// ListOverridesByUID returns all override instances for a given UID.
func (s *Service) ListOverridesByUID(ctx context.Context, uid string) ([]Journal, error) {
	rows, err := s.q.ListJournalOverridesByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

// Attendee CRUD

func (s *Service) ListAttendees(ctx context.Context, journalID int64) ([]model.Attendee, error) {
	rows, err := s.q.ListJournalAttendeesByJournalID(ctx, journalID)
	if err != nil {
		return nil, err
	}
	attendees := make([]model.Attendee, len(rows))
	for i, r := range rows {
		attendees[i] = model.Attendee{
			ID: r.ID, EventID: r.JournalID,
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

func (s *Service) ReplaceAttendees(ctx context.Context, journalID int64, attendees []model.Attendee) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()

	if err := qtx.DeleteJournalAttendeesByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete attendees: %w", err)
	}
	for _, a := range attendees {
		org := storage.BoolToInt(a.Organizer)
		rsvp := ""
		if a.RSVPRequested {
			rsvp = "TRUE"
		}
		_, err := qtx.CreateJournalAttendee(ctx, storage.CreateJournalAttendeeParams{
			JournalID:     journalID,
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
	s.markDirtyByID(ctx, journalID)
	return nil
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

	if err := qtx.DeleteCategoriesByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete categories: %w", err)
	}
	for _, c := range categories {
		_, err := qtx.CreateJournalCategory(ctx, storage.CreateJournalCategoryParams{
			JournalID: journalID,
			Category:  c,
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

func (s *Service) ListAttachments(ctx context.Context, journalID int64) ([]model.Attachment, error) {
	rows, err := s.q.ListJournalAttachmentsByJournalID(ctx, journalID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Attachment, len(rows))
	for i, r := range rows {
		out[i] = model.Attachment{ID: r.ID, URI: storage.NullableToString(r.Uri), FmtType: storage.NullableToString(r.Fmttype), Data: r.Data, Filename: storage.NullableToString(r.Filename)}
	}
	return out, nil
}

func (s *Service) ReplaceAttachments(ctx context.Context, journalID int64, attachments []model.Attachment) error {
	qtx, commit, rollback, err := s.txscope(ctx)
	if err != nil {
		return err
	}
	defer rollback()
	if err := qtx.DeleteJournalAttachmentsByJournalID(ctx, journalID); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	for _, a := range attachments {
		_, err := qtx.CreateJournalAttachment(ctx, storage.CreateJournalAttachmentParams{
			JournalID: journalID, Uri: storage.StringToNullable(a.URI), Fmttype: storage.StringToNullable(a.FmtType), Data: a.Data, Filename: storage.StringToNullable(a.Filename),
		})
		if err != nil {
			return fmt.Errorf("create attachment: %w", err)
		}
	}
	if err := commit(); err != nil {
		return err
	}
	s.markDirtyByID(ctx, journalID)
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
	s.markDirtyByID(ctx, journalID)
	return nil
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
	s.markDirtyByID(ctx, journalID)
	return nil
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
	s.markDirtyByID(ctx, journalID)
	return nil
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
	s.markDirtyByID(ctx, journalID)
	return nil
}

func fromStorageSlice(rows []storage.Journal) []Journal {
	journals := make([]Journal, len(rows))
	for i, r := range rows {
		journals[i] = fromStorage(r)
	}
	return journals
}
