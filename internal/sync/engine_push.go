package sync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

type pushResult struct {
	pushed           int
	conflicts        int
	autoResolved     int
	skippedConflicts int
	// skippedPendingDeletes counts the rows that the push held back
	// because a pending delete of their UID waits in the calendar.
	skippedPendingDeletes int
	errors                []error
	warnings              []ImportWarning
}

// push uploads the dirty resources of one calendar. strategy is the
// configured policy of a full sync pass: ConflictServerWins adopts the
// server body after a 412, ConflictPrompt records the conflict and keeps
// the local row dirty. opportunistic marks the save-time fast path after a
// user mutation: it forces the prompt behavior on a 412 (the user just
// chose these values) and disables the open-conflict skip so the failed
// PUT refreshes the recorded bodies with the latest local edit.
func (e *Engine) push(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL, pushIdentity string, strategy ConflictStrategy, opportunistic bool) (*pushResult, error) {
	// Serialize the push phase per calendar so a concurrent run cannot read the
	// same dirty row and create a duplicate server object. See pushLock and
	// issue #225.
	lock := e.pushLock(calendarID)
	lock.Lock()
	defer lock.Unlock()

	dirty, err := e.q.ListDirtySyncResourcesWithConflicts(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list dirty: %w", err)
	}
	// A tombstone of a UID in this calendar is a pending delete of its
	// object, and the tombstone pass owns that href. A dirty row of the same
	// UID must not PUT a body there first. An override that an import wrote
	// into a deleted series is such a row. Its series keeps the tombstone,
	// and the override stays local (issue #841). Only a pushed row can meet
	// this: the delete of an unpushed series removes its sync row. So the
	// push reads the tombstoned UIDs once, and only when a pushed row is
	// dirty. A plain create reads no tombstone.
	var pendingDelete map[string]bool
	for _, row := range dirty {
		if row.SyncResource.RemoteUrl == "" {
			continue
		}
		pendingDelete, err = e.pendingDeleteUIDs(ctx, calendarID)
		if err != nil {
			return nil, err
		}
		break
	}

	result := &pushResult{}
	for _, row := range dirty {
		res := row.SyncResource
		// In a full prompt-mode pass, skip resources that already have an
		// open, unresolved conflict. The local row is still dirty and carries
		// the ETag that already failed If-Match, so re-PUTing it just 412s
		// again on every sync. Hold off until the user resolves it via
		// ResolveConflict, which clears the conflict and refreshes the ETag.
		// See issue #104. ServerWins is excluded: it must process the row to
		// resolve the conflict it recorded. The opportunistic push is also
		// excluded. Its 412 refreshes the recorded local body with the latest
		// edit. A fresh write then updates the conflict row and does not wait
		// unpushed behind it.
		//
		// A move conflict skips first, under every strategy and on the
		// opportunistic push too. The skip of an edit conflict comes after the
		// organizer gate. No pass settles it. Its resolution pushes the live
		// row, not the recorded body. A new PUT can only repeat the refused
		// create and the refused source DELETE (issue #841). An edit conflict
		// keeps the rule per strategy, an unpushed moved copy included. A
		// server-wins pass adopts the server body and records the href, and
		// the source delete then goes on.
		if row.HasOpenConflict != 0 && row.OpenMoveSource != nil {
			e.logger.Debug("skip push: open move conflict pending resolution", "uid", res.Uid)
			result.skippedConflicts++
			continue
		}

		// CalDAV's PUT contract (RFC 4791 §4.1) only lets the organizer modify
		// a meeting resource. Attendees are supposed to round-trip RSVP
		// changes via iTIP REPLY, not PUT. Google rejects attendee PUTs with
		// HTTP 400 / 500 and a vague <D:error/> body. The skip of a
		// foreign-organized event here clears the dirty flag, so no sync
		// tries the PUT again. The local row stays as it is.
		if gated, oErr := gatedByOrganizerOn(ctx, e.q, calendarID, pushIdentity, res.OwnerType, res.Uid); oErr != nil || gated {
			if oErr != nil {
				// A failed lookup proves nothing. Keep the row dirty and retry
				// on the next pass rather than guess in either direction. A
				// false skip clears the dirty flag forever, and a true push
				// sends a guaranteed-rejected PUT.
				gateErr := fmt.Errorf("organizer lookup for %s: %w", res.Uid, oErr)
				result.errors = append(result.errors, gateErr)
				e.recordPushFailure(ctx, calendarID, res.Uid, gateErr)
				continue
			}
			e.logger.Info("skip push: not the organizer", "uid", res.Uid, "owner", pushIdentity)
			// The gate runs before the skip of an edit conflict. No push of
			// a row that the user does not organize settles that conflict.
			// Only a server-wins pass settles it. Under prompt and on the
			// opportunistic push, the user settles it, so the row stays
			// dirty and skipped.
			if row.HasOpenConflict != 0 {
				if strategy != ConflictServerWins || opportunistic {
					e.logger.Debug("skip push: open conflict of a gated row", "uid", res.Uid)
					result.skippedConflicts++
					continue
				}
				if res.RemoteUrl != "" {
					// A pushed row adopts the recorded server body, as the
					// server-wins edit path does, and the conflict closes
					// as server-auto.
					adopted, warns, err := e.adoptGatedEditConflict(ctx, calendarID, res)
					result.warnings = append(result.warnings, warns...)
					switch {
					case err != nil:
						result.errors = append(result.errors, err)
					case adopted:
						result.autoResolved++
					default:
						result.skippedConflicts++
					}
					continue
				}
				// An unpushed copy has no server object of its own. Its edit
				// conflict names the object at the planned href. The move
				// record below replaces it in the same statement, and a plain
				// gated row closes it after the record. A failed record then
				// keeps the edit conflict.
			}
			if res.RemoteUrl == "" {
				// An unpushed row can be the target half of a move. The
				// gate never sends its create, so the source delete waits
				// without end. A visible move conflict gives the
				// user the cancel to end the wait (issue #841). The skip
				// above handles the row on the next pass.
				// The outcome type has no value for a gone source: the
				// record finishes such a source inside its loop.
				outcome, err := e.recordGatedTargetMoveConflict(ctx, client, calendarID, res, row.HasOpenConflict != 0)
				if errors.Is(err, errTombstoneChanged) {
					e.logger.Debug("skip push: a source tombstone changed during the pass", "uid", res.Uid)
					continue
				}
				var held *moveSourceHeldError
				if errors.As(err, &held) {
					// Conflicts of other calendars hold every source. The row
					// stores the wait. The warning names the planned href of
					// the copy: the row has no server object yet. The href
					// comes from the client of this push and its remote URL,
					// with no query, in the same form as plannedHref.
					path := res.RemoteUrl
					if path == "" {
						if planned, perr := plannedHrefWith(client.CanonicalObjectRef, remoteURL, res.Uid); perr == nil {
							path = planned
						}
					}
					result.warnings = append(result.warnings, e.recordPushWait(ctx, calendarID, path, held))
					continue
				}
				if err != nil {
					result.errors = append(result.errors, err)
					e.recordPushFailure(ctx, calendarID, res.Uid, err)
					continue
				}
				if outcome == gatedRecorded {
					result.conflicts++
					continue
				}
				if outcome == gatedMoveOpen {
					// A move conflict row of the copy exists already. The
					// row stays dirty and visible: a clear of the flag
					// hides a move that no push can finish.
					gated := fmt.Errorf("push %s: the user does not organize the moved copy, so this calendar cannot create it; pick server on its move conflict", res.Uid)
					result.skippedConflicts++
					e.recordPushFailure(ctx, calendarID, res.Uid, gated)
					continue
				}
			}
			if row.HasOpenConflict != 0 {
				// A plain gated row under server-wins: no push settles its
				// edit conflict, so it closes as server-auto.
				if err := e.q.CloseOpenEditConflict(ctx, storage.CloseOpenEditConflictParams{CalendarID: calendarID, Uid: res.Uid}); err != nil {
					result.errors = append(result.errors, fmt.Errorf("close the edit conflict of %s: %w", res.Uid, err))
					continue
				}
			}
			if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
				CalendarID: calendarID,
				Uid:        res.Uid,
				Etag:       res.Etag,
			}); err != nil {
				e.logger.Error("clear non-owned dirty", "uid", res.Uid, "error", err)
			}
			continue
		}

		// The skip of an open edit conflict, per strategy (see above).
		if row.HasOpenConflict != 0 && strategy != ConflictServerWins && !opportunistic {
			e.logger.Debug("skip push: open conflict pending resolution", "uid", res.Uid)
			result.skippedConflicts++
			continue
		}

		// The skip of a pending delete runs before the export, after the
		// conflict rules. It holds back only a row that would PUT: a pushed
		// row with live local rows. A gone row goes on to the gone-row
		// branch below, which clears its dirty flag.
		if skip, err := e.holdsBackForPendingDelete(ctx, pendingDelete[res.Uid], res); err != nil {
			result.errors = append(result.errors, err)
			continue
		} else if skip {
			result.skippedPendingDeletes++
			continue
		}

		e.logger.Debug("pushing resource", "uid", res.Uid, "remote_url", res.RemoteUrl)

		// Export the local resource to iCal
		icalData, err := e.exportResource(ctx, calendarID, res.OwnerType, res.Uid)
		if err != nil {
			if errors.Is(err, errResourceMissing) {
				// No live local row backs this dirty sync_resource. The user purged it
				// from the trash, or the master and the override rows got into an
				// inconsistent state. Else the series of the UID lives in another
				// calendar. A retry on every sync races the same null lookup, so the
				// push clears the flag. It never deletes the server object: only a
				// local delete proves that the object must go (issue #842).
				//
				// A row without a server object goes instead. Nothing on the
				// server needs a delete. A row with remote_url = '' must mean
				// that a live local copy waits for a create. The source
				// delete of a move waits for such a row (issue #841).
				if res.RemoteUrl == "" {
					e.logger.Info("delete unpushed sync resource: local resource missing", "uid", res.Uid, "owner_type", res.OwnerType)
					n, derr := e.q.DeleteUnpushedSyncResource(ctx, storage.DeleteUnpushedSyncResourceParams{
						CalendarID: calendarID,
						Uid:        res.Uid,
						Rev:        res.Rev,
					})
					if derr != nil {
						e.logger.Error("delete unpushed missing resource", "uid", res.Uid, "error", derr)
						continue
					}
					// The conflicts of the gone row close with it, as the
					// delete of the item closes them.
					if n > 0 {
						if cerr := storage.CloseConflictsOfGoneItem(ctx, e.q, calendarID, res.Uid); cerr != nil {
							e.logger.Error("close the conflicts of a missing resource", "uid", res.Uid, "error", cerr)
						}
					}
					continue
				}
				e.logger.Info("clear dirty: local resource missing", "uid", res.Uid, "owner_type", res.OwnerType)
				if cerr := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
					CalendarID: calendarID,
					Uid:        res.Uid,
					Etag:       res.Etag,
				}); cerr != nil {
					e.logger.Error("clear missing-resource dirty", "uid", res.Uid, "error", cerr)
				}
				continue
			}
			e.logger.Error("export resource failed", "uid", res.Uid, "error", err)
			exportErr := fmt.Errorf("export %s: %w", res.Uid, err)
			result.errors = append(result.errors, exportErr)
			e.recordPushFailure(ctx, calendarID, res.Uid, exportErr)
			continue
		}

		// Parse the iCal data for PUT
		cal, parseErr := parseICalData(icalData)
		if parseErr != nil {
			parseErr = fmt.Errorf("parse ical for %s: %w", res.Uid, parseErr)
			result.errors = append(result.errors, parseErr)
			e.recordPushFailure(ctx, calendarID, res.Uid, parseErr)
			continue
		}

		// Determine PUT path
		var putPath string
		if res.RemoteUrl != "" {
			putPath, err = client.CanonicalObjectRef(remoteURL, res.RemoteUrl)
			if err != nil {
				err = fmt.Errorf("validate remote href for %s: %w", res.Uid, err)
				result.errors = append(result.errors, err)
				e.recordPushFailure(ctx, calendarID, res.Uid, err)
				continue
			}
		} else {
			putPath, err = client.CanonicalObjectRef(remoteURL, buildRemoteResourcePath(remoteURL, res.Uid))
			if err != nil {
				err = fmt.Errorf("build remote href for %s: %w", res.Uid, err)
				result.errors = append(result.errors, err)
				e.recordPushFailure(ctx, calendarID, res.Uid, err)
				continue
			}
		}

		// The row IDs before the first PUT tell a move from a delete, if
		// the row goes away during the PUT (queueDeleteOfVanishedObject).
		var rowIDs []int64
		if res.RemoteUrl == "" {
			rowIDs = e.resourceRowIDs(ctx, calendarID, res)
		}
		newEtag, putErr, moveErr := e.putOrSettleMove(ctx, client, calendarID, putPath, cal, res)
		if errors.Is(moveErr, errTombstoneChanged) {
			// The settle of the move skipped this pass: a write changed a
			// source tombstone. The row stays dirty, and no conflict and no
			// failure are recorded (issue #841).
			e.logger.Debug("skip push: the settle of the move waits for the next pass", "uid", res.Uid)
			continue
		}
		var held *moveSourceHeldError
		if errors.As(moveErr, &held) {
			// A move conflict of another calendar holds a source. The row
			// stays dirty, and the row stores the wait, so the sync doctor
			// and the sync status list it as a wait.
			result.warnings = append(result.warnings, e.recordPushWait(ctx, calendarID, putPath, held))
			continue
		}
		if moveErr != nil {
			var changed *moveSourceChangedError
			if errors.As(moveErr, &changed) {
				// The source object of the move changed on the server. The
				// user must pick a side, so the push records a move
				// conflict and keeps the resource dirty (issue #841).
				e.logger.Warn("move conflict detected during push", "uid", res.Uid, "source_calendar_id", changed.source.calendarID)
				if err := e.recordMoveConflict(ctx, client, calendarID, res, icalData, changed.source, false); err != nil {
					if errors.Is(err, errConflictHeld) {
						// An open edit conflict holds the moved copy. The
						// row waits for its resolution.
						e.logger.Debug("skip push: an open edit conflict holds the moved copy", "uid", res.Uid)
						result.skippedConflicts++
						continue
					}
					// No row exists. Advance the failure bookkeeping, so
					// the stuck-resource report shows the row.
					result.errors = append(result.errors, err)
					e.recordPushFailure(ctx, calendarID, res.Uid, err)
					continue
				}
				result.conflicts++
				continue
			}
			result.errors = append(result.errors, moveErr)
			e.recordPushFailure(ctx, calendarID, res.Uid, moveErr)
			continue
		}
		if putErr != nil {
			// A 412 Precondition Failed is an ETag conflict. A refused create
			// with the CALDAV:no-uid-conflict precondition (403) takes the
			// same path. The UID is in use, and the handler tells whether a
			// pending delete frees it (issue #841).
			if caldav.IsConflict(putErr) || (res.RemoteUrl == "" && caldav.IsUIDConflict(putErr)) {
				e.logger.Warn("conflict detected during push", "uid", res.Uid)
				outcome, warnings, errs := e.handlePutConflict(ctx, client, calendarID, res, putPath, icalData, strategy, opportunistic)
				result.warnings = append(result.warnings, warnings...)
				result.errors = append(result.errors, errs...)
				switch outcome {
				case putConflictRecreate:
					// The row has no href now. Send the create at the old
					// href, and the finalize records that href again.
					res.RemoteUrl, res.Etag = "", ""
					rowIDs = e.resourceRowIDs(ctx, calendarID, res)
					newEtag, putErr = e.putResource(ctx, client, putPath, cal, "", true)
				case putConflictLeftOpen:
					result.conflicts++
				case putConflictAutoResolved:
					result.autoResolved++
				case putConflictWaitsForSource:
					// The create waits for the delete of a sibling object.
					// The warning says so. handlePutConflict stores the wait
					// on the row, so the sync doctor lists the row as a wait.
				case putConflictRecordFailed:
					// No row exists, so nothing is counted. errs already
					// carries the failure to the caller.
				case putConflictServerDeleted:
					// The pull of the pass applies the remote delete.
				}
				if outcome != putConflictRecreate {
					continue
				}
			}
			if putErr != nil {
				e.logger.Error("PUT failed", "uid", res.Uid, "error", putErr)
				putErr = fmt.Errorf("put %s: %w", res.Uid, putErr)
				result.errors = append(result.errors, putErr)
				e.recordPushFailure(ctx, calendarID, res.Uid, putErr)
				continue
			}
		}

		// Store the new server ETag and clear the dirty flag — but only clear
		// dirty if the resource has not been edited since we captured res.Rev
		// (before exporting the body we just PUT). A local edit landing during
		// the PUT round-trip bumps rev and keeps dirty=1; an unconditional
		// clear here would wipe that flag and silently drop the edit (lost
		// update). The ETag still advances so the next push's If-Match matches
		// the server. See issue #92.
		if e.testHooks != nil && e.testHooks.afterPut != nil {
			e.testHooks.afterPut()
		}
		// A create records its href in the same statement, so a delete
		// after the finalize sees a pushed row and writes a tombstone.
		finalized, err := e.q.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
			CalendarID: calendarID,
			Uid:        res.Uid,
			Etag:       newEtag,
			Rev:        res.Rev,
			RemoteUrl:  createdHref(res, putPath),
			RowID:      res.ID,
		})
		if e.testHooks != nil && e.testHooks.afterFinalize != nil {
			e.testHooks.afterFinalize()
		}
		if err != nil {
			e.logger.Error("finalize pushed resource failed", "uid", res.Uid, "error", err)
			// The body reached the server, but the row keeps the old ETag
			// and the dirty flag. The next push would replay a phantom 412
			// on this resource. Report the failure in the sync result.
			result.errors = append(result.errors, fmt.Errorf("finalize push %s: %w", res.Uid, err))
		}
		// The body reached the server, so this attempt succeeded. Reset the
		// failure bookkeeping even when the finalize write above failed.
		e.clearPushFailure(ctx, calendarID, res.Uid)
		if err == nil && finalized == 0 {
			if terr := e.settleVanishedRow(ctx, calendarID, res, putPath, newEtag, rowIDs); terr != nil {
				// The object is on the server with no row and no tombstone.
				// The error says so, so the user can delete it by hand: the
				// next pull otherwise brings the item back. The row does
				// not count as pushed.
				result.errors = append(result.errors, terr)
				e.recordPushFailure(ctx, calendarID, res.Uid, terr)
				continue
			}
			result.pushed++
			continue
		}

		result.pushed++
		e.logger.Debug("pushed resource", "uid", res.Uid, "etag", newEtag)
	}

	return result, nil
}

