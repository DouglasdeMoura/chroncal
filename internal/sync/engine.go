package sync

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emersion/go-ical"

	authpkg "github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// SyncResult holds the outcome of a sync cycle for one calendar.
type SyncResult struct {
	CalendarID int64
	Pushed     int
	Pulled     int
	Deleted    int
	Conflicts  int
	Errors     []error
}

// ConflictStrategy determines how to handle conflicts.
type ConflictStrategy string

const (
	ConflictServerWins ConflictStrategy = "server-wins"
	ConflictPrompt     ConflictStrategy = "prompt"
)

// Engine orchestrates push and pull of CalDAV resources.
type Engine struct {
	db       *sql.DB
	q        *storage.Queries
	credStore authpkg.CredentialStore
	logger   *slog.Logger
}

// NewEngine creates a new sync engine.
func NewEngine(db *sql.DB, q *storage.Queries, credStore authpkg.CredentialStore, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{db: db, q: q, credStore: credStore, logger: logger}
}

// SyncCalendar runs a full sync cycle for one calendar.
func (e *Engine) SyncCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (*SyncResult, error) {
	result := &SyncResult{CalendarID: calendarID}

	// Load calendar and account
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("get calendar: %w", err)
	}
	if cal.AccountID == nil || *cal.AccountID == 0 {
		return nil, fmt.Errorf("calendar %d is not linked to an account", calendarID)
	}

	account, err := e.q.GetAccount(ctx, *cal.AccountID)
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}

	// Get credentials and create client
	cred, err := e.credStore.Get(account.ID)
	if err != nil {
		return nil, fmt.Errorf("get credentials: %w", err)
	}

	client, err := caldav.NewClientFromCredential(account.ServerUrl, cred)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	remoteURL := storage.NullableToString(cal.RemoteUrl)
	if remoteURL == "" {
		return nil, fmt.Errorf("calendar %d has no remote URL", calendarID)
	}

	e.logger.Info("sync started", "calendar_id", calendarID, "remote_url", remoteURL)

	// Phase 1: Push dirty resources
	pushResult, err := e.push(ctx, client, calendarID, remoteURL, strategy)
	if err != nil {
		e.logger.Error("push failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("push: %w", err))
	} else {
		result.Pushed = pushResult.pushed
		result.Conflicts = pushResult.conflicts
		result.Errors = append(result.Errors, pushResult.errors...)
	}

	// Phase 2: Pull changes from server
	pullResult, err := e.pull(ctx, client, calendarID, remoteURL)
	if err != nil {
		e.logger.Error("pull failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("pull: %w", err))
	} else {
		result.Pulled = pullResult.pulled
		result.Deleted = pullResult.deleted
	}

	// Phase 3: Process tombstones
	tombstoneCount, err := e.processTombstones(ctx, client, calendarID)
	if err != nil {
		e.logger.Warn("tombstone processing failed", "calendar_id", calendarID, "error", err)
	}
	result.Deleted += tombstoneCount

	// Cleanup stale tombstones
	if err := e.q.DeleteStaleTombstones(ctx); err != nil {
		e.logger.Warn("stale tombstone cleanup failed", "error", err)
	}

	e.logger.Info("sync completed",
		"calendar_id", calendarID,
		"pushed", result.Pushed,
		"pulled", result.Pulled,
		"deleted", result.Deleted,
		"conflicts", result.Conflicts,
		"errors", len(result.Errors),
	)

	return result, nil
}

// SyncAll syncs all calendars linked to accounts.
func (e *Engine) SyncAll(ctx context.Context, strategy ConflictStrategy) ([]*SyncResult, error) {
	accounts, err := e.q.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}

	var results []*SyncResult
	for _, account := range accounts {
		cals, err := e.q.ListCalendarsByAccount(ctx, &account.ID)
		if err != nil {
			e.logger.Error("list calendars for account", "account_id", account.ID, "error", err)
			continue
		}
		for _, cal := range cals {
			result, err := e.SyncCalendar(ctx, cal.ID, strategy)
			if err != nil {
				e.logger.Error("sync calendar failed", "calendar_id", cal.ID, "error", err)
				results = append(results, &SyncResult{
					CalendarID: cal.ID,
					Errors:     []error{err},
				})
				continue
			}
			results = append(results, result)
		}
	}
	return results, nil
}

type pushResult struct {
	pushed    int
	conflicts int
	errors    []error
}

