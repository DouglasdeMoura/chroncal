# Calendar-Centric Sync CLI Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the user-facing `account` workflow with calendar-centric sync configuration on `calendar create` and `calendar update`, while keeping the existing database structure intact.

**Architecture:** Keep the existing `accounts` table and calendar-to-account link as internal plumbing. Move remote setup, reconfiguration, and disconnect flows into the `calendar` command layer, add helpers that create and manage hidden account records per connected calendar, and update sync/status presentation to think in terms of connected calendars instead of accounts.

**Tech Stack:** Go, Cobra, SQLite via sqlc-generated queries plus handwritten services, credential storage in `internal/auth`

---

### Task 1: Inventory current CLI and service touchpoints

**Files:**
- Modify: `docs/plans/2026-04-05-calendar-centric-sync-cli-implementation.md`
- Review: `cmd/chroncal/account.go`
- Review: `cmd/chroncal/calendar.go`
- Review: `cmd/chroncal/sync.go`
- Review: `cmd/chroncal/freebusy.go`
- Review: `internal/calendar/service.go`
- Review: `internal/sync/engine.go`

**Step 1: Verify the current command and service entry points**

Run: `rg -n "accountCmd|calendar(Create|Update|Link|Unlink)|ListAccounts|ListCalendarsByAccount|GetAccount|LinkToAccount|UnlinkFromAccount" cmd/chroncal internal`
Expected: References showing the existing split between account setup and calendar link/sync behavior.

**Step 2: Note the concrete functions that will change**

Write down a short checklist in your scratchpad for:
- account command removal
- calendar create/update expansion
- hidden account helper introduction
- sync/status wording updates

Expected: A compact implementation checklist before editing code.

**Step 3: Commit**

This task is analysis-only. No commit.

### Task 2: Add failing CLI tests for calendar-centric remote setup

**Files:**
- Modify: `cmd/chroncal/calendar_test.go`
- Review: `cmd/chroncal/account_test.go`

**Step 1: Write the failing tests**

Add tests covering:
- `calendar create "Work"` without remote flags still creates a local calendar
- `calendar create "Work" --remote-url ... --username alice` stores a linked calendar
- `calendar update Work --remote-url ... --username alice` links an existing local calendar
- `calendar update Work --disconnect-remote` unlinks a connected calendar
- invalid flag combinations such as `--disconnect-remote` plus `--remote-url`

Expected: New tests fail because the command surface does not support the new remote flags yet.

**Step 2: Run the focused tests to verify failure**

Run: `go test ./cmd/chroncal -run 'TestCalendar(Create|Update)'`
Expected: FAIL with unknown flags, missing behavior, or old assertions.

**Step 3: Commit**

```bash
git add cmd/chroncal/calendar_test.go
git commit -m "test: define calendar-centric sync CLI behavior"
```

### Task 3: Add helper logic for hidden account management

**Files:**
- Modify: `cmd/chroncal/calendar.go`
- Create: `cmd/chroncal/calendar_remote.go`
- Review: `cmd/chroncal/account.go`
- Review: `internal/auth/credential.go`
- Review: `internal/storage/accounts.sql.go`

**Step 1: Write the failing tests for helper behavior if direct unit coverage is practical**

If the codebase supports small helper tests, add focused tests for:
- creating a hidden account for a calendar connection
- updating an existing hidden account for a relink
- deleting the hidden account when disconnecting a calendar that uniquely owns it

Expected: FAIL until helper logic exists.

**Step 2: Implement minimal hidden-account helper code**

Add helper functions that:
- derive the server URL from `--remote-url`
- create or update the hidden account record
- prompt for or obtain credentials using existing auth flows
- store credentials in the existing credential store
- link the calendar using existing `LinkToAccount`
- unlink and clean up orphaned hidden accounts on disconnect

Expected: A single path that the calendar commands can call without reusing the old `account` CLI surface.

**Step 3: Run the focused tests**

Run: `go test ./cmd/chroncal -run 'TestCalendar(Create|Update|Disconnect|Remote)'`
Expected: PASS for helper-related CLI behavior; unrelated tests remain untouched.

**Step 4: Commit**

```bash
git add cmd/chroncal/calendar.go cmd/chroncal/calendar_remote.go cmd/chroncal/calendar_test.go
git commit -m "feat: add hidden remote account handling for calendars"
```

### Task 4: Replace calendar link/unlink with create/update remote flags

**Files:**
- Modify: `cmd/chroncal/calendar.go`
- Review: `cmd/chroncal/main.go`

**Step 1: Remove old subcommands from the CLI surface**

Delete:
- `calendar link`
- `calendar unlink`

Update help text, examples, and long descriptions to show:
- local create
- create with `--remote-url`
- update with `--remote-url`
- update with `--disconnect-remote`

Expected: The CLI surface no longer teaches the old account/link workflow.

**Step 2: Extend `calendar create`**

Add flags:
- `--remote-url`
- `--username`
- `--auth`
- `--oauth-client-id`
- `--allow-insecure`
- `--allow-plaintext`

After local calendar creation, call the hidden-account helper when remote flags
are present.

Expected: Connected calendar creation works through one command.

**Step 3: Extend `calendar update`**

Add the same remote flags plus:
- `--disconnect-remote`