type tombstoneResult struct {
	deleted      int
	autoResolved int
	// conflicts counts the move conflicts that the pass recorded on a
	// refused source DELETE (issue #841).
	conflicts int
	errors    []error
}

func (e *Engine) processTombstones(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*tombstoneResult, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}
	result := &tombstoneResult{}
	if len(tombstones) == 0 {
		return result, nil
	}

	// Index the last-seen ETags so each DELETE can be made conditional. A real
	// lookup failure must abort rather than silently degrade to unconditional
	// DELETEs that could clobber a concurrent remote edit, so we surface it.
	syncResources, err := e.q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list sync resources: %w", err)
	}
	etagByUID := make(map[string]string, len(syncResources))
	for _, sr := range syncResources {
		etagByUID[sr.Uid] = sr.Etag
	}

	// Index the UIDs whose source DELETE waits for a calendar move, once
	// per pass (issue #841). The view move_wait_tombstones holds the
	// rule. The DELETE waits in three cases of a local move:
	//   - An open move conflict names this calendar as the source. The
	//     user did not pick a side yet. A DELETE with the stale etag gets
	//     412 and drops the tombstone. The next pull then brings the item
	//     back into the source calendar beside the moved copy.
	//   - The target calendar of the move holds the moved copy as a row
	//     without a server object. The DELETE leaves the item in no
	//     calendar on the server until that push lands. The push of the
	//     target sends the source DELETE itself when the server refuses
	//     the create (see settleMovedSources). Otherwise the next pass of
	//     the source sends it.
	//   - The moved copy has an open edit conflict. The move record must
	//     not replace that row, so the DELETE waits for its resolution.
	waits, err := storage.MoveDeleteWaitUIDs(ctx, e.q, calendarID)
	if err != nil {
		return nil, err
	}

	targetCals := map[int64]*storage.Calendar{}
	for _, ts := range tombstones {
		wait := waits[ts.Uid]
		if !wait {
			// A local move changes tombstones without a sync lock. Read the
			// tombstone and its wait status again right before the DELETE.
			// A rewrite skips the tombstone for this pass, with no error. A
			// tombstone that waits now goes to the wait branch below.
			if e.testHooks != nil && e.testHooks.beforeTombstoneDelete != nil {
				e.testHooks.beforeTombstoneDelete()
			}
			fresh, ok, waiting, err := e.rereadTombstone(ctx, calendarID, ts, true)
			if err != nil {
				result.errors = append(result.errors, err)
				continue
			}
			if !ok {
				e.logger.Debug("tombstone changed before the DELETE: skip it for this pass", "uid", ts.Uid)
				continue
			}
			ts = fresh
			wait = waiting
		}
		if wait {
			e.logger.Debug("tombstone waits for a calendar move", "uid", ts.Uid)
			if recorded, err := e.recordReadOnlyTargetMoveConflict(ctx, client, calendarID, remoteURL, ts, etagByUID[ts.Uid], targetCals); err != nil {
				result.errors = append(result.errors, err)
			} else if recorded {
				result.conflicts++
			}
			continue
		}
		switch outcome, err := e.deleteTombstone(ctx, client, calendarID, remoteURL, ts, etagByUID[ts.Uid]); outcome {
		case tombstoneDeleted:
			result.deleted++
		case tombstoneRemoteKept:
			result.autoResolved++
		case tombstoneMoveConflict:
			result.conflicts++
		case tombstoneFailed:
			result.errors = append(result.errors, err)
		case tombstoneSkipped:
			// A local rewrite in the window. The next pass acts on it.
		}
	}
	return result, nil
}

// deleteAnswer classifies the answer of the server to one DELETE. It says
// nothing about the local rows: see sendTombstoneDelete.
type deleteAnswer int

const (
	// deleteFailed means the DELETE did not complete.
	deleteFailed deleteAnswer = iota
	// deleteDone means the server object is absent now (2xx, 404, or 410).
	deleteDone
	// deleteRefused means the server answered 412: a remote edit changed
	// the object after the last sync.
	deleteRefused
)

// tombstoneOutcome classifies how one tombstone DELETE was settled.
type tombstoneOutcome int

const (
	// tombstoneFailed means the DELETE did not complete. The tombstone
	// stays, so the next pass sends the DELETE again.
	tombstoneFailed tombstoneOutcome = iota
	// tombstoneSkipped means a local write changed the tombstone between
	// the DELETE and the action on its answer. The pass does nothing, and
	// the next pass acts on the current tombstone. It is no error.
	tombstoneSkipped
	// tombstoneDeleted means the server object is absent now. The tombstone
	// and the sync row are gone.
	tombstoneDeleted
	// tombstoneRemoteKept means the server refused the DELETE with 412
	// because of a remote edit. The tombstone is gone and the sync row stays.
	tombstoneRemoteKept
	// tombstoneMoveConflict means the server refused the DELETE with 412,
	// and the tombstone is the source half of a local move. The pass
	// recorded a move conflict on the target calendar. The tombstone and
	// the sync row stay (issue #841).
	tombstoneMoveConflict
)

// deleteTombstone sends the DELETE for one tombstone of calendarID and
// clears the local rows that the result settles. etag is the last ETag
// that the sync row saw. An empty etag sends an unconditional DELETE.
//
// A 412 means that a remote edit changed the resource after the last sync.
// The delete would discard that edit, so the pass abandons it. It drops
// the tombstone (so the DELETE does not go out again on every sync) but
// keeps the sync_resource. The next pull then imports the remote version
// again and brings back the item with its remote edit. A delete-vs-edit
// conflict always keeps the remote edit (the non-destructive choice),
// independent of ConflictStrategy.
//
// A tombstone with a target calendar (MovedToCalendarID) is the source
// half of a local move. A server that keeps a UID unique per collection
// accepts the create in the target, so the source DELETE goes out from
// this pass. A 412 then records a move conflict on the target calendar
// and keeps the tombstone (issue #841). Without the record, the next pull
// brings the remote edit back beside the moved copy. A failed record
// keeps the tombstone too, so the next pass tries again. A plain delete
// of a UID that lives in two calendars has no target calendar, so it
// follows the delete-vs-edit rule above.
func (e *Engine) deleteTombstone(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string, ts storage.Tombstone, etag string) (tombstoneOutcome, error) {
	answer, err := e.sendTombstoneDelete(ctx, client, remoteURL, ts, etag)
	if answer == deleteFailed {
		return tombstoneFailed, err
	}
	// A local move can rewrite the tombstone while the server handles the
	// DELETE. The writes below act on the answer only for the tombstone
	// version that the pass sent the DELETE for (issue #841).
	if answer == deleteDone {
		// clearDeletedTombstone guards every write by the version and by the
		// wait view, in one transaction. A miss still records that the
		// server object is gone.
		res, err := e.clearDeletedTombstone(ctx, calendarID, ts, clearAfterOwnDelete())
		if err != nil {
			return tombstoneFailed, err
		}
		if held := res.heldError(ts.Uid); held != nil {
			// A conflict of another calendar holds the tombstone. The row of
			// this calendar stores the wait, as in the settle and the pull.
			e.recordPushWait(ctx, calendarID, ts.RemoteUrl, held)
			return tombstoneSkipped, nil
		}
		if res.changed {
			e.logger.Debug("tombstone changed during its DELETE: skip the move bookkeeping", "uid", ts.Uid)
			return tombstoneSkipped, nil
		}
		return tombstoneDeleted, nil
	}
	// A 412: a remote edit changed the object. A version-only read tells
	// whether the tombstone is still the one of the DELETE.
	if _, ok, _, err := e.rereadTombstone(ctx, calendarID, ts, false); err != nil {
		return tombstoneFailed, err
	} else if !ok {
		e.logger.Debug("tombstone changed after the DELETE: skip it for this pass", "uid", ts.Uid)
		return tombstoneSkipped, nil
	}
	target, found, err := e.moveTarget(ctx, ts)
	if err != nil {
		return tombstoneFailed, fmt.Errorf("tombstone %s: %w", ts.Uid, err)
	}
	if found {
		return e.recordTombstoneMoveConflict(ctx, client, target, movedSource{
			calendarID: calendarID, remoteURL: remoteURL, tombstone: ts, etag: etag, clear: clearAfterOwnDelete(),
		})
	}
	return e.keepRemoteEdit(ctx, ts), nil
}

