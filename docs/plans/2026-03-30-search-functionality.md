# Search Functionality Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add text search capability for events and todos to find calendar items by title, description, location, or other fields.

**Architecture:** Full-text search via SQL LIKE queries on event/todo tables. Searches across title, description, and location fields. Results filtered by calendar, date range, and status. Simple implementation using SQLite's built-in pattern matching.

**Tech Stack:** SQLC for queries, Cobra for CLI, existing storage layer.

---

## Task 1: Add Search Queries for Events

**Files:**
- Create: `db/queries/search.sql`
- Modify: `internal/event/service.go` (add Search method)

**Step 1: Write SQLC query file**

```sql
-- db/queries/search.sql
-- name: SearchEvents :many
SELECT 
    e.id,
    e.calendar_id,
    c.name as calendar_name,
    e.uid,
    e.title,
    e.description,
    e.location,
    e.start_time,
    e.end_time,
    e.all_day,
    e.status,
    e.priority,
    e.categories,
    e.timezone,
    e.rrule,
    e.created_at,
    e.updated_at
FROM events e
JOIN calendars c ON e.calendar_id = c.id
WHERE (
    e.title LIKE '%' || ?1 || '%' OR
    e.description LIKE '%' || ?1 || '%' OR
    e.location LIKE '%' || ?1 || '%' OR
    e.categories LIKE '%' || ?1 || '%'
)
AND (?2 = 0 OR e.calendar_id = ?2)
AND (?3 = '' OR e.start_time >= ?3)
AND (?4 = '' OR e.start_time <= ?4)
AND (?5 = '' OR e.status = ?5)
ORDER BY e.start_time ASC;
```

**Step 2: Regenerate SQLC code**

Run: `cd db && sqlc generate`

Expected: New SearchEvents query generated in `internal/storage/search.sql.go`

**Step 3: Add SearchParams struct and Search method to event service**

Modify `internal/event/service.go`:

```go
// Add after UpdateParams struct
type SearchParams struct {
    Query      string
    CalendarID int64  // 0 = all calendars
    From       string // RFC3339 or empty
    To         string // RFC3339 or empty
    Status     string // empty = all
}

// Add Search method before helper functions
func (s *Service) Search(ctx context.Context, p SearchParams) ([]Event, error) {
    calendarID := p.CalendarID
    if calendarID == 0 {
        calendarID = 0 // SQLite treats 0 as falsy for the OR condition
    }
    
    rows, err := s.q.SearchEvents(ctx, storage.SearchEventsParams{
        Column1:    p.Query,
        Column2:    calendarID,
        StartTime:  p.From,
        StartTime_2: p.To,
        Status:     p.Status,
    })
    if err != nil {
        return nil, fmt.Errorf("search events: %w", err)
    }
    
    events := make([]Event, len(rows))
    for i, r := range rows {
        events[i] = Event{
            ID:           r.ID,
            CalendarID:   r.CalendarID,
            CalendarName: r.CalendarName,
            UID:          r.Uid,
            Title:        r.Title,
            Description:  r.Description,
            Location:     r.Location,
            StartTime:    parseTime(r.StartTime),
            EndTime:      parseTime(r.EndTime),
            AllDay:       r.AllDay,
            Status:       r.Status,
            Priority:     r.Priority,
            Categories:   r.Categories,
            Timezone:     r.Timezone,
            RecurrenceRule: r.Rrule,
            CreatedAt:    parseTime(r.CreatedAt),
            UpdatedAt:    parseTime(r.UpdatedAt),
        }
    }
    return events, nil
}
```

**Step 4: Write test for Search**

Create `internal/event/service_search_test.go`:

