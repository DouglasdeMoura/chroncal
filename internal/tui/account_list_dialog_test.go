package tui

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

func TestAccountListDialogShowsAccountsAndCalendarCounts(t *testing.T) {
	accounts := []account.Account{
		{ID: 1, DisplayName: "Google", ServerURL: "https://google.example/caldav", AuthType: "oauth2", Username: "me@example.com"},
		{ID: 2, DisplayName: "Work", ServerURL: "https://work.example/dav", AuthType: "basic", Username: "me@work.example"},
	}
	calendars := map[int64]CalendarInfo{
		10: {Name: "Primary", AccountID: 1},
		11: {Name: "Holidays", AccountID: 1, RemoteAccess: "read"},
		12: {Name: "Team", AccountID: 2, RemoteMissing: true},
	}
	m := NewAccountListDialogModel(accounts, calendars, Theme{}).SetSize(140, 50)
	out := m.View()
	for _, want := range []string{"Google", "Work", "2 calendars", "oauth2", "me@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("account dialog missing %q: %q", want, out)
		}
	}
}

func TestAccountListDialogActionsTargetSelectedAccount(t *testing.T) {
	accounts := []account.Account{{ID: 7, DisplayName: "Google"}}
	m := NewAccountListDialogModel(accounts, nil, Theme{})
	actions := m.actions()
	refresh := actions[0].Msg().(AccountRefreshRequestedMsg)
	remove := actions[1].Msg().(AccountRemoveRequestedMsg)
	if refresh.AccountID != 7 || remove.AccountID != 7 || remove.Name != "Google" {
		t.Fatalf("actions targeted wrong account: refresh=%+v remove=%+v", refresh, remove)
	}
	if _, ok := m.shell.titleAction.Msg().(AccountDialogRequestedMsg); !ok {
		t.Fatalf("title action = %T, want AccountDialogRequestedMsg", m.shell.titleAction.Msg())
	}
}
