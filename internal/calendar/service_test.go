package calendar

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	return NewService(db, q)
}

func newTestServiceWithDB(t *testing.T) (*Service, *storage.Queries, *sql.DB) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	return NewService(db, q), q, db
}

func TestCalendarService_ListDefault(t *testing.T) {
	svc := newTestService(t)
	cals, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(cals) != 1 {
		t.Fatalf("List returned %d calendars, want 1", len(cals))
	}
	if cals[0].Name != "Personal" {
		t.Errorf("default calendar name = %q, want %q", cals[0].Name, "Personal")
	}
}

func TestCalendarService_Create(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, err := svc.Create(ctx, "Work", "#0284C7", "Work calendar")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if c.ID == 0 {
		t.Error("Create returned ID 0")
	}
	if c.Name != "Work" {
		t.Errorf("Name = %q, want %q", c.Name, "Work")
	}
	if c.Color != "#0284C7" {
		t.Errorf("Color = %q, want %q", c.Color, "#0284C7")
	}
	if c.Description != "Work calendar" {
		t.Errorf("Description = %q, want %q", c.Description, "Work calendar")
	}

	cals, _ := svc.List(ctx)
	if len(cals) != 2 {
		t.Errorf("List after Create returned %d, want 2", len(cals))
	}
}

func TestCalendarService_CreateAppendsDisplayOrder(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// The seed "Personal" calendar holds the lowest display_order; newly
	// created calendars must append after it (MAX+1) rather than collide at 0.
	seed, _ := svc.List(ctx)
	first, err := svc.Create(ctx, "Aardvark", "#111", "")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	second, err := svc.Create(ctx, "Beaver", "#222", "")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	if first.DisplayOrder != seed[0].DisplayOrder+1 {
		t.Errorf("first created DisplayOrder = %d, want %d", first.DisplayOrder, seed[0].DisplayOrder+1)
	}
	if second.DisplayOrder != first.DisplayOrder+1 {
		t.Errorf("second created DisplayOrder = %d, want %d", second.DisplayOrder, first.DisplayOrder+1)
	}

	// List orders by display_order, so the newest calendar lands at the bottom
	// even though "Beaver" would sort before some existing names alphabetically.
	cals, _ := svc.List(ctx)
	if cals[len(cals)-1].Name != "Beaver" {
		t.Errorf("last calendar = %q, want %q (newest appends to bottom)", cals[len(cals)-1].Name, "Beaver")
	}
}

func TestCalendarService_Get(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1) error: %v", err)
	}
	if c.Name != "Personal" {
		t.Errorf("Get(1).Name = %q, want %q", c.Name, "Personal")
	}

	_, err = svc.Get(ctx, 999)
	if err == nil {
		t.Error("Get(999) expected error, got nil")
	}
}

func TestCalendarService_Update(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, err := svc.Update(ctx, 1, "Personal Updated", "#FF0000", "Updated desc")
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if c.Name != "Personal Updated" {
		t.Errorf("Name = %q, want %q", c.Name, "Personal Updated")
	}
	if c.Color != "#FF0000" {
		t.Errorf("Color = %q, want %q", c.Color, "#FF0000")
	}
}

