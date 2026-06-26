package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/douglasdemoura/chroncal/internal/alarm"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/recurrence"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

type App struct {
	DB          *sql.DB
	Queries     *storage.Queries
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
}

func New(dbPath string) (*App, error) {
	db, queries, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	eventSvc := event.NewService(db, queries)
	todoSvc := todo.NewService(db, queries)
	journalSvc := journal.NewService(db, queries)

	return &App{
		DB:          db,
		Queries:     queries,
		Calendars:   calendar.NewService(db, queries),
		Events:      eventSvc,
		Todos:       todoSvc,
		Journals:    journalSvc,
		Alarms:      alarm.NewService(db, queries, eventSvc, todoSvc),
		Recurrences: recurrence.NewService(db, queries),
		Trash:       trash.NewService(eventSvc, todoSvc, journalSvc),
	}, nil
}

func (a *App) Close() error {
	return a.DB.Close()
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
