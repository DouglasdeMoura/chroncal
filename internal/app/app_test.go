package app

import (
	"path/filepath"
	"testing"
)

func TestApp_NewAndClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	a, err := New(dbPath)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

func TestApp_Services(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	a, err := New(dbPath)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer a.Close()

	if a.Accounts == nil {
		t.Error("Accounts service is nil")
	}
	if a.Calendars == nil {
		t.Error("Calendars service is nil")
	}
	if a.Events == nil {
		t.Error("Events service is nil")
	}
	if a.Todos == nil {
		t.Error("Todos service is nil")
	}
}

func TestApp_DefaultDBPath(t *testing.T) {
	path, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath error: %v", err)
	}
	if path == "" {
		t.Error("DefaultDBPath returned empty string")
	}
	if filepath.Base(path) != "chroncal.db" {
		t.Errorf("DefaultDBPath base = %q, want chroncal.db", filepath.Base(path))
	}
}