func TestCalendarService_UpdateLinkedColorMarksDirty(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
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
		RemoteUrl: storage.StringToNullable("https://example.com/cal/work"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	c, err := svc.Update(ctx, 1, "Personal", "#123456", "")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !c.ColorDirty {
		t.Fatal("expected linked calendar color change to mark color dirty")
	}
}

func TestCalendarService_UpdateColorFromSync(t *testing.T) {
	svc, _, db := newTestServiceWithDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "UPDATE calendars SET color_dirty = 1 WHERE id = 1"); err != nil {
		t.Fatalf("seed dirty color: %v", err)
	}

	if err := svc.UpdateColorFromSync(ctx, 1, "#abcdef", "#abcdef"); err != nil {
		t.Fatalf("UpdateColorFromSync: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cal.Color != "#abcdef" {
		t.Fatalf("Color = %q, want #abcdef", cal.Color)
	}
	if cal.RemoteColor != "#abcdef" {
		t.Fatalf("RemoteColor = %q, want #abcdef", cal.RemoteColor)
	}
	if cal.ColorDirty {
		t.Fatal("expected color dirty to be cleared")
	}
}

func TestCalendarService_LinkToAccount(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := svc.LinkToAccount(ctx, 1, account.ID, "https://example.com/cal/work"); err != nil {
		t.Fatalf("LinkToAccount: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cal.AccountID != account.ID {
		t.Fatalf("AccountID = %d, want %d", cal.AccountID, account.ID)
	}
	if cal.RemoteURL != "https://example.com/cal/work" {
		t.Fatalf("RemoteURL = %q", cal.RemoteURL)
	}
}

func TestCalendarService_UnlinkFromAccount(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := svc.LinkToAccount(ctx, 1, account.ID, "https://example.com/cal/work"); err != nil {
		t.Fatalf("LinkToAccount: %v", err)
	}

	if err := svc.UnlinkFromAccount(ctx, 1); err != nil {
		t.Fatalf("UnlinkFromAccount: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cal.AccountID != 0 {
		t.Fatalf("AccountID = %d, want 0", cal.AccountID)
	}
	if cal.RemoteURL != "" {
		t.Fatalf("RemoteURL = %q, want empty", cal.RemoteURL)
	}
}

func TestCalendarService_Delete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, _ := svc.Create(ctx, "Temp", "#000", "")
	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	_, err := svc.Get(ctx, c.ID)
	if err == nil {
		t.Error("Get after Delete expected error, got nil")
	}
}

func TestCalendarService_DeleteCascade(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	cal, _ := svc.Create(ctx, "Temp", "#000", "")

	// Insert an event into this calendar directly
	_, err := db.ExecContext(ctx,
		"INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"test-uid", cal.ID, "Test Event", "2026-04-01T10:00:00Z", "2026-04-01T11:00:00Z", "CONFIRMED", "OPAQUE", "PUBLIC")
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// Delete calendar should cascade
	if err := svc.Delete(ctx, cal.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE calendar_id = ?", cal.ID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 events after cascade delete, got %d", count)
	}
}

func TestCalendarService_DeleteLastCalendarBlocked(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// The seed migration creates one "Personal" calendar (ID 1).
	// Deleting it should fail because it's the last one.
	err := svc.Delete(ctx, 1)
	if err == nil {
		t.Fatal("expected error when deleting the last calendar, got nil")
	}
	if !errors.Is(err, ErrLastCalendar) {
		t.Errorf("unexpected error: %v", err)
	}

	// Calendar should still exist.
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after blocked delete: %v", err)
	}
	if cal.Name != "Personal" {
		t.Errorf("calendar name = %q, want %q", cal.Name, "Personal")
	}
}

func TestCalendarService_DeleteNonLastCalendarAllowed(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create a second calendar, then delete it.
	c, err := svc.Create(ctx, "Work", "#0284C7", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete non-last calendar: %v", err)
	}

	// Only the original should remain.
	cals, _ := svc.List(ctx)
	if len(cals) != 1 {
		t.Fatalf("expected 1 calendar after delete, got %d", len(cals))
	}
}

func TestCalendarService_SeedCalendarIsDefault(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def.Name != "Personal" || !def.IsDefault {
		t.Errorf("seed default = %+v, want Personal IsDefault=true", def)
	}
}

func TestCalendarService_CreateDoesNotStealDefault(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// A second calendar must NOT take the default away from the seed.
	c, err := svc.Create(ctx, "Work", "#0284C7", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.IsDefault {
		t.Errorf("newly-created calendar should not be default when one already exists")
	}
	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def.Name != "Personal" {
		t.Errorf("default = %q, want %q", def.Name, "Personal")
	}
}

func TestCalendarService_SetDefaultIsExclusive(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.Create(ctx, "Work", "#0284C7", "")
	if err != nil {
		t.Fatalf("Create Work: %v", err)
	}
	if err := svc.SetDefault(ctx, work.ID); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def.ID != work.ID {
		t.Errorf("default ID = %d, want %d", def.ID, work.ID)
	}
	// The old default should no longer carry the flag.
	cals, _ := svc.List(ctx)
	defaults := 0
	for _, c := range cals {
		if c.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("expected exactly 1 default after SetDefault, got %d", defaults)
	}
}

func TestCalendarService_DeleteDefaultRefusesWithoutPromotion(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "Work", "#0284C7", ""); err != nil {
		t.Fatalf("Create Work: %v", err)
	}
	def, _ := svc.GetDefault(ctx)
	err := svc.Delete(ctx, def.ID)
	if !errors.Is(err, ErrDefaultCalendarRequiresPromotion) {
		t.Errorf("Delete default got err=%v, want ErrDefaultCalendarRequiresPromotion", err)
	}
	// Calendar must still exist.
	if _, err := svc.Get(ctx, def.ID); err != nil {
		t.Errorf("default calendar should still exist after refused delete: %v", err)
	}
}

func TestCalendarService_DeleteAndPromote(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.Create(ctx, "Work", "#0284C7", "")
	if err != nil {
		t.Fatalf("Create Work: %v", err)
	}
	def, _ := svc.GetDefault(ctx)
	if err := svc.DeleteAndPromote(ctx, def.ID, work.ID); err != nil {
		t.Fatalf("DeleteAndPromote: %v", err)
	}
	if _, err := svc.Get(ctx, def.ID); err == nil {
		t.Errorf("expected old default to be deleted")
	}
	newDef, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault after promote: %v", err)
	}
	if newDef.ID != work.ID || !newDef.IsDefault {
		t.Errorf("new default = %+v, want id=%d IsDefault=true", newDef, work.ID)
	}
}

