package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestPullSkipsATodoWhoseUIDLivesInAnotherCalendar pulls into calendar B a
// todo and a journal whose UIDs live in calendar A (issue #911). The upsert
// would write the rows of A, so the pull skips each body with a warning.
// The rows stay in A, and the pull does not fail.
func TestPullSkipsATodoWhoseUIDLivesInAnotherCalendar(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	td, err := engine.todos.UpsertByUID(ctx, todo.UpsertParams{UID: "shared-todo", CalendarID: calA, Summary: "In A"})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	jr, err := engine.journals.UpsertByUID(ctx, journal.UpsertParams{UID: "shared-journal", CalendarID: calA, Summary: "In A"})
	if err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	bodies := map[string]string{
		"shared-todo":    "BEGIN:VTODO\r\nUID:shared-todo\r\nDTSTAMP:20261020T000000Z\r\nSUMMARY:From B\r\nEND:VTODO\r\n",
		"shared-journal": "BEGIN:VJOURNAL\r\nUID:shared-journal\r\nDTSTAMP:20261020T000000Z\r\nDTSTART;VALUE=DATE:20261021\r\nSUMMARY:From B\r\nEND:VJOURNAL\r\n",
	}
	for uid, comp := range bodies {
		data, err := parseICalData([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + comp + "END:VCALENDAR\r\n"))
		if err != nil {
			t.Fatalf("parse %s: %v", uid, err)
		}
		path := "/cal/b/" + uid + ".ics"
		gotUID, imported, warnings, err := engine.importFetchedResource(ctx, calB, nil, fetchedResource{path: path, href: path, etag: `"e1"`, data: data})
		if err != nil || imported || gotUID != uid {
			t.Fatalf("pull of %s = %q, %v, %v; want a skip", uid, gotUID, imported, err)
		}
		found := false
		for _, w := range warnings {
			if strings.Contains(w.Message, uid+" lives in calendar") && strings.Contains(w.Message, "the pull keeps the server copy out of this calendar") {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings of %s = %+v; want the skip", uid, warnings)
		}
	}
	if got, err := engine.todos.Get(ctx, td.ID); err != nil || got.CalendarID != calA || got.Summary != "In A" {
		t.Errorf("todo = %+v, %v; want it unchanged in A", got, err)
	}
	if got, err := engine.journals.Get(ctx, jr.ID); err != nil || got.CalendarID != calA || got.Summary != "In A" {
		t.Errorf("journal = %+v, %v; want it unchanged in A", got, err)
	}
}
