// Package maintenance holds background maintenance jobs — primarily the
// soft-delete purge job that trims rows past the retention window.
package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

// Purger hard-deletes soft-deleted rows older than a retention window
// across every trash-eligible domain (events, todos, journals) plus the
// event-specific instance and truncation logs. It delegates to the
// trash aggregator so new domains only need to be added once there.
// It also trims acknowledged alarm-state rows past the same window —
// rescheduled events leave state rows whose trigger time never recurs,
// and acked history serves no purpose once it is weeks old.
type Purger struct {
	trash  *trash.Service
	q      *storage.Queries
	days   int
	logger *slog.Logger
}

// NewPurger returns a Purger bound to the trash aggregator. A days value
// of 0 disables automatic purging; callers should guard the call to
// RunOnce. q may be nil, which skips alarm-state cleanup.
func NewPurger(trashSvc *trash.Service, q *storage.Queries, days int, logger *slog.Logger) *Purger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Purger{trash: trashSvc, q: q, days: days, logger: logger}
}

// RunOnce purges rows soft-deleted more than Days ago. Safe to call
// concurrently from multiple processes — SQLite serializes the DELETE.
// Returns the total number of rows purged across every domain + log.
func (p *Purger) RunOnce(ctx context.Context) (int, error) {
	if p.days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(p.days) * 24 * time.Hour)
	counts, err := p.trash.PurgeOld(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("trash purge: %w", err)
	}
	total := counts.Events + counts.EventInstanceLogs + counts.EventTruncateLogs + counts.Todos + counts.Journals
	if total > 0 {
		p.logger.Info("soft-delete purge",
			"events_purged", counts.Events,
			"instance_logs_purged", counts.EventInstanceLogs,
			"truncation_logs_purged", counts.EventTruncateLogs,
			"todos_purged", counts.Todos,
			"journals_purged", counts.Journals,
			"older_than_days", p.days,
		)
	}

	if p.q != nil {
		states, err := p.purgeAlarmStates(ctx, cutoff)
		if err != nil {
			// Alarm-state cleanup is housekeeping; don't fail the purge run.
			p.logger.Warn("alarm-state purge failed", "error", err)
		} else if states > 0 {
			p.logger.Info("alarm-state purge", "states_purged", states, "older_than_days", p.days)
			total += states
		}
	}
	return total, nil
}

// staleUnackedMultiplier sets the secondary retention for fired-but-never-
// acknowledged alarm-state rows: they back "alarm list", so they live much
// longer than acked history, but rescheduled events leave rows whose trigger
// never recurs and unbounded growth helps nobody.
const staleUnackedMultiplier = 4

// purgeAlarmStates deletes acknowledged alarm-state rows whose trigger time
// is older than the cutoff, plus unacknowledged rows older than four times
// the retention window (excluding rows snoozed into the future). Recently
// fired pending rows are kept — they back "alarm list" and snooze re-firing.
func (p *Purger) purgeAlarmStates(ctx context.Context, cutoff time.Time) (int, error) {
	cutoffStr := cutoff.UTC().Format(time.RFC3339)
	total := 0
	events, err := p.q.PurgeAcknowledgedAlarmStates(ctx, cutoffStr)
	if err != nil {
		return total, fmt.Errorf("purge alarm states: %w", err)
	}
	total += int(events)
	todos, err := p.q.PurgeAcknowledgedTodoAlarmStates(ctx, cutoffStr)
	if err != nil {
		return total, fmt.Errorf("purge todo alarm states: %w", err)
	}
	total += int(todos)

	staleCutoff := time.Now().Add(-time.Duration(p.days*staleUnackedMultiplier) * 24 * time.Hour)
	staleCutoffStr := staleCutoff.UTC().Format(time.RFC3339)
	staleEvents, err := p.q.PurgeStaleUnacknowledgedAlarmStates(ctx, staleCutoffStr)
	if err != nil {
		return total, fmt.Errorf("purge stale unacked alarm states: %w", err)
	}
	total += int(staleEvents)
	staleTodos, err := p.q.PurgeStaleUnacknowledgedTodoAlarmStates(ctx, staleCutoffStr)
	if err != nil {
		return total, fmt.Errorf("purge stale unacked todo alarm states: %w", err)
	}
	total += int(staleTodos)
	return total, nil
}

// RunDaily fires RunOnce once on start, then every 24h until ctx is done.
// Intended for the long-running TUI/daemon. CLI one-shot callers should use
// RunOnce directly.
func (p *Purger) RunDaily(ctx context.Context) {
	if p.days <= 0 {
		return
	}
	if _, err := p.RunOnce(ctx); err != nil {
		p.logger.Warn("initial soft-delete purge failed", "error", err)
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.RunOnce(ctx); err != nil {
				p.logger.Warn("soft-delete purge failed", "error", err)
			}
		}
	}
}
