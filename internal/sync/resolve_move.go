package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/retry"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// resolveMoveConflict resolves a move conflict (issue #841). The user moved
// the item from the source calendar to conflict.CalendarID. The source
// object changed on the server before the source DELETE. The caller holds
// the lifecycle locks of both calendars (lockCalendars).
//
// The stored state of the move (conflict.MoveState) decides the pick. The
// resolver and the engine write the state at the moment it changes:
//   - open: the record. The local pick completes the move, the server
//     pick cancels it.
//   - local-picked: the local pick ran. The picks work as for open.
//   - finished: the source object left the server. Both picks refuse.
//   - abandoned: the moved copy is gone (the moot step, or the local pick
//     that found it gone). No move back ran, and the trashed copy can
//     still hold the UID. Both picks refuse.
//   - cancelled: the server pick ran, or a local restore completed a
//     failed cancel. The local pick restores the recorded local body in
//     the source calendar, unless the target holds a live copy of the UID
//     again. The server pick refuses.
//   - cancel-failed: the cancel moved the rows back, and the import of
//     the server body failed. The local pick restores the local body, and
//     the row becomes cancelled. The server pick runs the import again.
//
// The local pick on an open move:
//   - A source row without a server object refuses the pick. Such a row
//     is a move back, and a create PUT with If-Match fails.
//   - A source without a row and without the move tombstone has no source
//     half; both picks refuse, and the state becomes finished.
//   - A target that the organizer gate holds back (the user does not
//     organize the event) cannot create the copy. A read-only remote target
//     cannot either. The pick refuses.
//   - A source object that is gone (404) finishes the move; the pick
//     refuses, and the next push creates the copy.
//   - Every refusal runs before the first write. The tombstone that the
//     pick writes again commits with the resolution.
//   - A moved copy that is gone leaves nothing to create and nothing to move
//     back. Either pick closes the row as abandoned (server-auto), as the
//     moot step does.
//   - A source with a server object and no move tombstone gets the
//     tombstone again, so the source DELETE goes out.
//   - The source sync row takes the etag of the remote edit. The
//     conditional source DELETE of the next push then matches it.
//   - When the record holds no etag, the pick fetches the current etag.
//     A server that gives no etag refuses the pick.
//   - A source without a sync row but with the move tombstone gets the
//     row again. The DELETE reads the etag from the row.
//   - The target row stays dirty when the create did not land yet. A
//     fresh remote edit after the record gets 412 again and records a
//     fresh conflict.
//
// The server pick on an open move:
//   - The move back follows the collision rule of uidmove.
//   - The pick fetches the current source object, by the href of the
//     tombstone or of the source row. A 404 means that the
//     move is finished: the pick refuses, and the push creates the copy.
//     A body that the encoder rejects, or an object without an etag,
//     refuses the pick. An unusable body changes nothing.
//   - The pick records the live body of the moved copy and the fetched
//     server body and etag. A later local pick then restores the body
//     with the right etag. A cancel that runs again skips the record.
//   - One transaction stamps the row resolved and cancelled and runs the
//     move back (storage.RecordResourceMoveBack). The rows go back to the
//     source. The source tombstone goes. An unpushed target row goes. A
//     landed target copy gets a plain tombstone, so the next pass deletes
//     it by the delete-vs-edit rule. A source without a sync row gets the
//     row again from the tombstone.
//   - The import writes the server body into the source calendar. A failed
//     import reopens the row as cancel-failed and clears the dirty flag of
//     the source row. No push then sends a part of the body.
//   - The source row takes the etag and loses the dirty flag.
func (s *Service) resolveMoveConflict(ctx context.Context, conflict storage.SyncConflict, pick string) (moveResolution, error) {
	sourceID := *conflict.MoveSourceCalendarID
	targetID := conflict.CalendarID

	switch conflict.MoveState {
	case MoveStateFinished:
		return moveResolution{}, fmt.Errorf("the move of %q is finished; move the item again", conflict.Uid)
	case MoveStateDetached:
		return moveResolution{}, fmt.Errorf("the move of %q ended because calendar %q was unlinked; move the item again", conflict.Uid, storage.CalendarLabel(ctx, s.q, sourceID))
	case MoveStateAbandoned:
		return moveResolution{}, fmt.Errorf("the move of %q is abandoned: the moved copy is gone, and the source keeps the server version; restore the copy from the trash and move it again", conflict.Uid)
	case MoveStateCancelled, MoveStateCancelFailed:
		if pick == "local" {
			// The restore pushes the recorded body from the source. A
			// read-only source never sends it, as for the open local pick.
			if err := s.refuseReadOnlySource(ctx, sourceID, conflict.Uid); err != nil {
				return moveResolution{}, err
			}
			// The rows are in the source calendar again. The restore writes
			// the recorded local body there with the etag that the cancel
			// fetched, and it does not move the item again. A live copy in the
			// target means a later move of the UID. The old body must not come
			// back beside it (defensive; a new move deletes the resolved row).
			exists, err := s.engine.movedCopyExists(ctx, targetID, conflict.OwnerType, conflict.Uid)
			if err != nil {
				return moveResolution{}, fmt.Errorf("check the moved copy: %w", err)
			}
			// movedCopyExists counts a landed copy that the cancel
			// tombstoned as gone: it is a pending delete, not a live copy.
			if exists {
				return moveResolution{}, fmt.Errorf("the target calendar holds a live copy of %q; the recorded version cannot be restored beside it", conflict.Uid)
			}
			// A later move can take the item to a third calendar of the
			// account, or to a local calendar. The old body must not come
			// back beside it there either. A copy of the UID in another
			// account is another item.
			ops, err := ownerOpsFor(conflict.OwnerType)
			if err != nil {
				return moveResolution{}, err
			}
			elsewhere, err := ops.countLiveRowsInMoveScope(ctx, s.engine, sourceID, conflict.Uid)
			if err != nil {
				return moveResolution{}, fmt.Errorf("count live copies: %w", err)
			}
			if elsewhere > 0 {
				return moveResolution{}, fmt.Errorf("another calendar holds a live copy of %q; the recorded version cannot be restored beside it", conflict.Uid)
			}
			// The restore completes the outcome of the cancel. A failed
			// cancel then no longer replays over the restored body. The
			// state commits with the resolution.
			newState := ""
			if conflict.MoveState == MoveStateCancelFailed {
				newState = MoveStateCancelled
			}
			// The etag of the push. A cancel that succeeded stamped the source
			// row. A dirty source row from the cancel (a source without a
			// server object) gets a new etag with the same rev when it pushes.
			// The row then wins. A failed cancel never stamped the row, so the
			// record (the etag that the cancel fetched) wins.
			etag := conflict.ServerEtag
			src, err := s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: sourceID, Uid: conflict.Uid})
			srcFound, err := storage.Found(err)
			if err != nil {
				return moveResolution{}, fmt.Errorf("get source sync resource: %w", err)
			}
			if conflict.MoveState == MoveStateCancelled && srcFound && src.RemoteUrl != "" && src.Etag != "" {
				etag = src.Etag
			}
			move := &restoreMove{
				newState:    newState,
				source:      conflict.MoveSourceCalendarID,
				cancelRev:   conflict.CancelSourceRev,
				cancelRowID: conflict.CancelSourceRowID,
				strictRev:   conflict.MoveState == MoveStateCancelled,
			}
			// A failed cancel can meet a gone source object. The replay of
			// the server pick refuses then. The local pick imports the
			// recorded body over the rows of the failed import. The restore
			// runs every local refusal first, and only then probes the
			// source object (probeGone). A gone object makes the row drop
			// its dead href, so the next push creates the item. A probe that
			// gets no answer, for example offline, refuses the pick. The
			// restore cannot tell a gone object from a live one. So it
			// writes nothing, and the conflict stays open.
			if conflict.MoveState == MoveStateCancelFailed && srcFound && src.RemoteUrl != "" {
				href := src.RemoteUrl
				move.probeGone = func(ctx context.Context) (bool, error) {
					res, err := s.fetchSourceObject(ctx, sourceID, storage.Tombstone{Uid: conflict.Uid, RemoteUrl: href})
					if err != nil {
						return false, probeFailure(storage.CalendarLabel(ctx, s.q, sourceID), err)
					}
					return res == nil, nil
				}
			}
			res, err := resolutionOf(s.restoreLocalVersion(ctx, conflict.CalendarID, sourceID, conflict.Uid, conflict.LocalIcal, etag, move))
			if err == nil && move.sourceGone {
				res.UID = conflict.Uid
				res.Note = "the source object is gone, so the next sync creates the item in the source calendar"
			}
			return res, err
		}
		if conflict.MoveState == MoveStateCancelled {
			return moveResolution{}, fmt.Errorf("the move of %q is already cancelled", conflict.Uid)
		}
		src, err := s.loadMoveSource(ctx, conflict)
		if err != nil {
			return moveResolution{}, err
		}
		return s.cancelMove(ctx, conflict, src, true)
	case MoveStateOpen, MoveStateLocalPicked:
	default:
		return moveResolution{}, fmt.Errorf("the move conflict of %q has the unknown state %q", conflict.Uid, conflict.MoveState)
	}
	src, err := s.loadMoveSource(ctx, conflict)
	if err != nil {
		return moveResolution{}, err
	}
	source, sourceFound := src.row, src.found
	sourceTombstone, sourceTombstoneFound := src.tombstone, src.tombstoneFound

	// Both picks need the moved copy. A copy that is gone leaves nothing
	// to create and nothing to move back: the cancel would un-delete the
	// trashed rows. The row becomes abandoned, as the moot step does.
	exists, err := s.engine.movedCopyExists(ctx, targetID, conflict.OwnerType, conflict.Uid)
	if err != nil {
		return moveResolution{}, fmt.Errorf("check the moved copy: %w", err)
	}
	if !exists {
		if err := s.q.CloseMoveConflict(ctx, storage.CloseMoveConflictParams{CalendarID: targetID, Uid: conflict.Uid}); err != nil {
			return moveResolution{}, fmt.Errorf("close move conflict: %w", err)
		}
		return moveResolution{}, fmt.Errorf("the moved copy of %q is gone, so the move is abandoned; the source keeps the server version", conflict.Uid)
	}

	if !sourceFound && !sourceTombstoneFound {
		// Defensive: the source half is gone without the finished mark.
		// Nothing remains to wait for or to act on, so the move finishes
		// whatever the target is. The settle still marks a pending create
		// dirty, and its note names a target that cannot create the copy.
		return s.settleGoneSourceRead(ctx, conflict, true)
	}

	// A clear marked the source object gone while this conflict held its
	// tombstone (source_gone). Both picks follow the rule of a gone source.
	if sourceTombstoneFound && sourceTombstone.SourceGone != 0 {
		return s.settleGoneSourceRead(ctx, conflict, false)
	}
	if pick == "server" {
		return s.cancelMove(ctx, conflict, src, false)
	}

	// The local pick completes the move. Every refusal runs before the
	// first write, so a refused pick changes nothing. The local refusals
	// run before the network read of the source object.
	if sourceFound && source.RemoteUrl == "" {
		return moveResolution{}, fmt.Errorf("the source calendar holds an unpushed copy of %q; the move is over, move the item again", conflict.Uid)
	}
	// The move took every row to the target. Live rows of the UID in the
	// source are an item that came back (an import). The source DELETE
	// removes its server object under those rows.
	ops, err := ownerOpsFor(conflict.OwnerType)
	if err != nil {
		return moveResolution{}, err
	}
	live, err := ops.countLiveRows(ctx, s.engine.q, sourceID, conflict.Uid)
	if err != nil {
		return moveResolution{}, fmt.Errorf("count the live rows of the source: %w", err)
	}
	if live > 0 {
		return moveResolution{}, fmt.Errorf("the source calendar holds %q again; delete it there or pick server", conflict.Uid)
	}
	// The source object exists, and no tombstone queues its delete when
	// the tombstone is gone. Without the tombstone no DELETE goes out, and
	// a server that keeps a UID unique per account refuses the create
	// without end. The transaction below writes the tombstone again.
	recreateTombstone := sourceFound && !sourceTombstoneFound
	fetchRef := sourceTombstone
	if recreateTombstone {
		fetchRef = storage.Tombstone{Uid: conflict.Uid, RemoteUrl: source.RemoteUrl}
	}
	target, err := s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: targetID, Uid: conflict.Uid})
	targetFound, err := storage.Found(err)
	if err != nil {
		return moveResolution{}, fmt.Errorf("get target sync resource: %w", err)
	}
	// One read decides whether the target can create the copy. The 404
	// path below reuses the answer, so both checks see one state.
	var targetRow *storage.SyncResource
	if targetFound {
		targetRow = &target
	}
	check, err := s.targetCanCreate(ctx, conflict, targetRow)
	if err != nil {
		return moveResolution{}, err
	}
	targetCal := check.cal
	switch check.block {
	case createGated:
		// The organizer gate never sends the create of an event that the
		// user does not organize. A local pick then arms the source
		// DELETE with nothing to create.
		return moveResolution{}, fmt.Errorf("the target calendar cannot create %q, because the user does not organize the event; pick server", conflict.Uid)
	case createReadOnly:
		// A read-only remote target never sends the create either.
		return moveResolution{}, fmt.Errorf("the target calendar is read-only, so it cannot create %q; pick server", conflict.Uid)
	case createReadOnlyEdit:
		// The landed copy holds an edit that no push of a read-only
		// calendar sends. The local pick would arm its loss.
		return moveResolution{}, fmt.Errorf("calendar %q is read-only and holds an unsent edit of %q; pick server", targetCal.Name, conflict.Uid)
	case createPending, createLanded, createLocal:
	}
	// A read-only source never sends the delete that the move needs.
	if err := s.refuseReadOnlySource(ctx, sourceID, conflict.Uid); err != nil {
		return moveResolution{}, err
	}
	// Every local refusal above ran without the network. Only now does the
	// pick read the source object, when the record holds no etag.
	sourceEtag := conflict.ServerEtag
	if sourceEtag == "" {
		if s.engine.testHooks != nil && s.engine.testHooks.beforeSourceFetch != nil {
			s.engine.testHooks.beforeSourceFetch()
		}
		res, err := s.fetchSourceObject(ctx, sourceID, fetchRef)
		if err != nil {
			return moveResolution{}, err
		}
		if res == nil {
			// The source object is gone. The rule of a gone source settles
			// the pick with the answer read above (settleGoneSource).
			return s.settleGoneSource(ctx, conflict, check, false)
		}
		if res.ETag == "" {
			return moveResolution{}, fmt.Errorf("the server gives no etag for the source object of %q, so the move cannot complete on this server; pick server, or delete the copy on the server by hand", conflict.Uid)
		}
		sourceEtag = res.ETag
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return moveResolution{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if recreateTombstone {
		if _, err := storage.CreateMoveTombstoneIfSynced(ctx, tx, sourceID, conflict.Uid, targetID); err != nil {
			return moveResolution{}, fmt.Errorf("create the source tombstone again: %w", err)
		}
	}
	switch {
	case sourceFound:
		if err := qtx.SetSyncResourceEtag(ctx, storage.SetSyncResourceEtagParams{
			Etag: sourceEtag, CalendarID: sourceID, Uid: conflict.Uid,
		}); err != nil {
			return moveResolution{}, fmt.Errorf("set source etag: %w", err)
		}
	case sourceTombstoneFound:
		if err := recreateSourceRowFromTombstone(ctx, qtx, sourceID, conflict, sourceTombstone, sourceEtag); err != nil {
			return moveResolution{}, err
		}
	}
	if targetFound && target.Dirty == 0 && target.RemoteUrl == "" {
		if err := qtx.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: targetID, Uid: conflict.Uid}); err != nil {
			return moveResolution{}, fmt.Errorf("mark target dirty: %w", err)
		}
	}
	if err := setMoveState(ctx, qtx, conflict, MoveStateLocalPicked); err != nil {
		return moveResolution{}, err
	}
	if err := markConflictResolvedOn(ctx, qtx, targetID, conflict.Uid, ResolutionLocal); err != nil {
		return moveResolution{}, fmt.Errorf("mark conflict resolved: %w", err)
	}
	return moveResolution{}, tx.Commit()
}

