package calendar

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

type memCredStore struct {
	creds map[int64]auth.Credential
}

func (s *memCredStore) Get(id int64) (auth.Credential, error) {
	c, ok := s.creds[id]
	if !ok {
		return auth.Credential{}, nil
	}
	return c, nil
}

func (s *memCredStore) Set(c auth.Credential) error {
	if s.creds == nil {
		s.creds = make(map[int64]auth.Credential)
	}
	s.creds[c.AccountID] = c
	return nil
}

func (s *memCredStore) Delete(id int64) error {
	delete(s.creds, id)
	return nil
}

func TestConnect_SeedsRemoteColor(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cal.Color == "#9FE1E7" {
		t.Fatal("seed precondition: default color must differ from remote color")
	}

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
		RemoteColor:   "#9FE1E7",
	}
	cred := auth.Credential{Username: "user", Password: "pass"}

	if err := svc.Connect(ctx, cal, link, cred, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after Connect: %v", err)
	}
	if got.Color != "#9FE1E7" {
		t.Errorf("Color = %q, want #9FE1E7 (adopted from remote at link time)", got.Color)
	}
	if got.RemoteColor != "#9FE1E7" {
		t.Errorf("RemoteColor = %q, want #9FE1E7", got.RemoteColor)
	}
	if got.ColorDirty {
		t.Error("ColorDirty must stay false right after seeding from the server")
	}
}

func TestConnect_RelinkDoesNotClobberLocalColorEdit(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()

	// Simulate a calendar that was previously linked to a hidden account.
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__calendar_1",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        1,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://example.com/dav/calendars/work/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	// User just changed the local color in the dialog: Update set color_dirty=1
	// and persisted the new color. Re-saving the dialog falls into the
	// existing-account branch of Connect.
	if _, err := db.ExecContext(ctx, "UPDATE calendars SET color = ?, color_dirty = 1 WHERE id = 1", "#FF0000"); err != nil {
		t.Fatalf("seed local color edit: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
		RemoteColor:   "#0000FF",
	}
	if err := svc.Connect(ctx, cal, link, auth.Credential{Username: "user", Password: "pass"}, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after Connect: %v", err)
	}
	if got.Color != "#FF0000" {
		t.Errorf("Color = %q, want #FF0000 (re-link must not clobber the user's local edit)", got.Color)
	}
	if !got.ColorDirty {
		t.Error("ColorDirty must stay true so the next sync pushes the local edit to the server")
	}
}

func TestConnect_NoRemoteColor_LeavesLocalColor(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	original, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	link := RemoteLink{
		RemoteURL:     "https://example.com/dav/calendars/work/",
		Username:      "user",
		AuthType:      "basic",
		AllowInsecure: false,
	}
	cred := auth.Credential{Username: "user", Password: "pass"}

	if err := svc.Connect(ctx, original, link, cred, &memCredStore{}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after Connect: %v", err)
	}
	if got.Color != original.Color {
		t.Errorf("Color = %q, want %q (no remote color → keep local)", got.Color, original.Color)
	}
	if got.RemoteColor != "" {
		t.Errorf("RemoteColor = %q, want empty when fetch yielded nothing", got.RemoteColor)
	}
}
