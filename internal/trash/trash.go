// Package trash aggregates soft-deleted rows from event, todo, and journal
// services into a single "Recently deleted" view. The TUI and CLI call
// Service.List to render a mixed list newest-first, then Service.Restore
// or Service.Purge to act on a single entry regardless of its kind.
//
// Each service still owns its own soft-delete + restore + purge plumbing.
// This package is an aggregator — it doesn't carry new storage state.
package trash

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// Kind discriminates the source domain of a trash entry. The numeric
// values intentionally do not overlap with event.TrashKind — callers
// should use the Kind methods (IsEvent, IsTodo, IsJournal) rather than
// comparing raw values.
type Kind int

const (
	// KindEvent is a soft-deleted events row.
	KindEvent Kind = iota
	// KindEventInstance is an EXDATE-based instance delete captured in the
	// event_exdate_deletes log.
	KindEventInstance
	// KindEventSeriesTail is an RRULE truncation captured in
	// event_truncate_deletes ("This and following").
	KindEventSeriesTail
	// KindTodo is a soft-deleted todos row.
	KindTodo
	// KindJournal is a soft-deleted journals row.
	KindJournal
)

// Entry is a unified row for the mixed trash dialog. Domain-specific
// fields are populated only for the matching Kind; readers should branch
// on Kind before inspecting them. Title and DeletedAt are always valid.
type Entry struct {
	Kind       Kind
	ID         int64
	CalendarID int64
	UID        string
	Title      string
	DeletedAt  time.Time

	// Event-specific (KindEvent, KindEventInstance, KindEventSeriesTail).
	InstanceTime  time.Time
	CutoffTime    time.Time
	PreviousRRule string
	StartTime     time.Time
	EndTime       time.Time
	AllDay        bool

	// Todo-specific (KindTodo).
	DueDate         time.Time
	PercentComplete int64

	// Common.
	Location    string
	Description string
	Status      string
	Categories  string
}

// Service aggregates soft-delete state across event, todo, and journal
// into one List/Restore/Purge surface.
type Service struct {
	events   *event.Service
	todos    *todo.Service
	journals *journal.Service
}

// NewService wires the aggregator. Any of the arguments may be nil to
// opt a domain out of trash aggregation (tests, partial features).
func NewService(e *event.Service, t *todo.Service, j *journal.Service) *Service {
	return &Service{events: e, todos: t, journals: j}
}

// PurgeCounts reports how many rows each domain dropped from a PurgeOld
// call so callers (maintenance, CLI) can log per-domain numbers.
type PurgeCounts struct {
	Events              int
	EventInstanceLogs   int
	EventTruncateLogs   int
	Todos               int
	TodoInstanceLogs    int
	Journals            int
	JournalInstanceLogs int
}

// List returns every trash entry for calendarID across all domains,
// sorted newest-first by DeletedAt.
func (s *Service) List(ctx context.Context, calendarID int64) ([]Entry, error) {
	var entries []Entry

	if s.events != nil {
		evTrash, err := s.events.ListTrash(ctx, calendarID)
		if err != nil {
			return nil, fmt.Errorf("list event trash: %w", err)
		}
		for _, e := range evTrash {
			entries = append(entries, fromEventTrash(e))
		}
	}

	if s.todos != nil {
		tdDeleted, err := s.todos.ListDeleted(ctx, calendarID)
		if err != nil {
			return nil, fmt.Errorf("list deleted todos: %w", err)
		}
		for _, td := range tdDeleted {
			entries = append(entries, fromTodo(td))
		}
	}

	if s.journals != nil {
		jDeleted, err := s.journals.ListDeleted(ctx, calendarID)
		if err != nil {
			return nil, fmt.Errorf("list deleted journals: %w", err)
		}
		for _, j := range jDeleted {
			entries = append(entries, fromJournal(j))
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].DeletedAt.After(entries[j].DeletedAt)
	})
	return entries, nil
}

