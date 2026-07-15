package app

import (
	"os"
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

func TestApp_LegacyCredentialMigrationIsLimitedToDefaultDatabase(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	defaultPath, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	defaultApp, err := New(defaultPath)
	if err != nil {
		t.Fatalf("New default database: %v", err)
	}
	defer defaultApp.Close()
	if !defaultApp.MigrateLegacyCredentials {
		t.Fatal("default database must migrate legacy unscoped credentials")
	}

	alternateApp, err := New(filepath.Join(t.TempDir(), "alternate.db"))
	if err != nil {
		t.Fatalf("New alternate database: %v", err)
	}
	defer alternateApp.Close()
	if alternateApp.MigrateLegacyCredentials {
		t.Fatal("alternate database must not claim legacy unscoped credentials")
	}
	if alternateApp.CredentialNamespace == defaultApp.CredentialNamespace {
		t.Fatalf("default and alternate databases share credential namespace %q", defaultApp.CredentialNamespace)
	}
}

func TestApp_HardLinkedDefaultDatabaseMigratesLegacyCredentials(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	defaultPath, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("DefaultDBPath: %v", err)
	}
	defaultApp, err := New(defaultPath)
	if err != nil {
		t.Fatalf("New default database: %v", err)
	}
	if err := defaultApp.Close(); err != nil {
		t.Fatalf("close default database: %v", err)
	}

	aliasPath := filepath.Join(t.TempDir(), "default-hardlink.db")
	if err := os.Link(defaultPath, aliasPath); err != nil {
		t.Fatalf("create hard link: %v", err)
	}
	aliasApp, err := New(aliasPath)
	if err != nil {
		t.Fatalf("New hard-linked database: %v", err)
	}
	defer aliasApp.Close()
	if !aliasApp.MigrateLegacyCredentials {
		t.Fatal("hard-linked default database must migrate legacy unscoped credentials")
	}
}
