# Bulk Export with Filters Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add filtering capabilities to the iCal export command to export subsets of calendar data (date ranges, categories, calendars, status).

**Architecture:** Extend existing export queries with WHERE clause filters. Use SQLC parameterized queries for calendar ID, date range, and category filters. Stream results to ICS output to handle large datasets efficiently.

**Tech Stack:** SQLC for queries, existing ical export package, Cobra for CLI.

---

## Task 1: Add Filtered Event Export Query

**Files:**
- Modify: `db/queries/events.sql`

**Step 1: Add filtered export query**

Add to `db/queries/events.sql`:

```sql
-- name: ListEventsForExport :many
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
    e.transp,
    e.priority,
    e.class,
    e.sequence,
    e.url,
    e.categories,
    e.geo,
    e.timezone,
    e.rrule,
    e.exdates,
    e.rdates,
    e.recurrence_id,
    e.created_at,
    e.updated_at
FROM events e
JOIN calendars c ON e.calendar_id = c.id
WHERE 
    (?1 = 0 OR e.calendar_id = ?1)
    AND (?2 = '' OR e.start_time >= ?2)
    AND (?3 = '' OR e.start_time <= ?3)
    AND (?4 = '' OR e.categories LIKE '%' || ?4 || '%')
    AND (?5 = '' OR e.status = ?5)
ORDER BY e.start_time ASC;
```

**Step 2: Regenerate SQLC**

Run: `cd db && sqlc generate`

Expected: New `ListEventsForExport` query generated

**Step 3: Add ExportParams and ExportFiltered method to event service**

Modify `internal/event/service.go`:

```go
// Add after SearchParams
type ExportParams struct {
    CalendarID int64  // 0 = all
    From       string // RFC3339 or empty
    To         string // RFC3339 or empty
    Category   string // empty = all
    Status     string // empty = all
}

// Add ExportFiltered method
func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Event, error) {
    calendarID := p.CalendarID
    if calendarID == 0 {
        calendarID = 0
    }
    
    rows, err := s.q.ListEventsForExport(ctx, storage.ListEventsForExportParams{
        Column1:    calendarID,
        StartTime:  p.From,
        StartTime_2: p.To,
        Column4:    p.Category,
        Status:     p.Status,
    })
    if err != nil {
        return nil, fmt.Errorf("export events: %w", err)
    }
    
    events := make([]Event, len(rows))
    for i, r := range rows {
        events[i] = fromStorage(r) // Use existing helper
    }
    return events, nil
}
```

**Step 4: Write test**

Create `internal/event/service_export_test.go`:

```go
package event

import (
    "context"
    "testing"
    "time"
    
    "github.com/douglasdemoura/tcal/internal/testutil"
)

func TestService_ExportFiltered(t *testing.T) {
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
    
    // Create events at different times
    base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
    
    _, err = svc.Create(ctx, CreateParams{
        CalendarID: cal.ID,
        Title:      "Q1 Meeting",
        Categories: "work,q1",
        StartTime:  base,
        EndTime:    base.Add(time.Hour),
    })
    if err != nil {
        t.Fatalf("create event 1: %v", err)
    }
    
    _, err = svc.Create(ctx, CreateParams{
        CalendarID: cal.ID,
        Title:      "Q2 Planning",
        Categories: "work,q2",
        StartTime:  base.AddDate(0, 3, 0),
        EndTime:    base.AddDate(0, 3, 0).Add(time.Hour),
    })
    if err != nil {
        t.Fatalf("create event 2: %v", err)
    }
    
    // Test export all
    results, err := svc.ExportFiltered(ctx, ExportParams{})
    if err != nil {
        t.Fatalf("export all: %v", err)
    }
    if len(results) != 2 {
        t.Errorf("expected 2 events, got %d", len(results))
    }
    
    // Test export by category
    results, err = svc.ExportFiltered(ctx, ExportParams{Category: "q1"})
    if err != nil {
        t.Fatalf("export by category: %v", err)
    }
    if len(results) != 1 {
        t.Errorf("expected 1 event for category 'q1', got %d", len(results))
    }
    
    // Test export by date range
    from := base.AddDate(0, 2, 0).Format(time.RFC3339)
    to := base.AddDate(0, 4, 0).Format(time.RFC3339)
    results, err = svc.ExportFiltered(ctx, ExportParams{From: from, To: to})
    if err != nil {
        t.Fatalf("export by date: %v", err)
    }
    if len(results) != 1 {
        t.Errorf("expected 1 event in date range, got %d", len(results))
    }
}
```

**Step 5: Run tests**

Run: `go test ./internal/event/ -v -run TestService_ExportFiltered`

Expected: PASS

**Step 6: Commit**