```go
package event

import (
    "context"
    "testing"
    "time"
    
    "github.com/douglasdemoura/tcal/internal/testutil"
)

func TestService_Search(t *testing.T) {
    ctx := context.Background()
    db, cleanup := testutil.SetupTestDB(t)
    defer cleanup()
    
    svc := NewService(db, testutil.NewQueries(db))
    
    // Create a calendar first
    q := testutil.NewQueries(db)
    cal, err := q.CreateCalendar(ctx, "test-cal", "#FF0000", "")
    if err != nil {
        t.Fatalf("create calendar: %v", err)
    }
    
    // Create test events
    _, err = svc.Create(ctx, CreateParams{
        CalendarID: cal.ID,
        Title:      "Budget Meeting Q1",
        Description: "Review quarterly budget",
        Location:   "Conference Room A",
        StartTime:  time.Now().Add(24 * time.Hour),
        EndTime:    time.Now().Add(25 * time.Hour),
    })
    if err != nil {
        t.Fatalf("create event 1: %v", err)
    }
    
    _, err = svc.Create(ctx, CreateParams{
        CalendarID: cal.ID,
        Title:      "Team Lunch",
        Description: "Budget-friendly options",
        Location:   "Cafeteria",
        StartTime:  time.Now().Add(48 * time.Hour),
        EndTime:    time.Now().Add(49 * time.Hour),
    })
    if err != nil {
        t.Fatalf("create event 2: %v", err)
    }
    
    // Test search
    results, err := svc.Search(ctx, SearchParams{Query: "budget"})
    if err != nil {
        t.Fatalf("search: %v", err)
    }
    
    if len(results) != 2 {
        t.Errorf("expected 2 results for 'budget', got %d", len(results))
    }
    
    // Test search by location
    results, err = svc.Search(ctx, SearchParams{Query: "Conference"})
    if err != nil {
        t.Fatalf("search: %v", err)
    }
    
    if len(results) != 1 {
        t.Errorf("expected 1 result for 'Conference', got %d", len(results))
    }
    
    // Test no results
    results, err = svc.Search(ctx, SearchParams{Query: "nonexistent"})
    if err != nil {
        t.Fatalf("search: %v", err)
    }
    
    if len(results) != 0 {
        t.Errorf("expected 0 results for 'nonexistent', got %d", len(results))
    }
}
```

**Step 5: Run tests**

Run: `go test ./internal/event/ -v -run TestService_Search`

Expected: PASS

**Step 6: Commit**

```bash
git add db/queries/search.sql internal/event/service.go internal/event/service_search_test.go
git commit -m "feat(search): add event search queries and service method"
```

---

## Task 2: Add Search Queries for Todos

**Files:**
- Modify: `db/queries/search.sql`
- Modify: `internal/todo/service.go`

**Step 1: Add todo search query**

Add to `db/queries/search.sql`:

```sql
-- name: SearchTodos :many
SELECT 
    t.id,
    t.calendar_id,
    c.name as calendar_name,
    t.uid,
    t.summary,
    t.description,
    t.location,
    t.due_date,
    t.start_date,
    t.duration,
    t.status,
    t.priority,
    t.percent_complete,
    t.completed_at,
    t.categories,
    t.rrule,
    t.created_at,
    t.updated_at
FROM todos t
JOIN calendars c ON t.calendar_id = c.id
WHERE (
    t.summary LIKE '%' || ?1 || '%' OR
    t.description LIKE '%' || ?1 || '%' OR
    t.location LIKE '%' || ?1 || '%' OR
    t.categories LIKE '%' || ?1 || '%'
)
AND (?2 = 0 OR t.calendar_id = ?2)
AND (?3 = '' OR t.status = ?3)
AND (?4 = 0 OR (?4 = 1 AND t.completed_at IS NOT NULL) OR (?4 = 2 AND t.completed_at IS NULL))
ORDER BY t.due_date ASC, t.priority DESC;
```

**Step 2: Regenerate SQLC**

Run: `cd db && sqlc generate`

**Step 3: Add Search to todo service**

Modify `internal/todo/service.go`:

