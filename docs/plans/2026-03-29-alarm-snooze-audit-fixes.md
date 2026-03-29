<!-- /autoplan restore point: /home/doug/.gstack/projects/tcal/master-autoplan-restore-20260329-171005.md -->
# Alarm Snooze Audit Fixes

**Goal:** Fix 5 bugs and 1 documentation gap found during alarm snooze CLI audit.

**Context:** The alarm snooze feature works correctly for the happy path (fire, snooze, re-fire, dismiss lifecycle). The issues are: input validation accepts nonsensical values (negative/zero durations, past-ended events), error messages expose raw SQL, snoozing dismissed alarms silently succeeds, and help text lacks output format examples.

**Execution order:** Task 1 -> Task 1b -> Task 1c -> Task 2 -> Task 3 (docs). All tasks touch the same 2-3 files and are independent.

**Scope decision:** RFC 9074 export (UID, ACKNOWLEDGED, snooze VALARM siblings) was evaluated and deferred. Both independent reviewers agreed: zero evidence of user demand, alarm identity is unstable (delete-and-recreate pattern), and importing ACKNOWLEDGED from untrusted ICS can ghost-dismiss alarms. Revisit when a real interop need arises.

---

## Task 1: Reject negative and zero snooze durations

**Bug:** `cmd/tcal/alarm.go:393` and `internal/alarm/service.go:199` — `time.ParseDuration` accepts negative values like `-5m` and zero `0s`. A negative duration snoozes into the past (immediately expires). Zero snoozes to "now" (also immediately expires). Both are nonsensical.

**Files:**
- `cmd/tcal/alarm.go` — add CLI validation after `time.ParseDuration` for friendly error
- `internal/alarm/service.go` — add `dur <= 0` guard in `ComputeSnooze` for domain safety

**Steps:**
1. In CLI: after `time.ParseDuration(forDur)`, check `dur <= 0` and return: `"snooze duration must be positive (e.g. 5m, 1h)"`
2. In `ComputeSnooze`: add `dur <= 0` guard as first check after params
3. Add test for negative and zero duration rejection in service_test.go

---

## Task 1b: Reject snooze on past-ended events

**Bug:** `internal/alarm/service.go:219` — `ComputeSnooze` caps the snooze at `evt.EndTime`, but if the event has already ended (`evt.EndTime < now`), the cap silently snoozes to a time in the past. Same class of bug as Task 1.

**Files:**
- `internal/alarm/service.go` — add guard in `ComputeSnooze` BEFORE the cap logic (between lines 209-212)
- `internal/alarm/service_test.go` — test for past-ended event rejection

**Steps:**
1. In `ComputeSnooze`, after fetching the event and computing `now`, check:
   - `!evt.EndTime.IsZero() && evt.EndTime.Before(now)` -> return error: `"event %q has already ended"`
   - The `!IsZero()` guard prevents zero-valued EndTime from rejecting all snoozes
2. Place this check BEFORE the cap logic at line 219 (otherwise cap silently sets Until to past time)
3. Add test confirming past-ended events are rejected

---

## Task 1c: Reject snooze on already-dismissed alarms

**Bug:** `internal/alarm/service.go:199-259` — `ComputeSnooze` and `SnoozeUntilStart` do not check whether the alarm state is already dismissed (`acked_at IS NOT NULL`). The `Snooze()` method blindly updates `snoozed_to`. Since `ListExpiredSnoozedAlarmStates` filters by `acked_at IS NULL`, the snoozed alarm never re-fires. The user sees "Snoozed alarm state N until HH:MM" but it is a lie.

**Files:**
- `internal/alarm/service.go` — add `st.AckedAt.Valid` guard in `ComputeSnooze` and `SnoozeUntilStart`
- `internal/alarm/service_test.go` — test: fire, dismiss, attempt snooze, expect error

