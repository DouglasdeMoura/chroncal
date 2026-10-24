package recurrence

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/recurset"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// Service handles recurrence expansion and the cache.
type Service struct {
	db *sql.DB
	q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
	return &Service{db: db, q: q}
}

// seriesKey identifies a recurring series. UID is unique per calendar
// (issue #756), so the key includes calendar_id.
func seriesKey(calendarID int64, uid string) string {
	return strconv.FormatInt(calendarID, 10) + "\x00" + uid
}

// canonicalRecurrenceID normalizes a stored recurrence_id to the same UTC
// RFC 3339 form used for expanded instance keys. A date-only or zoned id then
// compares equal to the occurrence it identifies. Suppression and orphan
// detection must use the same normalization (or neither) to stay in agreement.
// It falls back to the raw string when it cannot be parsed.
func canonicalRecurrenceID(rid string) string {
	if t := recurrenceTime(rid); !t.IsZero() {
		return t.Format(time.RFC3339)
	}
	return rid
}

// recurrenceTime parses a recurrence ID to UTC, with the rule of
// canonicalRecurrenceID. A value that does not parse gives the zero time.
// The expansion sets the field RecurrenceTime of an override with it.
func recurrenceTime(rid string) time.Time {
	t, err := timeutil.ParseRecurrenceID(rid)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// cancelledRecurringMaster reports whether the row is a recurring master that
// is cancelled. A cancel of a recurring series cancels the whole series.
// Such a master expands to no occurrences.
//
// Display, alarms, and free/busy all flow through the Expand* functions. All
// three intentionally see nothing for a cancelled series. Any still-CONFIRMED
// override is dropped with the series. That matches Google/iCloud
// whole-series-cancel semantics. Non-recurring cancelled events are left
// untouched for the caller to show or hide. ICS export bypasses this via a
// status-stripped probe so a CANCELLED master still round-trips (see
// ExportExpandedByDateRange). RecurrenceRule and Status are exported strings
// on event.Event, todo.Todo, and journal.Journal alike.
func cancelledRecurringMaster(recurrenceRule, status string) bool {
	return recurrenceRule != "" && strings.EqualFold(status, "CANCELLED")
}

// isRDateOnlyMaster reports whether a storage row's rdates field marks it as an
// RDATE-only recurring master: no RRULE but at least one RDATE stored. Such
// rows must follow the recurring-expansion path even though recurrence_rule IS
// NULL. Go-level filters that would otherwise include them as non-recurring
// singletons must skip them.
func isRDateOnlyMaster(rdates *string) bool {
	return rdates != nil && *rdates != ""
}

// recurringKind bundles the entity-specific operations expandRecurringRowsBy
// needs to merge a recurring master with its overrides. Row is the storage row,
// Model the domain model, and Inst the Expand* result (which carries the model
// plus its InstanceTime).
type recurringKind[Row any, Model any, Inst any] struct {
	fromRow       func(Row) Model
	expand        func(Model, time.Time, time.Time) []Inst
	instTime      func(Inst) time.Time
	applyInstance func(Inst) Model // master instance adjusted to its occurrence time
	uid           func(Row) string
	status        func(Row) string
	recurrenceID  func(Row) string
	// dateOnly reports whether the row holds a date-only value (an all-day
	// event, or a todo or journal with a date-only anchor). Such a row uses
	// the date-only window of timeutil.WindowFor.
	dateOnly func(Row) bool
	// overridesByUID batch-fetches every master's overrides in one query,
	// grouped by UID, and propagates any fetch error.
	overridesByUID func(context.Context, []Row) (map[string][]Row, error)
	// newOccChecker builds an orphan-detection checker for a master, parsing
	// its RRULE once for reuse across all of its overrides.
	newOccChecker func(master Model) recurset.Checker
	// emitOverride builds an override's model and reports whether its own
	// occurrence falls within [from, to).
	emitOverride func(o Row, from, to time.Time) (Model, bool)
}

// expandRecurringRowsBy expands recurring master rows into per-occurrence
// Models and applies overrides. For each master, an override is a row with a
// RECURRENCE-ID that matches. It suppresses the original RRULE instance. It
// is emitted separately at its own occurrence time. CANCELLED and orphan
// overrides are dropped. This is the shared engine behind the event, todo,
// and journal variants.
//
// Every master's overrides are fetched in a single batched query up front. A
// failed fetch is propagated so callers never render masters as if they had no
// overrides.
func expandRecurringRowsBy[Row any, Model any, Inst any](ctx context.Context, k recurringKind[Row, Model, Inst], rows []Row, from, to time.Time) ([]Model, error) {
	overridesByUID, err := k.overridesByUID(ctx, rows)
	if err != nil {
		return nil, err
	}

	var result []Model
	for _, row := range rows {
		master := k.fromRow(row)
		mFrom, mTo := timeutil.WindowFor(k.dateOnly(row), from, to)
		expanded := k.expand(master, mFrom, mTo)

		overrides := overridesByUID[k.uid(row)]
		overridden := make(map[string]struct{}, len(overrides))
		for _, o := range overrides {
			overridden[canonicalRecurrenceID(k.recurrenceID(o))] = struct{}{}
		}

		// Emit master instances, skipping any slot that has been overridden;
		// the override is emitted separately below at its own occurrence time.
		for _, inst := range expanded {
			instKey := k.instTime(inst).UTC().Format(time.RFC3339)
			if _, ok := overridden[instKey]; ok {
				continue
			}
			result = append(result, k.applyInstance(inst))
		}

		// Overrides are the exception, not the rule; skip the override work
		// (including the second RRULE parse the checker needs) for the common
		// case of a master with none.
		if len(overrides) == 0 {
			continue
		}

		// Emit overrides whose own occurrence falls within [from, to). A moved
		// occurrence belongs to its new day, not the day of the slot it replaced.
		// The cheap window check (in emitOverride) runs before the orphan probe;
		// the master's RRULE is parsed once for all of its overrides.
		checker := k.newOccChecker(master)
		for _, o := range overrides {
			if strings.EqualFold(k.status(o), "CANCELLED") {
				continue // CANCELLED override suppresses the instance.
			}
			oFrom, oTo := timeutil.WindowFor(k.dateOnly(o), from, to)
			m, ok := k.emitOverride(o, oFrom, oTo)
			if !ok {
				continue // override's own occurrence is outside the window
			}
			if !checker.OccursAt(k.recurrenceID(o)) {
				continue // orphan override (no matching master occurrence)
			}
			result = append(result, m)
		}
	}
	return result, nil
}

// populateCategories batch-loads categories for items and assigns the joined
// category string to each via setCats. idOf yields an item's primary key.
// fetch loads the join rows for a set of ids. rowCat splits a join row into
// its (id, category) pair. A fetch error is swallowed. Categories augment a
// list rather than gate it. This matches the per-domain behavior this unifies.
func populateCategories[T any, R any](
	ctx context.Context,
	items []T,
	idOf func(T) int64,
	fetch func(context.Context, []int64) ([]R, error),
	rowCat func(R) (id int64, category string),
	setCats func(item *T, joined string),
) {
	if len(items) == 0 {
		return
	}
	ids := make([]int64, len(items))
	for i := range items {
		ids[i] = idOf(items[i])
	}
	rows, err := fetch(ctx, ids)
	if err != nil {
		return
	}
	catMap := make(map[int64][]string, len(items))
	for _, r := range rows {
		id, cat := rowCat(r)
		catMap[id] = append(catMap[id], cat)
	}
	for i := range items {
		if cats, ok := catMap[idOf(items[i])]; ok {
			setCats(&items[i], timeutil.JoinCategoryList(cats))
		}
	}
}

// shiftDateString returns value advanced by offset. It keeps its date-only or
// RFC 3339 representation. It returns value unchanged when it is empty or its
// parsed form (parsed) is zero. A blank or unparseable field is then left intact.
func shiftDateString(value string, parsed time.Time, offset time.Duration) string {
	if value == "" || parsed.IsZero() {
		return value
	}
	shifted := parsed.Add(offset)
	if timeutil.IsDateOnly(value) {
		return shifted.Format("2006-01-02")
	}
	return shifted.Format(time.RFC3339)
}
