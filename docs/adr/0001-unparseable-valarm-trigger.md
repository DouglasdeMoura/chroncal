# ADR 0001: Drop an unparseable VALARM TRIGGER at import

- Status: Accepted
- Decided in: `2d7b926a`
- Related issues: #570, #567, #572, #568

## Context

A VALARM requires a TRIGGER property. RFC 5545 section 3.6.6 defines this
requirement. Some broken producers send values like `TRIGGER:next monday`.
chroncal cannot parse such a value.

No single representation of an unparseable trigger is simultaneously:

- storable in the database,
- fireable by the alarm engine,
- valid on the wire as iCal.

The importer must therefore pick one property to sacrifice. This ADR
explains the choice and blocks a return to the worse options.

## Options tried

| Behavior | Commit | Failure mode |
|---|---|---|
| Drop at import, no warning | before `3a348327` | The alarm is lost without notice. |
| Preserve the raw value verbatim | `3a348327` | Export emits `VALUE=DATE-TIME` garbage. Strict servers reject the PUT with HTTP 400. The resource stays dirty forever. |
| Preserve locally, skip the VALARM on export | `6ba476d2` | The PUT omits the alarm. The next unrelated edit deletes it from the server. |
| Drop at import, with a warning | `2d7b926a` (current) | The alarm is still lost, but a warning names it. |

The current answer is the least bad option. It is not a good option.

## Decision

Drop the VALARM at import when its TRIGGER cannot parse. Emit a warning that
names the record. Do not store the raw value.

## What must not come back

Do not preserve the raw value in `alarm.TriggerValue` unless both of these
exist:

- an export path that emits the value as valid iCal,
- UI that distinguishes the broken alarm from an armed reminder.

Without both, preservation is strictly worse than dropping. The same data is
lost at the next push instead of at import. Until the next push, the user sees a
reminder that never fires, and the alarm editor refuses to open the record.

## Future work

True preservation needs a home outside the TRIGGER slot. An x-property on the
parent component, for example `X-CHRONCAL-UNPARSEABLE-VALARM`, can carry the
original text through a round trip. The alarm still does not fire. UI must
mark it broken and non-fireable. Build this only if real data shows the case
happens in practice. The machinery is not free.

## Backstop

`exportableTrigger` in `internal/ical/export.go` stays. It skips rows written
during the `3a348327` window, in which import preserved raw values. It also
catches a bad value that a future caller stores directly.
