package sync

import (
	"context"
	"log/slog"
)

// hrefSet is the in-memory copy of one per-calendar href table. The pull
// loads it once per applySyncCollection. pendingHrefs and knownMisses embed
// it. The two tables keep different give-up policies, but they forget an
// href in the same way. V is the per-href state that the table keeps.
type hrefSet[V any] struct {
	logger     *slog.Logger
	calendarID int64
	// what names one row of the table in a log message.
	what   string
	byHref map[string]V
	// del deletes the row of one href from the table.
	del func(ctx context.Context, href string) error
}

// loadHrefSet builds an hrefSet from the stored rows. entry gives the href
// and the state of one row.
func loadHrefSet[R, V any](logger *slog.Logger, calendarID int64, what string, rows []R, entry func(R) (string, V), del func(context.Context, string) error) hrefSet[V] {
	byHref := make(map[string]V, len(rows))
	for _, row := range rows {
		href, v := entry(row)
		byHref[href] = v
	}
	return hrefSet[V]{logger: logger, calendarID: calendarID, what: what, byHref: byHref, del: del}
}

// forget deletes the row of href when the set holds it. A delete failure
// keeps the href in the set and writes a warning.
func (h *hrefSet[V]) forget(ctx context.Context, href string) {
	if _, ok := h.byHref[href]; !ok {
		return
	}
	if err := h.del(ctx, href); err != nil {
		h.logger.Warn("delete "+h.what, "calendar_id", h.calendarID, "href", href, "error", err)
		return
	}
	delete(h.byHref, href)
}

func (h *hrefSet[V]) forgetSet(ctx context.Context, hrefs map[string]bool) {
	for href := range hrefs {
		h.forget(ctx, href)
	}
}
