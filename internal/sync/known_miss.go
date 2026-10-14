package sync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// knownMissLimit is the number of pulls in a row that may fail to import
// the server body of a known href. Inside the budget the engine withholds
// the sync-token. After the budget the engine advances the token and keeps
// the local row. sync status then names the href. See issue #816.
const knownMissLimit = 3

const (
	knownMissReasonMissing     = "missing"
	knownMissReasonUnparseable = "unparseable"
)

// knownMisses is the retry budget for known multiget misses. A known miss
// is an href with a local row whose server body the pull cannot import.
// Without a budget, a server that keeps a bad body blocks the sync-token
// on each run. Load once per applySyncCollection. Speak two verbs:
//
//	noteMiss: bump the miss count and mark the row given up after the budget
//	forget: the body landed, or the href is gone
type knownMisses struct {
	q          *storage.Queries
	logger     *slog.Logger
	calendarID int64
	byHref     map[string]struct{}
}

// loadKnownMisses reads the rows of one calendar. It deletes each row whose
// href no longer maps to a local row. Such a row names no data to keep, and
// sync status must not show it.
func loadKnownMisses(ctx context.Context, q *storage.Queries, logger *slog.Logger, calendarID int64, localByPath map[string]storage.SyncResource) (*knownMisses, error) {
	rows, err := q.ListSyncKnownMissesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list known misses: %w", err)
	}
	k := &knownMisses{q: q, logger: logger, calendarID: calendarID, byHref: make(map[string]struct{}, len(rows))}
	for _, row := range rows {
		k.byHref[row.Href] = struct{}{}
		if _, ok := localByPath[row.Href]; !ok {
			k.forget(ctx, row.Href)
		}
	}
	return k, nil
}

func (k *knownMisses) forget(ctx context.Context, href string) {
	if _, ok := k.byHref[href]; !ok {
		return
	}
	if err := k.q.DeleteSyncKnownMiss(ctx, storage.DeleteSyncKnownMissParams{
		CalendarID: k.calendarID,
		Href:       href,
	}); err != nil {
		k.logger.Warn("delete known miss", "calendar_id", k.calendarID, "href", href, "error", err)
		return
	}
	delete(k.byHref, href)
}

func (k *knownMisses) forgetSet(ctx context.Context, hrefs map[string]bool) {
	for href := range hrefs {
		k.forget(ctx, href)
	}
}

// noteMiss bumps the miss count for the href. exhausted reports that the
// budget is spent, so the miss must not withhold the sync-token. gaveUp
// reports that this call spent the budget. The caller must then tell the
// user once.
func (k *knownMisses) noteMiss(ctx context.Context, href, uid, reason string) (exhausted, gaveUp bool, err error) {
	row, err := k.q.BumpSyncKnownMiss(ctx, storage.BumpSyncKnownMissParams{
		CalendarID: k.calendarID,
		Href:       href,
		Uid:        uid,
		Reason:     reason,
	})
	if err != nil {
		return false, false, err
	}
	k.byHref[href] = struct{}{}
	if row.MissCount < knownMissLimit {
		return false, false, nil
	}
	if row.GaveUpAt != nil {
		return true, false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := k.q.MarkSyncKnownMissGaveUp(ctx, storage.MarkSyncKnownMissGaveUpParams{
		GaveUpAt:   &now,
		CalendarID: k.calendarID,
		Href:       href,
	}); err != nil {
		return false, false, err
	}
	return true, true, nil
}
