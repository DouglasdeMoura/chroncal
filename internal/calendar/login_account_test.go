package calendar

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestConnect_RefusesTheLoginOfAnAccount covers issue #917. A link of a
// local calendar with the server, the user, and the auth type of an account
// that the user added made a second account. The link now refuses and names
// the account.
func TestConnect_RefusesTheLoginOfAnAccount(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "Work server", ServerUrl: "https://cal.example.com/dav/", AuthType: "basic", Username: "alice",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal, err := svc.Create(ctx, "Projects", "#00ff00", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	link := RemoteLink{RemoteURL: "https://CAL.example.com:443/dav/calendars/alice/projects/", Username: " Alice ", AuthType: "basic"}

	_, _, err = svc.ConnectTarget(ctx, cal, link)
	if !errors.Is(err, ErrLoginHasAccount) || !strings.Contains(err.Error(), `"Work server"`) {
		t.Fatalf("ConnectTarget = %v; want ErrLoginHasAccount that names the account", err)
	}
	if err := svc.Connect(ctx, cal, link, auth.Credential{Username: "alice", Password: "x"}, &memCredStore{}); !errors.Is(err, ErrLoginHasAccount) {
		t.Fatalf("Connect = %v; want ErrLoginHasAccount", err)
	}
	accounts, err := svc.q.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %d, %v; want the one account of the user", len(accounts), err)
	}

	for name, other := range map[string]RemoteLink{
		"another user":      {RemoteURL: link.RemoteURL, Username: "bob", AuthType: "basic"},
		"another auth type": {RemoteURL: link.RemoteURL, Username: "alice", AuthType: "bearer"},
		"another server":    {RemoteURL: "https://dav.example.org/dav/calendars/alice/projects/", Username: "alice", AuthType: "basic"},
	} {
		if _, _, err := svc.ConnectTarget(ctx, cal, other); err != nil {
			t.Errorf("%s: ConnectTarget = %v; want nil", name, err)
		}
	}
}

// TestConnect_AllowsTheLoginOfAHiddenAccount keeps the link of a login that
// only the hidden account of another calendar link holds: each calendar
// link keeps its own account.
func TestConnect_AllowsTheLoginOfAHiddenAccount(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	first, err := svc.Create(ctx, "First", "#00ff00", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := svc.Create(ctx, "Second", "#00ff00", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cred := auth.Credential{Username: "alice", Password: "x"}
	if err := svc.Connect(ctx, first, RemoteLink{RemoteURL: "https://cal.example.com/dav/calendars/alice/one/", Username: "alice", AuthType: "basic"}, cred, &memCredStore{}); err != nil {
		t.Fatalf("Connect first: %v", err)
	}
	if err := svc.Connect(ctx, second, RemoteLink{RemoteURL: "https://cal.example.com/dav/calendars/alice/two/", Username: "alice", AuthType: "basic"}, cred, &memCredStore{}); err != nil {
		t.Fatalf("Connect second = %v; want nil", err)
	}
}

// TestConnect_RefusesARelinkThatChangesTheLogin keeps one login on one
// account for a relink too. A calendar link with a hidden account that
// changes its login to the login of an account of the user refuses. A
// relink with the same login, for example a new password, passes. A
// calendar of the account of the user refuses as well: Connect would move
// it to a new hidden account with the same login.
func TestConnect_RefusesARelinkThatChangesTheLogin(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	work, err := svc.q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "Work server", ServerUrl: "https://cal.example.com/", AuthType: "basic", Username: "alice",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cal, err := svc.Create(ctx, "Projects", "#00ff00", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const url = "https://cal.example.com/dav/calendars/bob/projects/"
	if err := svc.Connect(ctx, cal, RemoteLink{RemoteURL: url, Username: "bob", AuthType: "basic"}, auth.Credential{Username: "bob", Password: "x"}, &memCredStore{}); err != nil {
		t.Fatalf("Connect as bob: %v", err)
	}
	if cal, err = svc.Get(ctx, cal.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	asAlice := RemoteLink{RemoteURL: url, Username: "alice", AuthType: "basic"}
	if _, _, err := svc.ConnectTarget(ctx, cal, asAlice); !errors.Is(err, ErrLoginHasAccount) {
		t.Errorf("ConnectTarget of a relink as alice = %v; want ErrLoginHasAccount", err)
	}
	if err := svc.Connect(ctx, cal, asAlice, auth.Credential{Username: "alice", Password: "x"}, &memCredStore{}); !errors.Is(err, ErrLoginHasAccount) {
		t.Errorf("Connect of a relink as alice = %v; want ErrLoginHasAccount", err)
	}
	if err := svc.Connect(ctx, cal, RemoteLink{RemoteURL: url, Username: "bob", AuthType: "basic"}, auth.Credential{Username: "bob", Password: "new"}, &memCredStore{}); err != nil {
		t.Errorf("Connect of a relink as bob = %v; want nil", err)
	}

	member, err := svc.Create(ctx, "Member", "#00ff00", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: member.ID, AccountID: &work.ID, RemoteUrl: storage.StringToNullable("/dav/calendars/alice/member/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if member, err = svc.Get(ctx, member.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, _, err := svc.ConnectTarget(ctx, member, RemoteLink{RemoteURL: "https://cal.example.com/dav/calendars/alice/member/", Username: "alice", AuthType: "basic"}); !errors.Is(err, ErrLoginHasAccount) {
		t.Errorf("ConnectTarget of a calendar of the account = %v; want ErrLoginHasAccount", err)
	}
}