// setMoveState writes the state of the move of conflict through q. The
// write names the source calendar of the conflict. Zero rows means that
// the row changed (another kind, or another source), and the pick stops.
func setMoveState(ctx context.Context, q *storage.Queries, conflict storage.SyncConflict, state string) error {
	n, err := q.SetMoveConflictState(ctx, storage.SetMoveConflictStateParams{
		MoveState: state, CalendarID: conflict.CalendarID, Uid: conflict.Uid, MoveSourceCalendarID: conflict.MoveSourceCalendarID,
	})
	if err != nil {
		return fmt.Errorf("set move state: %w", err)
	}
	if n == 0 {
		return errConflictChanged(conflict)
	}
	return nil
}

// errConflictChanged reports a conflict row that changed under a pick.
func errConflictChanged(conflict storage.SyncConflict) error {
	return fmt.Errorf("the conflict of %q changed; run chroncal sync conflicts again", conflict.Uid)
}

// moveSourceState holds the sync row and the move tombstone of the source
// calendar of a move conflict.
type moveSourceState struct {
	row            storage.SyncResource
	found          bool
	tombstone      storage.Tombstone
	tombstoneFound bool
}

// loadMoveSource reads the sync row and the tombstone of the source. The
// terminal states refuse without them, so only the branches that act on
// the source read them.
func (s *Service) loadMoveSource(ctx context.Context, conflict storage.SyncConflict) (moveSourceState, error) {
	sourceID := *conflict.MoveSourceCalendarID
	var st moveSourceState
	var err error
	st.row, err = s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: sourceID, Uid: conflict.Uid})
	st.found, err = storage.Found(err)
	if err != nil {
		return st, fmt.Errorf("get source sync resource: %w", err)
	}
	st.tombstone, err = s.q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: sourceID, Uid: conflict.Uid})
	st.tombstoneFound, err = storage.Found(err)
	if err != nil {
		return st, fmt.Errorf("get source tombstone: %w", err)
	}
	return st, nil
}