func (e *Engine) push(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string, strategy ConflictStrategy) (*pushResult, error) {
	dirty, err := e.q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list dirty: %w", err)
	}

	result := &pushResult{}
	for _, res := range dirty {
		e.logger.Debug("pushing resource", "uid", res.Uid, "remote_url", res.RemoteUrl)

		// Export the local resource to iCal
		icalData, err := e.exportResource(ctx, res.OwnerType, calendarID, res.Uid)
		if err != nil {
			e.logger.Error("export resource failed", "uid", res.Uid, "error", err)
			result.errors = append(result.errors, fmt.Errorf("export %s: %w", res.Uid, err))
			continue
		}

		// Parse the iCal data for PUT
		cal, parseErr := parseICalData(icalData)
		if parseErr != nil {
			result.errors = append(result.errors, fmt.Errorf("parse ical for %s: %w", res.Uid, parseErr))
			continue
		}

		// Determine PUT path
		putPath := res.RemoteUrl
		if putPath == "" {
			putPath = remoteURL + "/" + res.Uid + ".ics"
		}

		// PUT to server
		newEtag, putErr := client.PutResource(ctx, putPath, cal)
		if putErr != nil {
			// Check for 412 Precondition Failed (ETag conflict)
			if isConflictError(putErr) {
				e.logger.Warn("conflict detected during push", "uid", res.Uid)
				if strategy == ConflictServerWins {
					// Re-fetch server version and overwrite local
					e.logger.Info("resolving conflict: server wins", "uid", res.Uid)
				}
				result.conflicts++
				continue
			}
			e.logger.Error("PUT failed", "uid", res.Uid, "error", putErr)
			result.errors = append(result.errors, fmt.Errorf("put %s: %w", res.Uid, putErr))
			continue
		}

		// Clear dirty flag and update ETag
		if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
			CalendarID: calendarID,
			Uid:        res.Uid,
			Etag:       newEtag,
		}); err != nil {
			e.logger.Error("clear dirty failed", "uid", res.Uid, "error", err)
		}

		// Update remote URL if it was newly assigned
		if res.RemoteUrl == "" {
			if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
				CalendarID:   calendarID,
				Uid:          res.Uid,
				OwnerType:    res.OwnerType,
				RemoteUrl:    putPath,
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

type pullResult struct {
	pulled  int
	deleted int
}

func (e *Engine) pull(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*pullResult, error) {
	result := &pullResult{}

	// Fetch all resources from server
	resources, err := client.QueryAll(ctx, remoteURL)
	if err != nil {
		return nil, fmt.Errorf("query all: %w", err)
	}

	// Build map of known local resources
	localResources, err := e.q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list local resources: %w", err)
	}
	localByPath := make(map[string]storage.SyncResource, len(localResources))
	for _, r := range localResources {
		if r.RemoteUrl != "" {
			localByPath[r.RemoteUrl] = r
		}
	}

	// Process each remote resource
	remoteHrefs := make(map[string]bool, len(resources))
	for _, res := range resources {
		remoteHrefs[res.Path] = true

		local, exists := localByPath[res.Path]
		if exists && local.Etag == res.ETag {
			// No change
			continue
		}

		// Import the resource
		if res.Data == nil {
			continue
		}
		var buf bytes.Buffer
		enc := ical.NewEncoder(&buf)
		if err := enc.Encode(res.Data); err != nil {
			e.logger.Warn("encode fetched resource failed", "path", res.Path, "error", err)
			continue
		}

		importResult, err := icalPkg.ImportFile(strings.NewReader(buf.String()))
		if err != nil {
			e.logger.Warn("import fetched resource failed", "path", res.Path, "error", err)
			continue
		}

		// Extract UID from imported data
		uid := extractUID(importResult)
		if uid == "" {
			e.logger.Warn("no UID in fetched resource", "path", res.Path)
			continue
		}

		// Upsert sync resource
		ownerType := detectOwnerType(importResult)
		if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID:   calendarID,
			Uid:          uid,
			OwnerType:    ownerType,
			RemoteUrl:    res.Path,
			Etag:         res.ETag,
			Dirty:        0,
			SyncStrategy: "sync-token",
		}); err != nil {
			e.logger.Error("upsert sync resource", "uid", uid, "error", err)
		}

		result.pulled++
		e.logger.Debug("pulled resource", "uid", uid, "path", res.Path, "etag", res.ETag)
	}

	// Detect deletions: local resources whose path is no longer on the server
	for path, local := range localByPath {
		if !remoteHrefs[path] {
			e.logger.Debug("resource deleted on server", "uid", local.Uid, "path", path)
			if err := e.q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{
				CalendarID: calendarID,
				Uid:        local.Uid,
			}); err != nil {
				e.logger.Error("delete sync resource", "uid", local.Uid, "error", err)
			}
			result.deleted++
		}
	}

	return result, nil
}

func (e *Engine) processTombstones(ctx context.Context, client *caldav.Client, calendarID int64) (int, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return 0, fmt.Errorf("list tombstones: %w", err)
	}

	deleted := 0
	for _, ts := range tombstones {
		e.logger.Debug("deleting tombstone", "uid", ts.Uid, "remote_url", ts.RemoteUrl)
		if err := client.DeleteResource(ctx, ts.RemoteUrl); err != nil {
			e.logger.Warn("delete remote resource failed", "uid", ts.Uid, "error", err)
			continue
		}
		if err := e.q.DeleteTombstone(ctx, ts.ID); err != nil {
			e.logger.Warn("delete tombstone row failed", "uid", ts.Uid, "error", err)
		}
		deleted++
	}
	return deleted, nil
}

// exportResource exports a local resource to iCal bytes.
func (e *Engine) exportResource(ctx context.Context, ownerType string, calendarID int64, uid string) ([]byte, error) {
	// This is a simplified export — the full implementation would query the
	// specific event/todo/journal by UID and export it. For now, delegate
	// to the service layer (to be wired in Phase 5).
	_ = ctx
	_ = calendarID
	return nil, fmt.Errorf("export for %s uid=%s: not yet wired to services", ownerType, uid)
}

func parseICalData(data []byte) (*ical.Calendar, error) {
	dec := ical.NewDecoder(bytes.NewReader(data))
	return dec.Decode()
}

func isConflictError(err error) bool {
	return strings.Contains(err.Error(), "412") || strings.Contains(err.Error(), "Precondition Failed")
}

func extractUID(result icalPkg.ImportResult) string {
	if len(result.Events) > 0 {
		return result.Events[0].UID
	}
	if len(result.Todos) > 0 {
		return result.Todos[0].UID
	}
	if len(result.Journals) > 0 {
		return result.Journals[0].UID
	}
	return ""
}

func detectOwnerType(result icalPkg.ImportResult) string {
	if len(result.Events) > 0 {
		return "event"
	}
	if len(result.Todos) > 0 {
		return "todo"
	}
	if len(result.Journals) > 0 {
		return "journal"
	}
	return "event"
}
