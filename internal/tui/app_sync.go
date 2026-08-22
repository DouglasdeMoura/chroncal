package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/config"
	syncpkg "github.com/douglasdemoura/chroncal/internal/sync"
)

func (m Model) newSyncService() (*syncpkg.Service, error) {
	credStore, err := m.openCredentialStore()
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	return syncpkg.NewService(m.app.DB, m.app.Queries, credStore, m.app.Calendars, m.app.Events, m.app.Todos, m.app.Journals, config.SharedStateDirLogger()), nil
}

// runSyncAllPlan lists the connected calendars and emits a syncAllPlannedMsg
// so the Update loop can step through them one at a time. The actual sync work
// happens in runSyncOne so the footer can refresh between calendars.
func (m Model) runSyncAllPlan() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		cals, err := m.app.Queries.ListCalendars(ctx)
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		var targets []syncTarget
		for _, c := range cals {
			if c.AccountID == nil || *c.AccountID == 0 {
				continue
			}
			targets = append(targets, syncTarget{ID: c.ID, Name: c.Name})
		}
		return syncAllPlannedMsg{targets: targets}
	}
}

// runSyncOne syncs a single calendar inside a SyncAll run. It emits
// syncCalendarFinishedMsg so the Update loop can advance to the next target
// (or finalize) and refresh the footer.
func (m Model) runSyncOne(target syncTarget, index, total int) tea.Cmd {
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return syncCalendarFinishedMsg{index: index, total: total, name: target.Name, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := svc.SyncCalendar(ctx, target.ID, m.fullSyncStrategy)
		return syncCalendarFinishedMsg{index: index, total: total, name: target.Name, result: result, err: err}
	}
}

func (m Model) runSyncCalendar(id int64, name string) tea.Cmd {
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := svc.SyncCalendar(ctx, id, m.fullSyncStrategy)
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		label := name
		if label == "" {
			label = "calendar"
		}
		summary := syncSummary(label, syncTotals{
			pushed:           result.Pushed,
			pulled:           result.Pulled,
			deleted:          result.Deleted,
			conflicts:        result.Conflicts,
			autoResolved:     result.AutoResolved,
			skippedConflicts: result.SkippedConflicts,
			warnings:         len(result.Warnings),
		})
		var firstErr error
		if len(result.Errors) > 0 {
			firstErr = result.Errors[0]
		}
		return syncFinishedMsg{summary: summary, err: firstErr, reload: true}
	}
}

func (m Model) runSyncAccount(accountID int64, name string) tea.Cmd {
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		results, err := svc.SyncAccount(
			context.Background(), accountID, m.fullSyncStrategy,
		)
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		var totals syncTotals
		for _, result := range results {
			if result == nil {
				continue
			}
			totals.pushed += result.Pushed
			totals.pulled += result.Pulled
			totals.deleted += result.Deleted
			totals.conflicts += result.Conflicts
			totals.autoResolved += result.AutoResolved
			totals.skippedConflicts += result.SkippedConflicts
			totals.warnings += len(result.Warnings)
			if totals.firstErr == nil && len(result.Errors) > 0 {
				totals.firstErr = result.Errors[0]
			}
		}
		label := name
		if label == "" {
			label = "account"
		}
		return syncFinishedMsg{
			summary: syncSummary(label, totals),
			err:     totals.firstErr,
			reload:  true,
		}
	}
}

func (m Model) finishSync(msg syncFinishedMsg) (Model, tea.Cmd) {
	m.syncing = false
	m.statusToken++
	if msg.err != nil {
		if msg.summary != "" {
			m.syncStatus = fmt.Sprintf("%s — %s", msg.summary, msg.err.Error())
		} else {
			m.syncStatus = "Sync failed: " + msg.err.Error()
		}
	} else {
		m.syncStatus = msg.summary
	}
	// Always reload calendars so the sidebar health marker reflects the
	// just-attempted sync — including a failed run. Events only change on a
	// successful pull, so reload those only then.
	cmds := []tea.Cmd{m.expireStatusAfter(6*time.Second, m.statusToken), m.loadCalendars()}
	if msg.reload {
		cmds = append(cmds, m.loadEvents())
	}
	if m.pendingSyncCalendar.ID != 0 {
		next := m.pendingSyncCalendar
		m.pendingSyncCalendar = syncTarget{}
		cmds = append(cmds, func() tea.Msg {
			return SyncCalendarRequestedMsg(next)
		})
	}
	return m, tea.Batch(cmds...)
}

// startOAuthFlow opens the wait modal and launches browser authorization
// with the given client config. Add Account consumes the sign-in dialog.
// Account re-authentication keeps any edit draft under the flow.
// The caller has already recorded m.oauthPurpose.

func syncProgressLabel(name string) string {
	if name == "" {
		return "calendar"
	}
	const maxLen = 32
	runes := []rune(name)
	if len(runes) <= maxLen {
		return name
	}
	return string(runes[:maxLen-1]) + "…"
}

