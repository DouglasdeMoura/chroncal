<!-- /autoplan restore point: /home/doug/.gstack/projects/tcal/master-autoplan-restore-20260329-180720.md -->
# Stable VALARM Identity

**Goal:** Give each alarm a stable identity (UID) that survives event updates, so alarm state (fired/snoozed/dismissed) is preserved across edits. This is a prerequisite for future CalDAV sync and RFC 9074 compliance.

**Context:** Today, `ReplaceAlarms()` does delete-all-and-recreate. Every event update destroys all `event_alarms` rows and inserts fresh ones with new auto-increment IDs. Because `alarm_state.alarm_id` has `ON DELETE CASCADE` referencing `event_alarms(id)`, all fired/snoozed/dismissed state is silently wiped. A user who snoozes an alarm, then edits the event title, loses the snooze.

**The problem in concrete terms:**
1. User creates event with `-PT15M` DISPLAY alarm
2. Alarm fires, user snoozes it for 1 hour -> `alarm_state` row created
3. User edits event title -> `ReplaceAlarms()` runs -> old `event_alarms` row deleted -> `alarm_state` CASCADE deleted
4. Snooze is gone. Alarm fires again as if new.

**Design approach (revised after review):**

Two-layer identity:
1. **RFC 9074 UID** (stored in `uid` column): A globally unique random UUID per VALARM, assigned once at creation. Preserved on export/import for interop. Used in RFC 9074 ACKNOWLEDGED/snooze references. Never recomputed.
2. **Content-based merge key** (computed on the fly, not stored): Full-tuple comparison of all alarm fields (action, trigger, related, description, summary, repeat, duration, attendees). Used by `ReplaceAlarms` to match incoming alarms against existing rows. When a match is found, the existing row (and its UID and alarm_state) is preserved.

**Scope:** Events only. `alarm_state` only references `event_alarms`. Todo alarms get the UID column for schema symmetry, but no merge logic or state tracking until `todo_alarm_state` is implemented.

---

## Task 1: Add `uid` column to alarm tables

**Files:**
- `db/migrations/011_alarm_uid.sql` — add `uid TEXT NOT NULL DEFAULT ''` to `event_alarms` and `todo_alarms`
- `db/queries/alarms.sql` — add uid to CreateAlarm params
- `db/queries/todo_alarms.sql` — add uid to CreateTodoAlarm params
- `internal/storage/` — regenerate sqlc
- `internal/model/alarm.go` — add `UID string` field

**Steps:**
1. Create migration: `ALTER TABLE event_alarms ADD COLUMN uid TEXT NOT NULL DEFAULT ''`
2. Same for `todo_alarms`
3. Add `UNIQUE(event_id, uid)` index (excluding empty uid for backfill: `CREATE UNIQUE INDEX ... WHERE uid != ''`)
4. Update CreateAlarm query to include uid column
5. Regenerate sqlc

---

## Task 2: Backfill existing alarms with UUIDs

Existing alarms have empty UIDs. The merge logic depends on UIDs being populated to preserve `alarm_state` on the first update after upgrade.

**Files:**
- `internal/storage/connect.go` or `internal/app/app.go` — post-migration backfill

**Steps:**
1. On startup, query all alarms with `uid = ''`
2. For each, generate a random UUID (`github.com/google/uuid`) and update the row
3. Run in a transaction, once per startup, skip if no empty UIDs found
4. This is mandatory, not optional. The "treat empty as unmatched" alternative was rejected (it causes one-time state loss on upgrade, which is exactly the bug being fixed).

---

## Task 3: Change event `ReplaceAlarms` to merge-based update

Replace the delete-all-and-recreate pattern with a merge that preserves existing alarm rows when their content matches.

**Content match function:** Two alarms are "the same" if ALL these fields match:
- `action` (case-insensitive)
- `trigger_value` (case-insensitive, after normalization)
- `related` (case-insensitive)
- `description`
- `summary`
- `repeat`
- `duration`
- sorted attendee emails

This is a full-tuple comparison. If any field differs, the alarm is treated as new (different alarm).

**Duplicate handling:** If two incoming alarms have identical content, they get separate merge passes. Use a slice-based match (not a map) to handle multiset correctly: each existing alarm can only be matched once.

**Algorithm:**
```
existing := ListAlarmsByEventID(eventID) + their attendees
matched := set{} // indices of existing alarms already matched

for each incoming alarm:
    for each existing alarm NOT in matched:
        if contentMatch(incoming, existing):
            mark existing as matched
            // existing row survives, alarm_state preserved
            // update uid if empty (backfill edge case)
            break
    if not matched:
        // new alarm: generate UUID, insert
        CreateAlarm(alarm) with new UUID

for each existing alarm NOT matched:
    DeleteAlarmByID(existing.ID)  // cascade deletes alarm_state (correct)
```

