package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/auth"
)

type failingCredentialStore struct {
	setErr error
}

func (s failingCredentialStore) Get(accountID int64) (auth.Credential, error) {
	return auth.Credential{}, errors.New("unexpected Get")
}

func (s failingCredentialStore) Set(cred auth.Credential) error {
	return s.setErr
}

func (s failingCredentialStore) Delete(accountID int64) error {
	return nil
}

type recordingCredentialStore struct {
	deleted []int64
}

func (s *recordingCredentialStore) Get(accountID int64) (auth.Credential, error) {
	return auth.Credential{}, errors.New("unexpected Get")
}

func (s *recordingCredentialStore) Set(cred auth.Credential) error {
	return nil
}

func (s *recordingCredentialStore) Delete(accountID int64) error {
	s.deleted = append(s.deleted, accountID)
	return nil
}

func TestConnectCalendarRemote_RollsBackNewLinkWhenCredentialStoreFails(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	ctx := context.Background()
	cal, err := a.Calendars.Create(ctx, "Work", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	prevFactory := newCalendarCredentialStore
	newCalendarCredentialStore = func(bool) (auth.CredentialStore, error) {
		return failingCredentialStore{setErr: errors.New("boom")}, nil
	}
	t.Cleanup(func() {
		newCalendarCredentialStore = prevFactory
	})

	err = connectCalendarRemote(ctx, a, cal, calendarRemoteFlags{
		RemoteURL: "https://cal.example.com/dav/calendars/work/",
		Username:  "alice",
		AuthType:  "bearer",
	})
	if err == nil {
		t.Fatal("connectCalendarRemote should fail when credential storage fails")
	}
	if !strings.Contains(err.Error(), "store credentials") {
		t.Fatalf("error = %v, want credential storage failure", err)
	}

	got, err := a.Calendars.Get(ctx, cal.ID)
	if err != nil {
		t.Fatalf("calendar get: %v", err)
	}
	if got.AccountID != 0 {
		t.Fatalf("calendar account ID = %d, want 0 after rollback", got.AccountID)
	}
	if got.RemoteURL != "" {
		t.Fatalf("calendar remote URL = %q, want empty after rollback", got.RemoteURL)
	}
}

func TestConnectCalendarRemote_RollsBackExistingHiddenAccountUpdateWhenCredentialStoreFails(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	ctx := context.Background()
	calendarID, accountID := createLinkedCalendarForTest(t, dbPath)

	prevFactory := newCalendarCredentialStore
	newCalendarCredentialStore = func(bool) (auth.CredentialStore, error) {
		return failingCredentialStore{setErr: errors.New("boom")}, nil
	}
	t.Cleanup(func() {
		newCalendarCredentialStore = prevFactory
	})

	cal, err := a.Calendars.Get(ctx, calendarID)
	if err != nil {
		t.Fatalf("calendar get: %v", err)
	}

	err = connectCalendarRemote(ctx, a, cal, calendarRemoteFlags{
		RemoteURL: "https://cal.example.com/dav/calendars/renamed/",
		Username:  "alice",
		AuthType:  "bearer",
	})
	if err == nil {
		t.Fatal("connectCalendarRemote should fail when credential storage fails")
	}
	if !strings.Contains(err.Error(), "store credentials") {
		t.Fatalf("error = %v, want credential storage failure", err)
	}

	got, err := a.Calendars.Get(ctx, calendarID)
	if err != nil {
		t.Fatalf("calendar get: %v", err)
	}
	if got.AccountID != accountID {
		t.Fatalf("calendar account ID = %d, want %d after rollback", got.AccountID, accountID)
	}
	if got.RemoteURL != "https://cal.example.com/dav/calendars/work/" {
		t.Fatalf("calendar remote URL = %q, want original remote URL after rollback", got.RemoteURL)
	}

	account, err := a.Queries.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.ServerUrl != "https://cal.example.com/dav" {
		t.Fatalf("account server URL = %q, want original server URL after rollback", account.ServerUrl)
	}
}

func TestDeleteCalendarWithCleanup_RemovesHiddenAccountAndCredential(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	ctx := context.Background()
	_, accountID := createLinkedCalendarForTest(t, dbPath)

	store := &recordingCredentialStore{}
	prevFactory := newCalendarCredentialStore
	newCalendarCredentialStore = func(bool) (auth.CredentialStore, error) {
		return store, nil
	}
	t.Cleanup(func() {
		newCalendarCredentialStore = prevFactory
	})

	if err := deleteCalendarWithCleanup(ctx, a, 2, 0); err != nil {
		t.Fatalf("deleteCalendarWithCleanup: %v", err)
	}

	if _, err := a.Queries.GetAccount(ctx, accountID); err == nil {
		t.Fatalf("expected hidden account %d to be deleted", accountID)
	}
	if len(store.deleted) != 1 || store.deleted[0] != accountID {
		t.Fatalf("deleted credentials = %v, want [%d]", store.deleted, accountID)
	}
}
