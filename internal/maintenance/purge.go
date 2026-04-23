// Package maintenance holds background maintenance jobs — primarily the
// soft-delete purge job that trims rows past the retention window.
package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/trash"
)

// Purger hard-deletes soft-deleted rows older than a retention window
// across every trash-eligible domain (events, todos, journals) plus the
// event-specific instance and truncation logs. It delegates to the
// trash aggregator so new domains only need to be added once there.
type Purger struct {
	trash  *trash.Service
	days   int
	logger *slog.Logger
}

// NewPurger returns a Purger bound to the trash aggregator. A days value
// of 0 disables automatic purging; callers should guard the call to
// RunOnce.
func NewPurger(trashSvc *trash.Service, days int, logger *slog.Logger) *Purger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Purger{trash: trashSvc, days: days, logger: logger}
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
