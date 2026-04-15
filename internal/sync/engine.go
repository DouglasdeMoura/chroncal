package sync

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/google/uuid"

	authpkg "github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
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
	db        *sql.DB
	q         *storage.Queries
	credStore authpkg.CredentialStore
	calendars *calendar.Service
	events    *event.Service
	todos     *todo.Service
	journals  *journal.Service
	logger    *slog.Logger
}

var syncRetryOptions = caldav.RetryOptions{
	MaxAttempts: 3,
}

var newRemoteObjectName = func() string {
	return uuid.NewString() + ".ics"
}

func normalizeRemoteRef(ref string) string {
	if ref == "" {
		return ""
	}

	parsed, err := url.Parse(ref)
	if err != nil {
		return ref
	}

	if parsed.Path != "" {
		trailingSlash := strings.HasSuffix(parsed.Path, "/")
		cleaned := path.Clean(parsed.Path)
		switch {
		case cleaned == "." && trailingSlash:
			cleaned = "/"
		case trailingSlash && cleaned != "/":
			cleaned += "/"
		}
		parsed.Path = cleaned
	}

	return parsed.String()
}

func buildRemoteResourcePath(calendarRef, _ string) string {
	parsed, err := url.Parse(calendarRef)
	if err != nil {
		return normalizeRemoteRef(strings.TrimRight(calendarRef, "/") + "/" + newRemoteObjectName())
	}

	basePath := parsed.Path
	if basePath == "" {
		basePath = "/"
	}
	parsed.Path = path.Join(basePath, newRemoteObjectName())
	return normalizeRemoteRef(parsed.String())
}

// NewEngine creates a new sync engine.
func NewEngine(db *sql.DB, q *storage.Queries, credStore authpkg.CredentialStore, calendars *calendar.Service, events *event.Service, todos *todo.Service, journals *journal.Service, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{db: db, q: q, credStore: credStore, calendars: calendars, events: events, todos: todos, journals: journals, logger: logger}
}

