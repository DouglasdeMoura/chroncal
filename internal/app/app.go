package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/douglasdemoura/tcal/internal/calendar"
	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/storage"
)

type App struct {
	DB        *sql.DB
	Calendars *calendar.Service
	Events    *event.Service
}

func New(dbPath string) (*App, error) {
	db, queries, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	return &App{
		DB:        db,
		Calendars: calendar.NewService(queries),
		Events:    event.NewService(db, queries),
	}, nil
}

func (a *App) Close() error {
	return a.DB.Close()
}

func DefaultDBPath() (string, error) {
	dataDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, "tcal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "tcal.db"), nil
}
