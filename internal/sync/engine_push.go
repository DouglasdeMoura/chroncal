package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

type pushResult struct {
	pushed           int
	conflicts        int
	autoResolved     int
	skippedConflicts int
	errors           []error
	warnings         []ImportWarning
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

	dirty, err := e.q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list dirty: %w", err)
	}

	result := &pushResult{}
	for _, res := range dirty {
		// CalDAV's PUT contract (RFC 4791 §4.1) only lets the organizer
		// modify a meeting resource. Attendees are supposed to round-trip
		// RSVP changes via iTIP REPLY, not PUT — Google rejects attendee
		// PUTs with HTTP 400 / 500 and a vague <D:error/> body. Skipping
		// foreign-organized events here clears the dirty flag so we stop
		// retrying every sync; the local row is left untouched.
		if pushIdentity != "" && res.OwnerType == ownerTypeEvent && !e.userOrganizesEvent(ctx, res.Uid, pushIdentity) {
			e.logger.Info("skip push: not the organizer", "uid", res.Uid, "owner", pushIdentity)
			if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
				CalendarID: calendarID,
				Uid:        res.Uid,
				Etag:       res.Etag,
			}); err != nil {
				e.logger.Error("clear non-owned dirty", "uid", res.Uid, "error", err)
			}
			continue
		}

		// In a full prompt-mode pass, skip resources that already have an
		// open, unresolved conflict. The local row is still dirty and carries
		// the ETag that already failed If-Match, so re-PUTing it just 412s
		// again on every sync. Hold off until the user resolves it via
		// ResolveConflict, which clears the conflict and refreshes the ETag.
		// See issue #104. ServerWins is excluded: it must process the row to
		// resolve the conflict it recorded. The opportunistic push is also
		// excluded: its 412 refreshes the recorded local body with the latest
		// edit, so a fresh write updates the conflict row instead of sitting
		// unpushed behind it.
		if strategy != ConflictServerWins && !opportunistic {
			if open, cerr := e.q.CountOpenSyncConflicts(ctx, storage.CountOpenSyncConflictsParams{
				CalendarID: calendarID,
				Uid:        res.Uid,
			}); cerr != nil {
				e.logger.Error("check open conflict", "uid", res.Uid, "error", cerr)
			} else if open > 0 {
				e.logger.Debug("skip push: open conflict pending resolution", "uid", res.Uid)
				result.skippedConflicts++
				continue
			}
		}

		e.logger.Debug("pushing resource", "uid", res.Uid, "remote_url", res.RemoteUrl)

		// Export the local resource to iCal
		icalData, err := e.exportResource(ctx, res.OwnerType, res.Uid)
		if err != nil {
			if errors.Is(err, errResourceMissing) {
				// No live local row backs this dirty sync_resource (the user
				// purged it from trash, or the master/override pair got into
				// an inconsistent state). Retrying every sync just races the
				// same null lookup, so clear the flag and let processTombstones
				// handle any remote-side cleanup.
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

		// PUT to server. A PUT can reach the server and mutate the resource
		// even when its response is lost (e.g. connection reset while reading
		// the body), which Retry classifies as transient. The retried PUT then
		// re-sends the stale pre-PUT If-Match and the server — whose ETag has
		// already advanced — answers 412, masquerading as a conflict. When an
		// earlier attempt failed transiently, treat a 412 whose server body
		// equals what we PUT as the success that actually happened. See #294.
		priorAttemptMayHaveLanded := false
		newEtag, putErr := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
			etag, err := client.PutResource(ctx, putPath, cal, res.Etag)
			if err == nil {
				return etag, nil
			}
			// A 412 is never transient, so these branches are exclusive.
			if caldav.IsTransient(err) {
				priorAttemptMayHaveLanded = true
			} else if priorAttemptMayHaveLanded && caldav.IsConflict(err) {
				if landedEtag, ok := e.putAlreadyLanded(ctx, client, putPath, cal); ok {
					return landedEtag, nil
				}
			}
			return etag, err
		})
		if putErr != nil {
			// Check for 412 Precondition Failed (ETag conflict)
			if caldav.IsConflict(putErr) {
				e.logger.Warn("conflict detected during push", "uid", res.Uid)
				outcome, warnings, errs := e.handlePutConflict(ctx, client, calendarID, res, putPath, icalData, strategy, opportunistic)
				result.warnings = append(result.warnings, warnings...)
				result.errors = append(result.errors, errs...)
				switch outcome {
				case putConflictLeftOpen:
					result.conflicts++
				case putConflictAutoResolved:
					result.autoResolved++
				case putConflictRecordFailed:
					// No row exists, so nothing is counted. errs already
					// carries the failure to the caller.
				}
				continue
			}
			e.logger.Error("PUT failed", "uid", res.Uid, "error", putErr)
			putErr = fmt.Errorf("put %s: %w", res.Uid, putErr)
			result.errors = append(result.errors, putErr)
			e.recordPushFailure(ctx, calendarID, res.Uid, putErr)
			continue
		}

		// Store the new server ETag and clear the dirty flag — but only clear
		// dirty if the resource has not been edited since we captured res.Rev
		// (before exporting the body we just PUT). A local edit landing during
		// the PUT round-trip bumps rev and keeps dirty=1; an unconditional
		// clear here would wipe that flag and silently drop the edit (lost
		// update). The ETag still advances so the next push's If-Match matches
		// the server. See issue #92.
		if err := e.q.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
			CalendarID: calendarID,
			Uid:        res.Uid,
			Etag:       newEtag,
			Rev:        res.Rev,
		}); err != nil {
			e.logger.Error("finalize pushed resource failed", "uid", res.Uid, "error", err)
		}
		// The body reached the server, so this attempt succeeded. Reset the
		// failure bookkeeping even when the finalize write above failed.
		e.clearPushFailure(ctx, calendarID, res.Uid)

		// Update remote URL if it was newly assigned
		if res.RemoteUrl == "" {
			if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
				CalendarID:   calendarID,
				Uid:          res.Uid,
				OwnerType:    res.OwnerType,
				RemoteUrl:    normalizeRemoteRef(putPath),
				Etag:         newEtag,
				Dirty:        0,
				SyncStrategy: res.SyncStrategy,
			}); err != nil {
				e.logger.Error("update sync resource URL", "uid", res.Uid, "error", err)
			}
		}

		result.pushed++
		e.logger.Debug("pushed resource", "uid", res.Uid, "etag", newEtag)
	}

	return result, nil
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
)

