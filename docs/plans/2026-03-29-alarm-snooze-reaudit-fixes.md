# Alarm Snooze Re-Audit Fixes

**Goal:** Fix 3 minor issues found during second alarm snooze audit: 1 output inconsistency, 1 test gap, 1 documentation gap.

**Context:** The alarm snooze feature is now solid after the first audit round (input validation, error messages, stable VALARM identity). This second pass found no new bugs, just polish.

---

## Task 1: Normalize snooze JSON output to UTC

**Inconsistency:** `alarm snooze -o json` outputs `"until": "2026-03-29T22:44:01-03:00"` (local time with offset), but `alarm list -o json` outputs `"snoozed_to": "2026-03-30T01:44:01Z"` (UTC). Both represent the same instant, but the different formats are confusing for scripts.

**Files:**
- `cmd/tcal/alarm.go` — change `res.Until.Format(time.RFC3339)` to `res.Until.UTC().Format(time.RFC3339)`

**Steps:**
1. Line 420: add `.UTC()` before `.Format(time.RFC3339)`

---

## Task 2: Add alarm UID round-trip test

**Test gap:** No test verifies that alarm UIDs survive ICS export -> import. The code correctly exports UID in VALARM and parses it on import, but there's no regression guard.

**Files:**
- `internal/ical/roundtrip_test.go` — add test with alarm UID

**Steps:**
1. Create event with alarm that has a known UID
2. Export to ICS
3. Import the ICS
4. Verify the alarm UID matches the original

---

## Task 3: Improve snooze help text

**Documentation gap:** `alarm snooze` doesn't explain where `<state-id>` comes from. `alarm dismiss` does: "shown in brackets in `alarm list` output". This inconsistency between sibling commands is confusing.

**Files:**
- `cmd/tcal/alarm.go` — update snooze Long description

**Steps:**
1. Add sentence explaining state-id origin (matching dismiss's wording)
2. Add note that snooze state is app-level and not exported to .ics

---
