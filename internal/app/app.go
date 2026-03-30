package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/douglasdemoura/tcal/internal/alarm"
	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/storage"
	"github.com/douglasdemoura/tcal/internal/todo"
)

type App struct {
	DB        *sql.DB
	Calendars *calendar.Service
	Events    *event.Service
	Todos     *todo.Service
	Alarms    *alarm.Service
}

func New(dbPath string) (*App, error) {
	db, queries, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	eventSvc := event.NewService(db, queries)
	todoSvc := todo.NewService(db, queries)

	return &App{
		DB:        db,
		Calendars: calendar.NewService(queries),
		Events:    eventSvc,
		Todos:     todoSvc,
		Alarms:    alarm.NewService(db, queries, eventSvc, todoSvc),
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
	dir := filepath.Join(dataDir, "tcal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "tcal.db"), nil
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
