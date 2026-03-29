<!-- /autoplan restore point: /home/doug/.gstack/projects/tcal/master-autoplan-restore-20260329-163135.md -->
# Alarm List Audit Fixes

**Goal:** Fix 3 issues found during alarm list CLI audit: 1 RFC 5545 data loss bug, 1 validation gap, 1 documentation gap.

**Context:** The alarm list command and supporting infrastructure work correctly for the happy path. These issues are RFC 5545 compliance gaps found during end-to-end roundtrip testing.

**Execution order:** Task 1 → Task 2 → Task 3 (Task 1 is a schema migration so must go first; Task 2 is independent validation; Task 3 is independent docs).

---

## Task 1: Add SUMMARY field to alarm model, DB, import, export (hybrid approach)

**Bug:** RFC 5545 Section 3.6.6 requires `SUMMARY` for EMAIL action alarms. Currently:
- `model.Alarm` struct has no Summary field
- `event_alarms` table has no `summary` column
- `todo_alarms` table has no `summary` column
- `parseAlarm()` in import.go doesn't parse SUMMARY from ICS
- `buildValarm()` in export.go doesn't emit SUMMARY

Roundtrip test confirms: imported `SUMMARY:Meeting in 1 hour` is silently dropped.

**Approach (revised after CEO review):** Hybrid -- store SUMMARY in DB for import roundtrip fidelity, but default to the parent event/todo title at export time when SUMMARY is empty. **NO change to the `--alarm` CLI format** -- this avoids a breaking change to the positional colon-separated format. Users who need custom SUMMARY values can set them via ICS import.

**Architecture decision (from eng review):** Keep `buildValarm(alarm model.Alarm)` signature unchanged. Pre-populate `alarm.Summary` at export call sites before calling `buildValarm`:
- VEVENT path (export.go:151): set `alarm.Summary = e.Title` when `alarm.Summary == "" && alarm.Action == "EMAIL"`
- VTODO path (export.go:408): set `alarm.Summary = t.Summary` when same condition

This keeps `buildValarm` as a pure alarm-to-component mapper with no parent knowledge.

**Files:**
- `internal/model/alarm.go` -- add Summary field
- `db/migrations/010_alarm_summary.sql` -- add `summary` column to `event_alarms` and `todo_alarms` (with DOWN section for rollback)
- `db/queries/alarms.sql` -- update queries to include summary
- `db/queries/todo_alarms.sql` -- same for todos
- `internal/storage/` -- regenerate sqlc
- `internal/ical/import.go` -- parse SUMMARY in `parseAlarm()`
- `internal/ical/export.go` -- emit SUMMARY in `buildValarm()` when non-empty; pre-populate fallback at call sites
- `cmd/tcal/output.go` -- add summary to `jsonAlarm`

**Steps:**
1. Add `Summary string` field to `model.Alarm`
2. Create migration `010_alarm_summary.sql`:
   - UP: `ALTER TABLE event_alarms ADD COLUMN summary TEXT NOT NULL DEFAULT '';` same for `todo_alarms`
   - DOWN: `ALTER TABLE event_alarms DROP COLUMN summary;` same for `todo_alarms`
3. Update sqlc queries to include summary in INSERT and SELECT, regenerate
4. Parse SUMMARY in `parseAlarm()` import: `if prop := comp.Props.Get(ical.PropSummary); prop != nil { alarm.Summary = prop.Value }`
5. Emit SUMMARY in `buildValarm()` export: `if alarm.Summary != "" { valarm.Props.SetText(ical.PropSummary, alarm.Summary) }`
6. At VEVENT export call site: before calling buildValarm, if `alarm.Summary == "" && alarm.Action == "EMAIL"`, set `alarm.Summary = e.Title`
7. At VTODO export call site: same with `t.Summary`
8. Add `Summary string` to `jsonAlarm` struct with `json:"summary,omitempty"` tag
9. Populate Summary in both jsonAlarm construction sites (output.go:183-188 and 558-563)
10. Tests (see test plan below)

