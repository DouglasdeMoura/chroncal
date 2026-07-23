package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/alarm"
	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/fileid"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

type App struct {
	DB          *sql.DB
	Queries     *storage.Queries
	Accounts    *account.Service
	Calendars   *calendar.Service
	Events      *event.Service
	Todos       *todo.Service
	Journals    *journal.Service
	Alarms      *alarm.Service
	Recurrences *recurrence.Service
	Trash       *trash.Service

	// AllowPlaintext gates the plaintext credential-store fallback. It
	// defaults to false so that, when no OS keyring is available, credential
	// writes fail loudly instead of silently persisting secrets in cleartext.
	// The CLI sets it from config/--allow-plaintext; the TUI inherits it.
	AllowPlaintext bool

	// CredentialNamespace scopes external keyring/file keys to this database
	// file identity. PreviousCredentialNamespaces allow non-destructive reads
	// after a move or copy. MigrateLegacyCredentials is true only for the
	// default database, the sole unambiguous owner of pre-namespace keys.
	CredentialNamespace          string
	PreviousCredentialNamespaces []auth.PreviousCredentialScope
	MigrateLegacyCredentials     bool
}

func New(dbPath string) (*App, error) {
	db, queries, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	credentialScopes, err := storage.GetCredentialScopes(context.Background(), db, dbPath)
	if err != nil {
		db.Close()
		return nil, err
	}
	defaultDBPath, err := DefaultDBPath()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("resolve default database path: %w", err)
	}
	previousCredentialNamespaces := make([]auth.PreviousCredentialScope, len(credentialScopes.Previous))
	for i, previous := range credentialScopes.Previous {
		previousCredentialNamespaces[i] = auth.PreviousCredentialScope{
			Namespace:    previous.Namespace,
			MaxAccountID: previous.MaxAccountID,
		}
	}
	migrateLegacyCredentials := sameDatabasePath(dbPath, defaultDBPath)

	eventSvc := event.NewService(db, queries)
	todoSvc := todo.NewService(db, queries)
	journalSvc := journal.NewService(db, queries)

	return &App{
		DB:                           db,
		Queries:                      queries,
		Accounts:                     account.NewService(db, queries),
		Calendars:                    calendar.NewService(db, queries),
		Events:                       eventSvc,
		Todos:                        todoSvc,
		Journals:                     journalSvc,
		Alarms:                       alarm.NewService(db, queries, eventSvc, todoSvc),
		Recurrences:                  recurrence.NewService(db, queries),
		Trash:                        trash.NewService(eventSvc, todoSvc, journalSvc),
		CredentialNamespace:          credentialScopes.Current,
		PreviousCredentialNamespaces: previousCredentialNamespaces,
		MigrateLegacyCredentials:     migrateLegacyCredentials,
	}, nil
}

func (a *App) Close() error {
	return a.DB.Close()
}

func sameDatabasePath(a, b string) bool {
	if identityA, errA := fileid.Identity(a); errA == nil {
		if identityB, errB := fileid.Identity(b); errB == nil {
			return identityA == identityB
		}
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
}

func DefaultDBPath() (string, error) {
	dataDir, err := userDataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, "chroncal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "chroncal.db"), nil
}

// userDataDir returns the OS-appropriate directory for application data.
// On Linux this follows XDG: $XDG_DATA_HOME or ~/.local/share.
// On macOS and Windows, config and data share the same path.
func userDataDir() (string, error) {
	if runtime.GOOS == "linux" {
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return dir, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
	return os.UserConfigDir()
}
