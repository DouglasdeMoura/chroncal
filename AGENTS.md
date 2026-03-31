# Agent Guide for chroncal

## Service Layer Pattern

Each domain (event, todo, calendar, alarm, recurrence) has a service in `internal/{domain}/`:

```go
type Service struct {
    db *sql.DB
    q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
    return &Service{db: db, q: q}
}
```

- **event** - CRUD, search, export, recurrence-aware queries
- **todo** - CRUD, search, completion
- **calendar** - CRUD, color management
- **alarm** - Check due alarms, fire, dismiss, snooze
- **recurrence** - Expand recurring events/todos, handle overrides

Models live in `internal/{domain}/model.go` (e.g., `event.Event`) and shared models in `internal/model/` (e.g., `model.Alarm`, `model.Attendee`).

CLI commands live in `cmd/chroncal/`, one file per resource group. Each exports a `Command()` function returning a `*cobra.Command`. Commands use `resolveEvent()` / `resolveTodo()` helpers to resolve references by ID, UID, or UID+recurrenceID.

## Storage Layer

- `internal/storage/connect.go` is the only hand-written file. Everything else in `internal/storage/` is sqlc-generated and will be overwritten by `make generate`.
- **Never edit `*.sql.go` files or `db.go` or `models.go` directly.**
- Add new queries to `db/queries/*.sql`, then run `make generate`.
- After schema changes: add a migration to `db/migrations/`, update queries, then regenerate.
- Transaction pattern: `q.WithTx(tx)` inside a transaction.

## Gotchas

### Database
- `lower_unicode` is a custom SQLite function registered in `connect.go` for case-insensitive Unicode search. SQL queries reference it directly.
- `backfillAlarmUIDs` in `connect.go` assigns UUIDs to alarms from the pre-UID schema. Runs on every startup, no-ops when all alarms have UIDs.
- SQLite pragmas set in `connect.go:Open()`: WAL mode, foreign keys ON, 5s busy timeout, synchronous=NORMAL.

### Recurrence
- Recurring events are stored as a single row with `recurrence_rule`.
- Overrides are separate rows with the same `uid` but a non-empty `recurrence_id`.
- EXDATEs and RDATEs are comma-separated RFC 3339 strings.
- Expansion happens at query time via `recurrence.ListExpandedEvents()`.
- Half-open time ranges everywhere: `[start, end)`.

### Alarms
- Triggers are RFC 5545 duration strings (`-PT15M` = 15 minutes before).
- Absolute triggers use RFC 3339.
- State is tracked in `alarm_state` / `todo_alarm_state` tables (fired_at, acknowledged_at, snooze_until).
- Alarms older than 24h are skipped (`alarm.StaleThreshold`).
- Repeat logic: additional firings at `Duration` intervals up to `Repeat` count.

### iCal Round-Trip
- UID is required for round-trip fidelity.
- `recurrence_id` distinguishes overridden instances.
- Transient fields (Alarms, Attendees, etc.) are populated for export but not stored in the main event/todo tables.
- Duration can be expressed as either DTEND or DURATION (RFC 5545).
- Timezones are preserved via the `timezone` column and the `timezones` table.

### Time Handling
- All database times are RFC 3339 strings in UTC.
- Go code uses `time.Time` with `time.UTC`.
- All-day events have time component 00:00:00.

## Common Tasks

### Find an event by ID or UID
```go
evt, err := svc.Get(ctx, id)                                        // numeric ID
evt, err := svc.GetByUID(ctx, uid)                                  // string UID
evt, err := svc.GetByUIDAndRecurrenceID(ctx, uid, recurrenceID)     // override instance
```

### Query events in date range
```go
from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
to := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
events, err := svc.ListByDateRange(ctx, from, to)
```

### Handle recurring events
```go
recurSvc := recurrence.NewService(db, q)
expanded, err := recurSvc.ListExpandedEvents(ctx, from, to)
// Each ExpandedEvent has: Event, InstanceTime, IsOverride
```

### Create an alarm
```go
alarm := model.Alarm{
    Action:       "DISPLAY",
    TriggerValue: "-PT15M",
    Description:  "Meeting reminder",
    Related:      "START",
}
err := evtSvc.ReplaceAlarms(ctx, eventID, []model.Alarm{alarm})
```

### Check due alarms
```go
alarmSvc := alarm.NewService(db, q, eventSvc, todoSvc)
dueEvents, dueTodos, err := alarmSvc.Check(ctx, time.Now())
// Each DueAlarm has: Event, Alarm, TriggerAt, StateID
```

### Import/Export iCal
```go
events, todos, err := ical.Import(data, calendarID)

params := event.ExportParams{CalendarID: 1, From: "2026-04-01T00:00:00Z", To: "2026-04-30T23:59:59Z"}
events, err := svc.ListForExport(ctx, params)
ics, err := ical.Export(events, todos, "chroncal@example.com")
```

### Parse RFC 5545 duration
```go
err := duration.Validate("-PT15M")
newTime := duration.Add(time.Now(), "-PT15M")
durStr := duration.FromGo(15 * time.Minute)  // "PT15M"
```

## Skill routing

When the user's request matches an available skill, ALWAYS invoke it using the Skill
tool as your FIRST action. Do NOT answer directly, do NOT use other tools first.
The skill has specialized workflows that produce better results than ad-hoc answers.

Key routing rules:
- Product ideas, "is this worth building", brainstorming → invoke office-hours
- Bugs, errors, "why is this broken", 500 errors → invoke investigate
- Ship, deploy, push, create PR → invoke ship
- QA, test the site, find bugs → invoke qa
- Code review, check my diff → invoke review
- Update docs after shipping → invoke document-release
- Weekly retro → invoke retro
- Design system, brand → invoke design-consultation
- Visual audit, design polish → invoke design-review
- Architecture review → invoke plan-eng-review