// cancelMove is the server pick on an open move, or the second run of a
// cancel whose import failed (replay). See resolveMoveConflict.
func (s *Service) cancelMove(ctx context.Context, conflict storage.SyncConflict, src moveSourceState, replay bool) (moveResolution, error) {
	source, sourceFound := src.row, src.found
	sourceTombstone, sourceTombstoneFound := src.tombstone, src.tombstoneFound
	sourceID := *conflict.MoveSourceCalendarID
	targetID := conflict.CalendarID

	// The move back of a failed cancel removed the source tombstone. A
	// tombstone in the source now is a later delete, and the replay must
	// not undo it.
	if replay && sourceTombstoneFound {
		return moveResolution{}, fmt.Errorf("the item %q changed after the failed cancel; pick local or move it again", conflict.Uid)
	}
	serverIcal, serverEtag := conflict.ServerIcal, conflict.ServerEtag
	// The fetch reads the href of the move tombstone, or of the source row
	// when the tombstone is gone. A cancel never runs on stale data alone
	// while the source half exists.
	fetchRef, haveRef := sourceTombstone, sourceTombstoneFound
	if !haveRef && sourceFound && source.RemoteUrl != "" {
		fetchRef, haveRef = storage.Tombstone{Uid: conflict.Uid, RemoteUrl: source.RemoteUrl}, true
	}
	if haveRef {
		res, err := s.fetchSourceObject(ctx, sourceID, fetchRef)
		if err != nil {
			return moveResolution{}, err
		}
		if res == nil {
			// The source object is gone from the server. A cancel restores
			// a dead href and a stale etag, and the next pull deletes the
			// rows again. A first cancel follows the rule of a gone source
			// (settleGoneSource), as the picks do.
			if !replay {
				return s.settleGoneSourceRead(ctx, conflict, false)
			}
			// The replay found its rows in the source already, and they can
			// hold a part of a failed import. No server version exists to
			// complete the cancel, so the replay refuses and writes nothing.
			// The conflict stays open as a failed cancel. The local pick
			// ends it: it imports the recorded body over those rows.
			return moveResolution{}, fmt.Errorf("the source object of %q is gone, so the cancel cannot complete; the item stays as it is; pick local to keep the moved version in the source calendar", conflict.Uid)
		}
		fresh, err := caldav.EncodeCalendar(res.Data)
		if err != nil {
			return moveResolution{}, fmt.Errorf("the server body of %q cannot be read; fix it on the server, or pick local: %w", conflict.Uid, err)
		}
		if res.ETag == "" {
			return moveResolution{}, fmt.Errorf("the server gives no etag for the source object of %q, so the cancel cannot run on this server; pick local, or delete the copy on the server by hand", conflict.Uid)
		}
		serverIcal, serverEtag = string(fresh), res.ETag
	}
	// Check the server body before the first write. A body that the import
	// cannot use must not leave a half-cancelled move.
	parsed, err := icalPkg.ImportFileRemote(strings.NewReader(serverIcal))
	if err != nil {
		return moveResolution{}, fmt.Errorf("import server version: %w", err)
	}
	if len(parsed.Events) == 0 && len(parsed.Todos) == 0 && len(parsed.Journals) == 0 {
		return moveResolution{}, fmt.Errorf("server version has no importable data for %q", conflict.Uid)
	}
	ops, err := ownerOpsFor(conflict.OwnerType)
	if err != nil {
		return moveResolution{}, err
	}
	// The cancel discards the moved copy. Its live body goes into the
	// record, so a later local pick can restore it. A pull refreshes a
	// clean landed copy too, so the record does not depend on the dirty
	// flag. The record also takes the fetched server body and etag, so
	// that later pick pushes with the etag of the source object. A replay
	// skips the record: the live rows then hold the server body.
	var live []byte
	if !replay {
		live, err = s.engine.exportResource(ctx, conflict.CalendarID, conflict.OwnerType, conflict.Uid)
		if err != nil && !errors.Is(err, errResourceMissing) {
			return moveResolution{}, fmt.Errorf("export the moved copy: %w", err)
		}
		if err != nil {
			live = []byte(conflict.LocalIcal)
		}
	}
	// A replay finds the rows in the source already: the failed cancel
	// moved them back. It then runs the import only. The read runs before
	// the transaction. The rows cannot change under the locks of the
	// resolve, and a read on the pool waits for the open write.
	rowsInTarget := true
	if replay {
		ids, err := ops.rowIDs(ctx, s.engine, targetID, conflict.Uid)
		if err != nil {
			return moveResolution{}, fmt.Errorf("list the rows of the target: %w", err)
		}
		rowsInTarget = len(ids) > 0
	}
	if err := s.engine.inTx(ctx, func(tx *sql.Tx) error {
		qtx := s.q.WithTx(tx)
		// The record commits with the move back, so a refused or failed
		// move back leaves the record as it was.
		if !replay {
			n, err := qtx.UpdateSyncConflictCancelRecord(ctx, storage.UpdateSyncConflictCancelRecordParams{
				LocalIcal: string(live), ServerIcal: serverIcal, ServerEtag: serverEtag, CalendarID: targetID, Uid: conflict.Uid,
				MoveSourceCalendarID: conflict.MoveSourceCalendarID,
			})
			if err != nil {
				return fmt.Errorf("record the cancel: %w", err)
			}
			if n == 0 {
				return errConflictChanged(conflict)
			}
		}
		// The stamp runs first, and it is not redundant with the reopen at
		// the end. The move back takes the rows out of the target, and the
		// close of a gone item then abandons an open move conflict
		// (CloseMoveConflict). That close skips a failed cancel, so the
		// stamp keeps the row. The resolve is the guard of the move back:
		// RecordResourceMove refuses a UID with an open conflict.
		if err := setMoveState(ctx, qtx, conflict, MoveStateCancelFailed); err != nil {
			return err
		}
		if err := markConflictResolvedOn(ctx, qtx, targetID, conflict.Uid, ResolutionServer); err != nil {
			return fmt.Errorf("mark conflict resolved: %w", err)
		}
		// The move back runs through uidmove (Rows.MoveBack). It applies the
		// collision rule (the source can hold the UID again, an import after
		// the move) and records the move.
		if rowsInTarget {
			if err := ops.uidMove(qtx, conflict.Uid).MoveBack(ctx, targetID, sourceID); err != nil {
				return fmt.Errorf("move rows back to the source calendar: %w", err)
			}
		}
		if !sourceFound && sourceTombstoneFound {
			// The source object exists on the server. Without its sync row
			// the next push sends a create to a new href.
			if err := recreateSourceRowFromTombstone(ctx, qtx, sourceID, conflict, sourceTombstone, serverEtag); err != nil {
				return err
			}
		}
		// The move back is done: the row opens again as a failed cancel. It
		// waits there until the import below completes (FinishMoveCancel),
		// so a failure after this commit leaves the replay open.
		n, err := qtx.ReopenCancelledMoveConflict(ctx, storage.ReopenCancelledMoveConflictParams{CalendarID: targetID, Uid: conflict.Uid, MoveSourceCalendarID: conflict.MoveSourceCalendarID})
		if err != nil {
			return fmt.Errorf("open the failed cancel: %w", err)
		}
		if n == 0 {
			return errConflictChanged(conflict)
		}
		// The rev of the source row before the import. An import that stops
		// before its end leaves that rev, so the moot step does not count
		// the cancel as settled (ListSettledFailedCancels).
		sr, err := qtx.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: sourceID, Uid: conflict.Uid})
		found, err := storage.Found(err)
		if err != nil {
			return fmt.Errorf("get the source sync resource: %w", err)
		}
		if found {
			if err := qtx.SetMoveConflictCancelRev(ctx, storage.SetMoveConflictCancelRevParams{
				CancelSourceRev: sr.Rev, CancelSourceRowID: sr.ID, CalendarID: targetID, Uid: conflict.Uid, MoveSourceCalendarID: conflict.MoveSourceCalendarID,
			}); err != nil {
				return fmt.Errorf("record the rev of the cancel: %w", err)
			}
		}
		return nil
	}); err != nil {
		return moveResolution{}, err
	}

	// The import flows through the event/todo/journal services, which use
	// their own connection, so it runs outside the transactions here. See
	// ResolveConflict for the same order on an edit conflict. The move back
	// is idempotent, so a reopened row replays the pick.
	imported, revs, warnings, err := s.engine.importICal(ctx, sourceID, serverIcal)
	if err != nil || !imported {
		if err == nil {
			err = fmt.Errorf("server version has no importable data for %q", conflict.Uid)
		} else {
			err = fmt.Errorf("import server version: %w", err)
		}
		// The row is open as a failed cancel already (the cancel
		// transaction). A part of the body can be in the rows now, with a
		// dirty mark. No push sends it: the replay's import sets the final
		// state. One transaction records the rev and the id of the source
		// row first and then clears its dirty flag. A failure then leaves
		// both as they were. A later pull of the source changes the rev,
		// and the moot step then closes the row as cancelled
		// (ListSettledFailedCancels).
		if terr := s.engine.inTx(ctx, func(tx *sql.Tx) error {
			qtx := s.q.WithTx(tx)
			sr, gerr := qtx.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: sourceID, Uid: conflict.Uid})
			found, gerr := storage.Found(gerr)
			if gerr != nil {
				return fmt.Errorf("get the source sync resource: %w", gerr)
			}
			if found {
				if cerr := qtx.SetMoveConflictCancelRev(ctx, storage.SetMoveConflictCancelRevParams{
					CancelSourceRev: sr.Rev, CancelSourceRowID: sr.ID, CalendarID: targetID, Uid: conflict.Uid, MoveSourceCalendarID: conflict.MoveSourceCalendarID,
				}); cerr != nil {
					return fmt.Errorf("record the rev of the failure: %w", cerr)
				}
			}
			if cerr := qtx.ClearSyncResourceDirtyFlag(ctx, storage.ClearSyncResourceDirtyFlagParams{CalendarID: sourceID, Uid: conflict.Uid}); cerr != nil {
				return fmt.Errorf("clear the source dirty flag: %w", cerr)
			}
			return nil
		}); terr != nil {
			return moveResolution{}, fmt.Errorf("%w; %w", err, terr)
		}
		return moveResolution{}, err
	}
	sourceHasObject := (sourceFound && source.RemoteUrl != "") || (!sourceFound && sourceTombstoneFound)
	if sourceHasObject {
		// The rev guard keeps a local edit that landed after the import
		// dirty. See the server pick of ResolveConflict. The source row
		// without a server object needs no stamp: the next push creates
		// it again with the server body.
		if _, err := s.q.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
			CalendarID: sourceID,
			Uid:        conflict.Uid,
			Etag:       serverEtag,
			Rev:        revs[conflict.Uid],
		}); err != nil {
			return moveResolution{}, fmt.Errorf("clear dirty: %w", err)
		}
	}
	// The final state and the rev of the source row after the cancel commit
	// in one statement. A later local restore compares that rev. A failure
	// here leaves the row open as a failed cancel, so the replay runs again.
	if s.engine.testHooks != nil && s.engine.testHooks.beforeFinishCancel != nil {
		s.engine.testHooks.beforeFinishCancel()
	}
	// The rev and the id come from the source row after the import. When
	// that row does not exist, the values that the cancel transaction
	// recorded stay, so the statement never writes a 0 for an unknown row.
	cur, err := s.q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: targetID, Uid: conflict.Uid})
	if err != nil {
		return moveResolution{}, fmt.Errorf("get the move conflict of %s: %w", conflict.Uid, err)
	}
	finalRev, finalRowID := cur.CancelSourceRev, cur.CancelSourceRowID
	sr, err := s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: sourceID, Uid: conflict.Uid})
	srFound, err := storage.Found(err)
	if err != nil {
		return moveResolution{}, fmt.Errorf("get the source sync resource: %w", err)
	}
	if srFound {
		finalRev, finalRowID = sr.Rev, sr.ID
	}
	n, err := s.q.FinishMoveCancel(ctx, storage.FinishMoveCancelParams{
		CancelSourceRev: finalRev, CancelSourceRowID: finalRowID, CalendarID: targetID, Uid: conflict.Uid, MoveSourceCalendarID: conflict.MoveSourceCalendarID,
	})
	if err != nil {
		return moveResolution{}, fmt.Errorf("finish the cancel: %w", err)
	}
	if n == 0 {
		// The import succeeded, but the row left the failed cancel. The moot
		// step can settle it as cancelled, or an unlink can detach it, in
		// the window after the import. Both end the cancel, so the cancel
		// succeeded. The row keeps the rev that its settle recorded: the
		// cancel writes its rev only in its own state (FinishMoveCancel).
		row, err := s.q.GetMoveConflictOfUID(ctx, storage.GetMoveConflictOfUIDParams{CalendarID: targetID, Uid: conflict.Uid})
		found, err := storage.Found(err)
		if err != nil {
			return moveResolution{}, fmt.Errorf("get the move conflict of %s: %w", conflict.Uid, err)
		}
		if !found || row.MoveSourceCalendarID == nil || *row.MoveSourceCalendarID != sourceID {
			return moveResolution{}, errConflictChanged(conflict)
		}
		switch row.MoveState {
		case MoveStateCancelled, MoveStateDetached, MoveStateAbandoned:
			return moveResolution{Warnings: warnings}, nil
		default:
			return moveResolution{}, errConflictChanged(conflict)
		}
	}
	return moveResolution{Warnings: warnings}, nil
}