// keepRemoteEdit settles a refused DELETE by the delete-vs-edit rule: the
// remote edit stays, and the tombstone goes, so the DELETE does not go out
// again. The sync row stays, and the next pull imports the remote version
// again. A failed row delete only logs: the next pass gets 412 again.
func (e *Engine) keepRemoteEdit(ctx context.Context, ts storage.Tombstone) tombstoneOutcome {
	e.logger.Warn("tombstone delete conflict: remote edited, preserving remote version", "uid", ts.Uid)
	// The delete is guarded by the version that the caller read. A miss
	// means a write changed the tombstone, and the next pass acts on it.
	n, err := e.q.DeleteTombstoneIfVersion(ctx, storage.DeleteTombstoneIfVersionParams{ID: ts.ID, Version: ts.Version})
	if err != nil {
		e.logger.Warn("delete tombstone row after conflict failed", "uid", ts.Uid, "error", err)
	}
	if err == nil && n == 0 {
		return tombstoneSkipped
	}
	return tombstoneRemoteKept
}

// moveTargetRow names the moved copy of a move tombstone: the target
// calendar, the UID, and the owner type of the resource. local marks a
// target calendar without an account. Such a calendar has no sync row,
// so the row comes from the source row (issue #841).
type moveTargetRow struct {
	calendarID int64
	uid        string
	ownerType  string
	local      bool
	// replaceEdit lets the record replace an open edit conflict of the
	// copy in its upsert (CreateMoveConflictReplacingEdit).
	replaceEdit bool
	// etag is the etag of the sync row of a remote copy. The record seeds
	// the diverged-copy warning with it.
	etag string
}

// syncResource builds the sync row view of the moved copy for the
// conflict record. A local target has no stored row; the record needs
// the calendar, the UID, and the owner type only.
func (m moveTargetRow) syncResource() storage.SyncResource {
	return storage.SyncResource{CalendarID: m.calendarID, Uid: m.uid, OwnerType: m.ownerType, Etag: m.etag}
}

// moveTarget returns the moved copy of a move tombstone. found is false
// for a plain delete (no target calendar) and for a remote target without
// a sync row of the UID. A read-only target counts. The push of that
// calendar never sends the create, so the move waits there. The move
// conflict then gives the user the cancel to end the wait. The wait
// predicate (storage.MoveDeleteWaitUIDs) follows the same rule.
//
// A local target (a calendar without an account) has no sync row. The move
// conflict then lives on that calendar with the owner type of the source
// row. The local side is the export of the rows in the local calendar. The
// local pick deletes the source object with the fresh etag, and the server
// pick moves the rows back.
func (e *Engine) moveTarget(ctx context.Context, ts storage.Tombstone) (moveTargetRow, bool, error) {
	if ts.MovedToCalendarID == nil {
		return moveTargetRow{}, false, nil
	}
	cal, err := e.q.GetCalendar(ctx, *ts.MovedToCalendarID)
	if found, err := storage.Found(err); !found {
		if err != nil {
			err = fmt.Errorf("get move target calendar: %w", err)
		}
		return moveTargetRow{}, false, err
	}
	return e.moveTargetIn(ctx, ts, cal)
}

// moveTargetIn is moveTarget for a caller that holds the target calendar
// already.
func (e *Engine) moveTargetIn(ctx context.Context, ts storage.Tombstone, cal storage.Calendar) (moveTargetRow, bool, error) {
	if !storage.CalendarLinked(cal) {
		source, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: ts.CalendarID, Uid: ts.Uid})
		found, err := storage.Found(err)
		if err != nil {
			return moveTargetRow{}, false, fmt.Errorf("get move source resource: %w", err)
		}
		if !found {
			return moveTargetRow{}, false, nil
		}
		// A local target has no sync row, so its live rows tell whether
		// the moved copy exists. A copy that is gone leaves no move.
		exists, err := e.movedCopyExists(ctx, cal.ID, source.OwnerType, ts.Uid)
		if err != nil || !exists {
			return moveTargetRow{}, false, err
		}
		return moveTargetRow{calendarID: cal.ID, uid: ts.Uid, ownerType: source.OwnerType, local: true}, true, nil
	}
	target, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: cal.ID, Uid: ts.Uid})
	found, err := storage.Found(err)
	if err != nil {
		return moveTargetRow{}, false, fmt.Errorf("get move target resource: %w", err)
	}
	return moveTargetRow{calendarID: cal.ID, uid: ts.Uid, ownerType: target.OwnerType, etag: target.Etag}, found, nil
}

// recordGatedTargetMoveConflict records a move conflict for an unpushed row
// that the organizer gate holds back. It does so when the row is the target
// half of a move (a sibling move tombstone names this calendar). The outcome
// tells the caller what the row is:
//   - gatedRecorded: the record wrote a move conflict
//   - gatedMoveOpen: a move conflict of the copy waits for the user
//   - gatedPlain: a plain gated row (no move, an ended move, or a gone copy)
//
// replaceEdit lets the record replace an open edit conflict of the copy in
// the same statement (CreateMoveConflictReplacingEdit).
func (e *Engine) recordGatedTargetMoveConflict(ctx context.Context, client *caldav.Client, calendarID int64, res storage.SyncResource, replaceEdit bool) (gatedOutcome, error) {
	// A gated row with no move costs one query. With no move tombstone that
	// names this calendar and no move conflict that is not final, no move
	// into this calendar exists. A tombstone that names a third calendar
	// is the move into that calendar, so it never enters this path.
	// endMovesOf reads the list itself, with a read-only source and a
	// source of another account in it.
	hasMove, err := e.q.HasMoveIntoCalendar(ctx, storage.HasMoveIntoCalendarParams{Uid: res.Uid, CalendarID: calendarID})
	if err != nil {
		return gatedPlain, fmt.Errorf("check the moves into this calendar of %s: %w", res.Uid, err)
	}
	if hasMove == 0 {
		return gatedPlain, nil
	}
	all, err := e.movedSources(ctx, calendarID, res.Uid)
	if err != nil {
		return gatedPlain, fmt.Errorf("%s: list moved sources: %w", res.Uid, err)
	}
	var sources []movedSource
	for _, src := range all {
		if !src.plain {
			sources = append(sources, src)
		}
	}
	// The move row of the copy tells the case apart. An open edit conflict
	// is no move row, so it does not stop the record. With replaceEdit, the
	// record replaces it in the same statement. The gate closes it after
	// the record only for a plain gated row.
	existing, err := e.q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: calendarID, Uid: res.Uid})
	found, err := storage.Found(err)
	if err != nil {
		return gatedPlain, fmt.Errorf("get the move conflict of %s: %w", res.Uid, err)
	}
	if found {
		switch existing.MoveState {
		case MoveStateOpen, MoveStateLocalPicked, MoveStateCancelFailed:
			// The user has a move to settle: the row stays dirty.
			return gatedMoveOpen, nil
		default:
			// A terminal move row: the move is over. Every source tombstone
			// that names this calendar becomes a plain delete, and the row
			// is a plain gated row. A source that a conflict of another
			// calendar holds keeps its move target (endMovesOf).
			return gatedPlain, e.endMovesOf(ctx, calendarID, res.Uid)
		}
	}
	if len(sources) == 0 {
		// No source can get a record, so the path skips the record and its
		// warning. A gone copy still ends the move.
		exists, err := e.movedCopyExists(ctx, calendarID, res.OwnerType, res.Uid)
		if err != nil {
			return gatedPlain, fmt.Errorf("move %s: %w", res.Uid, err)
		}
		if !exists {
			return gatedPlain, e.endMovesOf(ctx, calendarID, res.Uid)
		}
		return gatedPlain, nil
	}
	// Every source of the move counts. The holds of all sources are read
	// first. A source that a conflict of another calendar holds gets no
	// record: that conflict names the move already. A source whose object
	// is gone finishes (recordMoveConflictFor drops its tombstone), and the
	// first live source that is not held gets the record. When no source
	// got the record and a source is held, the row waits on that hold.
	target := moveTargetRow{calendarID: calendarID, uid: res.Uid, ownerType: res.OwnerType, replaceEdit: replaceEdit}
	e.logger.Warn("move target is not organized by the user: record the move conflict", "uid", res.Uid)
	var firstHeld *moveSourceHeldError
	heldIDs := map[int64]bool{}
	for _, src := range sources {
		holders, err := e.q.ListTombstoneHoldingCalendars(ctx, storage.ListTombstoneHoldingCalendarsParams{ID: src.tombstone.ID, ExcludeCalendarID: calendarID})
		if err != nil {
			return gatedPlain, fmt.Errorf("move %s: read the holds of the source: %w", res.Uid, err)
		}
		if len(holders) > 0 {
			heldIDs[src.tombstone.ID] = true
			if firstHeld == nil {
				firstHeld = &moveSourceHeldError{uid: res.Uid, calendar: holders[0].Name, calendarID: holders[0].ID}
			}
		}
	}
	// endMove handles a gone copy. The caller clears the row as a plain
	// gated row, with no push failure and no wait: the row has nothing to
	// create. endMovesOf ends the move of each source that is not held.
	endMove := func() (gatedOutcome, error) {
		return gatedPlain, e.endMovesOf(ctx, calendarID, res.Uid)
	}
	if e.testHooks != nil && e.testHooks.beforeGatedSource != nil {
		e.testHooks.beforeGatedSource()
	}
	exists, err := e.movedCopyExists(ctx, calendarID, res.OwnerType, res.Uid)
	if err != nil {
		return gatedPlain, fmt.Errorf("move %s: %w", res.Uid, err)
	}
	if !exists {
		return endMove()
	}
	for _, src := range sources {
		if heldIDs[src.tombstone.ID] {
			continue
		}
		if e.testHooks != nil && e.testHooks.beforeGatedSource != nil {
			e.testHooks.beforeGatedSource()
		}
		recorded, err := e.recordMoveConflictFor(ctx, client, target, src)
		if errors.Is(err, errMoveSourceGone) {
			continue
		}
		var held *moveSourceHeldError
		if errors.As(err, &held) {
			// A hold that came after the read above.
			if firstHeld == nil {
				firstHeld = held
			}
			heldIDs[src.tombstone.ID] = true
			continue
		}
		if errors.Is(err, errTombstoneChanged) {
			// No gone copy: the caller keeps the row dirty for the next
			// pass, with no push failure.
			return gatedPlain, err
		}
		if err == nil && !recorded {
			// No exportable copy exists (errResourceMissing). The copy went
			// during the loop.
			return endMove()
		}
		if err != nil {
			return gatedPlain, err
		}
		return gatedRecorded, nil
	}
	if firstHeld != nil {
		// Every source that remains is held. The caller records the wait.
		return gatedPlain, firstHeld
	}
	return gatedPlain, nil
}

// endMovesOf turns every move tombstone of uid that names calendarID as the
// target into a plain delete. It reads the list right before the clear
// (ListMoveTombstonesOfTarget), so each clear has the current version. The
// list keeps a read-only source and a source of another account. The clear of
// each tombstone checks its hold in the same statement
// (ClearMoveTargetOfTombstone). So a source that a conflict of another
// calendar holds keeps its move target, also when the hold came after the
// caller read the holds. The conflict that holds it still names the move,
// and the row of that source keeps its wait. A tombstone that a write
// changed since the read stays for the next pass.
func (e *Engine) endMovesOf(ctx context.Context, calendarID int64, uid string) error {
	tombstones, err := e.q.ListMoveTombstonesOfTarget(ctx, storage.ListMoveTombstonesOfTargetParams{Uid: uid, CalendarID: &calendarID})
	if err != nil {
		return fmt.Errorf("list the move tombstones of %s: %w", uid, err)
	}
	for _, ts := range tombstones {
		n, err := e.q.ClearMoveTargetOfTombstone(ctx, storage.ClearMoveTargetOfTombstoneParams{
			ID: ts.ID, MovedToCalendarID: ts.MovedToCalendarID, Version: ts.Version,
		})
		if err != nil {
			return fmt.Errorf("end the move of %s: %w", uid, err)
		}
		if n == 0 {
			e.logger.Debug("tombstone changed or held before the end of its move: skip it", "uid", uid, "source_calendar_id", ts.CalendarID)
		}
	}
	return nil
}