```go
// Add after existing structs
type SearchParams struct {
    Query       string
    CalendarID  int64  // 0 = all
    Status      string // empty = all
    Completed   int    // 0 = all, 1 = completed only, 2 = incomplete only
}

// Add Search method
func (s *Service) Search(ctx context.Context, p SearchParams) ([]Todo, error) {
    calendarID := p.CalendarID
    if calendarID == 0 {
        calendarID = 0
    }
    
    completedFilter := int64(p.Completed)
    
    rows, err := s.q.SearchTodos(ctx, storage.SearchTodosParams{
        Column1:   p.Query,
        Column2:   calendarID,
        Status:    p.Status,
        Column4:   completedFilter,
    })
    if err != nil {
        return nil, fmt.Errorf("search todos: %w", err)
    }
    
    todos := make([]Todo, len(rows))
    for i, r := range rows {
        todos[i] = fromStorageRow(r)
    }
    return todos, nil
}
```

You'll need to create a `fromStorageRow` helper or use existing conversion logic.

**Step 4: Write test**

Create `internal/todo/service_search_test.go`:

```go
package todo

import (
    "context"
    "testing"
    
    "github.com/douglasdemoura/tcal/internal/testutil"
)

func TestService_Search(t *testing.T) {
    ctx := context.Background()
    db, cleanup := testutil.SetupTestDB(t)
    defer cleanup()
    
    svc := NewService(db, testutil.NewQueries(db))
    q := testutil.NewQueries(db)
    
    // Create calendar
    cal, err := q.CreateCalendar(ctx, "test-cal", "#FF0000", "")
    if err != nil {
        t.Fatalf("create calendar: %v", err)
    }
    
    // Create todos
    _, err = svc.Create(ctx, CreateParams{
        CalendarID: cal.ID,
        Summary:    "Buy groceries",
        Description: "Milk, eggs, budget items",
    })
    if err != nil {
        t.Fatalf("create todo 1: %v", err)
    }
    
    _, err = svc.Create(ctx, CreateParams{
        CalendarID: cal.ID,
        Summary:    "Review budget proposal",
        Description: "Q1 financial review",
    })
    if err != nil {
        t.Fatalf("create todo 2: %v", err)
    }
    
    // Search
    results, err := svc.Search(ctx, SearchParams{Query: "budget"})
    if err != nil {
        t.Fatalf("search: %v", err)
    }
    
    if len(results) != 2 {
        t.Errorf("expected 2 results for 'budget', got %d", len(results))
    }
    
    // Search no results
    results, err = svc.Search(ctx, SearchParams{Query: "nonexistent"})
    if err != nil {
        t.Fatalf("search: %v", err)
    }
    
    if len(results) != 0 {
        t.Errorf("expected 0 results for 'nonexistent', got %d", len(results))
    }
}
```

**Step 5: Run tests**

Run: `go test ./internal/todo/ -v -run TestService_Search`

Expected: PASS

**Step 6: Commit**

```bash
git add db/queries/search.sql internal/todo/service.go internal/todo/service_search_test.go
git commit -m "feat(search): add todo search queries and service method"
```

---

## Task 3: Add CLI Command for Event Search

**Files:**
- Modify: `cmd/tcal/event.go`

**Step 1: Add search subcommand**

Add after existing event subcommands in `cmd/tcal/event.go`:

```go
func init() {
    // ... existing init code ...
    
    // Add search command
    searchCmd := &cobra.Command{
        Use:   "search <query>",
        Short: "Search events by title, description, or location",
        Long:  `Search for events containing the query string in title, description, location, or categories.`,
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            a, err := initApp()
            if err != nil {
                return err
            }
            defer a.Close()
            
            calendarID, _ := cmd.Flags().GetInt64("calendar")
            from, _ := cmd.Flags().GetString("from")
            to, _ := cmd.Flags().GetString("to")
            status, _ := cmd.Flags().GetString("status")
            
            events, err := a.Events.Search(cmd.Context(), event.SearchParams{
                Query:      args[0],
                CalendarID: calendarID,
                From:       from,
                To:         to,
                Status:     status,
            })
            if err != nil {
                return err
            }
            
            return printEvents(events, outputFmt)
        },
    }
    
    searchCmd.Flags().Int64P("calendar", "c", 0, "calendar ID (0 = all calendars)")
    searchCmd.Flags().String("from", "", "start date filter (RFC3339)")
    searchCmd.Flags().String("to", "", "end date filter (RFC3339)")
    searchCmd.Flags().String("status", "", "status filter (TENTATIVE, CONFIRMED, CANCELLED)")
    
    eventCmd.AddCommand(searchCmd)
}
```