// createBlock is the answer of targetCanCreate: whether the target of a
// move can carry the moved copy, and why.
type createBlock int

const (
	// createPending means the target can create the copy, and a create is
	// still pending.
	createPending createBlock = iota
	// createLanded means the copy landed on the server already.
	createLanded
	// createLocal means the target is a local or unlinked calendar. The
	// item lives there, and no server needs a create.
	createLocal
	// createGated means the organizer gate holds the copy back.
	createGated
	// createReadOnly means the target calendar is read-only, so it cannot
	// create the copy.
	createReadOnly
	// createReadOnlyEdit means the copy landed in a read-only calendar, and
	// its row holds an unsent edit that no push can send.
	createReadOnlyEdit
)

// targetCanCreate reads once whether the target of conflict can carry the
// moved copy. The local pick and the rule of a gone source share it. The
// target can when one of these holds:
//   - the copy landed already, and the target can keep the copy as it is:
//     the target is writable, or the row has no unsent edit;
//   - the target is a local or unlinked calendar;
//   - the target is linked and writable, and the organizer gate does not
//     hold the copy back.
//
// target is the sync row of the copy, or nil when none exists. The
// calendar of the target comes back for the messages.
func (s *Service) targetCanCreate(ctx context.Context, conflict storage.SyncConflict, target *storage.SyncResource) (createCheck, error) {
	targetCal, err := s.q.GetCalendar(ctx, conflict.CalendarID)
	if err != nil {
		return createCheck{}, fmt.Errorf("get target calendar: %w", err)
	}
	gated, err := gateForCreate(ctx, s.q, targetCal, target, conflict)
	if err != nil {
		return createCheck{}, err
	}
	return createCheck{block: createBlockOf(targetCal, target, gated), cal: targetCal}, nil
}