// targetPushGatedFor reports whether the organizer gate of the calendar
// cal holds back the event uid. The calendar has a push identity, and the
// event has an organizer that is not that identity. A local calendar and a
// todo or a journal are never gated. It runs on the queries q, so a caller
// in a transaction reads the account and the organizer there.
func targetPushGatedFor(ctx context.Context, q *storage.Queries, cal storage.Calendar, ownerType, uid string) (bool, error) {
	if ownerType != ownerTypeEvent || !storage.CalendarLinked(cal) {
		return false, nil
	}
	account, err := q.GetAccount(ctx, *cal.AccountID)
	if err != nil {
		return false, fmt.Errorf("get account: %w", err)
	}
	return gatedByOrganizerOn(ctx, q, cal.ID, resolvePushIdentity(cal, account), ownerType, uid)
}

// gatedByOrganizerOn is the organizer gate of the push, on the queries q.
// It reports whether a push with identity must hold back the item uid of
// calendarID: an event whose organizer is not identity. The gate never
// holds back a todo, a journal, or an item of an empty identity. The push
// and targetPushGatedFor share it. A caller in a transaction passes its
// queries.
func gatedByOrganizerOn(ctx context.Context, q *storage.Queries, calendarID int64, identity, ownerType, uid string) (bool, error) {
	if identity == "" || ownerType != ownerTypeEvent {
		return false, nil
	}
	organizes, err := userOrganizesEventOn(ctx, q, calendarID, uid, identity)
	if err != nil {
		return false, err
	}
	return !organizes, nil
}

// recordReadOnlyTargetMoveConflict records a move conflict for a move whose
// target became read-only after the move. The push of that calendar never
// sends the create, so the source delete waits without end. A visible
// conflict gives the user the cancel to end the wait (issue #841). recorded
// is true when the pass wrote a new row. Three cases record nothing. The
// first is a writable target, and the second is a plain tombstone. The third
// is a target with a move conflict row of the UID (open, or resolved by the
// user). A resolved row means the user chose, and the tombstone proceeds
// once the target can push.
func (e *Engine) recordReadOnlyTargetMoveConflict(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string, ts storage.Tombstone, etag string, cals map[int64]*storage.Calendar) (bool, error) {
	if ts.MovedToCalendarID == nil {
		return false, nil
	}
	// The pass reads each target calendar once (cals). The target row
	// comes only for a read-only remote target.
	cal, ok := cals[*ts.MovedToCalendarID]
	if !ok {
		got, err := e.q.GetCalendar(ctx, *ts.MovedToCalendarID)
		found, err := storage.Found(err)
		if err != nil {
			return false, err
		}
		if found {
			cal = &got
		}
		cals[*ts.MovedToCalendarID] = cal
	}
	if cal == nil || !storage.CalendarLinked(*cal) || !calendaraccess.IsReadOnly(cal.RemoteAccess) {
		return false, nil
	}
	target, found, err := e.moveTargetIn(ctx, ts, *cal)
	if err != nil || !found {
		return false, err
	}
	// A move row of the UID, or an open edit conflict, already holds the
	// target. The pass records nothing then.
	n, err := e.q.CountMoveRecordBlockers(ctx, storage.CountMoveRecordBlockersParams{CalendarID: target.calendarID, Uid: ts.Uid})
	if err != nil {
		return false, fmt.Errorf("count move conflicts of %s: %w", ts.Uid, err)
	}
	if n > 0 {
		return false, nil
	}
	e.logger.Warn("move target is read-only: record the move conflict", "uid", ts.Uid, "target_calendar_id", target.calendarID)
	src := movedSource{calendarID: calendarID, remoteURL: remoteURL, tombstone: ts, etag: etag, clear: clearSourceGone(0)}
	recorded, err := e.recordMoveConflictFor(ctx, client, target, src)
	var held *moveSourceHeldError
	if errors.As(err, &held) {
		// A conflict of another calendar holds the source. The row of the
		// source stores the wait.
		e.recordPushWait(ctx, calendarID, ts.RemoteUrl, held)
		return false, nil
	}
	if errors.Is(err, errMoveSourceGone) || errors.Is(err, errConflictHeld) || errors.Is(err, errTombstoneChanged) {
		return false, nil
	}
	return recorded, err
}

// recordMoveConflictFor exports the moved copy and records the move
// conflict on the target. recorded is false when the moved copy is gone:
// the move then has no local side. The check reads the live copy in the
// target first (movedCopyExists). The export then reads the series in the
// target only: another calendar can hold the same UID (issue #842).
func (e *Engine) recordMoveConflictFor(ctx context.Context, client *caldav.Client, target moveTargetRow, src movedSource) (bool, error) {
	exists, err := e.movedCopyExists(ctx, target.calendarID, target.ownerType, target.uid)
	if err != nil {
		return false, fmt.Errorf("move conflict %s: %w", target.uid, err)
	}
	if !exists {
		return false, nil
	}
	icalData, err := e.exportResource(ctx, target.calendarID, target.ownerType, target.uid)
	if errors.Is(err, errResourceMissing) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("move conflict %s: export the moved copy: %w", target.uid, err)
	}
	// A local target has no stored sync row; the record takes the view of
	// the moved copy that moveTarget built.
	if err := e.recordMoveConflict(ctx, client, target.calendarID, target.syncResource(), icalData, src, target.replaceEdit); err != nil {
		if caldav.IsAbsent(err) {
			// The source object is gone, so only the create of the copy
			// remains. The finish drops the source tombstone and its sync
			// row, so no pass fails on this record again.
			res, cerr := e.clearDeletedTombstone(ctx, src.calendarID, src.tombstone, src.clear)
			if cerr != nil {
				return false, cerr
			}
			if held := res.heldError(target.uid); held != nil {
				// A conflict of another calendar holds the source. The clear
				// marked the source gone. The caller records the wait.
				return false, held
			}
			if res.changed {
				// A write changed the source tombstone, or a wait of the view
				// holds it. The move is not finished, and the copy is not
				// gone either. The caller skips the record for this pass.
				return false, errTombstoneChanged
			}
			return false, errMoveSourceGone
		}
		return false, err
	}
	return true, nil
}

// errMoveSourceGone reports that the source object of a move is gone from
// the server (404 or 410) when a move record reads it. The move is then
// finished (recordMoveConflictFor).
var errMoveSourceGone = errors.New("the source object of the move is gone")

// recordTombstoneMoveConflict records the move conflict of a source DELETE
// that got 412 in the tombstone pass. The record writes a row of the target
// calendar under the locks of the source calendar. That is safe without the
// target's locks. The record is one guarded upsert. A resolve of the row
// holds the account lock of the source (lockCalendars with both calendars),
// which excludes this pass. target is the sync row of the moved copy. The
// local side is the export of that copy. A moved copy that is gone leaves no
// move to settle, so the tombstone follows the plain delete-vs-edit rule
// instead.
func (e *Engine) recordTombstoneMoveConflict(ctx context.Context, client *caldav.Client, target moveTargetRow, src movedSource) (tombstoneOutcome, error) {
	e.logger.Warn("move conflict detected in the tombstone pass", "uid", target.uid, "source_calendar_id", src.calendarID, "target_calendar_id", target.calendarID, "local_target", target.local)
	recorded, err := e.recordMoveConflictFor(ctx, client, target, src)
	if errors.Is(err, errMoveSourceGone) {
		return tombstoneDeleted, nil
	}
	if errors.Is(err, errTombstoneChanged) {
		// A write changed the tombstone. This is no gone copy, so the plain
		// rule does not apply. The next pass acts on the current state.
		return tombstoneSkipped, nil
	}
	var held *moveSourceHeldError
	if errors.As(err, &held) {
		// A conflict of another calendar holds the source. The row of the
		// source stores the wait.
		e.recordPushWait(ctx, src.calendarID, src.tombstone.RemoteUrl, held)
		return tombstoneSkipped, nil
	}
	if err != nil {
		return tombstoneFailed, err
	}
	if !recorded {
		// The moved copy is in the trash or purged. The move has no local
		// side any more, so the plain delete-vs-edit rule applies.
		e.logger.Info("the moved copy is gone; the tombstone follows the plain rule", "uid", target.uid)
		return e.keepRemoteEdit(ctx, src.tombstone), nil
	}
	return tombstoneMoveConflict, nil
}

// sendTombstoneDelete sends the DELETE of one tombstone and classifies the
// answer of the server. It changes no local row. The caller settles the rows: see
// deleteTombstone for the tombstone pass and settleMovedSources for a
// calendar move. etag is the last ETag that the sync row saw. An empty
// etag sends an unconditional DELETE.
func (e *Engine) sendTombstoneDelete(ctx context.Context, client *caldav.Client, remoteURL string, ts storage.Tombstone, etag string) (deleteAnswer, error) {
	deletePath, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl)
	if hrefErr != nil {
		return deleteFailed, fmt.Errorf("validate tombstone %s: %w", ts.Uid, hrefErr)
	}
	// Make the DELETE conditional on the ETag we last saw so the server
	// rejects it (412) if the resource was edited remotely after our last
	// sync. Without this, the tombstone push would silently destroy that
	// concurrent edit. An untracked resource (never synced, or already
	// cleaned up) has no ETag, which falls back to an unconditional DELETE.
	e.logger.Debug("deleting tombstone", "uid", ts.Uid, "remote_url", deletePath)
	if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, client.DeleteResource(ctx, deletePath, etag)
	}); err != nil && !errors.Is(err, caldav.ErrResourceGone) {
		// A 404/410 means the resource is already absent server-side —
		// the desired end state — so fall through and clear the local
		// rows. Do not send the DELETE again on every sync.
		if caldav.IsConflict(err) {
			e.afterTombstoneDeleteHook()
			return deleteRefused, nil
		}
		e.logger.Warn("delete remote resource failed", "uid", ts.Uid, "error", err)
		return deleteFailed, fmt.Errorf("delete tombstone %s: %w", ts.Uid, err)
	}
	e.afterTombstoneDeleteHook()
	return deleteDone, nil
}

// resourceRowIDs lists the row IDs of res in calendarID. A failed read
// returns nil: the vanished-row check then treats the case as a delete,
// the safe side.
func (e *Engine) resourceRowIDs(ctx context.Context, calendarID int64, res storage.SyncResource) []int64 {
	ops, err := ownerOpsFor(res.OwnerType)
	if err != nil {
		return nil
	}
	ids, err := ops.rowIDs(ctx, e, calendarID, res.Uid)
	if err != nil {
		e.logger.Warn("list the rows of a resource before its PUT", "uid", res.Uid, "error", err)
		return nil
	}
	return ids
}

// queueDeleteOfVanishedObject handles a sync row that went away during
// its create PUT. The server holds the object now, so a tombstone for the
// new href queues its delete. Without it the next pull brings the item
// back (issue #841). The push and the doctor push share it.
//
// rowIDs are the row IDs of the resource before the PUT. A domain move keeps
// the row IDs and takes every row of the UID to the target. The moved copy
// waits there as an unpushed row. The tombstone names that calendar only
// when one of those rows lives there now, so the refused create there
// settles the delete. A delete keeps the rows in the trash of this calendar,
// and a purge removes them. An unrelated import of the UID has other row
// IDs. None of those makes a move tombstone.
func (e *Engine) queueDeleteOfVanishedObject(ctx context.Context, calendarID int64, res storage.SyncResource, putPath string, rowIDs []int64) error {
	e.logger.Warn("sync resource gone after the PUT: queue the delete of the new object", "uid", res.Uid)
	movedTo, err := e.movedRowsTarget(ctx, calendarID, res, rowIDs)
	if err != nil {
		// The move lookup failed. A plain tombstone still queues the
		// delete, so the next pull does not bring the item back.
		e.logger.Warn("find the move target of a vanished row; queue a plain delete", "uid", res.Uid, "error", err)
		movedTo = nil
	}
	href := normalizeRemoteRef(putPath)
	if err := e.q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID:        calendarID,
		Uid:               res.Uid,
		RemoteUrl:         href,
		MovedToCalendarID: movedTo,
	}); err != nil {
		return fmt.Errorf("queue the delete of %s after its row went away: %w; the server keeps the object at %s, delete it there", res.Uid, err, href)
	}
	return nil
}

