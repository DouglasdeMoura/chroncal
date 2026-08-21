package sync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// pendingHrefMissLimit is the number of unknown multiget 404s the engine
// accepts for one href. After that count the engine drops the href. Google
// can list a stale invitation href forever. A new event that 404s once
// must still be fetched on the next pull. See issue #576.
const pendingHrefMissLimit = 3

// pullView is the completeness predicate for one applySyncCollection pass.
// Token advance and absence-inferred deletion used to share one miss
// counter. They still share the local-row rule. The two questions are
// distinct:
//
//	inventoryObserved: may absence-inferred deletion run?
//	localRowsSafe: may the sync-token advance?
//
// A known miss (a local row whose body was not fetched) answers no to
// both. That is the data-loss guard. An unknown miss has no local row.
// It does not flip inventoryObserved. Token advance still requires a
// recorded retry obligation. A record failure withholds the token. It
// does not withhold absence deletion.
type pullView struct {
	truncated          bool
	knownMisses        int
	persistFailures    int
	pendingRecordFails int
}

func (v pullView) inventoryObserved() bool {
	return !v.truncated && v.knownMisses == 0 && v.persistFailures == 0
}

func (v pullView) localRowsSafe() bool {
	return v.inventoryObserved() && v.pendingRecordFails == 0
}

func (v pullView) absenceWithholdReason() string {
	var parts []string
	if v.truncated {
		parts = append(parts, "response truncated (RFC 6578 §3.6)")
	}
	if v.knownMisses > 0 {
		parts = append(parts, fmt.Sprintf("%d known multiget miss(es)", v.knownMisses))
	}
	if v.persistFailures > 0 {
		parts = append(parts, fmt.Sprintf("%d persist failure(s)", v.persistFailures))
	}
	if len(parts) == 0 {
		return "complete"
	}
	return strings.Join(parts, " and ")
}

func (v pullView) incomplete() bool {
	return v.knownMisses > 0 || v.persistFailures > 0 || v.pendingRecordFails > 0
}

// pendingHrefs is the single gate for unknown-multiget-miss retry.
// After the token advances, the server will not re-list a 404'd href.
// Load once per applySyncCollection. Speak three verbs:
//
//	forget: the href is gone (deleted, tombstoned) or the body landed
//	appendUnseen: add stored hrefs that the change list did not already name
//	noteMiss: bump the miss count and drop the row after the budget
type pendingHrefs struct {
	q          *storage.Queries
	logger     *slog.Logger
	calendarID int64
	byHref     map[string]struct{}
}

func loadPendingHrefs(ctx context.Context, q *storage.Queries, logger *slog.Logger, calendarID int64) (*pendingHrefs, error) {
	rows, err := q.ListSyncPendingHrefsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list pending hrefs: %w", err)
	}
	byHref := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		byHref[row.Href] = struct{}{}
	}
	return &pendingHrefs{q: q, logger: logger, calendarID: calendarID, byHref: byHref}, nil
}

func (p *pendingHrefs) forget(ctx context.Context, href string) {
	if _, ok := p.byHref[href]; !ok {
		return
	}
	if err := p.q.DeleteSyncPendingHref(ctx, storage.DeleteSyncPendingHrefParams{
		CalendarID: p.calendarID,
		Href:       href,
	}); err != nil {
		p.logger.Warn("delete pending href", "calendar_id", p.calendarID, "href", href, "error", err)
		return
	}
	delete(p.byHref, href)
}

func (p *pendingHrefs) forgetSet(ctx context.Context, hrefs map[string]bool) {
	for href := range hrefs {
		p.forget(ctx, href)
	}
}

func (p *pendingHrefs) appendUnseen(fetchPaths []string) []string {
	if len(p.byHref) == 0 {
		return fetchPaths
	}
	seen := make(map[string]bool, len(fetchPaths)+len(p.byHref))
	for _, pth := range fetchPaths {
		seen[pth] = true
	}
	for href := range p.byHref {
		if seen[href] {
			continue
		}
		fetchPaths = append(fetchPaths, href)
		seen[href] = true
	}
	return fetchPaths
}

func (p *pendingHrefs) noteMiss(ctx context.Context, href string) error {
	row, err := p.q.BumpSyncPendingHref(ctx, storage.BumpSyncPendingHrefParams{
		CalendarID: p.calendarID,
		Href:       href,
	})
	if err != nil {
		return err
	}
	if row.MissCount >= pendingHrefMissLimit {
		if err := p.q.DeleteSyncPendingHref(ctx, storage.DeleteSyncPendingHrefParams{
			CalendarID: p.calendarID,
			Href:       href,
		}); err != nil {
			return err
		}
		delete(p.byHref, href)
		return nil
	}
	p.byHref[href] = struct{}{}
	return nil
}

type multigetMissKind string

const (
	multigetMissUncanonical multigetMissKind = "uncanonical"
	multigetMissKnown       multigetMissKind = "known"
	multigetMissUnknown     multigetMissKind = "unknown"
)

// classifyMultigetMiss sorts one multiget 404 by the risk it carries.
//
//	multigetMissUncanonical: CanonicalObjectRef rejected the href. No
//	local row can map to it, so it is not a data-loss signal. The caller
//	skips it or takes the unknown-miss budget path.
//	multigetMissKnown: the path maps to a local row. The caller must
//	treat the pull as incomplete.
//	multigetMissUnknown: no local row maps to the path. The caller may
//	record a retry obligation and advance the token.
func classifyMultigetMiss(canonical string, hrefErr error, localByPath map[string]storage.SyncResource) (multigetMissKind, storage.SyncResource) {
	if hrefErr != nil {
		return multigetMissUncanonical, storage.SyncResource{}
	}
	if local, ok := localByPath[canonical]; ok {
		return multigetMissKnown, local
	}
	return multigetMissUnknown, storage.SyncResource{}
}