// handlePutConflict settles one 412 Precondition Failed answer. It records
// the conflict before any resolution so the local body survives even when
// the pass adopts the server version. icalData is the exact body that just
// failed the PUT, so the handler does not export it again (issue #264). An
// encode failure is tolerated: the row records an empty server body, the
// local edit is the half we must not lose, and ResolveConflict refuses a
// server pick it cannot import. A failed re-fetch records nothing — without
// the server body and ETag the row could not be resolved safely — and
// surfaces as an error. Prompt mode and the opportunistic push stop after
// the record. A server-wins pass adopts the server version and marks the
// row resolved; every failure on that path leaves the row open so the user
// can still resolve it by hand. See issue #610.
func (e *Engine) handlePutConflict(ctx context.Context, client *caldav.Client, calendarID int64, res storage.SyncResource, putPath string, icalData []byte, strategy ConflictStrategy, opportunistic bool) (putConflictOutcome, []ImportWarning, []error) {
	var errs []error

	serverRes, fetchErr := client.GetResource(ctx, putPath)
	if fetchErr != nil {
		e.logger.Warn("re-fetch server resource failed", "uid", res.Uid, "error", fetchErr)
		return putConflictRecordFailed, nil, append(errs, fmt.Errorf("conflict re-fetch %s: %w", res.Uid, fetchErr))
	}
	serverIcal, encodeErr := caldav.EncodeCalendar(serverRes.Data)
	if encodeErr != nil {
		e.logger.Warn("encode server resource for conflict record", "uid", res.Uid, "error", encodeErr)
	}
	ownerID, lookupErr := e.lookupOwnerID(ctx, res.OwnerType, res.Uid)
	if lookupErr != nil {
		e.logger.Warn("lookup owner id for conflict record", "uid", res.Uid, "owner_type", res.OwnerType, "error", lookupErr)
	}
	if err := e.q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calendarID,
		OwnerType:  res.OwnerType,
		OwnerID:    ownerID,
		Uid:        res.Uid,
		LocalIcal:  string(icalData),
		ServerIcal: string(serverIcal),
		ServerEtag: serverRes.ETag,
	}); err != nil {
		e.logger.Error("record conflict", "uid", res.Uid, "error", err)
		return putConflictRecordFailed, nil, append(errs, fmt.Errorf("record conflict %s: %w", res.Uid, err))
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
	if encodeErr != nil {
		errs = append(errs, fmt.Errorf("encode server resource %s: %w", res.Uid, encodeErr))
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
	// Clear dirty and update ETag to accept server version. Guard the clear
	// on the rev persistImported captured inside its transaction so a local
	// edit landing after the import committed is not silently dropped (lost
	// update). See issues #92, #417 and #494.
	if err := e.clearDirtyAfterImport(ctx, calendarID, res.Uid, serverRes.ETag, revs[res.Uid]); err != nil {
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