// createCheck is the answer of targetCanCreate: the block and the calendar
// of the target for the messages.
type createCheck struct {
	block createBlock
	cal   storage.Calendar
}

// gateForCreate reads the organizer gate only where the rule needs it: a
// linked, writable target whose copy has no href. It reads through q, so a
// transaction passes its own queries. It reuses the calendar targetCal of
// the caller.
func gateForCreate(ctx context.Context, q *storage.Queries, targetCal storage.Calendar, target *storage.SyncResource, conflict storage.SyncConflict) (bool, error) {
	if !storage.CalendarLinked(targetCal) || calendaraccess.IsReadOnly(targetCal.RemoteAccess) {
		return false, nil
	}
	if target != nil && target.RemoteUrl != "" {
		return false, nil
	}
	gated, err := targetPushGatedFor(ctx, q, targetCal, conflict.OwnerType, conflict.Uid)
	if err != nil {
		return false, fmt.Errorf("check the organizer of the moved copy: %w", err)
	}
	return gated, nil
}

// createBlockOf is the rule of targetCanCreate on values that the caller
// read. gated is the answer of the organizer gate for a copy that has not
// landed. It reads nothing, so a transaction can apply the rule again to
// its own reads.
func createBlockOf(targetCal storage.Calendar, target *storage.SyncResource, gated bool) createBlock {
	if !storage.CalendarLinked(targetCal) {
		return createLocal
	}
	readOnly := calendaraccess.IsReadOnly(targetCal.RemoteAccess)
	if target != nil && target.RemoteUrl != "" {
		if readOnly && target.Dirty != 0 {
			return createReadOnlyEdit
		}
		return createLanded
	}
	if readOnly {
		return createReadOnly
	}
	if gated {
		return createGated
	}
	return createPending
}

