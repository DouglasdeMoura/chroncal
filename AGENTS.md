# Agent Guide for chroncal

## Service Layer Pattern

Each domain has a service in `internal/{domain}/`. The service uses this shape:

```go
type Service struct {
    db *sql.DB
    q  *storage.Queries
}

func NewService(db *sql.DB, q *storage.Queries) *Service {
    return &Service{db: db, q: q}
}
```

Core data services:

- **event** - CRUD, search, export, recurrence queries, soft-delete, restore, purge
- **todo** - CRUD, search, completion, soft-delete, restore, purge
- **journal** - CRUD, search, soft-delete, restore, purge
- **calendar** - CRUD, color control, remote-link metadata
- **alarm** - Check due alarms. Fire, dismiss, and snooze alarms
- **recurrence** - Expand recurring events, todos, and journals. Apply overrides
- **trash** - Mixed soft-delete view of events, todos, and journals (list, restore, purge)

Integration packages and infrastructure packages do not use the `NewService` shape above. Each package has its own constructor:

- **sync** - CalDAV sync engine. Detect and resolve conflicts (`NewService` with extra dependencies). Write paths push through `PushLocalEdits`; a 412 records a conflict and keeps the edit dirty. The conflict strategy governs full passes only. A resolved conflict row stays, so `sync resolve --pick local` can restore the recorded local body.
- **caldav** - Low-level CalDAV client (discovery, REPORT, PROPFIND, VFREEBUSY). Constructor: `NewClient`
- **freebusy** - Local free/busy results plus a remote CalDAV query. Plain functions (`Compute`)
- **auth** - Credential store (OS keyring, optional plaintext). OAuth2 PKCE. Plain functions
- **maintenance** - Background purge loop for soft-deleted rows. Constructor: `NewPurger`
- **notify** - Desktop notifications plus SMTP email for EMAIL alarms. Plain functions (`Display`, `Audio`, `Email`)
- **retry** - HTTP retry and backoff helpers for sync and caldav. Plain functions

Models live in `internal/{domain}/model.go` (for example, `event.Event`). Shared models live in `internal/model/` (for example, `model.Alarm`, `model.Attendee`).

CLI commands live in `cmd/chroncal/`. Each resource group has one file that exports a `Command()` function. The function returns a `*cobra.Command`. `event_rsvp.go` is the one exception. It holds the RSVP subcommand, and `event.go` wires it into the event command. `tui_open.go` is not a resource group file. It resolves the root `--event`, `--at`, and `--recurrence-id` flags for a TUI launch. Commands use `resolveEvent()`, `resolveTodo()`, and `resolveJournal()` to resolve a reference by ID, UID, or UID plus recurrenceID.

## Storage Layer

Hand-written files in `internal/storage/`:

- `connect.go` (database setup)
- `nullable.go` (helpers)
- `query_builder.go` (dynamic WHERE clauses)
- `scan_helpers.go` (row scanners)
- `events_dynamic.go` and `todos_dynamic.go` (filtered query methods)
- `xprop_helpers.go` (alarm X-property attach and replace for event and todo services)

sqlc generates every other file in that directory. `make generate` overwrites those files.

The dynamic query files replace the sqlc pattern `arg = '' OR column = arg`. They build the WHERE clause at run time so SQLite can use indexes. Queries use `SELECT *`. When a migration adds columns to `events` or `todos`, update the scan functions in `scan_helpers.go` so they match.

- Do not edit `*.sql.go` files, `db.go`, or `models.go`.
- Add new queries to `db/queries/*.sql`.
- Then run `make generate`.

After a schema change:

1. Add a migration to `db/migrations/`.
2. Update the queries.
3. Then regenerate.

Use `q.WithTx(tx)` inside a transaction.

## Special cases

### Database

Case-insensitive Unicode search uses FTS5 (`unicode61 remove_diacritics 2` tokenizer). See the `*_fts` virtual tables in `db/migrations/`. The code does not register a custom `lower_unicode` SQLite function. The project removed an unused registration.

Do not add `strings.ToLower` case folding. That fold is simple case folding only. It does not match the diacritic-insensitive FTS tokenizer.

`Open` in `connect.go` composes four phases: `openDB` in `open.go` (connection settings), `runMigrations` in `migrate.go`, `ensureCredentialNamespace` in `credentials.go`, and the heal steps. The declared `healSteps` slice in `heal.go` holds every startup heal in run order. Each heal is idempotent and lives in its own file. `backfillAlarmUIDs` in `heal_alarm_uids.go` assigns UUIDs to alarms from the pre-UID schema. The function runs on every startup. It does nothing when all alarms have UIDs.

SQLite pragmas in `open.go:openDB()`:

- WAL mode
- foreign keys ON
- 5s busy timeout
- synchronous=NORMAL

### Recurrence

