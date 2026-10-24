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
- **calendar** - CRUD, color control, remote-link metadata, and `Apply` for the `[[calendars]]` entries of `config.toml` (issue #809). `initApp` calls `Apply` on every launch. `Apply` creates a missing calendar and updates a declared color, description, or `local_path`. It never deletes a calendar.
- **alarm** - Check due alarms. Fire, dismiss, and snooze alarms
- **recurrence** - Expand recurring events, todos, and journals. Apply overrides
- **trash** - Mixed soft-delete view of events, todos, and journals (list, restore, purge)

Integration packages and infrastructure packages do not use the `NewService` shape above. Each package has its own constructor:

- **sync** - CalDAV sync engine. Detect and resolve conflicts (`NewService` with extra dependencies). Write paths push through `PushLocalEdits`; a 412 records a conflict and keeps the edit dirty. The conflict strategy governs full passes only. A resolved conflict row stays, so `sync resolve --pick local` can restore the recorded local body. One exception applies. A new move of the UID drops the resolved move rows of the earlier move in its account. Their recorded bodies describe a move that no longer exists. A create PUT sends `If-None-Match: *`, so it never overwrites an object. A dirty row wins over a remote delete: the pull and a 412 on a gone object drop its href, and the push creates the object again. A server-wins pass, a read-only calendar, and a row with an open conflict keep the delete. A link of a local calendar gives each local item a dirty row (`MarkLocalItemsDirtyInCalendar`).
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
- `event_uid_move.go` (`MoveEventUIDRows`: the move of the event rows and the delete-log rows of one UID; the event service and the startup heal share it)
- `delete_logs_dynamic.go` (the delete logs of a UID that name a set of row IDs; sqlc cannot bind an argument inside `json_each`)

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

A calendar move of a UID moves every row of that UID as one unit. The move rules live in `internal/uidmove`: the override rule, the target-collision check, and the move record. The event, todo, and journal services bind their queries to `uidmove.Rows` and call it inside the update transaction. Fix a move rule in `uidmove`. Do not fix it per domain.

Two calendars can hold an event series with the same UID (issue #842). A series lookup takes the calendar of the series (`findSeriesMasterTx` in `internal/event/series_scope.go`). A legacy override is an override row in a calendar with no master of its UID. `uidmove.LegacyOwner` names the series that owns it. The one live master of the UID owns it. With no live master, the one master in the trash owns it. The view `event_series_rows` of migration 057 holds that rule in SQL, and the series queries in `db/queries/events.sql` read the view. To change the rule, add a new migration. The migration drops the view and creates it again with the new rule. Change `uidmove.LegacyOwner` in the same change. See `db/migrations/README.md` for the view and for a rebuild of the `events` table.

A pull of one calendar writes the rows of that calendar only. The server object of a series also carries the legacy overrides that the series owns in other calendars. A prune or a remote delete in the pull does not write those rows. The pull has no view of the sync state and the access policy of their calendars. Such a row stays until the user acts on it. The startup heal `healLegacyEventOverrides` in `internal/storage/heal_legacy_overrides.go` moves the legacy overrides of a local calendar into the calendar of their live owner, when that calendar is local too. It makes no server write, and no pull can prune the moved rows. Each calendar must have no read-only access, and no sync row, tombstone, or open conflict of the UID. The heal also keeps a row on an open move conflict of the UID, or on a row of the owner calendar at the same recurrence ID. The heal uses the queries of the move rules, because `uidmove` imports storage. A pull of a linked calendar can still write a legacy override, so the code paths for a legacy override stay. `sync doctor` lists each kept row with the `event update` command that moves it home.

A series with its master in the trash keeps its live rows local. This rule applies to the rows beside the master and to the legacy overrides that it owns. A pull can bring back such a row. A restore of a row that a delete log names refuses (`ErrMasterInTrash`). A delete by ID of a row while the master is in the trash writes no log. The undo or the restore by ID of that row brings back the row alone. Such a restore or an edit of the row writes no sync state. The restore of the series marks the UID, and the next push sends the series with that edit (see `dirtyCalendar` in `internal/storage/sync_helpers.go`). The export refuses the UID, so a push never sends a body with no master over the object of the series.

Two legacy overrides of one series can hold the same occurrence. The expansion, the export, and the edit of the occurrence use one rule: the row of the owner calendar wins, then the lowest ID. Only a delete by UID and recurrence ID returns `ErrAmbiguousOverride`, so the user chooses the row. An occurrence delete (`DeleteInstance`) chooses no row. It hides every row of the occurrence that the series owns, and the occurrence log names each of them. The rule has one SQL form and one Go form. The SQL form is the view column `beside_master` of migration 057, and the series override queries order by it. The Go form is `uidmove.TieBefore`, beside `uidmove.LegacyOwner`. Change the two forms together.

### Alarms

Triggers are RFC 5545 duration strings (`-PT15M` = 15 minutes before). Absolute triggers use RFC 3339. The `alarm_state` and `todo_alarm_state` tables store the state (`fired_at`, `acknowledged_at`, `snooze_until`). The service skips alarms older than 24 hours (`alarm.StaleThreshold`). The service fires extra alarms at `Duration` intervals, up to the `Repeat` count.

Default alarms (issue #815) are virtual. The check loop synthesizes them at check time for events without any alarm row. The `calendars.default_alarms` column holds a per-calendar spec list (`NULL` inherits the global default, `''` turns defaults off), and the `default_alarm_state` table stores their state. Its claim key is `(event_id, action, trigger_value, trigger_at)`: two actions can share one trigger. A default alarm is never written to `event_alarms`, so sync and export never see it. Parse default-alarm specs with `model.ParseDefaultAlarmSpec` (`"AUDIO:-PT5M"` or a bare duration).

Three paths share one rule set. `alarm.defaultAlarmCandidates` decides which expanded events receive defaults. `alarm.defaultStateEligible` decides whether an existing `default_alarm_state` row may still notify. `alarm.DescribeDefaultAlarms` reports the decision for one event, so `event get` can name the reason a default alarm does not apply. Keep the three in step. Fix the shared helper, not one caller.

Eligibility filters the notification path only. `alarm.ListPendingDefaultAlarms` returns every unacknowledged row and carries an `Eligible` flag. The `alarm list` output shows an ineligible row as "will not fire again", because a hidden row gives the user no way to find its ID and dismiss it.

`CheckMissed` returns three slices: stored event alarms, todo alarms, and default alarms. Its forward window includes the default triggers, or a long default lead time never enters the window and `alarm missed` reports nothing.

### iCal Round-Trip

UID is required for round-trip fidelity. `recurrence_id` marks an overridden instance. Export fills transient fields (Alarms, Attendees, and others). The main event and todo tables do not store those fields. You can express duration as DTEND or as DURATION (RFC 5545). The `timezone` column and the `timezones` table preserve timezones.

### Time

All database times are RFC 3339 strings in UTC. Go code uses `time.Time` with `time.UTC`. An all-day event has the time component 00:00:00.

A date-only value is a floating date (RFC 5545 section 3.3.4). This rule applies to an all-day event, a date-only todo DUE or DTSTART, and a date-only journal DTSTART. The database stores the value at midnight UTC, but the day starts at local midnight in every zone (issue #875). Do not call `.Local()` on a date-only value. West of UTC, the result is the evening of the day before. Use the helpers in `internal/timeutil`:

- `FloatingLocal` gives the local moment of a stored date. Use it for a display, a free/busy interval, an alarm anchor, and a snooze until the start. `event.Event.LocalStart` and `LocalEnd` call it for an all-day event.
- `WindowFor` gives the window of a date-only row for a range filter. `expandRecurringRowsBy` applies it to each master and each override.
- `CoverWindow` gives the SQL bounds that read the rows of both kinds. The caller then keeps each row with `event.Event.Overlaps`.

A relative alarm trigger on a date-only value counts from local midnight of the date. Other clients use the same rule.

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

### Test speed

Three rules keep the suite fast. Follow them in every new test.

1. Never touch the real OS keyring. `TestMain` in `cmd/chroncal/main_test.go` sets `CHRONCAL_SECURITY_DISABLE_KEYRING=1` for the test binary and its child processes. A locked desktop keyring blocks each D-Bus call for the full method timeout. One credential command then stalls a test for tens of seconds.
2. Take test databases from `internal/testutil`. Use `testutil.NewTestDB(t)` or `testutil.DBPath(t)`. Do not call `storage.Open` on a fresh path per test. A fresh open runs the full migration set and costs about a quarter second of CPU. The migration up/down tests in `internal/storage` are the one exception, because they must run the migration set.
3. Keep `tea.Tick` on the fake clock. A `tea.Tick` starts its timer when the code builds the command, not when the command runs. Run the model update and the command check inside one `synctest.Test` bubble. See `batchEmits` in `internal/tui/oauth_wiring_test.go`.

## Common Tasks

### Find an event by ID or UID

```go
evt, err := svc.Get(ctx, id)                                        // numeric ID
evt, err := svc.GetByUID(ctx, uid)                                  // string UID; ErrAmbiguousUID when two calendars hold a live master
evt, err := svc.SeriesMaster(ctx, calendarID, uid)                  // master of the series in one calendar (0 = the one live master)
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
alarmSvc.SetDefaultConfig(alarm.DefaultAlarmConfig{
    Triggers:   []model.DefaultAlarm{{Action: "DISPLAY", TriggerValue: "-PT15M"}},
    SkipAllDay: true,
})

dueEvents, dueTodos, err := alarmSvc.Check(ctx, time.Now())
// Each DueAlarm has: Event, Alarm, TriggerAt, StateID, IsDefault

status, err := alarmSvc.DescribeDefaultAlarms(ctx, evt)
// Each DefaultAlarmStatus has: Specs, Source, Applied, Suppressed
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
err := svc.DeleteSeries(ctx, calendarID, uid) // soft-deletes master + overrides (event service: in one calendar)

// Delete scopes of a series (event service only). calendarID names the
// calendar of the series, because two calendars can hold the same UID
// (issue #842). 0 picks the one live master, or returns ErrAmbiguousUID.
err := svc.DeleteInstance(ctx, calendarID, uid, at)     // one occurrence: EXDATE on the master + override soft-delete
err := svc.DeleteFromInstance(ctx, calendarID, uid, at) // truncates the series (RRULE UNTIL) at that time

// Restore:
err := svc.RestoreByID(ctx, id)    // un-hides one row; a master also reverses its logged series delete
err := svc.RestoreByUID(ctx, uid)  // reverses the one series delete of the UID; ErrAmbiguousUID for two
// Returns svc.ErrNotDeleted when the row is live or missing.

// Purge (hard-delete soft-deleted rows):
err := svc.PurgeByID(ctx, id)         // one row, refuses live rows
n, err := svc.PurgeDeleted(ctx, cutoff) // all rows older than cutoff
```

For todos and journals, a restore of an override of a series clears the EXDATE that its delete recorded. The same transaction does both. Expansion then shows the occurrence again. Events follow the occurrence invariant below.

The EXDATE-provenance rule lives in `internal/softdelete`. Strip only EXDATEs that a delete recorded. Do not strip imported EXDATEs (issue #86). The todo and journal services call `softdelete.ClearMasterEXDATE` through a thin `clearMasterEXDATE` wrapper. Fix the contract in the shared helper. Do not fix it per domain.

Events keep one invariant. An EXDATE that a delete recorded exists at an occurrence only while no live override of the series sits there. Locally, an EXDATE never hides an override. But Google and iCloud drop an override whose RECURRENCE-ID has an EXDATE, so the export must never carry both. The occurrence log (`event_exdate_deletes`) holds the record. `hidden_ids` has one `seq:row` entry for each row of each delete. `owns_exdate` flags the copy of the EXDATE that the log owns. `delete_seq` numbers the deletes. Two helpers in `internal/softdelete` hold the invariant:

- `RecordOccurrenceDelete`: a delete by ID or by occurrence appends the EXDATE only when no live override of the series remains at the occurrence. The list of the master must not hold the EXDATE yet. The log owns the EXDATE only when its delete appended it. Else the delete records the hidden row with no EXDATE.
- `SettleOccurrence`: a path that makes an override live at the occurrence again strips the EXDATE that the log owns. The log keeps the entries of the rows that stay hidden. The paths are the restore by ID, the undo, the revive in an edit, and the trash restore. `UpsertByUID` does it for a pull or an import.

When the last live override of the occurrence goes later, the delete adds the EXDATE again. A pull or an import through `UpsertByUID` that changes the EXDATE list of a master clears `owns_exdate` of every log of that master. A server or an imported EXDATE then never goes. A purge of an override removes its ID from every delete log (the trigger `events_purge_drops_row_from_logs`), and the occurrence log settles. Bind new paths through `recordOccurrenceDeleteTx` and `settleOccurrenceTx` in `internal/event/occurrence_log.go`.

Every event undo carries the identity of its delete and reverses only the rows that this identity still names:

- An occurrence delete and a delete by ID of an orphan row keep the log ID and the seq of the entry (`UndoMeta.ExdateLogID`, `UndoMeta.ExdateDeleteSeq`).
- A series delete keeps the ID of its series-delete log (`UndoMeta.SeriesLogID`). The undo of a series delete of a master restores that log only, and only the rows with its delete token. Rows that a delete of orphan rows merged into the log keep their own token, and their own undo restores them. A restore by UID or by ID restores the whole log.
- A delete by ID beside a trashed master writes no log. A delete log that names its row now belongs to a newer delete.

One helper, `identityRefusalTx` in `internal/event/occurrence_log.go`, holds the rule. The undo is all or nothing: one refusal refuses the whole undo. For each row of the delete:

- No row, or a live row: the row is done. A purge took it, or a restore came first. With no row to bring back, the undo gives `ErrAlreadyRestored` when a row is live, else `ErrNotDeleted`.
- A row in the trash that a newer entry or log names: the undo refuses with `ErrDeletedAgain`. A newer record wins over the identity of the delete.
- A row in the trash that the identity names: the row comes back.
- A row in the trash that no log names: a trigger dropped the log, or a pull hid the row with no log. The delete token tells the two apart. A row with the token of the undo (`UndoMeta.DeleteToken`) is still as the delete left it, so it comes back. A later delete, a pull or a prune included, wrote a newer token. The undo then refuses with `ErrChangedSince`, and the trash still restores the row.

Each soft-delete statement of an event writes a fresh delete token on its rows (`events.delete_token`, migration 057). `NextEventDeleteToken` takes the token from the one-row counter `event_delete_token`, and every soft-delete query takes it as a parameter. Give a new soft-delete path its own token, and keep it in the undo data of the delete. Every undo checks the token through `tokenSplitTx` in `internal/event/occurrence_log.go`. It reads the rows in the restore transaction and sorts the rows in the trash by token. The filter `refuseOther` refuses a row with another token with `ErrChangedSince`. The filter `matchIDs` takes the rows of the delete and leaves the rows of other deletes. The undo of a master, of a series with a master, and of a truncation use it. A series-delete log keeps the token of its master, and the revive of that master drops that log only. The restore of a master with no log pairs its rows by token. A row from before migration 057 has token 0, and the trash time pairs it.

A purge of the local trash makes no server write, so no purge path has a write guard. The age purge, `PurgeByID`, and the purge of an occurrence entry empty the trash of a read-only calendar too. Restores and undos keep their guards.

The restore by ID of a row beside a trashed master uses the same helper. A log that names the row gives `ErrMasterInTrash` there.

An undo of an occurrence delete that hid no row needs its entry in the log. Another log, or no entry, gives `ErrAlreadyRestored`. A restore of an occurrence refuses with `ErrMasterInTrash` while the master is in the trash. Restore the series first.

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