// readTargetCanCreate reads the sync row of the copy and calls
// targetCanCreate.
func (s *Service) readTargetCanCreate(ctx context.Context, conflict storage.SyncConflict) (createCheck, error) {
	row, err := s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: conflict.CalendarID, Uid: conflict.Uid})
	found, err := storage.Found(err)
	if err != nil {
		return createCheck{}, fmt.Errorf("get target sync resource: %w", err)
	}
	if !found {
		return s.targetCanCreate(ctx, conflict, nil)
	}
	return s.targetCanCreate(ctx, conflict, &row)
}

// moveResolution is the result of a pick. Finished reports a move or a
// cancel that the pick ended because the source object is gone. The pick
// did not apply the picked side then. UID and Note say what the pick did
// and what remains (issue #841). An error of a pick always means a failure
// or a refusal.
type moveResolution struct {
	Warnings []ImportWarning
	Finished bool
	UID      string
	Note     string
}

// resolutionOf wraps the warnings and the error of a pick that does not
// finish a move.
func resolutionOf(warnings []ImportWarning, err error) (moveResolution, error) {
	return moveResolution{Warnings: warnings}, err
}

// finishedResolution is the result of a pick that ended a move.
func finishedResolution(uid, note string) moveResolution {
	return moveResolution{Finished: true, UID: uid, Note: note}
}

