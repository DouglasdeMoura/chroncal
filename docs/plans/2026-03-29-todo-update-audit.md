# Todo Update Audit — Fix Plan

Branch: master | Date: 2026-03-29

## Issues Found (from CLI + roundtrip testing)

### Phase 1: JSON/YAML output missing 6 fields (data loss)
- `jsonTodo` struct in `output.go:485` is missing RecurrenceRule, ExDates, RDates, Timezone, RecurrenceID, Geo
- `toJSONTodo()` at line 514 doesn't populate them
- `jsonEvent` at line 65 has all these fields — todo was never updated to match
- **Impact:** `-o json` silently drops recurrence data. Scripts consuming JSON lose fields.

### Phase 2: Text output incomplete
- `printTodo()` in `output.go:565` only shows summary, status, due, location, description, url, categories, priority, calendar, id
- Missing: start_date, duration, completed_at, class, recurrence_rule, exdates, rdates, alarms, attendees, attachments, comments, contacts, resources, relations
- **Impact:** `todo get` in text mode hides most of what was stored

### Phase 3: Cannot clear optional fields in todo update
- `--due ""` fails validation (parse error on empty string)
- No way to unset due, start, location, description, url, rrule, categories, duration
- Blocks switching from DUE to DURATION (mutually exclusive per RFC 5545)
- **Fix:** Accept empty string to clear string fields, skip validation when clearing

### Phase 4: COMPLETED + progress inconsistency
- `--status COMPLETED` auto-sets percent_complete=100
- But `--progress 75` then overrides it back to 75
- Creates COMPLETED todo at 75% — semantically inconsistent
- **Fix:** Error when --progress is explicitly non-100 with --status COMPLETED

### Phase 5: Missing help text for todo update
- `todoUpdateCmd()` has only `Short: "Update an existing todo"`
- No `Long` description, no `Example`
- `todoAddCmd()` has 6 examples and detailed behavioral text
- **Fix:** Add Long description and Examples explaining partial updates, field clearing, etc.