**Files:**
- `internal/event/service.go` — rewrite `ReplaceAlarms`
- `db/queries/alarms.sql` — add `UpdateAlarmUID :exec`, `DeleteAlarmByID :exec`
- `db/queries/alarm_attendees.sql` — ensure `DeleteAlarmAttendeesByAlarmID` exists
- `internal/storage/` — regenerate sqlc

**Steps:**
1. Add `DeleteAlarmByID :exec` query
2. Add `UpdateAlarmUID :exec` query (for backfill edge case)
3. Implement `contentMatch(a, b model.Alarm) bool` with case-insensitive comparison on action/trigger/related
4. Rewrite `ReplaceAlarms` with the merge algorithm above
5. Keep the entire merge within the existing transaction
6. Explicitly call `DeleteAlarmAttendeesByAlarmID` + re-create if attendees changed on a matched alarm

---

## Task 4: Populate UID on alarm creation and import

**Files:**
- `cmd/tcal/event.go` — generate UUID for new alarms
- `cmd/tcal/todo.go` — same
- `internal/ical/import.go` — parse UID from VALARM if present (RFC 9074), otherwise generate
- `internal/ical/export.go` — emit UID property in VALARM

**Steps:**
1. In CLI: after `parseAlarmFlags()`, generate `uuid.New()` for each alarm's UID
2. In ICS import: if VALARM has a UID property, use it. Otherwise generate UUID.
3. Always validate imported UIDs: non-empty, max 255 chars, no null bytes. Reject invalid -> generate new UUID.
4. In `buildValarm()`: emit `UID` property from alarm.UID (valid per RFC 9074 Section 4)
5. The UID is set once at creation and never recomputed

---

## Task 5: Tests

**Files:**
- `internal/event/service_test.go`
- `internal/alarm/integration_test.go`
- `internal/ical/roundtrip_test.go`

**Test cases:**
1. Update event title -> alarm_state preserved (alarm unchanged -> merge keeps row)
2. Add new alarm to existing event -> existing alarm_state preserved, new alarm gets new UUID
3. Remove one alarm from event -> that alarm's state deleted, other alarm's state preserved
4. Change alarm trigger value -> old alarm deleted (different content), new alarm created, old state lost (correct: it's a different alarm)
5. Change alarm description -> old alarm deleted, new alarm created (correct: content changed)
6. Two identical alarms on one event -> both survive merge (multiset handling)
7. ICS import with UID property -> UID preserved in database
8. ICS import without UID -> UUID generated
9. ICS import with malicious UID (empty, huge, null bytes) -> sanitized
10. ICS export -> UID emitted on VALARM
11. Snooze survives event update (integration test: fire, snooze, edit title, verify snooze still pending)
12. Empty alarm set on update -> all existing alarms deleted
13. Backfill on startup -> empty UIDs filled with random UUIDs
14. UID pinning test: known input -> verify UID is a valid UUID format (regression guard)

---

<!-- AUTONOMOUS DECISION LOG -->
## Decision Audit Trail

| # | Phase | Decision | Classification | Principle | Rationale | Rejected |
|---|-------|----------|---------------|-----------|-----------|----------|
| 1 | CEO | Keep UID approach (user chose B) | User Challenge | User sovereignty | User heading toward CalDAV sync | Tuple-diff only |
| 2 | CEO | Use random UUID not content hash for uid column | Mechanical | P5 (explicit) | RFC 9074 requires globally unique | Content-based sha256 |
| 3 | CEO | Merge by full content tuple, not by UID | Mechanical | P1 (completeness) | All fields matter for identity | 3-field hash |
| 4 | CEO | Events only for merge logic | Mechanical | P3 (pragmatic) | No alarm_state for todos | Both event+todo |
| 5 | Eng | Include all fields in content match | Mechanical | P1 (completeness) | Partial match preserves wrong state | 3-field match |
| 6 | Eng | Slice-based match not map for duplicates | Mechanical | P5 (explicit) | Map collapses duplicates | map[uid]alarm |
| 7 | Eng | Mandatory backfill, not optional | Mechanical | P1 (completeness) | Optional causes one-time state loss | Tolerate empty |
| 8 | Eng | Validate imported UIDs | Mechanical | P3 (pragmatic) | Crafted ICS can inject bad UIDs | Trust external |
| 9 | Eng | UNIQUE(event_id, uid) WHERE uid != '' | Mechanical | P5 (explicit) | Prevent silent collision data loss | No constraint |

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 1 | DONE | UID hash replaced with random UUID, full-tuple merge, events-only |
| Codex Review (CEO) | `/codex review` | Independent 2nd opinion | 1 | DONE | 6 findings: framing, identity spec, duplicates, todos premature |
| Eng Review | `/plan-eng-review` | Architecture & tests | 1 | DONE | 10+5 findings: content match, UNIQUE constraint, backfill, security |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | SKIPPED (no UI) | -- |

**VERDICT:** REVIEWED — Plan revised with all critical findings addressed. Ready for approval.
