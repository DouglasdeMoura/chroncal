package tui

import (
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/calendar"
)

func TestSortedCalendarListItemsGroupsLocalThenAccounts(t *testing.T) {
	calendars := map[int64]CalendarInfo{
		1: {Name: "Local", DisplayOrder: 9},
		2: {Name: "Holidays in Brazil", AccountID: 7, AccountName: "Zulu", AccountOrder: 0, DisplayOrder: 0, RemoteAccess: "read"},
		3: {Name: "Holidays in Brazil", AccountID: 5, AccountName: "Alpha", AccountOrder: 1, DisplayOrder: 4, RemoteMissing: true},
		4: {Name: "Primary", AccountID: 5, AccountName: "Alpha", AccountOrder: 1, DisplayOrder: 1},
	}

	items := sortedCalendarListItems(calendars)
	wantIDs := []int64{1, 2, 4, 3}
	if len(items) != len(wantIDs) {
		t.Fatalf("item count = %d, want %d", len(items), len(wantIDs))
	}
	for i, want := range wantIDs {
		if items[i].ID != want {
			t.Fatalf("item %d ID = %d, want %d; items = %+v", i, items[i].ID, want, items)
		}
	}
	if items[0].AccountName != "Local" || items[1].AccountName != "Zulu" || items[2].AccountName != "Alpha" {
		t.Fatalf("account names = %+v", items)
	}
	if !items[3].Missing || items[1].Access != "read" {
		t.Fatalf("remote metadata missing from rows: %+v", items)
	}
}

// TestBuildCalendarInfoMapCachesNormalizedAccountAuthType proves the
// calendar-loading path caches each linked account's normalized auth type on
// CalendarInfo. An OAuth account stored with non-normalized casing and
// surrounding whitespace must reach AccountAuthType as the canonical "oauth2",
// and local calendars (no account) must stay empty. The cached value is what
// later ownership checks and re-auth routing read.
func TestBuildCalendarInfoMapCachesNormalizedAccountAuthType(t *testing.T) {
	cals := []calendar.Calendar{
		{ID: 1, Name: "On device"},
		{ID: 2, Name: "Personal", AccountID: 7},
	}
	accounts := []account.Account{
		{ID: 7, DisplayName: "Google", AuthType: " OAuth2 "},
	}

	info := buildCalendarInfoMap(cals, accounts, func(int64) (int64, error) { return 0, nil })

	if info[1].AccountAuthType != "" {
		t.Errorf("local calendar AccountAuthType = %q, want empty", info[1].AccountAuthType)
	}
	if got := info[2].AccountAuthType; got != "oauth2" {
		t.Errorf("linked calendar AccountAuthType = %q, want oauth2", got)
	}
	// The normalized value is the only auth metadata cached on the row; it
	// must not leak onto local calendars via the shared map lookup.
	if info[2].AccountID != 7 {
		t.Errorf("linked calendar AccountID = %d, want 7", info[2].AccountID)
	}
}