// Restore reverses the delete recorded in e. Dispatches by Kind.
func (s *Service) Restore(ctx context.Context, e Entry) error {
	switch e.Kind {
	case KindEvent, KindEventInstance, KindEventSeriesTail:
		if s.events == nil {
			return fmt.Errorf("events service not configured")
		}
		return s.events.RestoreTrash(ctx, toEventTrash(e))
	case KindTodo:
		if s.todos == nil {
			return fmt.Errorf("todos service not configured")
		}
		return s.todos.RestoreByID(ctx, e.ID)
	case KindJournal:
		if s.journals == nil {
			return fmt.Errorf("journals service not configured")
		}
		return s.journals.RestoreByID(ctx, e.ID)
	default:
		return fmt.Errorf("unknown trash kind %d", e.Kind)
	}
}

// Purge hard-removes e from the trash.
func (s *Service) Purge(ctx context.Context, e Entry) error {
	switch e.Kind {
	case KindEvent, KindEventInstance, KindEventSeriesTail:
		if s.events == nil {
			return fmt.Errorf("events service not configured")
		}
		return s.events.PurgeTrashEntry(ctx, toEventTrash(e))
	case KindTodo:
		if s.todos == nil {
			return fmt.Errorf("todos service not configured")
		}
		return s.todos.PurgeByID(ctx, e.ID)
	case KindJournal:
		if s.journals == nil {
			return fmt.Errorf("journals service not configured")
		}
		return s.journals.PurgeByID(ctx, e.ID)
	default:
		return fmt.Errorf("unknown trash kind %d", e.Kind)
	}
}

// PurgeOld walks each domain's retention purge and returns per-domain
// counts. The log-row purges (event_exdate_deletes, event_truncate_deletes)
// also run so the "This event" / "This and following" history doesn't
// pile up indefinitely.
func (s *Service) PurgeOld(ctx context.Context, olderThan time.Time) (PurgeCounts, error) {
	var counts PurgeCounts
	if s.events != nil {
		n, err := s.events.PurgeDeleted(ctx, olderThan)
		if err != nil {
			return counts, fmt.Errorf("purge events: %w", err)
		}
		counts.Events = n
		n, err = s.events.PurgeOldInstanceDeletes(ctx, olderThan)
		if err != nil {
			return counts, fmt.Errorf("purge event instance logs: %w", err)
		}
		counts.EventInstanceLogs = n
		n, err = s.events.PurgeOldTruncationDeletes(ctx, olderThan)
		if err != nil {
			return counts, fmt.Errorf("purge event truncation logs: %w", err)
		}
		counts.EventTruncateLogs = n
	}
	if s.todos != nil {
		n, err := s.todos.PurgeDeleted(ctx, olderThan)
		if err != nil {
			return counts, fmt.Errorf("purge todos: %w", err)
		}
		counts.Todos = n
		n, err = s.todos.PurgeOldInstanceDeletes(ctx, olderThan)
		if err != nil {
			return counts, fmt.Errorf("purge todo instance logs: %w", err)
		}
		counts.TodoInstanceLogs = n
	}
	if s.journals != nil {
		n, err := s.journals.PurgeDeleted(ctx, olderThan)
		if err != nil {
			return counts, fmt.Errorf("purge journals: %w", err)
		}
		counts.Journals = n
		n, err = s.journals.PurgeOldInstanceDeletes(ctx, olderThan)
		if err != nil {
			return counts, fmt.Errorf("purge journal instance logs: %w", err)
		}
		counts.JournalInstanceLogs = n
	}
	return counts, nil
}

// fromEventTrash converts an event.TrashEntry into the unified Entry
// shape. Preserves every field so the trash dialog can render the same
// detail content it did before the aggregator was introduced.
func fromEventTrash(e event.TrashEntry) Entry {
	return Entry{
		Kind:          mapEventKind(e.Kind),
		ID:            e.ID,
		CalendarID:    e.CalendarID,
		UID:           e.UID,
		Title:         e.Title,
		DeletedAt:     e.DeletedAt,
		InstanceTime:  e.InstanceTime,
		CutoffTime:    e.CutoffTime,
		PreviousRRule: e.PreviousRRule,
		StartTime:     e.StartTime,
		EndTime:       e.EndTime,
		AllDay:        e.AllDay,
		Location:      e.Location,
		Description:   e.Description,
		Status:        e.Status,
		Categories:    e.Categories,
	}
}

