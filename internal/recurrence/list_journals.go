package recurrence

import (
	"context"
	"sort"
	"time"

	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// journalOverridesByUID is the journal analogue of eventOverridesByUID.
func (s *Service) journalOverridesByUID(ctx context.Context, masters []storage.Journal) (map[string][]storage.Journal, error) {
	if len(masters) == 0 {
		return nil, nil
	}
	uids := make([]string, len(masters))
	for i, m := range masters {
		uids[i] = m.Uid
	}
	rows, err := s.q.ListJournalOverridesByUIDs(ctx, uids)
	if err != nil {
		return nil, err
	}
	byUID := make(map[string][]storage.Journal, len(rows))
	for _, r := range rows {
		byUID[r.Uid] = append(byUID[r.Uid], r)
	}
	return byUID, nil
}

// ── Journal recurrence ────────────────────────────────────────────────

// ExpandedJournal represents a single occurrence of a (possibly recurring) journal.
type ExpandedJournal struct {
	journal.Journal
	InstanceTime time.Time
	IsOverride   bool
}

// JournalListParams holds composable filters for journal lists.
type JournalListParams struct {
	CalendarID int64
	Status     string
	// HideCancelled, when true, omits CANCELLED journals. Default (false)
	// returns every status, matching the iCal model where a cancelled
	// journal is still a real row the caller may want to see.
	HideCancelled bool
	From          time.Time
	To            time.Time
	// IncludeDeleted, when true, returns soft-deleted journals alongside
	// live rows. Default (false) hides them.
	IncludeDeleted bool
}

func journalFromRow(row storage.Journal) journal.Journal {
	return journal.Journal{
		ID:             row.ID,
		UID:            row.Uid,
		CalendarID:     row.CalendarID,
		Summary:        row.Summary,
		Description:    storage.NullableToString(row.Description),
		StartDate:      storage.NullableToString(row.StartDate),
		Status:         row.Status,
		Class:          row.Class,
		URL:            storage.NullableToString(row.Url),
		RecurrenceRule: storage.NullableToString(row.RecurrenceRule),
		Timezone:       storage.NullableToString(row.Timezone),
		Sequence:       row.Sequence,
		ExDates:        storage.NullableToString(row.Exdates),
		RDates:         storage.NullableToString(row.Rdates),
		RecurrenceID:   row.RecurrenceID,
		DtStamp:        storage.NullableToString(row.Dtstamp),
		CreatedAt:      timeutil.ParseDateTime(row.CreatedAt),
		UpdatedAt:      timeutil.ParseDateTime(row.UpdatedAt),
	}
}

func (s *Service) populateJournalCategories(ctx context.Context, journals []journal.Journal) {
	populateCategories(ctx, journals,
		func(j journal.Journal) int64 { return j.ID },
		s.q.ListCategoriesByJournalIDs,
		func(r storage.JournalCategory) (int64, string) { return r.JournalID, r.Category },
		func(j *journal.Journal, joined string) { j.Categories = joined },
	)
}

// newJournalRRuleSet parses j's RRULE into a reusable rruleSet. ok is false when
// the journal has no start date (so it cannot be expanded). Journals are points
// in time, so the set carries no duration.
func newJournalRRuleSet(j journal.Journal, includeExDates bool) (rruleSet, bool) {
	anchor := j.ParseStartDate()
	if anchor.IsZero() {
		return rruleSet{}, false
	}
	return newRRuleSet(j.RecurrenceRule, j.Timezone, anchor, 0,
		j.ParseExDates(), j.ParseRDates(), includeExDates)
}

// newJournalOccChecker builds a reusable orphan-detection checker for a
// recurring journal master. A cancelled series matches nothing (see
// newEventOccChecker).
func newJournalOccChecker(j journal.Journal) occChecker {
	if cancelledRecurringMaster(j.RecurrenceRule, j.Status) {
		return occChecker{}
	}
	rs, _ := newJournalRRuleSet(j, false)
	return occChecker{rs: rs, anchor: j.ParseStartDate()}
}

// singleJournalInstance returns j as a lone occurrence (non-recurring or
// unparseable RRULE) if its start date falls within [from, to).
func singleJournalInstance(j journal.Journal, from, to time.Time) []ExpandedJournal {
	anchor := j.ParseStartDate()
	if anchor.IsZero() || anchor.Before(from) || !anchor.Before(to) {
		return nil
	}
	return []ExpandedJournal{{Journal: j, InstanceTime: anchor}}
}

// ExpandJournal generates all occurrences of a journal within a date range.
func ExpandJournal(j journal.Journal, from, to time.Time) []ExpandedJournal {
	// A cancelled recurring master has no occurrences (see cancelledRecurringMaster).
	if cancelledRecurringMaster(j.RecurrenceRule, j.Status) {
		return nil
	}
	rs, ok := newJournalRRuleSet(j, true)
	if !ok {
		return singleJournalInstance(j, from, to)
	}

	var instances []ExpandedJournal
	for _, occ := range rs.between(from, to) {
		_, isRDate := rs.rdateSet[rdateKey(occ)]
		instances = append(instances, ExpandedJournal{Journal: j, InstanceTime: occ.UTC(), IsOverride: isRDate})
	}
	return instances
}

func (s *Service) expandRecurringJournalRows(ctx context.Context, rows []storage.Journal, from, to time.Time) ([]journal.Journal, error) {
	k := recurringKind[storage.Journal, journal.Journal, ExpandedJournal]{
		fromRow:  journalFromRow,
		expand:   ExpandJournal,
		instTime: func(i ExpandedJournal) time.Time { return i.InstanceTime },
		applyInstance: func(i ExpandedJournal) journal.Journal {
			jj := i.Journal
			if anchor := jj.ParseStartDate(); !anchor.IsZero() {
				jj.StartDate = shiftDateString(jj.StartDate, anchor, i.InstanceTime.Sub(anchor))
			}
			return jj
		},
		uid:            func(r storage.Journal) string { return r.Uid },
		status:         func(r storage.Journal) string { return r.Status },
		recurrenceID:   func(r storage.Journal) string { return r.RecurrenceID },
		overridesByUID: s.journalOverridesByUID,
		newOccChecker:  newJournalOccChecker,
		emitOverride: func(o storage.Journal, from, to time.Time) (journal.Journal, bool) {
			oj := journalFromRow(o)
			anchor := oj.ParseStartDate()
			if anchor.IsZero() {
				// No datable anchor: fall back to the replaced slot for the
				// window check. An unparseable recurrence_id leaves anchor zero,
				// which fails inWindow and is dropped (the orphan probe that
				// follows would drop it too).
				anchor, _ = timeutil.ParseRecurrenceID(o.RecurrenceID)
			}
			return oj, inWindow(anchor, from, to)
		},
	}
	return expandRecurringRowsBy(ctx, k, rows, from, to)
}

// ListFilteredJournals returns journals that match all supplied filters. When a
// date range is provided, recurring journals are expanded. Otherwise master
// entries are returned as-is.
func (s *Service) ListFilteredJournals(ctx context.Context, p JournalListParams) ([]journal.Journal, error) {
	fromStr := ""
	toStr := ""
	hasRange := !p.From.IsZero() || !p.To.IsZero()
	if !p.From.IsZero() {
		fromStr = p.From.Format("2006-01-02")
	}
	if !p.To.IsZero() {
		toStr = p.To.Format("2006-01-02")
	}

	rows, err := s.q.ListJournalsFiltered(ctx, storage.JournalFilterParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		HideCancelled:  p.HideCancelled,
		FromDate:       fromStr,
		ToDate:         toStr,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}

	var result []journal.Journal
	for _, row := range rows {
		result = append(result, journalFromRow(row))
	}

	recurringRows, err := s.q.ListRecurringJournalsFiltered(ctx, storage.JournalFilterParams{
		CalendarID:     p.CalendarID,
		FilterStatus:   p.Status,
		HideCancelled:  p.HideCancelled,
		IncludeDeleted: p.IncludeDeleted,
	})
	if err != nil {
		return nil, err
	}
	if hasRange {
		expanded, err := s.expandRecurringJournalRows(ctx, recurringRows, p.From, p.To)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded...)
	} else {
		for _, row := range recurringRows {
			result = append(result, journalFromRow(row))
		}
	}

	s.populateJournalCategories(ctx, result)
	sort.Slice(result, func(i, j int) bool {
		di := result[i].ParseStartDate()
		dj := result[j].ParseStartDate()
		if di.IsZero() {
			return false
		}
		if dj.IsZero() {
			return true
		}
		return di.Before(dj)
	})
	return result, nil
}