Implement behavior:
- local-only field updates leave remote config unchanged
- remote flags connect or reconfigure
- `--disconnect-remote` detaches the remote link and resets sync state as needed

Expected: Connection lifecycle is fully owned by `calendar update`.

**Step 4: Run the focused tests**

Run: `go test ./cmd/chroncal -run 'TestCalendar(Create|Update)'`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/chroncal/calendar.go cmd/chroncal/calendar_remote.go cmd/chroncal/calendar_test.go
git commit -m "feat: move remote calendar setup into calendar commands"
```

### Task 5: Remove the user-facing account command

**Files:**
- Modify: `cmd/chroncal/main.go`
- Delete or retire: `cmd/chroncal/account.go`
- Modify: `cmd/chroncal/account_test.go`

**Step 1: Write or update tests that assert the root command no longer exposes `account`**

Expected: FAIL until the command is removed from the root CLI.

**Step 2: Remove `account` from the root command tree**

Make sure no help text, examples, or onboarding copy still points users to
`account add` or `account discover`.

Expected: `chroncal --help` and related command help are calendar-centric.

**Step 3: Run the focused tests**

Run: `go test ./cmd/chroncal -run 'TestRoot|TestHelp|TestCalendar'`
Expected: PASS

**Step 4: Commit**

```bash
git add cmd/chroncal/main.go cmd/chroncal/account.go cmd/chroncal/account_test.go cmd/chroncal/calendar.go
git commit -m "refactor: remove account as a user-facing CLI concept"
```

### Task 6: Update sync and freebusy messaging to be calendar-centric

**Files:**
- Modify: `cmd/chroncal/sync.go`
- Modify: `cmd/chroncal/freebusy.go`
- Modify: `cmd/chroncal/safe_output.go`
- Review: `internal/sync/engine.go`

**Step 1: Write failing tests for user-facing copy if coverage exists**

Cover messages such as:
- “No synced calendars” copy
- errors that currently mention linked accounts
- freebusy errors for unconnected calendars

Expected: FAIL where text still reflects the old account concept.

**Step 2: Update command descriptions and terminal output**

Change wording from:
- “linked calendars with CalDAV accounts”

To wording like:
- “connected calendars”
- “remote CalDAV calendar”
- “calendar is not connected”

Expected: User-visible terminology matches the new model.

**Step 3: Run focused tests**

Run: `go test ./cmd/chroncal -run 'Test(Sync|Freebusy|SafeOutput)'`
Expected: PASS

**Step 4: Commit**

```bash
git add cmd/chroncal/sync.go cmd/chroncal/freebusy.go cmd/chroncal/safe_output.go
git commit -m "docs: make sync command output calendar-centric"
```

### Task 7: Refactor sync iteration to operate calendar-first

**Files:**
- Modify: `internal/sync/engine.go`
- Review: `internal/storage/calendars.sql.go`

**Step 1: Write the failing test**

Add or extend sync engine coverage so `SyncAll` operates over connected
calendars directly rather than enumerating accounts first.

Expected: FAIL until the engine changes.

**Step 2: Implement the minimal refactor**

Replace account-first iteration with a direct query over connected calendars.
If there is no sqlc query for that yet, add the SQL query and regenerate code.

Expected: Sync behavior remains the same while the code matches the new mental
model.

**Step 3: Run focused tests**

Run: `go test ./internal/sync -run TestSyncAll`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/sync/engine.go db/queries/calendars.sql internal/storage/calendars.sql.go
git commit -m "refactor: sync connected calendars directly"
```

### Task 8: Verify full CLI behavior and help output

**Files:**
- Review: `cmd/chroncal/calendar.go`
- Review: `cmd/chroncal/main.go`
- Review: `cmd/chroncal/sync.go`

**Step 1: Run the full relevant test set**

Run: `go test ./cmd/chroncal ./internal/calendar ./internal/sync`
Expected: PASS

**Step 2: Spot-check help output**

Run:
- `go run ./cmd/chroncal --help`
- `go run ./cmd/chroncal calendar --help`
- `go run ./cmd/chroncal calendar create --help`
- `go run ./cmd/chroncal calendar update --help`

Expected: No `account` setup flow remains in help text; examples use
calendar-centric remote configuration.

**Step 3: Commit**

```bash
git add cmd/chroncal/calendar.go cmd/chroncal/main.go cmd/chroncal/sync.go cmd/chroncal/freebusy.go cmd/chroncal/safe_output.go internal/sync/engine.go
git commit -m "test: verify calendar-centric sync CLI"
```

### Task 9: Final review and cleanup

**Files:**
- Review: `cmd/chroncal/account.go`
- Review: `cmd/chroncal/calendar_remote.go`
- Review: `docs/plans/2026-04-05-calendar-centric-sync-cli-design.md`
- Review: `docs/plans/2026-04-05-calendar-centric-sync-cli-implementation.md`

**Step 1: Remove dead code and stale references**

Search for:
- `account add`
- `account discover`
- `calendar link`
- `calendar unlink`

Run: `rg -n "account add|account discover|calendar link|calendar unlink|linked to an account" .`
Expected: Only intentional internal references remain.

**Step 2: Run final verification**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add docs/plans/2026-04-05-calendar-centric-sync-cli-design.md docs/plans/2026-04-05-calendar-centric-sync-cli-implementation.md
git commit -m "docs: capture calendar-centric sync CLI redesign"
```