**Steps:**
1. In `ComputeSnooze`, after `GetAlarmStateByID`, check `st.AckedAt.Valid` and return: `"alarm state %d is already dismissed"`
2. Same guard in `SnoozeUntilStart`
3. Add test confirming dismissed alarms cannot be snoozed

---

## Task 2: User-friendly error for nonexistent alarm state ID

**Bug:** `internal/alarm/service.go:200-202` — When `GetAlarmStateByID` fails with `sql.ErrNoRows`, the error bubbles up as `"get alarm state 9999: sql: no rows in result set"`. Users see raw SQL errors.

**Files:**
- `internal/alarm/service.go` — use `errors.Is(err, sql.ErrNoRows)` in `ComputeSnooze`, `SnoozeUntilStart`, AND `Dismiss`

**Steps:**
1. In `ComputeSnooze` and `SnoozeUntilStart`, after `GetAlarmStateByID`:
   ```go
   if errors.Is(err, sql.ErrNoRows) {
       return ..., fmt.Errorf("alarm state %d not found (use 'tcal alarm list' to see pending alarms)", stateID)
   }
   if err != nil {
       return ..., fmt.Errorf("get alarm state %d: %w", stateID, err)
   }
   ```
2. Fix `Dismiss` (line 165-167): currently wraps ALL errors as "not found". Add `errors.Is` to distinguish `ErrNoRows` from real DB errors.
3. Add `"errors"` to imports
4. Add test for the user-friendly error message

---

## Task 3: Improve alarm snooze help text

**Documentation gap:** `alarm snooze` help doesn't show `-o json` output format.

**Files:**
- `cmd/tcal/alarm.go` — update snooze command examples and Long description

**Steps:**
1. Add example showing JSON output: `tcal alarm snooze 5 --for 1h -o json`
2. Add note about checking snooze status with `alarm list`

---

<!-- AUTONOMOUS DECISION LOG -->
## Decision Audit Trail

| # | Phase | Decision | Classification | Principle | Rationale | Rejected |
|---|-------|----------|---------------|-----------|-----------|----------|
| 1 | CEO | Cut Task 3 (RFC 9074) | User Challenge | P1+P3 | Both models: no demand, unstable identity, ghost-dismiss risk | Full RFC 9074 impl |
| 2 | CEO | Add Task 1b (past-ended) | Mechanical | P2 (boil lakes) | Same bug class as Task 1, in blast radius | Ignore the edge case |
| 3 | CEO | Mode: SELECTIVE EXPANSION | Mechanical | P3 | Clear scope, no ambiguity | SCOPE EXPANSION |
| 4 | Eng | Add dismissed-alarm guard (Task 1c) | Mechanical | P1 (completeness) | Both models agree, same code location | Leave unvalidated |
| 5 | Eng | Duration check in service too | Mechanical | P5 (explicit) | Domain rule in domain layer | CLI-only validation |
| 6 | Eng | Fix Dismiss error wrapping | Mechanical | P2 (boil lakes) | In blast radius, same pattern | Leave inconsistent |
| 7 | Eng | Zero-EndTime guard in Task 1b | Mechanical | P5 (explicit) | Defensive, low effort | Skip guard |
| 8 | Eng | Skip TOCTOU fix | Mechanical | P3 (pragmatic) | Local CLI tool, not concurrent | Add transaction |
| 9 | Eng | Skip CLI-level tests | Mechanical | P3 (pragmatic) | Service tests cover logic | Add cmd tests |

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 1 | DONE | Task 3 cut, Task 1b added, 6/6 consensus |
| Codex Review (CEO) | `/codex review` | Independent 2nd opinion | 1 | DONE | 6 findings, aligned with Claude |
| Eng Review | `/plan-eng-review` | Architecture & tests | 1 | DONE | 5 findings each model, dismissed-alarm guard added |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | SKIPPED (no UI) | -- |

**VERDICT:** REVIEWED — CEO (6/6 consensus) + Eng (6/6 consensus). Ready for implementation.
