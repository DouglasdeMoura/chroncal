package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

func TestFriendlyCreateAccountError_DuplicateName(t *testing.T) {
	a, err := app.New(filepath.Join(t.TempDir(), "chroncal.db"))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	ctx := context.Background()
	_, err = a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "Work",
		ServerUrl: "https://cal.example.com/dav",
		AuthType:  "bearer",
		Username:  "alice",
	})
	if err != nil {
		t.Fatalf("CreateAccount first insert: %v", err)
	}

	_, err = a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "Work",
		ServerUrl: "https://other.example.com/dav",
		AuthType:  "bearer",
		Username:  "alice",
	})
	if err == nil {
		t.Fatal("expected duplicate CreateAccount to fail")
	}

	got := friendlyCreateAccountError("Work", err)
	if got == nil {
		t.Fatal("friendlyCreateAccountError returned nil")
	}
	if !strings.Contains(got.Error(), `account "Work" already exists`) {
		t.Fatalf("friendlyCreateAccountError = %q, want duplicate account message", got.Error())
	}
	if strings.Contains(got.Error(), "UNIQUE constraint failed") {
		t.Fatalf("friendlyCreateAccountError = %q, want user-friendly message", got.Error())
	}
}

func TestFriendlyCreateAccountError_PreservesUnknownError(t *testing.T) {
	base := errors.New("boom")
	got := friendlyCreateAccountError("Work", base)
	if !errors.Is(got, base) {
		t.Fatalf("friendlyCreateAccountError should wrap original error")
	}
}