// movedRowsTarget returns the calendar that the rows rowIDs moved to from
// calendarID, when that calendar holds the moved copy as an unpushed sync
// row. It returns nil for every other case.
func (e *Engine) movedRowsTarget(ctx context.Context, calendarID int64, res storage.SyncResource, rowIDs []int64) (*int64, error) {
	ops, err := ownerOpsFor(res.OwnerType)
	if err != nil {
		return nil, err
	}
	for _, id := range rowIDs {
		target, found, err := ops.calendarOfRow(ctx, e, id)
		if err != nil {
			return nil, fmt.Errorf("find the calendar of row %d: %w", id, err)
		}
		if !found || target == calendarID {
			continue
		}
		sr, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: target, Uid: res.Uid})
		found, err = storage.Found(err)
		if err != nil {
			return nil, fmt.Errorf("get the sync row of the moved copy: %w", err)
		}
		if found && sr.RemoteUrl == "" {
			return &target, nil
		}
	}
	return nil, nil
}

// errTombstoneChanged reports that a tombstone changed after the caller
// read it, or that it waits now. Every caller turns it into a skip for this
// pass, with no error. The row stays as it is, and the next pass acts on
// the current state. It never means that a copy is gone (issue #841).
var errTombstoneChanged = errors.New("the tombstone changed or waits now")

// clearMode names the wait check of clearDeletedTombstone. The zero value
// is no mode, so a caller must pick one of the constructors below.
type clearMode int

const (
	clearModeAfterOwnDelete clearMode = iota + 1
	clearModeSourceGone
)

// tombstoneClear selects the wait check of clearDeletedTombstone. The clear
// always compares the version of the tombstone. It reads the wait check in
// the same transaction, so no write between the check and the clear can
// slip through. Build it with clearAfterOwnDelete or clearSourceGone.
type tombstoneClear struct {
	mode           clearMode
	pushCalendarID int64
}

// clearAfterOwnDelete is the check of the tombstone pass after its own
// DELETE. The tombstone must not be in the view move_wait_tombstones.
func clearAfterOwnDelete() tombstoneClear {
	return tombstoneClear{mode: clearModeAfterOwnDelete}
}

// clearSourceGone is the check for a source object that is gone for
// another reason. The settle of a move deleted it, or the server reports it
// absent. The tombstone can wait for the create of its own move then. So
// the clear checks only that no open move conflict of another calendar
// holds it (ListTombstoneHoldingCalendars). A conflict on pushCalendarID
// does not count. A caller without a push passes 0.
func clearSourceGone(pushCalendarID int64) tombstoneClear {
	return tombstoneClear{mode: clearModeSourceGone, pushCalendarID: pushCalendarID}
}

// clearResult tells the caller what clearDeletedTombstone did.
type clearResult struct {
	// changed is true when the tombstone stays for a later pass.
	changed bool
	// held is true when the tombstone stays because its wait check holds,
	// at the version that the caller read. The tombstone is marked
	// source_gone then, and the sync row keeps its href and its etag.
	held bool
	// holder names a calendar whose open move conflict holds the
	// tombstone. It can be empty for a hold of the view without such a
	// conflict.
	holder string
	// holderID is the ID of the calendar that holder names, or 0.
	holderID int64
}

// clearDeletedTombstone settles the tombstone of a server object that is
// absent now, in one transaction guarded by rule and the version of ts:
//   - On a match, the tombstone goes, and the move of a move tombstone is
//     marked finished. The sync row goes when no live row of the UID
//     remains. With live rows, the row loses its href and its etag and
//     becomes dirty, so the next push creates the item again.
//   - When the wait check of rule holds at the same version, the tombstone
//     stays and is marked source_gone. The sync row keeps its href and its
//     etag, so the local pick of the conflict that holds it finds the
//     source. That pick reads the flag and finishes the move.
//   - When a write changed the tombstone, the server object is gone all
//     the same, so the row loses its href and its etag. It becomes dirty
//     only when live rows exist. The tombstone and the move stay for the
//     next pass.
//
// Any error rolls the transaction back and keeps the tombstone, so the next
// pass sends the DELETE again (a 404) and retries.
func (e *Engine) clearDeletedTombstone(ctx context.Context, calendarID int64, ts storage.Tombstone, rule tombstoneClear) (res clearResult, err error) {
	err = e.inTx(ctx, func(tx *sql.Tx) error {
		res = clearResult{}
		qtx := e.q.WithTx(tx)
		var holders []storage.ListTombstoneHoldingCalendarsRow
		var held bool
		switch rule.mode {
		case clearModeAfterOwnDelete:
			n, err := qtx.IsTombstoneWaiting(ctx, ts.ID)
			if err != nil {
				return fmt.Errorf("tombstone %s: read its wait status: %w", ts.Uid, err)
			}
			held = n > 0
			if held {
				// The view can hold the tombstone through a conflict of a
				// calendar. The name of that calendar goes into the report.
				holders, err = qtx.ListTombstoneHoldingCalendars(ctx, storage.ListTombstoneHoldingCalendarsParams{ID: ts.ID, ExcludeCalendarID: 0})
				if err != nil {
					return fmt.Errorf("tombstone %s: read the holds of its move: %w", ts.Uid, err)
				}
			}
		case clearModeSourceGone:
			var err error
			holders, err = qtx.ListTombstoneHoldingCalendars(ctx, storage.ListTombstoneHoldingCalendarsParams{ID: ts.ID, ExcludeCalendarID: rule.pushCalendarID})
			if err != nil {
				return fmt.Errorf("tombstone %s: read the holds of its move: %w", ts.Uid, err)
			}
			held = len(holders) > 0
		default:
			return fmt.Errorf("tombstone %s: the clear has no wait check", ts.Uid)
		}
		var miss bool
		if held {
			n, err := qtx.MarkTombstoneSourceGone(ctx, storage.MarkTombstoneSourceGoneParams{ID: ts.ID, Version: ts.Version})
			if err != nil {
				return fmt.Errorf("tombstone %s: mark the source gone: %w", ts.Uid, err)
			}
			if n > 0 {
				res = clearResult{changed: true, held: true}
				if len(holders) > 0 {
					res.holder, res.holderID = holders[0].Name, holders[0].ID
				}
				return nil
			}
			miss = true
		} else {
			n, err := qtx.DeleteTombstoneIfVersion(ctx, storage.DeleteTombstoneIfVersionParams{ID: ts.ID, Version: ts.Version})
			if err != nil {
				return fmt.Errorf("tombstone %s: delete it: %w", ts.Uid, err)
			}
			miss = n == 0
		}
		found, live, err := liveRowsOfSyncRow(ctx, qtx, calendarID, ts.Uid)
		if err != nil {
			return fmt.Errorf("tombstone %s: %w", ts.Uid, err)
		}
		if miss {
			res.changed = true
			if !found {
				return nil
			}
			if live > 0 {
				return qtx.ClearSyncResourceRemote(ctx, storage.ClearSyncResourceRemoteParams{CalendarID: calendarID, Uid: ts.Uid})
			}
			return qtx.ClearSyncResourceHref(ctx, storage.ClearSyncResourceHrefParams{CalendarID: calendarID, Uid: ts.Uid})
		}
		if ts.MovedToCalendarID != nil {
			if err := finishMoveOn(ctx, qtx, *ts.MovedToCalendarID, ts.Uid, calendarID); err != nil {
				return fmt.Errorf("tombstone %s: %w", ts.Uid, err)
			}
		}
		if !found {
			return nil
		}
		if live > 0 {
			return qtx.ClearSyncResourceRemote(ctx, storage.ClearSyncResourceRemoteParams{CalendarID: calendarID, Uid: ts.Uid})
		}
		return qtx.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{CalendarID: calendarID, Uid: ts.Uid})
	})
	if err != nil {
		return clearResult{}, err
	}
	return res, nil
}

// liveRowsOfSyncRow reads the sync row of uid in calendarID through q and
// counts the live rows of its owner type there. found is false when no
// sync row exists.
func liveRowsOfSyncRow(ctx context.Context, q *storage.Queries, calendarID int64, uid string) (found bool, live int64, err error) {
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	found, err = storage.Found(err)
	if err != nil || !found {
		if err != nil {
			err = fmt.Errorf("get the sync row: %w", err)
		}
		return found, 0, err
	}
	ops, err := ownerOpsFor(sr.OwnerType)
	if err != nil {
		return true, 0, err
	}
	live, err = ops.countLiveRows(ctx, q, calendarID, uid)
	if err != nil {
		return true, 0, fmt.Errorf("count the live rows: %w", err)
	}
	return true, live, nil
}

// finishMoveOn is the one writer of the finished state of a move conflict,
// on the queries q. A caller in a transaction marks the move finished in
// that transaction. targetCalendarID holds the conflict row, and
// sourceCalendarID is the source of the move. An open row takes the resolution
// local too (FinishMoveConflict), so the push of the target goes on with
// the create.
func finishMoveOn(ctx context.Context, q *storage.Queries, targetCalendarID int64, uid string, sourceCalendarID int64) error {
	if err := q.FinishMoveConflict(ctx, storage.FinishMoveConflictParams{
		CalendarID:           targetCalendarID,
		Uid:                  uid,
		MoveSourceCalendarID: &sourceCalendarID,
	}); err != nil {
		return fmt.Errorf("mark the move finished: %w", err)
	}
	return nil
}

// movedSource is an unsent tombstone of a sibling calendar: a calendar of
// the same account as the calendar that the push serves. etag is the last
// known etag of the source object, or empty.
type movedSource struct {
	calendarID int64
	remoteURL  string
	tombstone  storage.Tombstone
	etag       string
	// plain marks a tombstone that names no move target: a move from
	// before the upgrade, or a plain delete. Its delete frees the UID for
	// the create, and a 412 on it follows the plain delete-vs-edit rule.
	plain bool
	// clear is the wait check of the clear after the source object is
	// gone (clearDeletedTombstone).
	clear tombstoneClear
}

// movedSources lists the unsent tombstones of uid in the other writable
// calendars of the account. A local move between two calendars of one
// account leaves a tombstone in the source calendar and a new resource in
// the target calendar (issue #828).
func (e *Engine) movedSources(ctx context.Context, calendarID int64, uid string) ([]movedSource, error) {
	rows, err := e.q.ListSiblingTombstones(ctx, storage.ListSiblingTombstonesParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		return nil, err
	}
	var out []movedSource
	for _, row := range rows {
		if calendaraccess.IsReadOnly(row.CalendarRemoteAccess) {
			continue
		}
		ts := row.Tombstone
		out = append(out, movedSource{
			calendarID: ts.CalendarID,
			remoteURL:  storage.NullableToString(row.CalendarRemoteUrl),
			tombstone:  ts,
			etag:       row.Etag,
			plain:      ts.MovedToCalendarID == nil,
			clear:      clearSourceGone(calendarID),
		})
	}
	return out, nil
}

// moveSourceChangedError reports that the source object of a local move
// changed on the server after the move. The source DELETE got 412, so the
// server kept the remote edit in the source calendar. The tombstone of the
// source stays, so no pull brings the item back there. The push records a
// move conflict (recordMoveConflict), and the user picks a side with
// sync resolve (issue #841).
type moveSourceChangedError struct {
	source movedSource
}

func (e *moveSourceChangedError) Error() string {
	return "the source object changed on the server after the move; " +
		"pick a side with chroncal sync resolve"
}

// settleMovedSources handles a refused create PUT of a moved resource. Some
// servers keep a UID unique in the whole account. They refuse the create
// while the source object still exists: iCloud with 412, an RFC 4791 server
// with 403 and the CALDAV:no-uid-conflict precondition. The new href then
// answers 404 or 410 (issue #828). Only that case sends the source DELETEs
// that wait. retryPut reports that the caller must send the PUT once more. A
// refusal with a target object is a real conflict. retryPut is false, the
// error is nil, and the caller handles the PUT error as usual.
//
// The source DELETE is reactive. A server that accepts the create keeps the
// source object until the source calendar's own tombstone pass. A failed
// create then cannot lose the object on the server.
//
// The calendars of one account share one sync lock, so the source DELETE
// cannot race the source calendar's own pass. An error means one of three
// cases. The GET of the target href failed with a status other than 404 or
// 410. A DELETE did not complete. Or the server kept a remotely edited
// source object (*moveSourceChangedError). The caller then keeps the
// resource dirty. A failed GET proves nothing about the target object, so
// the next pass checks again. A 412 on the source DELETE keeps the source
// tombstone: the push records a move conflict, and the user picks a side.
func (e *Engine) settleMovedSources(ctx context.Context, client *caldav.Client, calendarID int64, putPath, uid string, sources []movedSource) (retryPut bool, err error) {
	_, getErr := client.GetResource(ctx, putPath)
	if getErr == nil {
		return false, nil
	}
	if !caldav.IsAbsent(getErr) {
		return false, fmt.Errorf("move %s: check the move target: %w", uid, getErr)
	}
	// Check every source before the first DELETE. A held source would
	// otherwise stop the settle after an earlier source is already gone.
	for i := range sources {
		if e.testHooks != nil && e.testHooks.beforeSettleSource != nil {
			e.testHooks.beforeSettleSource()
		}
		fresh, err := e.settleSourceReady(ctx, calendarID, uid, sources[i])
		if err != nil {
			return false, err
		}
		sources[i].tombstone = fresh
	}
	for i, src := range sources {
		// Read the source again right before its DELETE. The clear of an
		// earlier source runs between the check above and this DELETE.
		if i > 0 {
			fresh, err := e.settleSourceReady(ctx, calendarID, uid, src)
			if err != nil {
				return false, err
			}
			src.tombstone = fresh
		}
		switch answer, delErr := e.sendTombstoneDelete(ctx, client, src.remoteURL, src.tombstone, src.etag); answer {
		case deleteFailed:
			return false, fmt.Errorf("move %s: %w", uid, delErr)
		case deleteRefused:
			if src.plain {
				// No move names this calendar. The plain rule keeps the
				// remote edit, and the create stays refused: the server
				// holds the UID there.
				e.keepRemoteEdit(ctx, src.tombstone)
				return false, fmt.Errorf("create %s: the server already holds this UID in another calendar, with a remote edit; delete or move that copy", uid)
			}
			return false, fmt.Errorf("move %s: %w", uid, &moveSourceChangedError{source: src})
		case deleteDone:
			res, err := e.clearDeletedTombstone(ctx, src.calendarID, src.tombstone, src.clear)
			if err != nil {
				return false, fmt.Errorf("move %s: %w", uid, err)
			}
			if held := res.heldError(uid); held != nil {
				// A conflict of another calendar holds the source now. The
				// clear marked the source gone. The push records the wait.
				return false, held
			}
			if res.changed {
				// A write changed the tombstone. The PUT does not retry, and
				// the next pass acts on the current state.
				return false, errTombstoneChanged
			}
		}
	}
	return true, nil
}