Note: Update the existing init() function or merge with existing flag setup.

**Step 2: Test CLI**

Build and test:
```bash
go build -o tcal ./cmd/tcal
./tcal event add "Budget Meeting" --date 2026-04-01
./tcal event search "budget"
```

Expected: Shows the budget meeting event

**Step 3: Commit**

```bash
git add cmd/tcal/event.go
git commit -m "feat(cli): add event search command"
```

---

## Task 4: Add CLI Command for Todo Search

**Files:**
- Modify: `cmd/tcal/todo.go`

**Step 1: Add search subcommand**

Add to `cmd/tcal/todo.go`:

```go
func init() {
    // ... existing init code ...
    
    searchCmd := &cobra.Command{
        Use:   "search <query>",
        Short: "Search todos by summary, description, or location",
        Long:  `Search for todos containing the query string in summary, description, location, or categories.`,
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            a, err := initApp()
            if err != nil {
                return err
            }
            defer a.Close()
            
            calendarID, _ := cmd.Flags().GetInt64("calendar")
            status, _ := cmd.Flags().GetString("status")
            completed, _ := cmd.Flags().GetBool("completed")
            incomplete, _ := cmd.Flags().GetBool("incomplete")
            
            completedFilter := 0 // all
            if completed {
                completedFilter = 1
            } else if incomplete {
                completedFilter = 2
            }
            
            todos, err := a.Todos.Search(cmd.Context(), todo.SearchParams{
                Query:      args[0],
                CalendarID: calendarID,
                Status:     status,
                Completed:  completedFilter,
            })
            if err != nil {
                return err
            }
            
            return printTodos(todos, outputFmt)
        },
    }
    
    searchCmd.Flags().Int64P("calendar", "c", 0, "calendar ID (0 = all)")
    searchCmd.Flags().String("status", "", "status filter (NEEDS-ACTION, IN-PROCESS, COMPLETED, CANCELLED)")
    searchCmd.Flags().Bool("completed", false, "show only completed todos")
    searchCmd.Flags().Bool("incomplete", false, "show only incomplete todos")
    
    todoCmd.AddCommand(searchCmd)
}
```

**Step 2: Test CLI**

```bash
go build -o tcal ./cmd/tcal
./tcal todo add "Buy groceries" --due 2026-04-01
./tcal todo search "groceries"
```

Expected: Shows the grocery todo

**Step 3: Commit**

```bash
git add cmd/tcal/todo.go
git commit -m "feat(cli): add todo search command"
```

---

## Task 5: Update Documentation

**Files:**
- Modify: `.reference/TODO.md`

Add search to feature list:

```markdown
## Features

### Implemented
- ...existing features...
- **Search**: Text search for events and todos by title, description, location, categories
```

**Step 1: Update documentation**

**Step 2: Commit**

```bash
git add .reference/TODO.md
git commit -m "docs: document search feature"
```

---

## Summary

This implementation adds:
1. SQL queries for searching events and todos
2. Service layer methods for search
3. CLI commands: `tcal event search <query>` and `tcal todo search <query>`
4. Filters: calendar, date range, status

**Usage Examples:**
```bash
# Search events
tcal event search "budget"
tcal event search "meeting" --calendar=1 --status=CONFIRMED
tcal event search "review" --from=2026-01-01 --to=2026-03-31

# Search todos
tcal todo search "urgent"
tcal todo search "call" --incomplete
tcal todo search "project" --status=IN-PROCESS
```