---

## Task 2: Warn when EMAIL alarm has no ATTENDEE

**Validation gap:** RFC 5545 requires EMAIL action to have at least one ATTENDEE. Currently `parseOneAlarm()` accepts `EMAIL:-PT1H` with no attendees silently.

**Design choice:** Warn to stderr, don't error. Reason: EMAIL falls back to DISPLAY if SMTP is not configured, so a zero-attendee EMAIL alarm still works as a DISPLAY alarm. Hard-erroring would break existing workflows.

**Architecture decision (from eng review):** Place the warning in `parseAlarmFlags` (the caller), not inside `parseOneAlarm`. This keeps `parseOneAlarm` as a pure parser function with no I/O side effects.

**Files:**
- `cmd/tcal/event.go` -- add warning in `parseAlarmFlags` after each `parseOneAlarm` call

**Steps:**
1. In `parseAlarmFlags`, after `a, err := parseOneAlarm(val)`, check: if `a.Action == "EMAIL" && len(a.Attendees) == 0`, write warning to stderr
2. Warning text: `tcal: warning: EMAIL alarm has no attendees (RFC 5545 requires at least one; alarm will behave as DISPLAY)`

---

## Task 3: Improve `alarm list` help text and documentation

**Doc gap:** The `alarm list` help text is minimal. A first-time user doesn't know what the output looks like or how the alarm lifecycle works.

**Files:**
- `cmd/tcal/alarm.go` -- rewrite `alarmListCmd()` Long description and examples

**Acceptance criteria:**
1. Describe output format: state ID in brackets, trigger time, action type, event title, snoozed status
2. Document JSON output fields: id, alarm_id, event_id, event, action, trigger_at, fired_at, snoozed_to
3. Add workflow examples: check -> list -> dismiss/snooze cycle
4. Cross-reference `alarm check` for how alarms enter the pending list
5. Note that dismissed alarms are permanently removed from the list

---

## Test Plan

### Task 1 tests (roundtrip_test.go or new alarm_summary_test.go):

