package tui

import "testing"

func TestSortedCalendarListItemsGroupsLocalThenAccounts(t *testing.T) {
	calendars := map[int64]CalendarInfo{
		1: {Name: "Local", DisplayOrder: 9},
		2: {Name: "Holidays in Brazil", AccountID: 7, AccountName: "Zulu", DisplayOrder: 0, RemoteAccess: "read"},
		3: {Name: "Holidays in Brazil", AccountID: 5, AccountName: "Alpha", DisplayOrder: 4, RemoteMissing: true},
		4: {Name: "Primary", AccountID: 5, AccountName: "Alpha", DisplayOrder: 1},
	}

	items := sortedCalendarListItems(calendars)
	wantIDs := []int64{1, 4, 3, 2}
	if len(items) != len(wantIDs) {
		t.Fatalf("item count = %d, want %d", len(items), len(wantIDs))
	}
	for i, want := range wantIDs {
		if items[i].ID != want {
			t.Fatalf("item %d ID = %d, want %d; items = %+v", i, items[i].ID, want, items)
		}
	}
	if items[0].AccountName != "Local" || items[1].AccountName != "Alpha" || items[3].AccountName != "Zulu" {
		t.Fatalf("account names = %+v", items)
	}
	if items[2].Missing != true || items[3].Access != "read" {
		t.Fatalf("remote metadata missing from rows: %+v", items)
	}
}