// runOpportunisticPush pushes unpushed changes for a single calendar with no
// pull. Best-effort: failures do not surface as errors. The dirty flag
// survives and the background tick will retry. A 412 records a conflict and
// keeps the saved edit; the footer then names the conflict count. Returns
// nil for local-only calendars (Synced=false). Callers can then batch it
// into the post-save command with no UI noise for offline calendars.
func (m Model) runOpportunisticPush(calendarID int64) tea.Cmd {
	info, ok := m.calendars[calendarID]
	if !ok || !info.Synced {
		return nil
	}
	name := info.Name
	return func() tea.Msg {
		svc, err := m.newSyncService()
		if err != nil {
			return opportunisticPushFinishedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := svc.PushLocalEdits(ctx, calendarID)
		if err != nil {
			return opportunisticPushFinishedMsg{err: err}
		}
		if result.Pushed == 0 && result.Deleted == 0 && len(result.Errors) == 0 && len(result.Warnings) == 0 &&
			result.Conflicts == 0 && result.AutoResolved == 0 &&
			result.SkippedConflicts == 0 {
			return opportunisticPushFinishedMsg{}
		}
		label := name
		if label == "" {
			label = "calendar"
		}
		summary := syncSummary(label, syncTotals{
			pushed:           result.Pushed,
			deleted:          result.Deleted,
			conflicts:        result.Conflicts,
			autoResolved:     result.AutoResolved,
			skippedConflicts: result.SkippedConflicts,
			warnings:         len(result.Warnings),
		})
		var firstErr error
		if len(result.Errors) > 0 {
			firstErr = result.Errors[0]
		}
		return opportunisticPushFinishedMsg{summary: summary, err: firstErr}
	}
}

// syncSummary builds the footer confirmation for a completed sync. It lists
// only the non-zero counters. A no-op sync reads "Synced Work · up to date"
// instead of five "· 0" segments behind it. t.warnings counts the
// import warnings on the SyncResult: values the importer had to fabricate.
// The count is the in-UI signal. The full text is in the state-dir log.
// Take syncTotals rather than five positional ints. A transposed
// counter then cannot compile in silence.
func syncSummary(label string, t syncTotals) string {
	var parts []string
	if t.pushed > 0 {
		parts = append(parts, fmt.Sprintf("pushed %d", t.pushed))
	}
	if t.pulled > 0 {
		parts = append(parts, fmt.Sprintf("pulled %d", t.pulled))
	}
	if t.deleted > 0 {
		parts = append(parts, fmt.Sprintf("deleted %d", t.deleted))
	}
	if t.conflicts > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", t.conflicts, pluralize(t.conflicts, "conflict", "conflicts")))
	}
	if t.autoResolved > 0 {
		parts = append(parts, fmt.Sprintf("%d auto-resolved", t.autoResolved))
	}
	if t.skippedConflicts > 0 {
		parts = append(parts, fmt.Sprintf("%d held by conflicts", t.skippedConflicts))
	}
	if t.warnings > 0 {
		parts = append(parts, importWarningsSegment(t.warnings))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Synced %s · up to date", label)
	}
	return fmt.Sprintf("Synced %s · %s", label, strings.Join(parts, " · "))
}

// syncNewlyLinkedCalendars runs the first pull for each calendar an account
// link just created. It returns how many synced cleanly, the total import-
// warning count (fabricated values the status line must mention), and the
// first error. Later calendars still sync. One failure then does not strand
// the rest unsynced.
func syncNewlyLinkedCalendars(ctx context.Context, svc *syncpkg.Service, ids []int64, strategy syncpkg.ConflictStrategy) (synced, warnings int, syncErr error) {
	for _, calendarID := range ids {
		syncResult, err := svc.SyncCalendar(ctx, calendarID, strategy)
		if syncResult != nil {
			warnings += len(syncResult.Warnings)
		}
		if err == nil && len(syncResult.Errors) > 0 {
			err = errors.Join(syncResult.Errors...)
		}
		if err != nil {
			if syncErr == nil {
				syncErr = err
			}
			continue
		}
		synced++
	}
	return synced, warnings, syncErr
}

// importWarningsSegment renders the "N import warning(s)" counter shared by
// syncSummary and the account-link status lines.
func importWarningsSegment(warnings int) string {
	return fmt.Sprintf("%d %s", warnings, pluralize(warnings, "import warning", "import warnings"))
}

// appendImportWarnings suffixes a status line with the import-warning count
// when there is one. Account-link pulls then surface fabricated values the
// same way syncSummary does for every other TUI sync path.
func appendImportWarnings(status string, warnings int) string {
	if warnings <= 0 {
		return status
	}
	return status + " · " + importWarningsSegment(warnings)
}

func (m Model) expireStatusAfter(d time.Duration, token int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return syncStatusExpiredMsg{token: token}
	})
}