The database stores a recurring event as one row with `recurrence_rule`. Each override is a separate row with the same `uid` and a non-empty `recurrence_id`. The unique key is `(calendar_id, uid, recurrence_id)`, so two calendars can store the same UID. EXDATEs and RDATEs are comma-separated RFC 3339 strings. The service expands recurrences at query time with `recurrence.ListExpandedEvents()`. Time ranges are half-open everywhere: `[start, end)`.

### Alarms

Triggers are RFC 5545 duration strings (`-PT15M` = 15 minutes before). Absolute triggers use RFC 3339. The `alarm_state` and `todo_alarm_state` tables store the state (`fired_at`, `acknowledged_at`, `snooze_until`). The service skips alarms older than 24 hours (`alarm.StaleThreshold`). The service fires extra alarms at `Duration` intervals, up to the `Repeat` count.

### iCal Round-Trip

UID is required for round-trip fidelity. `recurrence_id` marks an overridden instance. Export fills transient fields (Alarms, Attendees, and others). The main event and todo tables do not store those fields. You can express duration as DTEND or as DURATION (RFC 5545). The `timezone` column and the `timezones` table preserve timezones.

### Time

All database times are RFC 3339 strings in UTC. Go code uses `time.Time` with `time.UTC`. An all-day event has the time component 00:00:00.

### Logs

Code that can run while the TUI owns the terminal must never write to stderr. Bubble Tea runs in the alternate screen. A write to stderr prints over the display. This includes `slog.Default()`.

If you pass a nil `*slog.Logger` to `maintenance.NewPurger` or `sync.NewEngine`, those constructors stay silent. The constructors use `slog.New(slog.DiscardHandler)`. They do not use `slog.Default()`. Keep that contract when you add constructors. Regression tests in `internal/maintenance/purge_test.go` and `internal/sync/engine_test.go` guard it.

The TUI background purge loop writes logs to the state-dir file `$XDG_STATE_HOME/chroncal/chroncal.log`. The call is `purgeLogger()` in `cmd/chroncal/main.go`. The path comes from `config.LogFilePath()`. Send other TUI background jobs that need durable logs to that file. CLI commands that need visible logs pass an explicit stderr logger (see `sync run`).

### TUI Buttons

There are exactly two variants: `Button` (neutral default) and `ButtonDanger` (destructive). There is no Primary, no Secondary, and no Ghost.

`ButtonDanger` at rest shares the same pill and background as `Button`. Only the label is bold red, derived from `Theme.Error`. A lightness clamp keeps the label at least 0.25 OKLCh L from the pill and at or above 3:1 contrast (bold text). On focus, Danger inverts (red background, contrast foreground). It does not use `FormHighlight`. Some themes have a warm or red focus highlight. Red text on that highlight is not readable.

Put the red on the background. Compute a contrast label with `oklch.ContrastingFg`. That pair stays readable on every theme. It also shows the destructive signal when the user is about to commit.

Color carries one signal: destructive or not. Focus highlight carries the other signal: which button Enter triggers. Do not mix those two signals.

`Form.SetSubmitVariant` defaults to `Button`. Only a destructive prompt needs to opt in with `ConfirmDialogModel.Destructive()`.

For a custom button that does not use `Form`, render with `ButtonStyles.Normal` (or `.Danger` for a destructive button). Do not use a more prominent style. There is no such style.

Confirm dialogs focus Cancel by default (`form.FocusCancel()`). A quick Enter cancels. It does not confirm. Keep that behavior when you build a new destructive prompt.

## Common Tasks

### Find an event by ID or UID

```go
evt, err := svc.Get(ctx, id)                                        // numeric ID
evt, err := svc.GetByUID(ctx, uid)                                  // string UID
evt, err := svc.GetByCalendarUID(ctx, calendarID, uid)              // UID on one calendar
evt, err := svc.GetByUIDAndRecurrenceID(ctx, uid, recurrenceID)     // override instance
```

### Query events in a date range

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

### Import and export iCal

```go
result, err := ical.ImportFile(r) // r is io.Reader
// result.Events, result.Todos, result.Timezones, result.Warnings

params := event.ExportParams{CalendarID: 1, From: "2026-04-01T00:00:00Z", To: "2026-04-30T23:59:59Z"}
events, err := svc.ExportFiltered(ctx, params)
ics, err := ical.ExportEvents(events, "Work")
ics, err := ical.ExportTodos(todos, "Work")
```

### Parse an RFC 5545 duration

```go
err := duration.Validate("-PT15M")
newTime := duration.Add(time.Now(), "-PT15M")
durStr := duration.FromGo(15 * time.Minute)  // "PT15M"
```

### Soft-delete, restore, and purge

Events, todos, and journals share the same reversible-delete contract. `Delete` and `DeleteSeries` set `deleted_at`. Live reads use `deleted_at IS NULL`. Each domain service owns its own restore and purge:

```go
// Soft-delete: the Delete methods already do this.
err := svc.Delete(ctx, id)        // sets deleted_at, keeps row
err := svc.DeleteSeries(ctx, uid) // soft-deletes master + overrides

// Recurring delete scopes (event service only):
err := svc.DeleteInstance(ctx, uid, at)     // one occurrence: EXDATE on the master + override soft-delete
err := svc.DeleteFromInstance(ctx, uid, at) // truncates the series (RRULE UNTIL) at that time

// Restore:
err := svc.RestoreByID(ctx, id)    // un-hides one row
err := svc.RestoreByUID(ctx, uid)  // un-hides master + all overrides
// Returns svc.ErrNotDeleted when the row is live or missing.

// Purge (hard-delete soft-deleted rows):
err := svc.PurgeByID(ctx, id)         // one row, refuses live rows
n, err := svc.PurgeDeleted(ctx, cutoff) // all rows older than cutoff
```

When you restore a recurring override, the same transaction also clears the related EXDATE on the master. Expansion then shows the occurrence again.

The EXDATE-provenance rule lives in `softdelete.ClearMasterEXDATE`. Strip only EXDATEs that a delete recorded. Do not strip imported EXDATEs (issue #86). Each domain `clearMasterEXDATE` is a thin wrapper that binds the sqlc queries of that domain to the shared helper. Fix the contract in the shared helper. Do not fix it per domain.

### List or purge mixed trash

The `internal/trash` package joins all three domains:

```go
trashSvc := trash.NewService(a.Events, a.Todos, a.Journals)
entries, err := trashSvc.List(ctx, calendarID) // newest-first, all kinds
err = trashSvc.Restore(ctx, entries[0])
err = trashSvc.Purge(ctx, entries[0])
counts, err := trashSvc.PurgeOld(ctx, time.Now().Add(-30*24*time.Hour))
```

`Entry.Kind` (KindEvent, KindEventInstance, KindEventSeriesTail, KindTodo, KindJournal) tells the caller which fields have values.

## AI-assisted contributions

Follow the [Linux kernel AI coding assistant guidelines](https://docs.kernel.org/process/coding-assistants.html) for any AI-assisted commit in this repo.

- The human contributor is the sole git author. AI tools are not authors. Do not put AI tools in `Co-authored-by` trailers.
- AI agents must **not** add `Signed-off-by` tags.
- When AI assistance has a material effect on a commit, add an `Assisted-by` trailer to the commit message body (not the author field):

  ```
  Assisted-by: AGENT_NAME:MODEL_VERSION [TOOL1] [TOOL2]
  ```

  Use the real agent framework and model version (for example, `Assisted-by: Cursor:gpt-5.3-codex`). List only specialized analysis tools in brackets. Do not list git, editors, gcc, or make.
- Do not use `Co-developed-by` or `Co-authored-by` for AI attribution.
- Use `git commit-tree` or amend with a message file. Use this when the environment would add `Co-authored-by: Cursor <cursoragent@cursor.com>`.

## Documentation style

Write all documentation in [ASD-STE100 Simplified Technical English](https://www.asd-ste100.org/). This rule applies to the Markdown files (`AGENTS.md`, each `README`, and `docs/`), to the code comments, and to the JSDoc. These are the core rules:

- **One word, one meaning.** Use the same term for the same thing each time. Do not use a synonym for variety. A "client" stays a "client". It is not a "patient" and not a "member".
- **Use the active voice.** Write "the job writes the row". Do not write "the row is written".
- **Use the simple tenses.** Use the simple present, the simple past, or the simple future. Do not use the perfect tenses.
- **Give one instruction in one sentence.** Divide a compound step into separate steps.
- **Write short sentences.** Use a maximum of 20 words for an instruction and a maximum of 25 words for a description. Use a maximum of six sentences in a paragraph.
- **Keep the articles.** Write "the migration file". Do not write "migration file".
- **Do not use an `-ing` form**, unless the word is part of a technical name.
- **Use a maximum of three nouns together.** Divide "provider availability tally job" with a preposition or a hyphen.
- **Do not use jargon, an idiom, or slang.** Do not write "hit the endpoint" or "under the hood".
- **Start a warning with the instruction.** Give the command first, then give the reason.
- **Use a vertical list** for a set of conditions and for a sequence of steps.

A technical name keeps its usual form and is an exception to these rules. A technical name includes an identifier, a table name, a column name, a product name, and a domain term.

A plan is also an exception. For plan mode, the concision rule above has priority.

## Skill routes

When the request of the user matches an available skill, invoke that skill with the Skill tool as the first action. Do not answer directly. Do not use other tools first. The skill has a special workflow. That workflow gives a better result than a free-form answer.

Key rules:

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