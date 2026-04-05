# Calendar-Centric Sync CLI Design

**Date:** 2026-04-05

## Goal

Redesign the sync-related CLI UX so users manage remote sync through `calendar`
commands instead of a separate `account` command, while keeping the existing
database structure intact.

## Problem

The current CLI exposes two overlapping concepts:

- `account add` creates a CalDAV account with credentials.
- `calendar link` connects a local calendar to that account and a remote
  calendar URL.

This split is technically valid but confusing in practice. Users think in terms
of calendars, not reusable account records. A user wants to create a local
calendar and optionally connect or disconnect it from a remote CalDAV calendar
later. Requiring them to first model an `account` and then separately link a
calendar leaks internal storage concepts into the CLI.

## Constraints

- Local calendars remain first-class and can exist without any remote sync
  configuration.
- Users must be able to connect a calendar at creation time or later when
  updating it.
- No interactive wizard in this iteration.
- No backwards-compatibility requirement for the current CLI surface.
- Keep the existing database structure for now, including `accounts` and the
  calendar-to-account link.

## User Model

Users manage calendars. A calendar may be:

- local-only
- connected to a remote CalDAV calendar

Users do not manage top-level accounts as part of the normal workflow. Any
account records needed to support sync become an internal implementation detail.

## Command Surface

`calendar` becomes the only user-facing resource for this workflow.

### Create a local calendar

```bash
chroncal calendar create "Work"
```

Creates a calendar with no remote sync configuration.

### Create and connect in one step

```bash
chroncal calendar create "Work" \
  --remote-url https://cal.example.com/dav/calendars/work/ \
  --username alice
```

Creates the calendar and configures remote sync immediately.

### Connect or reconfigure later

```bash
chroncal calendar update Work \
  --remote-url https://cal.example.com/dav/calendars/work/ \
  --username alice
```

Updates an existing calendar to connect it, or replaces its remote sync
configuration.

### Disconnect while preserving local data

```bash
chroncal calendar update Work --disconnect-remote
```

Removes the remote link and credentials for that calendar without deleting the
calendar itself.

### Inspect state

- `chroncal calendar list` shows whether each calendar is local or connected.
- `chroncal calendar get <id>` shows sync configuration and status without
  exposing secrets.

## Flag Model

Keep existing local calendar flags:

- `--color`
- `--description`
- `--name` on update

Add remote sync flags to `calendar create` and `calendar update`:

- `--remote-url`
- `--username`
- `--auth`
- `--oauth-client-id`
- `--allow-insecure`
- `--allow-plaintext`
- `--disconnect-remote`

### Why only `--remote-url`

The primary path should optimize for the case where the user already knows the
exact remote calendar URL. Requiring both a server URL and a calendar URL is
redundant in that flow and exposes implementation details. The CLI should accept
one `--remote-url` and derive or store whatever internal account metadata it
needs.

## Internal Model

Keep the current schema unchanged for now:

- `accounts` remains in the database
- `calendars.account_id` and `calendars.remote_url` remain the sync link fields
- credentials remain stored outside the main tables, keyed by account ID

However, `account` stops being a top-level CLI concept. The CLI owns the
calendar-centric flow and internally creates, updates, resolves, and deletes the
hidden account records needed to satisfy the existing service and sync code.

## Internal Behavior

### Calendar create

- If no remote flags are provided, create a normal local calendar.
- If remote flags are provided, create the calendar and then configure its
  hidden account and remote link.

### Calendar update

- If only local fields are provided, update the local calendar as today.
- If remote flags are provided, create or update the hidden account and relink
  the calendar.
- If `--disconnect-remote` is provided, remove the link and clear sync-related
  state without deleting local events.

### Sync

- Sync commands should think in terms of connected calendars, even if the
  underlying queries still use accounts internally.
- `sync run` and `sync status` should iterate connected calendars directly in
  their user-facing logic.

## Hidden Account Rules

Because the schema stays the same, the implementation must define account
ownership rules. The simplest rule for this redesign is:

- one hidden account per connected calendar

This avoids complicated sharing semantics and keeps disconnect/delete behavior
obvious:

- connecting a calendar creates or updates that calendar's hidden account
- disconnecting a calendar removes its link and can remove its hidden account if
  it is no longer referenced
- deleting a connected calendar can remove the hidden account if it is only used
  by that calendar

This is less normalized than a shared-account model, but it matches the new UX
and keeps the implementation predictable without a schema change.

## Validation Rules

- `--disconnect-remote` must not be combined with remote connection flags.
- If `--remote-url` is present, validate all auth inputs required by the chosen
  auth mode.
- Changing remote connection details should reset sync state such as sync token,
  ctag, last sync timestamps, and last sync errors when the target effectively
  changes.
- `calendar list` and `calendar get` must never display secrets.

## Migration Strategy

No database migration is required for this redesign.

The work is limited to:

- removing `account` from the CLI surface
- moving remote setup and teardown into `calendar create` and `calendar update`
- refactoring sync/status output to present connected calendars instead of
  account-centric terminology

Existing data continues to work as-is.

## Tradeoffs

### Benefits

- Much clearer mental model for users
- No schema churn
- Existing sync engine and storage layer can be reused incrementally
- Local-only calendars remain clean and first-class

### Costs

- The internal model still contains hidden accounts that no longer exist in the
  product vocabulary
- Some implementation code must translate between the calendar-centric CLI and
  account-centric storage
- A later schema cleanup may still be desirable if hidden-account handling grows
  complex

## Recommendation

Implement the calendar-centric CLI now without changing the database structure.
Treat accounts as private plumbing, not a user-facing resource. This delivers a
better UX immediately and keeps the refactor bounded.