// settleSourceReady reads the tombstone of one source of a settle again and
// returns it. A source that is gone, replaced, or retargeted ends the
// settle for this pass with errTombstoneChanged. The row stays dirty, and no
// conflict is recorded (issue #841). An open move conflict of another
// calendar that holds the source ends it with *moveSourceHeldError. The
// wait for the create of this push does not stop the settle: the settle
// sends that create. The clear reads the holds again in its transaction.
func (e *Engine) settleSourceReady(ctx context.Context, calendarID int64, uid string, src movedSource) (storage.Tombstone, error) {
	fresh, err := e.q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: src.calendarID, Uid: uid})
	found, err := storage.Found(err)
	if err != nil {
		return fresh, fmt.Errorf("move %s: read the source tombstone: %w", uid, err)
	}
	if !found || fresh.ID != src.tombstone.ID || !sameTarget(fresh.MovedToCalendarID, src.tombstone.MovedToCalendarID) {
		return fresh, errTombstoneChanged
	}
	holders, err := e.q.ListTombstoneHoldingCalendars(ctx, storage.ListTombstoneHoldingCalendarsParams{ID: fresh.ID, ExcludeCalendarID: calendarID})
	if err != nil {
		return fresh, fmt.Errorf("move %s: read the holds of the source: %w", uid, err)
	}
	if len(holders) > 0 {
		return fresh, &moveSourceHeldError{uid: uid, calendar: holders[0].Name, calendarID: holders[0].ID}
	}
	return fresh, nil
}

// moveSourceHeldError reports that an open move conflict on another
// calendar names a source of the move as its source. The source DELETE
// waits for the resolution of that conflict. The push stores the wait on the
// sync row (recordPushWait), with no push failure. The sync doctor and the
// sync status show the row as a wait (issue #841).
type moveSourceHeldError struct {
	uid string
	// calendar and calendarID name the calendar of the conflict that holds
	// the source.
	calendar   string
	calendarID int64
}

func (e *moveSourceHeldError) Error() string {
	return fmt.Sprintf("move %s waits for %s; resolve it first", e.uid, e.reason())
}

// reason names the step of the wait. The wait list derives the same text
// from the stored holder (waitReason).
func (e *moveSourceHeldError) reason() string {
	return heldWaitReason(e.calendar)
}

// The kinds of a push wait (the column wait_kind). See issue #841.
const (
	waitKindNone       = 0
	waitKindHeld       = 1
	waitKindSourceGone = 2
)

// heldWaitReason is the reason text of a hold by the move conflict of the
// calendar name.
func heldWaitReason(name string) string {
	return fmt.Sprintf("the move conflict on calendar %q", name)
}

// sourceDeleteWaitReason is the reason text of a create that waits for the
// source delete of its UID in another calendar.
const sourceDeleteWaitReason = "the source delete of the UID in another calendar"

// heldError returns the wait of a clear that a conflict of another calendar
// holds. It is nil for a hold of the view without such a conflict: the
// next pass acts on that one.
func (r clearResult) heldError(uid string) *moveSourceHeldError {
	if !r.held || r.holderID == 0 {
		return nil
	}
	return &moveSourceHeldError{uid: uid, calendar: r.holder, calendarID: r.holderID}
}

// recordPushWait stores the wait of held on the sync row of uid in
// calendarID and returns the warning for the result. A wait is no failure,
// so push_fail_count stays as it is. A failed write only logs: the next
// pass records the wait again.
func (e *Engine) recordPushWait(ctx context.Context, calendarID int64, path string, held *moveSourceHeldError) ImportWarning {
	w := ImportWarning{Path: path, UID: held.uid, Message: held.Error()}
	logImportWarnings(e.logger, []ImportWarning{w})
	if err := e.q.SetSyncResourceWait(ctx, storage.SetSyncResourceWaitParams{
		WaitKind: waitKindHeld, WaitHolderCalendarID: held.calendarID, CalendarID: calendarID, Uid: held.uid,
	}); err != nil {
		e.logger.Warn("record push wait", "uid", held.uid, "error", err)
	}
	return w
}

// sameTarget reports whether two move targets name the same calendar.
func sameTarget(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// recordMoveConflict records the conflict of a local move whose source
// object changed on the server (issue #841). The row lives on the target
// calendar, where the dirty resource is. The local body is the moved body
// that just failed the PUT. The server body is the remote edit of the
// source object, and the server etag is its etag. The resource stays dirty,
// and the source tombstone stays. Every pass leaves the row open: a move is
// an explicit choice of the user, so no strategy settles it in silence.
// The user picks a side with sync resolve. A failed fetch or encode of the
// source object records nothing (see recordConflictRow), and the error
// says so.
func (e *Engine) recordMoveConflict(ctx context.Context, client *caldav.Client, calendarID int64, res storage.SyncResource, icalData []byte, src movedSource, replaceEdit bool) error {
	sourcePath, err := client.CanonicalObjectRef(src.remoteURL, src.tombstone.RemoteUrl)
	if err != nil {
		return fmt.Errorf("move conflict %s: validate source href: %w", res.Uid, err)
	}
	sourceCalendarID := src.calendarID
	if _, _, err := e.recordConflictRow(ctx, client, calendarID, res, icalData, sourcePath, &sourceCalendarID, nil, replaceEdit); err != nil {
		return fmt.Errorf("move conflict %s: %w", res.Uid, err)
	}
	return nil
}

// recordConflictRow fetches the server object at serverPath, encodes it,
// and records the conflict row of res on calendarID. moveSource names the
// source calendar of a move conflict; nil records an edit conflict. The
// push after a 412 and the two move paths share it.
//
// A failed fetch or a failed write records nothing. A row without the
// server etag has no safe server side. A move conflict also needs the
// server body: the source UID has a tombstone, so no pull refreshes it.
// An edit conflict tolerates a body that the encoder rejects, because the
// next pull refreshes the body. After a failure the caller keeps the
// local state and tries again on the next pass. The function returns the
// fetched resource and its encoded body. fetched is the resource when the
// caller read it already; nil makes the function fetch it.
func (e *Engine) recordConflictRow(ctx context.Context, client *caldav.Client, calendarID int64, res storage.SyncResource, icalData []byte, serverPath string, moveSource *int64, fetched *caldav.Resource, replaceEdit bool) (*caldav.Resource, []byte, error) {
	serverRes := fetched
	if serverRes == nil {
		var err error
		serverRes, err = client.GetResource(ctx, serverPath)
		if err != nil {
			e.logger.Warn("fetch server resource for conflict record failed", "uid", res.Uid, "error", err)
			return nil, nil, fmt.Errorf("conflict re-fetch %s: %w", res.Uid, err)
		}
	}
	serverIcal, encodeErr := caldav.EncodeCalendar(serverRes.Data)
	if encodeErr != nil {
		e.logger.Warn("encode server resource for conflict record failed", "uid", res.Uid, "error", encodeErr)
		if moveSource != nil {
			return nil, nil, fmt.Errorf("encode server resource %s: %w", res.Uid, encodeErr)
		}
		// An edit conflict tolerates the failure. The row records an
		// empty server body, the local edit is the half to keep, and the
		// next pull refreshes the body. ResolveConflict refuses a server
		// pick without a body.
		serverIcal = nil
	}
	ownerID, lookupErr := e.lookupOwnerID(ctx, calendarID, res.OwnerType, res.Uid)
	if lookupErr != nil {
		e.logger.Warn("lookup owner id for conflict record", "uid", res.Uid, "owner_type", res.OwnerType, "error", lookupErr)
	}
	create := e.q.CreateSyncConflict
	if replaceEdit {
		create = func(ctx context.Context, arg storage.CreateSyncConflictParams) (int64, error) {
			return e.q.CreateMoveConflictReplacingEdit(ctx, storage.CreateMoveConflictReplacingEditParams(arg))
		}
	}
	n, err := create(ctx, storage.CreateSyncConflictParams{
		CalendarID:           calendarID,
		OwnerType:            res.OwnerType,
		OwnerID:              ownerID,
		Uid:                  res.Uid,
		LocalIcal:            string(icalData),
		ServerIcal:           string(serverIcal),
		ServerEtag:           serverRes.ETag,
		MoveSourceCalendarID: moveSource,
	})
	if err != nil {
		e.logger.Error("record conflict", "uid", res.Uid, "error", err)
		return nil, nil, fmt.Errorf("record conflict %s: %w", res.Uid, err)
	}
	if n == 0 {
		return nil, nil, fmt.Errorf("record conflict %s: %w", res.Uid, errConflictHeld)
	}
	if moveSource != nil && res.Etag != "" {
		// The copy at its known etag is the state that the user moved. The
		// pull then warns only about a later change of the copy.
		if err := e.q.SeedMoveConflictWarned(ctx, storage.SeedMoveConflictWarnedParams{Etag: res.Etag, CalendarID: calendarID, Uid: res.Uid}); err != nil {
			e.logger.Warn("seed the diverged-copy etag", "uid", res.Uid, "error", err)
		}
	}
	return serverRes, serverIcal, nil
}

// errConflictHeld reports that an open conflict of the other kind holds
// the resource, so the record changed nothing (see CreateSyncConflict).
// The caller skips the resource until the user resolves that conflict.
var errConflictHeld = errors.New("an open conflict of another kind holds the resource")

// putOrSettleMove sends the PUT of res and settles a refused create of a
// moved resource. The normal push and the doctor push share it.
//
// A new resource can be the target half of a local move between two
// calendars of this account. Only a server that keeps UIDs unique per
// account refuses that create: iCloud with 412, an RFC 4791 server with
// 403 and the CALDAV:no-uid-conflict precondition. The lookup of the move
// sources runs after that refusal only, so a plain create never depends on
// it (issue #828). See settleMovedSources.
//
// putErr is the result of the last PUT. moveErr reports a failed lookup of
// the move sources or a failed settle. The caller then keeps the resource
// dirty and skips the PUT result. A *moveSourceChangedError in moveErr
// asks the caller to record a move conflict (recordMoveConflict).
func (e *Engine) putOrSettleMove(ctx context.Context, client *caldav.Client, calendarID int64, putPath string, cal *ical.Calendar, res storage.SyncResource) (newEtag string, putErr, moveErr error) {
	newEtag, putErr = e.putResource(ctx, client, putPath, cal, res.Etag, res.RemoteUrl == "")
	refusedCreate := putErr != nil && res.RemoteUrl == "" && (caldav.IsConflict(putErr) || caldav.IsUIDConflict(putErr))
	if !refusedCreate {
		return newEtag, putErr, nil
	}
	movedFrom, err := e.movedSources(ctx, calendarID, res.Uid)
	if err != nil {
		// A failed lookup proves nothing. The next pass tries again.
		return "", putErr, fmt.Errorf("%s: list moved sources: %w", res.Uid, err)
	}
	if len(movedFrom) == 0 {
		return newEtag, putErr, nil
	}
	retryPut, err := e.settleMovedSources(ctx, client, calendarID, putPath, res.Uid, movedFrom)
	if err != nil {
		return "", putErr, err
	}
	if retryPut {
		newEtag, putErr = e.putAfterSettle(ctx, client, putPath, cal)
	}
	return newEtag, putErr, nil
}

// settleRetryDelays are the waits before each extra PUT after a settle. A
// server can free the UID of a deleted source object a short time after
// the DELETE. Without the extra PUTs, the object is in no calendar on the
// server until the next pass.
var settleRetryDelays = []time.Duration{500 * time.Millisecond, 2 * time.Second}

// putAfterSettle sends the PUT of a create after settleMovedSources
// deleted the move sources. It sends the PUT again after each wait of
// settleRetryDelays while the server refuses the create. Another answer
// ends the retries. The last answer is the result.
func (e *Engine) putAfterSettle(ctx context.Context, client *caldav.Client, putPath string, cal *ical.Calendar) (string, error) {
	delays := settleRetryDelays
	if e.testHooks != nil && e.testHooks.settleRetryDelays != nil {
		delays = e.testHooks.settleRetryDelays
	}
	newEtag, err := e.putResource(ctx, client, putPath, cal, "", true)
	for _, delay := range delays {
		if err == nil || (!caldav.IsConflict(err) && !caldav.IsUIDConflict(err)) {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		newEtag, err = e.putResource(ctx, client, putPath, cal, "", true)
	}
	return newEtag, err
}

// putResource sends one PUT with the sync retry policy. A PUT can reach the
// server and change the resource, and the client can still lose the
// response (for example, a connection reset while the client reads the
// body). Retry classifies that loss as transient. The retried PUT then
// sends the old If-Match again. The server already advanced the ETag, so it
// answers 412, a false conflict. After a transient failure, putResource
// counts a 412 as the success that happened when the server body equals
// the sent body. See issue #294.
//
// create sends the PUT of a new object with If-None-Match: *, and etag is
// then not used. An object at putPath already makes the server refuse the
// create with 412. The push then records a conflict and does not overwrite
// the object, for example after the link of a local calendar (issue #866).
// A retried create whose first PUT landed gets 412 too, and the body check
// above counts it as the success.
func (e *Engine) putResource(ctx context.Context, client *caldav.Client, putPath string, cal *ical.Calendar, etag string, create bool) (string, error) {
	priorAttemptMayHaveLanded := false
	return caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
		var newEtag string
		var err error
		if create {
			newEtag, err = client.CreateResource(ctx, putPath, cal)
		} else {
			newEtag, err = client.PutResource(ctx, putPath, cal, etag)
		}
		if err == nil {
			return newEtag, nil
		}
		// A 412 is never transient, so these branches are exclusive.
		if caldav.IsTransient(err) {
			priorAttemptMayHaveLanded = true
		} else if priorAttemptMayHaveLanded && caldav.IsConflict(err) {
			if landedEtag, ok := e.putAlreadyLanded(ctx, client, putPath, cal); ok {
				return landedEtag, nil
			}
		}
		return newEtag, err
	})
}