// SyncCalendar runs a full sync cycle for one calendar.
func (e *Engine) SyncCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (result *SyncResult, err error) {
	// Load calendar and account
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("get calendar: %w", err)
	}
	result = &SyncResult{CalendarID: cal.ID}
	attemptedAt := time.Now().UTC().Format(time.RFC3339)
	defer func() {
		if updateErr := e.updateSyncHealth(ctx, cal.ID, attemptedAt, result, err); updateErr != nil {
			e.logger.Warn("update sync health failed", "calendar_id", cal.ID, "error", updateErr)
			result.Errors = append(result.Errors, fmt.Errorf("update sync health: %w", updateErr))
		}
	}()

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

	client, err := caldav.NewClientFromCredential(account.ServerUrl, cred, func(updated authpkg.Credential) error {
		return e.credStore.Set(updated)
	})
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	remoteURL := storage.NullableToString(cal.RemoteUrl)
	if remoteURL == "" {
		return nil, fmt.Errorf("calendar %d has no remote URL", calendarID)
	}

	e.logger.Info("sync started", "calendar_id", calendarID, "remote_url", remoteURL)

	// Phase 0: Sync calendar metadata
	if err := e.syncCalendarMetadata(ctx, client, calendarID, remoteURL); err != nil {
		e.logger.Warn("calendar metadata sync failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("calendar metadata: %w", err))
	}

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
	tombstoneResult, err := e.processTombstones(ctx, client, calendarID, remoteURL)
	if err != nil {
		e.logger.Warn("tombstone processing failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("tombstones: %w", err))
	} else {
		result.Deleted += tombstoneResult.deleted
		result.Errors = append(result.Errors, tombstoneResult.errors...)
	}

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

// SyncAll syncs all connected calendars.
func (e *Engine) SyncAll(ctx context.Context, strategy ConflictStrategy) ([]*SyncResult, error) {
	cals, err := e.q.ListCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}

	var results []*SyncResult
	for _, cal := range cals {
		if cal.AccountID == nil || *cal.AccountID == 0 {
			continue
		}

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
		icalData, err := e.exportResource(ctx, res.OwnerType, res.Uid)
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
		var putPath string
		if res.RemoteUrl != "" {
			putPath, err = client.CanonicalObjectRef(remoteURL, res.RemoteUrl)
			if err != nil {
				result.errors = append(result.errors, fmt.Errorf("validate remote href for %s: %w", res.Uid, err))
				continue
			}
		} else {
			putPath, err = client.CanonicalObjectRef(remoteURL, buildRemoteResourcePath(remoteURL, res.Uid))
			if err != nil {
				result.errors = append(result.errors, fmt.Errorf("build remote href for %s: %w", res.Uid, err))
				continue
			}
		}

		// PUT to server
		newEtag, putErr := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
			return client.PutResource(ctx, putPath, cal, res.Etag)
		})
		if putErr != nil {
			// Check for 412 Precondition Failed (ETag conflict)
			if caldav.IsConflict(putErr) {
				e.logger.Warn("conflict detected during push", "uid", res.Uid)
				if strategy == ConflictServerWins {
					// Re-fetch server version, clear dirty flag, accept server state
					e.logger.Info("resolving conflict: server wins", "uid", res.Uid)
					serverRes, fetchErr := client.GetResource(ctx, putPath)
					if fetchErr != nil {
						e.logger.Error("re-fetch server resource failed", "uid", res.Uid, "error", fetchErr)
						result.errors = append(result.errors, fmt.Errorf("conflict re-fetch %s: %w", res.Uid, fetchErr))
					} else {
						var buf bytes.Buffer
						enc := ical.NewEncoder(&buf)
						if err := enc.Encode(serverRes.Data); err != nil {
							e.logger.Error("encode server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("encode server resource %s: %w", res.Uid, err))
							result.conflicts++
							continue
						}
						importResult, err := icalPkg.ImportFile(strings.NewReader(buf.String()))
						if err != nil {
							e.logger.Error("import server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("import server resource %s: %w", res.Uid, err))
							result.conflicts++
							continue
						}
						if err := e.persistImported(ctx, calendarID, importResult); err != nil {
							e.logger.Error("persist server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("persist server resource %s: %w", res.Uid, err))
							result.conflicts++
							continue
						}
						// Clear dirty and update ETag to accept server version
						if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
							CalendarID: calendarID,
							Uid:        res.Uid,
							Etag:       serverRes.ETag,
						}); err != nil {
							e.logger.Error("clear dirty after conflict", "uid", res.Uid, "error", err)
						}
					}
				} else {
					// ConflictPrompt: record conflict for manual resolution
					localIcal, exportErr := e.exportResource(ctx, res.OwnerType, res.Uid)
					if exportErr != nil {
						e.logger.Warn("export local resource for conflict record", "uid", res.Uid, "error", exportErr)
					}
					serverRes, fetchErr := client.GetResource(ctx, putPath)
					if fetchErr == nil {
						serverIcal, encodeErr := caldav.EncodeCalendar(serverRes.Data)
						if encodeErr != nil {
							e.logger.Warn("encode server resource for conflict record", "uid", res.Uid, "error", encodeErr)
						}
						ownerID := e.lookupOwnerID(ctx, res.OwnerType, res.Uid)
						_ = e.q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
							CalendarID: calendarID,
							OwnerType:  res.OwnerType,
							OwnerID:    ownerID,
							Uid:        res.Uid,
							LocalIcal:  string(localIcal),
							ServerIcal: string(serverIcal),
							ServerEtag: serverRes.ETag,
						})
					}
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

type pullResult struct {
	pulled  int
	deleted int
}

func (e *Engine) pull(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*pullResult, error) {
	result := &pullResult{}

	// Fetch all resources from server
	resources, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) ([]caldav.Resource, error) {
		return client.QueryAll(ctx, remoteURL)
	})
	if err != nil {
		return nil, fmt.Errorf("query all: %w", err)
	}

	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}
	tombstonedPaths := make(map[string]bool, len(tombstones))
	tombstonedUIDs := make(map[string]bool, len(tombstones))
	for _, ts := range tombstones {
			if ts.RemoteUrl != "" {
				remotePath, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl)
				if hrefErr != nil {
					e.logger.Warn("ignore invalid tombstone href", "calendar_id", calendarID, "uid", ts.Uid, "remote_url", ts.RemoteUrl, "error", hrefErr)
					continue
				}
				tombstonedPaths[remotePath] = true
			}
		if ts.Uid != "" {
			tombstonedUIDs[ts.Uid] = true
		}
	}

	// Build map of known local resources
	localResources, err := e.q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list local resources: %w", err)
	}
	localByPath := make(map[string]storage.SyncResource, len(localResources))
	for _, r := range localResources {
		if r.RemoteUrl != "" {
			remotePath, hrefErr := client.CanonicalObjectRef(remoteURL, r.RemoteUrl)
			if hrefErr != nil {
				e.logger.Warn("ignore invalid sync resource href", "calendar_id", calendarID, "uid", r.Uid, "remote_url", r.RemoteUrl, "error", hrefErr)
				continue
			}
			localByPath[remotePath] = r
		}
	}

	// Process each remote resource
	remoteHrefs := make(map[string]bool, len(resources))
	for _, res := range resources {
		resPath, hrefErr := client.CanonicalObjectRef(remoteURL, res.Path)
		if hrefErr != nil {
			e.logger.Warn("skip out-of-scope remote href", "calendar_id", calendarID, "path", res.Path, "error", hrefErr)
			continue
		}
		remoteHrefs[resPath] = true
		if tombstonedPaths[resPath] {
			e.logger.Debug("skip tombstoned remote resource by path", "path", resPath)
			continue
		}

		local, exists := localByPath[resPath]
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
		if tombstonedUIDs[uid] {
			e.logger.Debug("skip tombstoned remote resource by uid", "uid", uid, "path", resPath)
			continue
		}

		// Persist imported data to the database
		ownerType := detectOwnerType(importResult)
		if persistErr := e.persistImported(ctx, calendarID, importResult); persistErr != nil {
			e.logger.Error("persist imported resource", "uid", uid, "path", res.Path, "error", persistErr)
			continue
		}

		// Upsert sync resource tracking
		if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID:   calendarID,
			Uid:          uid,
			OwnerType:    ownerType,
			RemoteUrl:    resPath,
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
			if err := e.deleteLocalResourceByUID(ctx, local.OwnerType, local.Uid); err != nil {
				e.logger.Error("delete local resource", "uid", local.Uid, "owner_type", local.OwnerType, "error", err)
				continue
			}
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

func (e *Engine) deleteLocalResourceByUID(ctx context.Context, ownerType, uid string) error {
	switch ownerType {
	case "event":
		return e.q.DeleteEventsByUID(ctx, uid)
	case "todo":
		return e.q.DeleteTodosByUID(ctx, uid)
	case "journal":
		return e.q.DeleteJournalsByUID(ctx, uid)
	default:
		return fmt.Errorf("unsupported owner type %q", ownerType)
	}
}

func (e *Engine) lookupOwnerID(ctx context.Context, ownerType, uid string) int64 {
	switch ownerType {
	case "event":
		row, err := e.q.GetEventByUID(ctx, uid)
		if err == nil {
			return row.ID
		}
	case "todo":
		row, err := e.q.GetTodoByUID(ctx, uid)
		if err == nil {
			return row.ID
		}
	case "journal":
		row, err := e.q.GetJournalByUID(ctx, uid)
		if err == nil {
			return row.ID
		}
	}
	return 0
}

type tombstoneResult struct {
	deleted int
	errors  []error
}

func (e *Engine) processTombstones(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*tombstoneResult, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}

	result := &tombstoneResult{}
	for _, ts := range tombstones {
		deletePath, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl)
		if hrefErr != nil {
			result.errors = append(result.errors, fmt.Errorf("validate tombstone %s: %w", ts.Uid, hrefErr))
			continue
		}
		e.logger.Debug("deleting tombstone", "uid", ts.Uid, "remote_url", deletePath)
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.DeleteResource(ctx, deletePath)
		}); err != nil {
			e.logger.Warn("delete remote resource failed", "uid", ts.Uid, "error", err)
			result.errors = append(result.errors, fmt.Errorf("delete tombstone %s: %w", ts.Uid, err))
			continue
		}
		if err := e.q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{
			CalendarID: calendarID,
			Uid:        ts.Uid,
		}); err != nil {
			e.logger.Warn("delete sync resource after tombstone", "uid", ts.Uid, "error", err)
		}
		if err := e.q.DeleteTombstone(ctx, ts.ID); err != nil {
			e.logger.Warn("delete tombstone row failed", "uid", ts.Uid, "error", err)
		}
		result.deleted++
	}
	return result, nil
}