func TestCalendarService_DeleteAndPromoteRejectsSelf(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "Work", "#0284C7", ""); err != nil {
		t.Fatalf("Create Work: %v", err)
	}
	def, _ := svc.GetDefault(ctx)
	err := svc.DeleteAndPromote(ctx, def.ID, def.ID)
	if !errors.Is(err, ErrInvalidPromotionTarget) {
		t.Errorf("got err=%v, want ErrInvalidPromotionTarget", err)
	}
}

func TestCalendarService_DeleteAndPromoteRejectsUnknownTarget(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "Work", "#0284C7", ""); err != nil {
		t.Fatalf("Create Work: %v", err)
	}
	def, _ := svc.GetDefault(ctx)
	err := svc.DeleteAndPromote(ctx, def.ID, 9999)
	if !errors.Is(err, ErrInvalidPromotionTarget) {
		t.Errorf("got err=%v, want ErrInvalidPromotionTarget", err)
	}
}

func TestCalendarService_DeleteNonDefaultAllowedWithoutPromotion(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.Create(ctx, "Work", "#0284C7", "")
	if err != nil {
		t.Fatalf("Create Work: %v", err)
	}
	// Personal stays default; deleting Work needs no promotion.
	if err := svc.Delete(ctx, work.ID); err != nil {
		t.Errorf("Delete non-default: %v", err)
	}
	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def.Name != "Personal" {
		t.Errorf("default = %q, want Personal", def.Name)
	}
}

func TestFromStorageIncludesRemoteDiscoveryMetadata(t *testing.T) {
	remoteURL := "/calendars/me/work/"
	row := storage.Calendar{
		ID:               42,
		Name:             "Work",
		Color:            "#123456",
		CreatedAt:        "2026-07-14T00:00:00Z",
		UpdatedAt:        "2026-07-14T00:00:00Z",
		RemoteUrl:        &remoteURL,
		RemoteName:       "Remote Work",
		RemoteAccess:     "read",
		RemoteComponents: "VEVENT,VTODO",
		RemoteMissing:    1,
	}

	got := fromStorage(row)
	if got.RemoteName != "Remote Work" || got.RemoteAccess != "read" ||
		got.RemoteComponents != "VEVENT,VTODO" || !got.RemoteMissing {
		t.Fatalf("remote discovery metadata = %+v", got)
	}
}
