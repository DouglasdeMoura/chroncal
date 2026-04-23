// Package maintenance holds background maintenance jobs — primarily the
// soft-delete purge job that trims rows past the retention window.
package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// Purger knows how to hard-delete soft-deleted rows older than a retention
// window. Keep it small on purpose: just events in v1; todos and journals
// expand in v3.
type Purger struct {
	events *event.Service
	days   int
	logger *slog.Logger
}

// NewPurger returns a Purger bound to the given services. A days value of 0
// disables automatic purging; callers should guard the call to RunOnce.
func NewPurger(events *event.Service, days int, logger *slog.Logger) *Purger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Purger{events: events, days: days, logger: logger}
}

// RunOnce purges rows soft-deleted more than Days ago. Safe to call
// concurrently from multiple processes — SQLite serializes the DELETE.
// Returns the total number of rows purged across events and the
// instance-delete log.
func (p *Purger) RunOnce(ctx context.Context) (int, error) {
	if p.days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(p.days) * 24 * time.Hour)
	eventsPurged, err := p.events.PurgeDeleted(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge events: %w", err)
	}
	logsPurged, err := p.events.PurgeOldInstanceDeletes(ctx, cutoff)
	if err != nil {
		return eventsPurged, fmt.Errorf("purge instance-delete log: %w", err)
	}
	total := eventsPurged + logsPurged
	if total > 0 {
		p.logger.Info("soft-delete purge",
			"events_purged", eventsPurged,
			"instance_logs_purged", logsPurged,
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
