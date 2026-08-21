# ADR 0002: Preserve an unparseable server DTEND through a round trip

- Status: Accepted
- Related issues: #567, #649, #651

## Context

Some CalDAV servers, Exchange in particular, emit a DTEND with a TZID that
no IANA tz database resolves. chroncal cannot parse the value. RFC 5545
section 3.8.2.2 defines DTEND. chroncal must still do two things:

- show the event locally with a usable span,
- push the resource back without damage to the server copy.

The VALARM case (ADR 0001) dropped the unparseable value at import. That
answer does not fit here, for one reason: the DTEND string is valid on the
server that sent it. The raw value survives a round trip. A broken TRIGGER
had no valid wire form at all.

## Decision

Preserve the raw value, gated on provenance.

- The CalDAV pull path (`ImportFileRemote`) stores the raw DTEND in the
  x-property `X-CHRONCAL-ORIGINAL-DTEND`.
- The local row keeps a fabricated span, so the event stays functional in
  the UI. No marking is necessary. The event works: it lists, it alarms, it
  expands. A preserved VALARM could not do that.
- Export emits the stored string as DTEND whenever no DURATION is set. The
  server receives the exact value it sent.
- `emitXProperties` never writes the slot to the wire as an x-property.
  That would duplicate DTEND on the same component.
- A file import (`ImportFile`) does not set the slot. The value did not
  come from the target server. A push of the value could send the target
  server a DTEND it rejects, and the resource would stay dirty.

## Slot invalidation

The slot holds a server value, not a user value. A local edit that changes
the end time or the duration invalidates it (issue #649). The edit clears
the slot inside its own transaction, so the next export emits the edited
span. Without the clear, the edit would push the stale server value back
and the user's new end time would never reach the server.

Only local edits clear the slot. A sync upsert never goes through the local
edit path. A fresh server body sets the slot again from current data, so the
clear would destroy the value it needs to keep. The `ConflictServerWins`
accept-server path therefore still preserves the slot after a re-pull.

## What must not come back

- Do not emit `X-CHRONCAL-ORIGINAL-DTEND` as an x-property on the wire.
- Do not set the slot on a file import.
- Do not clear the slot on a sync-driven upsert.
- Do not clear the slot when a local edit leaves the span unchanged. The
  server value still matches the local end time.

## Backstop

`internal/event` clears the slot in the local update transactions
(`updateEventTx` and the override update path). Two regression tests hold
the contract: a local span edit drops the stale value, and the ServerWins
re-pull sets it again.