// toEventTrash is the inverse of fromEventTrash, used to hand an Entry
// back to event.Service.RestoreTrash / PurgeTrashEntry.
func toEventTrash(e Entry) event.TrashEntry {
	return event.TrashEntry{
		Kind:          unmapEventKind(e.Kind),
		ID:            e.ID,
		CalendarID:    e.CalendarID,
		UID:           e.UID,
		Title:         e.Title,
		DeletedAt:     e.DeletedAt,
		InstanceTime:  e.InstanceTime,
		CutoffTime:    e.CutoffTime,
		PreviousRRule: e.PreviousRRule,
		StartTime:     e.StartTime,
		EndTime:       e.EndTime,
		AllDay:        e.AllDay,
		Location:      e.Location,
		Description:   e.Description,
		Status:        e.Status,
		Categories:    e.Categories,
	}
}

func mapEventKind(k event.TrashKind) Kind {
	switch k {
	case event.TrashKindEvent:
		return KindEvent
	case event.TrashKindInstance:
		return KindEventInstance
	case event.TrashKindTruncation:
		return KindEventSeriesTail
	default:
		return KindEvent
	}
}

func unmapEventKind(k Kind) event.TrashKind {
	switch k {
	case KindEvent:
		return event.TrashKindEvent
	case KindEventInstance:
		return event.TrashKindInstance
	case KindEventSeriesTail:
		return event.TrashKindTruncation
	default:
		return event.TrashKindEvent
	}
}

func fromTodo(td todo.Todo) Entry {
	deletedAt := time.Time{}
	if td.DeletedAt != nil {
		deletedAt = *td.DeletedAt
	} else if !td.UpdatedAt.IsZero() {
		// Fallback for pre-v3 rows that were deleted before DeletedAt was
		// surfaced on the domain model; UpdatedAt is bumped alongside
		// deleted_at in SoftDeleteTodo, so it's a close stand-in.
		deletedAt = td.UpdatedAt
	}
	dueDate := time.Time{}
	if td.DueDate != "" {
		if t, err := time.Parse(time.RFC3339, td.DueDate); err == nil {
			dueDate = t
		} else if t, err := time.Parse("2006-01-02", td.DueDate); err == nil {
			dueDate = t
		}
	}
	return Entry{
		Kind:            KindTodo,
		ID:              td.ID,
		CalendarID:      td.CalendarID,
		UID:             td.UID,
		Title:           td.Summary,
		DeletedAt:       deletedAt,
		DueDate:         dueDate,
		PercentComplete: td.PercentComplete,
		Location:        td.Location,
		Description:     td.Description,
		Status:          td.Status,
		Categories:      td.Categories,
	}
}

func fromJournal(j journal.Journal) Entry {
	deletedAt := time.Time{}
	if j.DeletedAt != nil {
		deletedAt = *j.DeletedAt
	} else if !j.UpdatedAt.IsZero() {
		deletedAt = j.UpdatedAt
	}
	startDate := time.Time{}
	if j.StartDate != "" {
		if t, err := time.Parse(time.RFC3339, j.StartDate); err == nil {
			startDate = t
		} else if t, err := time.Parse("2006-01-02", j.StartDate); err == nil {
			startDate = t
		}
	}
	return Entry{
		Kind:        KindJournal,
		ID:          j.ID,
		CalendarID:  j.CalendarID,
		UID:         j.UID,
		Title:       j.Summary,
		DeletedAt:   deletedAt,
		StartTime:   startDate,
		Description: j.Description,
		Status:      j.Status,
	}
}

// IsEvent reports whether the entry originates from the events table
// (event row, EXDATE log, or truncation log).
func (k Kind) IsEvent() bool {
	return k == KindEvent || k == KindEventInstance || k == KindEventSeriesTail
}

// IsTodo reports whether the entry is a soft-deleted todos row.
func (k Kind) IsTodo() bool { return k == KindTodo }

// IsJournal reports whether the entry is a soft-deleted journals row.
func (k Kind) IsJournal() bool { return k == KindJournal }

// Label is a short human-facing name for the kind, used in the trash
// dialog's "Kind" detail row.
func (k Kind) Label() string {
	switch k {
	case KindEvent:
		return "Event"
	case KindEventInstance:
		return "Event instance"
	case KindEventSeriesTail:
		return "Series tail"
	case KindTodo:
		return "Todo"
	case KindJournal:
		return "Journal"
	default:
		return "Unknown"
	}
}