func (e *Engine) syncCalendarMetadata(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) error {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("get calendar for metadata sync: %w", err)
	}

	remoteColor, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
		return client.GetCalendarColor(ctx, remoteURL)
	})
	if err != nil {
		return fmt.Errorf("get remote calendar color: %w", err)
	}

	if cal.ColorDirty != 0 {
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.SetCalendarColor(ctx, remoteURL, cal.Color)
		}); err != nil {
			return fmt.Errorf("set remote calendar color: %w", err)
		}
		if err := e.calendars.ClearColorDirty(ctx, calendarID, cal.Color); err != nil {
			return fmt.Errorf("clear calendar color dirty: %w", err)
		}
		return nil
	}

	if remoteColor != storage.NullableToString(cal.RemoteColor) {
		if err := e.calendars.UpdateColorFromSync(ctx, calendarID, remoteColor, remoteColor); err != nil {
			return fmt.Errorf("update calendar color from sync: %w", err)
		}
	}

	return nil
}

// exportResource exports a local resource to iCal bytes.
func (e *Engine) exportResource(ctx context.Context, ownerType string, uid string) ([]byte, error) {
	switch ownerType {
	case "event":
		evt, err := e.events.GetByUID(ctx, uid)
		if err != nil {
			return nil, fmt.Errorf("get event by uid %s: %w", uid, err)
		}
		evt.Alarms, _ = e.events.ListAlarms(ctx, evt.ID)
		evt.Attendees, _ = e.events.ListAttendees(ctx, evt.ID)
		evt.Attachments, _ = e.events.ListAttachments(ctx, evt.ID)
		evt.Comments, _ = e.events.ListComments(ctx, evt.ID)
		evt.Contacts, _ = e.events.ListContacts(ctx, evt.ID)
		evt.Resources, _ = e.events.ListResources(ctx, evt.ID)
		evt.Relations, _ = e.events.ListRelations(ctx, evt.ID)
		evt.XProperties, _ = e.events.ListXProperties(ctx, evt.ID)
		return icalPkg.ExportEvents([]event.Event{evt}, "")
	case "todo":
		t, err := e.todos.GetByUID(ctx, uid)
		if err != nil {
			return nil, fmt.Errorf("get todo by uid %s: %w", uid, err)
		}
		t.Alarms, _ = e.todos.ListAlarms(ctx, t.ID)
		t.Attendees, _ = e.todos.ListAttendees(ctx, t.ID)
		t.Attachments, _ = e.todos.ListAttachments(ctx, t.ID)
		t.Comments, _ = e.todos.ListComments(ctx, t.ID)
		t.Contacts, _ = e.todos.ListContacts(ctx, t.ID)
		t.Resources, _ = e.todos.ListResources(ctx, t.ID)
		t.Relations, _ = e.todos.ListRelations(ctx, t.ID)
		t.XProperties, _ = e.todos.ListXProperties(ctx, t.ID)
		return icalPkg.ExportTodos([]todo.Todo{t}, "")
	case "journal":
		j, err := e.journals.GetByUID(ctx, uid)
		if err != nil {
			return nil, fmt.Errorf("get journal by uid %s: %w", uid, err)
		}
		j.Attendees, _ = e.journals.ListAttendees(ctx, j.ID)
		j.Attachments, _ = e.journals.ListAttachments(ctx, j.ID)
		j.Comments, _ = e.journals.ListComments(ctx, j.ID)
		j.Contacts, _ = e.journals.ListContacts(ctx, j.ID)
		j.Relations, _ = e.journals.ListRelations(ctx, j.ID)
		j.XProperties, _ = e.journals.ListXProperties(ctx, j.ID)
		return icalPkg.ExportJournals([]journal.Journal{j}, "")
	default:
		return nil, fmt.Errorf("unknown owner type: %s", ownerType)
	}
}