// refuseGoneSource is the refusal of a gone source for a target that
// cannot carry the copy. It is nil when the target can.
func refuseGoneSource(conflict storage.SyncConflict, block createBlock, targetCal storage.Calendar) error {
	switch block {
	case createReadOnlyEdit:
		return fmt.Errorf("the source object of %q is gone, and calendar %q is read-only and holds an unsent edit; make it writable, or move the item to another calendar", conflict.Uid, targetCal.Name)
	case createGated, createReadOnly:
		return fmt.Errorf("the source object of %q is gone, and calendar %q cannot create the item; make it writable, or move the item to another calendar", conflict.Uid, targetCal.Name)
	case createPending, createLanded, createLocal:
	}
	return nil
}

// settleGoneSource settles a move whose source object is gone from the
// server, with one rule for both picks. The move cannot be cancelled: no
// server version exists to bring back. check is the answer of
// targetCanCreate that the caller read. The transaction reads the moved
// copy, the calendar, the sync row, and the organizer gate again. It
// applies the rule to them (createBlockOf):
//   - A copy that is gone ends the move as abandoned.
//   - A target that cannot carry the copy refuses, and the conflict stays
//     open. With keep, the move finishes all the same: the source half is
//     gone, so nothing remains to wait for. The item then stays only in the
//     target calendar, and the note says so.
//   - Otherwise the move finishes. A pending create in a clean row marks
//     the row dirty, and the next push creates the copy.
//
// The note says what remains. The settle sends no request (issue #841).
func (s *Service) settleGoneSource(ctx context.Context, conflict storage.SyncConflict, check createCheck, keep bool) (moveResolution, error) {
	if !keep {
		if err := refuseGoneSource(conflict, check.block, check.cal); err != nil {
			return moveResolution{}, err
		}
	}
	if s.engine.testHooks != nil && s.engine.testHooks.beforeGoneSourceSettle != nil {
		s.engine.testHooks.beforeGoneSourceSettle()
	}
	targetID := conflict.CalendarID
	block, targetCal := check.block, check.cal
	abandoned := false
	if err := s.engine.inTx(ctx, func(tx *sql.Tx) error {
		qtx := s.q.WithTx(tx)
		// The rule of the earlier check, in this transaction (the view
		// live_move_copies).
		present, err := qtx.MovedCopyExists(ctx, storage.MovedCopyExistsParams{CalendarID: targetID, Uid: conflict.Uid, OwnerType: conflict.OwnerType})
		if err != nil {
			return fmt.Errorf("check the moved copy: %w", err)
		}
		if present == 0 {
			abandoned = true
			return qtx.CloseMoveConflict(ctx, storage.CloseMoveConflictParams{CalendarID: targetID, Uid: conflict.Uid})
		}
		cal, err := qtx.GetCalendar(ctx, targetID)
		if err != nil {
			return fmt.Errorf("get target calendar: %w", err)
		}
		var row *storage.SyncResource
		if storage.CalendarLinked(cal) {
			r, err := qtx.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: targetID, Uid: conflict.Uid})
			found, err := storage.Found(err)
			if err != nil {
				return fmt.Errorf("get the sync row of the moved copy: %w", err)
			}
			if found {
				row = &r
			}
		}
		gated, err := gateForCreate(ctx, qtx, cal, row, conflict)
		if err != nil {
			return err
		}
		block = createBlockOf(cal, row, gated)
		targetCal = cal
		if !keep {
			if err := refuseGoneSource(conflict, block, cal); err != nil {
				return err
			}
		}
		if err := finishMoveOn(ctx, qtx, targetID, conflict.Uid, *conflict.MoveSourceCalendarID); err != nil {
			return err
		}
		// A live copy in a linked calendar has its sync row (the view
		// live_move_copies), so row is set for a pending create.
		if block == createPending && row != nil && row.Dirty == 0 {
			// The row is scoped to the target calendar. MarkResourceDirty
			// could move the mark to another calendar of the UID.
			if err := qtx.UpsertDirtySyncResourceOfCalendar(ctx, storage.UpsertDirtySyncResourceOfCalendarParams{
				CalendarID: targetID, Uid: conflict.Uid, OwnerType: conflict.OwnerType,
			}); err != nil {
				return fmt.Errorf("mark target dirty: %w", err)
			}
		}
		return nil
	}); err != nil {
		return moveResolution{}, err
	}
	if abandoned {
		return moveResolution{}, fmt.Errorf("the moved copy of %q is gone, so the move is abandoned", conflict.Uid)
	}
	note := "the source is gone, so the move finished"
	switch block {
	case createPending:
		note += "; the next sync creates the moved copy"
	case createLanded:
		note += "; the moved copy is on the server already"
	case createLocal:
		note += fmt.Sprintf("; the item stays in calendar %q", targetCal.Name)
	case createGated, createReadOnly, createReadOnlyEdit:
		note += fmt.Sprintf("; the item stays only in calendar %q locally until the calendar is writable", targetCal.Name)
	}
	return finishedResolution(conflict.Uid, note), nil
}