```bash
git add db/queries/events.sql internal/event/service.go internal/event/service_export_test.go
git commit -m "feat(export): add filtered event export query and service method"
```

---

## Task 2: Add Filtered Todo Export Query

**Files:**
- Modify: `db/queries/todos.sql`

**Step 1: Add filtered export query**

Add to `db/queries/todos.sql`:

```sql
-- name: ListTodosForExport :many
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
    t.class,
    t.sequence,
    t.url,
    t.categories,
    t.geo,
    t.rrule,
    t.exdates,
    t.rdates,
    t.recurrence_id,
    t.created_at,
    t.updated_at
FROM todos t
JOIN calendars c ON t.calendar_id = c.id
WHERE 
    (?1 = 0 OR t.calendar_id = ?1)
    AND (?2 = '' OR t.categories LIKE '%' || ?2 || '%')
    AND (?3 = '' OR t.status = ?3)
    AND (?4 = 0 OR (?4 = 1 AND t.completed_at IS NOT NULL) OR (?4 = 2 AND t.completed_at IS NULL))
ORDER BY t.due_date ASC;
```

**Step 2: Regenerate SQLC**

Run: `cd db && sqlc generate`

**Step 3: Add ExportFiltered to todo service**

Modify `internal/todo/service.go`:

```go
// Add after SearchParams
type ExportParams struct {
    CalendarID int64  // 0 = all
    Category   string // empty = all
    Status     string // empty = all
    Completed  int    // 0 = all, 1 = completed, 2 = incomplete
}

// Add ExportFiltered method
func (s *Service) ExportFiltered(ctx context.Context, p ExportParams) ([]Todo, error) {
    calendarID := p.CalendarID
    if calendarID == 0 {
        calendarID = 0
    }
    
    completedFilter := int64(p.Completed)
    
    rows, err := s.q.ListTodosForExport(ctx, storage.ListTodosForExportParams{
        Column1:  calendarID,
        Column2:  p.Category,
        Status:   p.Status,
        Column4:  completedFilter,
    })
    if err != nil {
        return nil, fmt.Errorf("export todos: %w", err)
    }
    
    todos := make([]Todo, len(rows))
    for i, r := range rows {
        todos[i] = fromStorageRow(r)
    }
    return todos, nil
}
```

**Step 4: Write test**

Create `internal/todo/service_export_test.go`:

```go
package todo

import (
    "context"
    "testing"
    
    "github.com/douglasdemoura/tcal/internal/testutil"
)

func TestService_ExportFiltered(t *testing.T) {
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
        Summary:    "Work task 1",
        Categories: "work",
    })
    if err != nil {
        t.Fatalf("create todo 1: %v", err)
    }
    
    _, err = svc.Create(ctx, CreateParams{
        CalendarID: cal.ID,
        Summary:    "Personal task",
        Categories: "personal",
    })
    if err != nil {
        t.Fatalf("create todo 2: %v", err)
    }
    
    // Test export by category
    results, err := svc.ExportFiltered(ctx, ExportParams{Category: "work"})
    if err != nil {
        t.Fatalf("export by category: %v", err)
    }
    if len(results) != 1 {
        t.Errorf("expected 1 todo for category 'work', got %d", len(results))
    }
    
    // Test export incomplete only
    results, err = svc.ExportFiltered(ctx, ExportParams{Completed: 2})
    if err != nil {
        t.Fatalf("export incomplete: %v", err)
    }
    if len(results) != 2 {
        t.Errorf("expected 2 incomplete todos, got %d", len(results))
    }
}
```

**Step 5: Run tests**

Run: `go test ./internal/todo/ -v -run TestService_ExportFiltered`

Expected: PASS

**Step 6: Commit**

```bash
git add db/queries/todos.sql internal/todo/service.go internal/todo/service_export_test.go
git commit -m "feat(export): add filtered todo export query and service method"
```

---

## Task 3: Update CLI Export Command with Filters

**Files:**
- Modify: `cmd/tcal/ical.go`

**Step 1: Update export command with filter flags**

Find the export command in `cmd/tcal/ical.go` and add flags:

```go
func init() {
    // ... existing init code ...
    
    exportCmd := &cobra.Command{
        Use:   "export",
        Short: "Export calendar to iCal (.ics) format",
        Long:  `Export events and todos to an iCal (.ics) file with optional filters.`,
        RunE: func(cmd *cobra.Command, args []string) error {
            a, err := initApp()
            if err != nil {
                return err
            }
            defer a.Close()
            
            output, _ := cmd.Flags().GetString("output")
            calendarID, _ := cmd.Flags().GetInt64("calendar")
            from, _ := cmd.Flags().GetString("from")
            to, _ := cmd.Flags().GetString("to")
            category, _ := cmd.Flags().GetString("category")
            status, _ := cmd.Flags().GetString("status")
            includeEvents, _ := cmd.Flags().GetBool("events")
            includeTodos, _ := cmd.Flags().GetBool("todos")
            
            // Default to including both
            if !includeEvents && !includeTodos {
                includeEvents = true
                includeTodos = true
            }
            
            ctx := cmd.Context()
            
            // Get filtered events
            var events []event.Event
            if includeEvents {
                events, err = a.Events.ExportFiltered(ctx, event.ExportParams{
                    CalendarID: calendarID,
                    From:       from,
                    To:         to,
                    Category:   category,
                    Status:     status,
                })
                if err != nil {
                    return fmt.Errorf("export events: %w", err)
                }
            }
            
            // Get filtered todos
            var todos []todo.Todo
            if includeTodos {
                todos, err = a.Todos.ExportFiltered(ctx, todo.ExportParams{
                    CalendarID: calendarID,
                    Category:   category,
                    Status:     status,
                })
                if err != nil {
                    return fmt.Errorf("export todos: %w", err)
                }
            }
            
            // Generate ICS
            ics, err := ical.Export(ical.ExportData{
                Events: events,
                Todos:  todos,
            })
            if err != nil {
                return fmt.Errorf("generate ics: %w", err)
            }
            
            // Write to file or stdout
            if output == "-" || output == "" {
                fmt.Print(ics)
            } else {
                if err := os.WriteFile(output, []byte(ics), 0644); err != nil {
                    return fmt.Errorf("write file: %w", err)
                }
                fmt.Printf("Exported %d events and %d todos to %s\n", len(events), len(todos), output)
            }
            
            return nil
        },
    }
    
    exportCmd.Flags().StringP("output", "o", "", "output file (default: stdout)")
    exportCmd.Flags().Int64P("calendar", "c", 0, "calendar ID (0 = all calendars)")
    exportCmd.Flags().String("from", "", "export events from this date (RFC3339)")
    exportCmd.Flags().String("to", "", "export events until this date (RFC3339)")
    exportCmd.Flags().String("category", "", "filter by category")
    exportCmd.Flags().String("status", "", "filter by status")
    exportCmd.Flags().Bool("events", false, "include events (default: true if neither specified)")
    exportCmd.Flags().Bool("todos", false, "include todos (default: true if neither specified)")
    
    icalCmd.AddCommand(exportCmd)
}
```

**Step 2: Test CLI**

Build and test:
```bash
go build -o tcal ./cmd/tcal
./tcal event add "Work Meeting" --date 2026-04-01 --categories work
./tcal event add "Personal Event" --date 2026-04-02 --categories personal
./tcal ical export -o filtered.ics --category work
./tcal ical export -o q1.ics --from 2026-01-01T00:00:00Z --to 2026-03-31T23:59:59Z
```

Expected: Filtered ICS files created

**Step 3: Commit**

```bash
git add cmd/tcal/ical.go
git commit -m "feat(cli): add filter flags to ical export command"
```

---

## Task 4: Add Import Filters

**Files:**
- Modify: `cmd/tcal/ical.go`

**Step 1: Add filter flags to import command**

Add to import command:

```go
importCmd.Flags().Int64P("calendar", "c", 0, "target calendar ID (0 = default)")
importCmd.Flags().String("category", "", "add category to all imported items")
importCmd.Flags().Bool("skip-events", false, "skip importing events")
importCmd.Flags().Bool("skip-todos", false, "skip importing todos")
```

Update the import handler to apply these filters when creating events/todos.

**Step 2: Test**

```bash
./tcal ical import calendar.ics -c 1 --category imported
./tcal ical import events.ics --skip-todos
```

**Step 3: Commit**

```bash
git add cmd/tcal/ical.go
git commit -m "feat(cli): add filter flags to ical import command"
```

---

## Summary

This implementation adds:
1. SQL queries for filtered export of events and todos
2. Service layer methods for filtered export
3. CLI flags for `tcal ical export`:
   - `--calendar` - Filter by calendar
   - `--from` / `--to` - Date range filter (events)
   - `--category` - Category filter
   - `--status` - Status filter
   - `--events` / `--todos` - Component type filter
4. CLI flags for `tcal ical import`:
   - `--calendar` - Target calendar
   - `--category` - Add category to imports
   - `--skip-events` / `--skip-todos` - Component filter

**Usage Examples:**
```bash
# Export work calendar Q1 events only
tcal ical export -o q1-work.ics --calendar=1 --from=2026-01-01 --to=2026-03-31 --events

# Export all meetings
tcal ical export -o meetings.ics --category=meeting

# Export incomplete todos only
tcal ical export -o todos.ics --todos --status=NEEDS-ACTION

# Import with category
tcal ical import holidays.ics --category=holidays
```