// putConflictOutcome classifies how a 412 on one resource was settled.
type putConflictOutcome int

const (
	// putConflictRecordFailed means no conflict row exists. The failure
	// surfaced as an error, so the caller counts nothing.
	putConflictRecordFailed putConflictOutcome = iota
	// putConflictLeftOpen means the row is recorded and the resource stays
	// dirty. The user must resolve the conflict by hand.
	putConflictLeftOpen
	// putConflictAutoResolved means a server-wins pass adopted the server
	// body and marked the row resolved.
	putConflictAutoResolved
	// putConflictWaitsForSource means the server refused a create and the new
	// href holds no object. A sibling calendar still holds the UID with a
	// delete that waits for its pass. The pass of that calendar sends its
	// delete, and the next push creates the object (issue #841).
	putConflictWaitsForSource
	// putConflictRecreate means the server refused an update because its
	// object is gone: another client deleted it. The local edit wins. The
	// row lost its href and its etag, and the caller sends a create (issue
	// #866).
	putConflictRecreate
	// putConflictServerDeleted means a server-wins pass met an update of an
	// object that another client deleted. The pass keeps the delete: the
	// row stays dirty, and the pull of the pass moves the item to the
	// trash (issue #884).
	putConflictServerDeleted
)

// handlePutConflict settles one 412 Precondition Failed answer. It records
// the conflict before any resolution so the local body survives even when
// the pass adopts the server version. icalData is the exact body that just
// failed the PUT, so the handler does not export it again (issue #264). The
// handler tolerates an encode failure. The row records an empty server body,
// the local edit is the half to keep, and ResolveConflict refuses a server
// pick it cannot import. A failed re-fetch records nothing. Without the
// server ETag the row has no safe server side. The failure surfaces as an
// error (see recordConflictRow). Prompt mode and the opportunistic push stop
// after the record. A server-wins pass adopts the server version and marks
// the row resolved. Every failure on that path leaves the row open, so the
// user can still resolve it by hand. See issue #610. When the object of an
// update is gone, a server-wins pass records no conflict and keeps the
// remote delete (putConflictServerDeleted, issue #884).
func (e *Engine) handlePutConflict(ctx context.Context, client *caldav.Client, calendarID int64, res storage.SyncResource, putPath string, icalData []byte, strategy ConflictStrategy, opportunistic bool) (putConflictOutcome, []ImportWarning, []error) {
	var errs []error

	// One fetch of the href serves the wait check and the conflict record.
	fetched, fetchErr := client.GetResource(ctx, putPath)
	if res.RemoteUrl == "" {
		// A refused create without an object at the new href is no edit
		// conflict: a sibling object holds the UID. putOrSettleMove settles
		// a tombstone that names this calendar, and a legacy one, inline.
		// This branch catches the rest: a sibling tombstone that names a
		// third calendar, or a plain delete of the UID (issue #841). The
		// create waits only when such a delete waits in a writable sibling
		// calendar: the pass of that calendar sends it. Without one, no
		// pass ever frees the UID, so the push reports a failure that the
		// sync doctor lists.
		if caldav.IsAbsent(fetchErr) {
			pending, heldBy, err := e.pendingSiblingDelete(ctx, calendarID, res.Uid)
			if err != nil {
				return putConflictRecordFailed, nil, append(errs, err)
			}
			if pending {
				w := ImportWarning{
					Path:    putPath,
					UID:     res.Uid,
					Message: "the server refused the create, because another calendar still holds the UID; the create waits for the source delete of the next pass",
				}
				logImportWarnings(e.logger, []ImportWarning{w})
				// The row stores the wait, so the sync doctor and the sync
				// status list it. A wait is no failure. The next push clears
				// the wait on a success or a failure.
				if err := e.q.SetSyncResourceWait(ctx, storage.SetSyncResourceWaitParams{
					WaitKind: waitKindSourceGone, WaitHolderCalendarID: 0, CalendarID: calendarID, Uid: res.Uid,
				}); err != nil {
					e.logger.Warn("record push wait", "uid", res.Uid, "error", err)
				}
				return putConflictWaitsForSource, []ImportWarning{w}, nil
			}
			// The UID can sit in a read-only sibling calendar, whose delete
			// never goes out, or in this collection under another href.
			msg := "the server already holds this UID; delete or move the other copy"
			if heldBy != "" {
				msg = fmt.Sprintf("the server already holds this UID in the read-only calendar %q; delete or move that copy", heldBy)
			}
			err = fmt.Errorf("create %s: %s", res.Uid, msg)
			e.recordPushFailure(ctx, calendarID, res.Uid, err)
			return putConflictRecordFailed, nil, append(errs, err)
		}
	}
	if res.RemoteUrl != "" && caldav.IsAbsent(fetchErr) {
		if strategy == ConflictServerWins && !opportunistic {
			return e.keepServerDelete(ctx, calendarID, res, putPath, errs)
		}
		// The update met no object: another client deleted it. The local
		// edit wins over the delete, as on the pull (keepUnpushedEdit). A
		// server-wins pass keeps the delete: its pull moves the item to
		// the trash. An open conflict keeps the delete too, because the
		// user did not pick a side yet (issue #866).
		open, err := e.q.CountOpenSyncConflicts(ctx, storage.CountOpenSyncConflictsParams{CalendarID: calendarID, Uid: res.Uid})
		if err != nil {
			return putConflictRecordFailed, nil, append(errs, fmt.Errorf("count the open conflicts of %s: %w", res.Uid, err))
		}
		if open > 0 {
			return putConflictRecordFailed, nil, append(errs, fmt.Errorf("update %s: the server deleted the item, and its conflict is open; resolve the conflict (chroncal sync conflicts)", res.Uid))
		}
		n, err := e.q.ForgetGoneServerObject(ctx, storage.ForgetGoneServerObjectParams{CalendarID: calendarID, Uid: res.Uid, RemoteUrl: res.RemoteUrl})
		if err != nil {
			return putConflictRecordFailed, nil, append(errs, fmt.Errorf("drop the href of the gone object %s: %w", res.Uid, err))
		}
		if n == 0 {
			// A write changed the row during the PUT. The next pass acts on
			// the current row.
			return putConflictRecordFailed, nil, errs
		}
		w := ImportWarning{
			Path:    putPath,
			UID:     res.Uid,
			Message: "the server deleted this item, but it has a local edit; the local edit wins, and the push creates the item again",
		}
		logImportWarnings(e.logger, []ImportWarning{w})
		return putConflictRecreate, []ImportWarning{w}, errs
	}
	if fetchErr != nil {
		e.logger.Warn("fetch server resource for conflict record failed", "uid", res.Uid, "error", fetchErr)
		return putConflictRecordFailed, nil, append(errs, fmt.Errorf("conflict re-fetch %s: %w", res.Uid, fetchErr))
	}
	serverRes, serverIcal, recordErr := e.recordConflictRow(ctx, client, calendarID, res, icalData, putPath, nil, fetched, false)
	if recordErr != nil {
		return putConflictRecordFailed, nil, append(errs, recordErr)
	}

	// Prompt mode and the opportunistic push stop here. The conflict row
	// holds both bodies and the local row stays dirty until the user
	// resolves it with ResolveConflict.
	if strategy != ConflictServerWins || opportunistic {
		return putConflictLeftOpen, nil, errs
	}

	// ServerWins: adopt the server version and mark the recorded conflict
	// resolved. Every failure below leaves the row open so the user can
	// still resolve it by hand.
	e.logger.Info("resolving conflict: server wins", "uid", res.Uid)
	if len(serverIcal) == 0 {
		// The record tolerated a body that the encoder rejects. The pass
		// cannot adopt it. Report the failure and leave the row open.
		errs = append(errs, fmt.Errorf("encode server resource %s: the server body cannot be encoded; resolve the conflict by hand", res.Uid))
		return putConflictLeftOpen, nil, errs
	}
	imported, revs, importWarnings, err := e.importICal(ctx, calendarID, string(serverIcal))
	if err != nil {
		e.logger.Error("import server resource failed", "uid", res.Uid, "error", err)
		errs = append(errs, fmt.Errorf("import server resource %s: %w", res.Uid, err))
		return putConflictLeftOpen, nil, errs
	}
	warnings := importWarnings
	if !imported {
		// The server's 412 body carried no importable VEVENT/VTODO/VJOURNAL,
		// so nothing was applied. Clearing dirty and stamping the server
		// ETag here would drop the local edit behind a server version we
		// never adopted. Keep dirty so the next push retries. Mirrors the
		// manual ResolveConflict guard. See issue #495.
		e.logger.Warn("server resource has no importable data; keeping local dirty", "uid", res.Uid)
		return putConflictLeftOpen, warnings, errs
	}
	// Clear dirty and update ETag to accept server version. Guard the clear on
	// the rev that persistImported captured inside its transaction. A local
	// edit that lands after the import committed then survives (no lost
	// update). See issues #92, #417 and #494. A create met an object at its
	// href. The same statement records that href, so the next push is a
	// conditional update of the object (issue #841). One statement commits
	// both writes, or neither.
	if err := e.clearDirtyAfterImportAt(ctx, calendarID, res.Uid, serverRes.ETag, revs[res.Uid], createdHref(res, putPath)); err != nil {
		e.logger.Error("clear dirty after conflict", "uid", res.Uid, "error", err)
		errs = append(errs, fmt.Errorf("clear dirty after conflict %s: %w", res.Uid, err))
		return putConflictLeftOpen, warnings, errs
	}
	// FinalizePushedResource clears dirty only on a rev match, so read the
	// flag back. A local edit that landed after the import keeps the
	// resource dirty and the conflict open.
	sr, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        res.Uid,
	})
	if err != nil {
		e.logger.Error("re-read sync resource after conflict", "uid", res.Uid, "error", err)
		errs = append(errs, fmt.Errorf("re-read sync resource %s: %w", res.Uid, err))
		return putConflictLeftOpen, warnings, errs
	}
	if sr.Dirty != 0 {
		e.logger.Info("conflict stays open: local edit landed during import", "uid", res.Uid)
		return putConflictLeftOpen, warnings, errs
	}
	if err := e.markConflictResolved(ctx, calendarID, res.Uid, ResolutionServerAuto); err != nil {
		e.logger.Error("mark conflict resolved", "uid", res.Uid, "error", err)
		errs = append(errs, fmt.Errorf("mark conflict resolved %s: %w", res.Uid, err))
		return putConflictLeftOpen, warnings, errs
	}
	return putConflictAutoResolved, warnings, errs
}