// settleGoneSourceRead is settleGoneSource with its own read of
// targetCanCreate, for a caller that did not read it.
func (s *Service) settleGoneSourceRead(ctx context.Context, conflict storage.SyncConflict, keep bool) (moveResolution, error) {
	check, err := s.readTargetCanCreate(ctx, conflict)
	if err != nil {
		return moveResolution{}, err
	}
	return s.settleGoneSource(ctx, conflict, check, keep)
}

// sourceClientError marks a fetch of the source object that failed before
// the request. The client of the source calendar did not load, for example
// because its credentials did not load.
type sourceClientError struct{ err error }

func (e *sourceClientError) Error() string { return e.err.Error() }
func (e *sourceClientError) Unwrap() error { return e.err }

// probeFailure turns a failed probe of the source object into the refusal
// of the pick. It keeps the cause. It tells three classes apart:
//   - a client that did not load, 401, or 403: the credentials do not work;
//   - any other HTTP status: the server answered with it;
//   - no status, a transport or timeout error: the server cannot be
//     reached.
func probeFailure(calendar string, err error) error {
	var clientErr *sourceClientError
	code := retry.StatusCode(err)
	switch {
	case errors.As(err, &clientErr) || code == 401 || code == 403:
		return fmt.Errorf("the credentials of calendar %q do not work; fix them (chroncal account credentials or chroncal account reauth) and try again: %w", calendar, err)
	case code != 0:
		return fmt.Errorf("the server of calendar %q answered %d; try again later: %w", calendar, code, err)
	default:
		return fmt.Errorf("the server of calendar %q cannot be reached; try again when it can: %w", calendar, err)
	}
}

// recreateSourceRowFromTombstone writes the sync row of the source object
// again, in the transaction of qtx, from the href of the move tombstone
// and etag. The row is clean, because the object exists on the server.
// The DELETE or the update of the next push reads the etag from the row.
func recreateSourceRowFromTombstone(ctx context.Context, qtx *storage.Queries, sourceID int64, conflict storage.SyncConflict, tombstone storage.Tombstone, etag string) error {
	if err := qtx.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   sourceID,
		Uid:          conflict.Uid,
		OwnerType:    conflict.OwnerType,
		RemoteUrl:    tombstone.RemoteUrl,
		Etag:         etag,
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		return fmt.Errorf("create source sync resource: %w", err)
	}
	return nil
}

// fetchSourceObject reads the current source object of a move from the
// server. tombstone is the source tombstone of the move. A nil resource
// with a nil error means that the object is absent (404 or 410). The
// picks need the fresh body and etag: no pull refreshes the record of a
// move conflict, because the source UID has a tombstone.
func (s *Service) fetchSourceObject(ctx context.Context, sourceID int64, tombstone storage.Tombstone) (*caldav.Resource, error) {
	_, _, client, remoteURL, err := s.engine.loadCalendarClient(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("connect to the source calendar: %w", &sourceClientError{err: err})
	}
	path, err := client.CanonicalObjectRef(remoteURL, tombstone.RemoteUrl)
	if err != nil {
		return nil, fmt.Errorf("validate the source href of %q: %w", tombstone.Uid, err)
	}
	res, err := client.GetResource(ctx, path)
	if caldav.IsAbsent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch the source object of %q: %w", tombstone.Uid, err)
	}
	return res, nil
}

// refuseReadOnlySource refuses a local pick when the source calendar of
// the move is read-only. Such a source never sends a delete or a restore.
func (s *Service) refuseReadOnlySource(ctx context.Context, sourceID int64, uid string) error {
	sourceCal, err := s.q.GetCalendar(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("get source calendar: %w", err)
	}
	if calendaraccess.IsReadOnly(sourceCal.RemoteAccess) {
		return fmt.Errorf("the source calendar is read-only, so it cannot delete or restore %q; pick server", uid)
	}
	return nil
}