// persistImported saves parsed iCal data to the local database using the same
// upsert pattern as the CLI import command.
func (e *Engine) persistImported(ctx context.Context, calendarID int64, result icalPkg.ImportResult) error {
	// Store timezones
	for _, tz := range result.Timezones {
		if _, err := e.q.UpsertTimezone(ctx, storage.UpsertTimezoneParams{
			Tzid:          tz.TZID,
			VtimezoneData: tz.Data,
		}); err != nil {
			e.logger.Warn("store VTIMEZONE", "tzid", tz.TZID, "error", err)
		}
	}

	// Import events
	for _, ev := range result.Events {
		saved, err := e.events.UpsertByUID(ctx, event.UpsertParams{
			UID: ev.UID, CalendarID: calendarID,
			Title: ev.Title, Description: ev.Description, Location: ev.Location,
			StartTime: ev.StartTime, EndTime: ev.EndTime, AllDay: ev.AllDay,
			RecurrenceRule: ev.RecurrenceRule, Timezone: ev.Timezone,
			Status: ev.Status, Transp: ev.Transp, Sequence: ev.Sequence,
			Priority: ev.Priority, Class: ev.Class, URL: ev.URL,
			Categories: ev.Categories, ExDates: ev.ExDates, RDates: ev.RDates,
			RecurrenceID: ev.RecurrenceID, Geo: ev.Geo,
			DurationValue: ev.DurationValue, DtStamp: ev.DtStamp,
		})
		if err != nil {
			return fmt.Errorf("upsert event %q: %w", ev.UID, err)
		}
		if len(ev.Alarms) > 0 {
			_ = e.events.ReplaceAlarms(ctx, saved.ID, ev.Alarms)
		}
		if len(ev.Attendees) > 0 {
			_ = e.events.ReplaceAttendees(ctx, saved.ID, ev.Attendees)
		}
		if len(ev.Attachments) > 0 {
			_ = e.events.ReplaceAttachments(ctx, saved.ID, ev.Attachments)
		}
		if len(ev.Comments) > 0 {
			_ = e.events.ReplaceComments(ctx, saved.ID, ev.Comments)
		}
		if len(ev.Contacts) > 0 {
			_ = e.events.ReplaceContacts(ctx, saved.ID, ev.Contacts)
		}
		if len(ev.Resources) > 0 {
			_ = e.events.ReplaceResources(ctx, saved.ID, ev.Resources)
		}
		if len(ev.Relations) > 0 {
			_ = e.events.ReplaceRelations(ctx, saved.ID, ev.Relations)
		}
		if len(ev.XProperties) > 0 {
			_ = e.events.ReplaceXProperties(ctx, saved.ID, ev.XProperties)
		}
	}

	// Import todos
	for _, t := range result.Todos {
		saved, err := e.todos.UpsertByUID(ctx, todo.UpsertParams{
			UID: t.UID, CalendarID: calendarID,
			Summary: t.Summary, Description: t.Description, Location: t.Location,
			DueDate: t.DueDate, StartDate: t.StartDate, Duration: t.Duration,
			CompletedAt: t.CompletedAt, PercentComplete: t.PercentComplete,
			Status: t.Status, Priority: t.Priority, Class: t.Class,
			URL: t.URL, Categories: t.Categories,
			RecurrenceRule: t.RecurrenceRule, Timezone: t.Timezone,
			Sequence: t.Sequence, ExDates: t.ExDates, RDates: t.RDates,
			RecurrenceID: t.RecurrenceID, Geo: t.Geo,
			DtStamp: t.DtStamp,
		})
		if err != nil {
			return fmt.Errorf("upsert todo %q: %w", t.UID, err)
		}
		if len(t.Alarms) > 0 {
			_ = e.todos.ReplaceAlarms(ctx, saved.ID, t.Alarms)
		}
		if len(t.Attendees) > 0 {
			_ = e.todos.ReplaceAttendees(ctx, saved.ID, t.Attendees)
		}
		if len(t.Attachments) > 0 {
			_ = e.todos.ReplaceAttachments(ctx, saved.ID, t.Attachments)
		}
		if len(t.Comments) > 0 {
			_ = e.todos.ReplaceComments(ctx, saved.ID, t.Comments)
		}
		if len(t.Contacts) > 0 {
			_ = e.todos.ReplaceContacts(ctx, saved.ID, t.Contacts)
		}
		if len(t.Resources) > 0 {
			_ = e.todos.ReplaceResources(ctx, saved.ID, t.Resources)
		}
		if len(t.Relations) > 0 {
			_ = e.todos.ReplaceRelations(ctx, saved.ID, t.Relations)
		}
		if len(t.XProperties) > 0 {
			_ = e.todos.ReplaceXProperties(ctx, saved.ID, t.XProperties)
		}
	}

	// Import journals
	for _, j := range result.Journals {
		saved, err := e.journals.UpsertByUID(ctx, journal.UpsertParams{
			UID: j.UID, CalendarID: calendarID,
			Summary: j.Summary, Description: j.Description,
			StartDate: j.StartDate, Status: j.Status, Class: j.Class,
			URL: j.URL, Categories: j.Categories,
			RecurrenceRule: j.RecurrenceRule, Timezone: j.Timezone,
			Sequence: j.Sequence, ExDates: j.ExDates, RDates: j.RDates,
			RecurrenceID: j.RecurrenceID,
			DtStamp:      j.DtStamp,
		})
		if err != nil {
			return fmt.Errorf("upsert journal %q: %w", j.UID, err)
		}
		if len(j.Attendees) > 0 {
			_ = e.journals.ReplaceAttendees(ctx, saved.ID, j.Attendees)
		}
		if len(j.Attachments) > 0 {
			_ = e.journals.ReplaceAttachments(ctx, saved.ID, j.Attachments)
		}
		if len(j.Comments) > 0 {
			_ = e.journals.ReplaceComments(ctx, saved.ID, j.Comments)
		}
		if len(j.Contacts) > 0 {
			_ = e.journals.ReplaceContacts(ctx, saved.ID, j.Contacts)
		}
		if len(j.Relations) > 0 {
			_ = e.journals.ReplaceRelations(ctx, saved.ID, j.Relations)
		}
		if len(j.XProperties) > 0 {
			_ = e.journals.ReplaceXProperties(ctx, saved.ID, j.XProperties)
		}
	}

	return nil
}

func parseICalData(data []byte) (*ical.Calendar, error) {
	dec := ical.NewDecoder(bytes.NewReader(data))
	return dec.Decode()
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

func (e *Engine) updateSyncHealth(ctx context.Context, calendarID int64, attemptedAt string, result *SyncResult, runErr error) error {
	lastSyncAt := ""
	lastSyncError := summarizeSyncError(result, runErr)
	if runErr == nil && len(result.Errors) == 0 {
		lastSyncAt = attemptedAt
		lastSyncError = ""
	}

	return e.q.UpdateCalendarSyncHealth(ctx, storage.UpdateCalendarSyncHealthParams{
		ID:                  calendarID,
		LastSyncAttemptedAt: storage.StringToNullable(attemptedAt),
		LastSyncAt:          storage.StringToNullable(lastSyncAt),
		LastSyncError:       storage.StringToNullable(lastSyncError),
	})
}

func summarizeSyncError(result *SyncResult, runErr error) string {
	if runErr != nil {
		return runErr.Error()
	}
	if len(result.Errors) == 0 {
		return ""
	}
	if len(result.Errors) == 1 {
		return result.Errors[0].Error()
	}
	return fmt.Sprintf("%s (+%d more)", result.Errors[0], len(result.Errors)-1)
}