// keepServerDelete settles an update whose object another client deleted,
// on a server-wins pass. The pass keeps the remote delete: no conflict row
// can hold a server side, so the push records none and reports no error.
// The row stays dirty, and the pull of the same pass moves the item to the
// trash (issue #884). A warning tells the user that the local edit goes to
// the trash. The push failure of the row records the case too. When the
// pull applies no delete (for example, the server holds the UID under
// another href), the sync status then lists the row.
func (e *Engine) keepServerDelete(ctx context.Context, calendarID int64, res storage.SyncResource, putPath string, errs []error) (putConflictOutcome, []ImportWarning, []error) {
	e.logger.Info("keep the remote delete: server wins", "uid", res.Uid)
	e.recordPushFailure(ctx, calendarID, res.Uid, fmt.Errorf("update %s: the server deleted the item; a server-wins pass keeps the delete", res.Uid))
	w := ImportWarning{
		Path:    putPath,
		UID:     res.Uid,
		Message: "the server deleted this item, and the pass is server-wins; the local edit goes to the trash",
	}
	logImportWarnings(e.logger, []ImportWarning{w})
	return putConflictServerDeleted, []ImportWarning{w}, errs
}

// pendingSiblingDelete reports whether a tombstone of uid waits in a
// writable remote calendar of the same account. Such a delete goes out
// on the next pass of that calendar, so a refused create of uid waits for
// it (issue #841). A tombstone that the wait view holds waits for another
// move, so it does not count. heldBy names a read-only sibling calendar
// with a tombstone of uid when no writable one exists: its delete never
// goes out.
func (e *Engine) pendingSiblingDelete(ctx context.Context, calendarID int64, uid string) (pending bool, heldBy string, err error) {
	rows, err := e.q.ListSiblingTombstonesAny(ctx, storage.ListSiblingTombstonesAnyParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		return false, "", fmt.Errorf("%s: list sibling tombstones: %w", uid, err)
	}
	for _, row := range rows {
		// A read-only holder names itself, held or not: its delete never
		// goes out.
		if calendaraccess.IsReadOnly(row.CalendarRemoteAccess) {
			if heldBy == "" {
				heldBy = row.CalendarName
			}
			continue
		}
		if row.Held == 0 {
			pending = true
		}
	}
	if pending {
		return true, "", nil
	}
	return false, heldBy, nil
}

// recordPushFailure stores one more consecutive failed push attempt for a
// resource. The push loop and the sync doctor call it after a push attempt
// that fails on an export, a parse, or a PUT. The doctor reads the counter
// to show how long a resource stayed wedged. A failed bookkeeping write
// only logs: the push error itself stays the reported failure.
func (e *Engine) recordPushFailure(ctx context.Context, calendarID int64, uid string, err error) {
	if rerr := e.q.RecordSyncResourcePushFailure(ctx, storage.RecordSyncResourcePushFailureParams{
		LastPushError: err.Error(),
		CalendarID:    calendarID,
		Uid:           uid,
	}); rerr != nil {
		e.logger.Warn("record push failure", "uid", uid, "error", rerr)
	}
}

// clearPushFailure resets the push-failure bookkeeping after a successful
// push. A failed bookkeeping write only logs: the pushed body already
// reached the server.
func (e *Engine) clearPushFailure(ctx context.Context, calendarID int64, uid string) {
	if cerr := e.q.ClearSyncResourcePushFailure(ctx, storage.ClearSyncResourcePushFailureParams{
		CalendarID: calendarID,
		Uid:        uid,
	}); cerr != nil {
		e.logger.Warn("clear push failure", "uid", uid, "error", cerr)
	}
}

// putAlreadyLanded reports whether the server's current body for path equals
// the calendar we just PUT. It returns the server's ETag when it matches.
//
// It distinguishes a genuine 412 conflict from a retried PUT whose
// predecessor landed before its response was lost. If the server now
// holds exactly our payload, that earlier write won. We then adopt its ETag
// instead of a false conflict. A mismatch (a real concurrent edit)
// or any fetch/encode failure falls back to the 412. See #294.
func (e *Engine) putAlreadyLanded(ctx context.Context, client *caldav.Client, path string, sent *ical.Calendar) (string, bool) {
	serverRes, err := client.GetResource(ctx, path)
	if err != nil {
		return "", false
	}
	sentBody, err := caldav.EncodeCalendar(sent)
	if err != nil {
		return "", false
	}
	serverBody, err := caldav.EncodeCalendar(serverRes.Data)
	if err != nil {
		return "", false
	}
	// An empty ETag would disable the next push's If-Match precondition, so
	// fall back to the 412 rather than adopt it as a successful write.
	if serverRes.ETag == "" || !bytes.Equal(sentBody, serverBody) {
		return "", false
	}
	return serverRes.ETag, true
}

// adoptGatedEditConflict settles the open edit conflict of a pushed row
// that the organizer gate holds back, in a server-wins pass. It imports
// the recorded server body, clears the dirty flag with the server etag,
// and closes the conflict as server-auto. adopted is false when the body
// has no importable data, or a local edit landed during the import: the
// conflict then stays open (issue #841).
func (e *Engine) adoptGatedEditConflict(ctx context.Context, calendarID int64, res storage.SyncResource) (bool, []ImportWarning, error) {
	c, err := e.q.GetOpenSyncConflict(ctx, storage.GetOpenSyncConflictParams{CalendarID: calendarID, Uid: res.Uid})
	if err != nil {
		return false, nil, fmt.Errorf("get the edit conflict of %s: %w", res.Uid, err)
	}
	if c.ServerIcal == "" {
		return false, nil, fmt.Errorf("adopt the server body of %s: the conflict holds no server body; resolve the conflict by hand", res.Uid)
	}
	imported, revs, warnings, err := e.importICal(ctx, calendarID, c.ServerIcal)
	if err != nil {
		return false, warnings, fmt.Errorf("import server resource %s: %w", res.Uid, err)
	}
	if !imported {
		e.logger.Warn("server resource has no importable data; keeping local dirty", "uid", res.Uid)
		return false, warnings, nil
	}
	if err := e.clearDirtyAfterImport(ctx, calendarID, res.Uid, c.ServerEtag, revs[res.Uid]); err != nil {
		return false, warnings, fmt.Errorf("clear dirty after conflict %s: %w", res.Uid, err)
	}
	sr, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: res.Uid})
	if err != nil {
		return false, warnings, fmt.Errorf("re-read sync resource %s: %w", res.Uid, err)
	}
	if sr.Dirty != 0 {
		return false, warnings, nil
	}
	if err := e.markConflictResolved(ctx, calendarID, res.Uid, ResolutionServerAuto); err != nil {
		return false, warnings, fmt.Errorf("mark conflict resolved %s: %w", res.Uid, err)
	}
	return true, warnings, nil
}

// createdHref returns the href that the finalize of a create records: the
// normalized PUT path when res has no server object yet. It returns ""
// for an update, and the finalize then keeps the href of the row.
func createdHref(res storage.SyncResource, putPath string) string {
	if res.RemoteUrl != "" {
		return ""
	}
	return normalizeRemoteRef(putPath)
}

// settleVanishedRow handles a PUT whose sync row went away before the
// finalize (issue #841). A new row of the UID can exist now, after a delete
// and an import in the PUT window. The new row owns the UID, so nothing
// here deletes an object under it:
//   - A new row without a server object takes the href and the etag of
//     the new object and stays dirty. The next push sends its body as an
//     update with If-Match.
//   - A new row whose href is the PUT path: the PUT overwrote its object.
//     The row takes the new etag and stays dirty, so its body goes out
//     again with If-Match.
//   - A new row with another href: the object at the PUT path is an
//     orphan. The push reports an error that names it, so the user can
//     delete it. A tombstone cannot hold it: a tombstone is keyed by the
//     UID, which the new row holds.
//
// Without a new row, the delete of the new object goes in a tombstone.
func (e *Engine) settleVanishedRow(ctx context.Context, calendarID int64, res storage.SyncResource, putPath, newEtag string, rowIDs []int64) error {
	now, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: res.Uid})
	found, err := storage.Found(err)
	if err != nil {
		return fmt.Errorf("get the sync row of %s after the PUT: %w", res.Uid, err)
	}
	if !found {
		return e.queueDeleteOfVanishedObject(ctx, calendarID, res, putPath, rowIDs)
	}
	// The finalize matched no row, so a row found here has another id.
	href := normalizeRemoteRef(putPath)
	switch now.RemoteUrl {
	case "":
		e.logger.Info("sync row came back during the PUT: adopt the new object", "uid", res.Uid)
		if err := e.q.SetSyncResourceCreateConflictRef(ctx, storage.SetSyncResourceCreateConflictRefParams{
			RemoteUrl: href, Etag: newEtag, CalendarID: calendarID, Uid: res.Uid,
		}); err != nil {
			return fmt.Errorf("record the href of %s on its new row: %w", res.Uid, err)
		}
		return nil
	case href:
		e.logger.Info("sync row came back during the PUT at the same href: push its body again", "uid", res.Uid)
		if err := e.q.MarkSyncResourceDirtyWithEtag(ctx, storage.MarkSyncResourceDirtyWithEtagParams{Etag: newEtag, CalendarID: calendarID, Uid: res.Uid}); err != nil {
			return fmt.Errorf("mark the new row of %s dirty: %w", res.Uid, err)
		}
		return nil
	default:
		return fmt.Errorf("the push of %s left an object at %s, and a new row of the UID names %s; delete the object at %s on the server", res.Uid, href, now.RemoteUrl, href)
	}
}

// gatedOutcome is the result of recordGatedTargetMoveConflict. A source
// whose object is gone finishes inside the record, so the result has no
// value for it (issue #841).
type gatedOutcome int

const (
	// gatedPlain means the row is a plain gated row: no move, or the move
	// is over.
	gatedPlain gatedOutcome = iota
	// gatedRecorded means the record wrote a move conflict.
	gatedRecorded
	// gatedMoveOpen means a move conflict of the copy waits for the user.
	gatedMoveOpen
)

// pendingDeleteUIDs returns the UIDs with a tombstone in calendarID. Such a
// tombstone is a pending delete of the object, and the tombstone pass owns
// that href. The push and the doctor push read it (issue #841).
func (e *Engine) pendingDeleteUIDs(ctx context.Context, calendarID int64) (map[string]bool, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}
	out := make(map[string]bool, len(tombstones))
	for _, ts := range tombstones {
		out[ts.Uid] = true
	}
	return out, nil
}

// holdsBackForPendingDelete reports whether a pending delete of the UID of
// res holds back its PUT. pending tells whether a tombstone of the UID
// waits in the calendar. Only a pushed row with live local rows would PUT.
// A row without live rows is a gone row, which the caller settles.
func (e *Engine) holdsBackForPendingDelete(ctx context.Context, pending bool, res storage.SyncResource) (bool, error) {
	if res.RemoteUrl == "" || !pending {
		return false, nil
	}
	ops, err := ownerOpsFor(res.OwnerType)
	if err != nil {
		return false, err
	}
	live, err := ops.countLiveRows(ctx, e.q, res.CalendarID, res.Uid)
	if err != nil {
		return false, fmt.Errorf("count the live rows of %s: %w", res.Uid, err)
	}
	if live == 0 {
		return false, nil
	}
	e.logger.Debug("skip push: a pending delete of the UID waits in this calendar", "uid", res.Uid)
	return true, nil
}

// rereadTombstone reads ts again by its calendar and UID. ok is false in
// three cases: the tombstone is gone, another one replaced it, or a rewrite
// changed its version since the caller read it. With withWait, waiting
// reports that the tombstone is in the view move_wait_tombstones now. The
// read after a DELETE needs only the version. The clear checks the wait
// again in its transaction (issue #841).
func (e *Engine) rereadTombstone(ctx context.Context, calendarID int64, ts storage.Tombstone, withWait bool) (fresh storage.Tombstone, ok, waiting bool, err error) {
	fresh, err = e.q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calendarID, Uid: ts.Uid})
	found, err := storage.Found(err)
	if err != nil {
		return fresh, false, false, fmt.Errorf("tombstone %s: read it again: %w", ts.Uid, err)
	}
	if !found || fresh.ID != ts.ID || fresh.Version != ts.Version {
		return fresh, false, false, nil
	}
	if !withWait {
		return fresh, true, false, nil
	}
	n, err := e.q.IsTombstoneWaiting(ctx, fresh.ID)
	if err != nil {
		return fresh, false, false, fmt.Errorf("tombstone %s: read its wait status: %w", ts.Uid, err)
	}
	return fresh, true, n > 0, nil
}

// afterTombstoneDeleteHook runs the test hook of the window after the
// answer to a DELETE.
func (e *Engine) afterTombstoneDeleteHook() {
	if e.testHooks != nil && e.testHooks.afterTombstoneDelete != nil {
		e.testHooks.afterTombstoneDelete()
	}
}