1. **EMAIL alarm with SUMMARY roundtrip**: Import ICS with `SUMMARY:Custom Subject` on EMAIL alarm -> export -> verify SUMMARY present in output
2. **DISPLAY alarm with SUMMARY roundtrip**: Import ICS with SUMMARY on DISPLAY alarm -> export -> verify SUMMARY preserved (Decision #3: emit whenever non-empty)
3. **EMAIL alarm with empty SUMMARY fallback**: Create event via CLI with `--alarm "EMAIL:-PT1H"` -> export -> verify SUMMARY equals event title in exported ICS
4. **Todo alarm SUMMARY roundtrip**: Import ICS VTODO with SUMMARY on alarm -> export -> verify preserved
5. **SUMMARY with special characters**: Import ICS with SUMMARY containing colons and semicolons -> export -> verify not corrupted

### Task 2 tests (event_test.go):

6. **EMAIL without ATTENDEE parses but warns**: Call `parseAlarmFlags([]string{"EMAIL:-PT1H"})` -> succeeds -> verify alarm has Action=EMAIL and no attendees (warning goes to stderr, not captured in unit test but verifiable in integration)

---

## NOT in scope

- **CLI `--alarm` format changes for SUMMARY**: Positional format is fragile. Adding fields would break existing attendee strings. Users set custom SUMMARY via ICS import.
- **Filtering flags** (`--calendar`, `--event`, `--action`): Nice-to-have but not blocking RFC compliance.
- **Show dismissed alarms** (`--all`): History feature, separate concern.
- **AUDIO ATTACH property**: RFC 5545 recommends but doesn't require it for AUDIO action.
- **Daemon reliability bugs** (absolute triggers, DST): Separate workstream, higher user impact. Should be prioritized next.
- **Generic roundtrip fidelity harness**: Would catch future property-drop bugs systematically. Deferred to TODOS.
- **EMAIL DESCRIPTION default**: RFC also requires DESCRIPTION for EMAIL; pre-existing gap not introduced by this plan.
- **Import text field length cap**: General import validation concern, not SUMMARY-specific.

---

## What already exists

| Sub-problem | Existing code |
|---|---|
| Alarm data model (all fields except Summary) | `internal/model/alarm.go` |
| Extended alarm CLI format | `cmd/tcal/event.go:948-1068` |
| ICS export with VALARM | `internal/ical/export.go:492-535` |
| ICS import with VALARM | `internal/ical/import.go:407-440` |
| JSON alarm output (all fields except Summary) | `cmd/tcal/output.go:100-114` |
| Alarm state lifecycle | `internal/alarm/service.go` |
| Roundtrip tests | `internal/ical/roundtrip_test.go` |

<!-- AUTONOMOUS DECISION LOG -->
## Decision Audit Trail

| # | Phase | Decision | Classification | Principle | Rationale | Rejected |
|---|-------|----------|---------------|-----------|-----------|----------|
| 1 | CEO | Use dedicated DB column for SUMMARY | Mechanical | P5 | Every alarm field is a column; matches existing pattern | JSON blob, concatenate with DESC |
| 2 | CEO | Hybrid: store in DB, default to event title on export, NO CLI format change | Taste | P5+P3 | Achieves RFC compliance + roundtrip fidelity; avoids breaking positional format | Full surface area with CLI format change |
| 3 | CEO | Emit SUMMARY whenever non-empty, not just EMAIL | Mechanical | P5 | Stored data should roundtrip regardless of action type | Only emit for EMAIL |
| 4 | CEO | Fire-time EMAIL errors already logged (alarm.go:137) | Mechanical | P6 | Already handled | Add duplicate logging |
| 5 | CEO | Daemon bugs separate scope | Mechanical | P3 | Different workstream; noted priority concern | Pull into this plan |
| 6 | CEO | Roundtrip fidelity harness deferred to TODOS | Taste | P3 | Good idea but scope expansion; specific SUMMARY test covers this plan | Build harness now |
| 7 | Eng | Pre-populate alarm.Summary at export call sites, keep buildValarm signature unchanged | Mechanical | P5 | Lower coupling, function stays a pure mapper | Change buildValarm signature |
| 8 | Eng | EMAIL DESCRIPTION default is pre-existing, defer | Mechanical | P3 | Not introduced by this plan | Fix in this plan |
| 9 | Eng | alarm list uses state data, not jsonAlarm; no change needed for list path | Mechanical | P5 | Summary is for event/todo JSON output | Thread SUMMARY into alarm list |
| 10 | Eng | Add DOWN section to migration | Mechanical | P1 | Completeness for rollback safety | Skip rollback |
| 11 | Eng | Move EMAIL warning to parseAlarmFlags (caller) | Mechanical | P5 | Keep parseOneAlarm as pure parser | Write to stderr inside parser |
| 12 | Eng | Expand test plan: 5 roundtrip scenarios + 1 warning test | Mechanical | P1 | Roundtrip fidelity fix needs roundtrip tests | Single happy-path test |

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 1 | Clean | 7 findings (3 high: format breaking change, inherit-from-event alternative, positional format scaling). Resolved via hybrid approach. |
| Codex Review | `/codex review` | Independent 2nd opinion | 1 | Clean | Aligned with CEO findings on format fragility and EMAIL usage reality. |
| Eng Review | `/plan-eng-review` | Architecture & tests | 1 | Clean | 8 findings (1 high: test coverage, 2 medium: buildValarm coupling, data flow). All resolved. |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | Skipped | No UI scope. |

**VERDICT:** APPROVED. All review phases complete. Hybrid approach locks in RFC 5545 compliance without breaking CLI format.
