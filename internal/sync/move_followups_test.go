package sync

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// shortSettleRetries keeps the waits of putAfterSettle short in a test.
var shortSettleRetries = []time.Duration{time.Millisecond, time.Millisecond}

// TestSettleRetriesALateCreate moves an event on iCloud. The server frees
// the UID of the deleted source a short time after the DELETE, so the
// first PUT after the settle gets 412 again. The push must send the PUT
// once more after a wait, so the object lands in B in the same pass
// (issue #863).
func TestSettleRetriesALateCreate(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	putsBefore, _ := srv.counts()
	engine.testHooks = &engineTestHooks{
		settleRetryDelays: shortSettleRetries,
		afterTombstoneDelete: func() {
			srv.setRefusePuts(1)
		},
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 || res.Conflicts != 0 {
		t.Fatalf("PushLocalEdits = %+v, %v; want one clean push", res, err)
	}
	// The refused create, the refused PUT after the settle, and the PUT
	// after the wait.
	if puts, _ := srv.counts(); puts != putsBefore+3 {
		t.Fatalf("puts = %d, want %d", puts, putsBefore+3)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 0 || sr.RemoteUrl == "" || sr.PushFailCount != 0 {
		t.Fatalf("sync resource B = %+v, %v; want it clean with a remote href", sr, err)
	}
}

// TestSettleRetriesStopAfterTheLastWait moves an event on iCloud, and the
// server refuses every create after the settle. The push sends one PUT per
// wait and then reports the refusal. The row of B stays dirty for the next
// pass (issue #863).
func TestSettleRetriesStopAfterTheLastWait(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	putsBefore, _ := srv.counts()
	engine.testHooks = &engineTestHooks{
		settleRetryDelays: shortSettleRetries,
		afterTombstoneDelete: func() {
			srv.setRefusePuts(100)
		},
	}
	res, err := engine.PushLocalEdits(ctx, calB)
	engine.testHooks = nil
	if err != nil || res.Pushed != 0 || len(res.Errors) == 0 {
		t.Fatalf("PushLocalEdits = %+v, %v; want a reported refusal", res, err)
	}
	want := putsBefore + 2 + len(shortSettleRetries)
	if puts, _ := srv.counts(); puts != want {
		t.Fatalf("puts = %d, want %d", puts, want)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 || sr.RemoteUrl != "" {
		t.Fatalf("sync resource B = %+v, %v; want it dirty with no href", sr, err)
	}
}

// TestCreateBlockOfALocalTarget checks each branch of the rule of
// targetCanCreate. A local or unlinked target must give createLocal and
// never a pending create, also with no sync row of the copy (issue #863).
func TestCreateBlockOfALocalTarget(t *testing.T) {
	t.Parallel()
	accountID, noAccount := int64(1), int64(0)
	linked := storage.Calendar{AccountID: &accountID, RemoteUrl: storage.StringToNullable("/cal/b/")}
	readOnly := linked
	readOnly.RemoteAccess = "read"
	landed := &storage.SyncResource{RemoteUrl: "/cal/b/x.ics"}
	landedEdit := &storage.SyncResource{RemoteUrl: "/cal/b/x.ics", Dirty: 1}
	unpushed := &storage.SyncResource{Dirty: 1}
	tests := []struct {
		name   string
		cal    storage.Calendar
		target *storage.SyncResource
		gated  bool
		want   createBlock
	}{
		{"local without a row", storage.Calendar{}, nil, false, createLocal},
		{"local with a row", storage.Calendar{}, unpushed, false, createLocal},
		{"account ID 0", storage.Calendar{AccountID: &noAccount}, nil, false, createLocal},
		{"linked and landed", linked, landed, false, createLanded},
		{"read-only and landed", readOnly, landed, false, createLanded},
		{"read-only with an unsent edit", readOnly, landedEdit, false, createReadOnlyEdit},
		{"read-only and unpushed", readOnly, unpushed, false, createReadOnly},
		{"linked and gated", linked, unpushed, true, createGated},
		{"linked and pending", linked, unpushed, false, createPending},
		{"linked without a row", linked, nil, false, createPending},
	}
	for _, tt := range tests {
		if got := createBlockOf(tt.cal, tt.target, tt.gated); got != tt.want {
			t.Errorf("%s: createBlockOf = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// TestGoneSourceOfAMoveToALocalCalendarKeepsTheItem moves an event from A
// to a local calendar. A remote edit of the source records a move
// conflict, and then the source object leaves the server. The pick must
// finish the move with the note of a local target: the item stays in the
// local calendar, and no create is pending (issue #863).
func TestGoneSourceOfAMoveToALocalCalendarKeepsTheItem(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, _ := newICloudPairEngine(t)
	ctx := context.Background()
	svc := newICloudPairService(engine, q)
	local, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, local.ID)
	srv.editRemoteSummary("/cal/a/"+evt.UID+".ics", "Dentist (remote)")
	if res, err := engine.SyncCalendar(ctx, calA, ConflictPrompt); err != nil || res.Conflicts != 1 {
		t.Fatalf("pass of A = %+v, %v; want one move conflict", res, err)
	}
	c := wantMoveConflict(t, q, calA, local.ID, evt.UID)
	srv.mu.Lock()
	delete(srv.objects, "/cal/a/"+evt.UID+".ics")
	srv.mu.Unlock()

	out, err := svc.ResolveConflictOutcome(ctx, c.ID, "server")
	want := `the source is gone, so the move finished; the item stays in calendar "local"`
	if err != nil || !out.MoveFinished || out.Note != want {
		t.Fatalf("server pick with the source gone = %+v, %v; want the note %q", out, err, want)
	}
	got, err := engine.events.Get(ctx, evt.ID)
	if err != nil || got.CalendarID != local.ID {
		t.Fatalf("event = %+v, %v; want it in the local calendar", got, err)
	}
}

// TestPlannedHrefIsNormal checks that plannedHrefWith normalizes the href
// of the canonicalizer. The planned href and the href of the push must
// compare equal, also when the server gives a href with a dot segment or
// a double slash (issue #863).
func TestPlannedHrefIsNormal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		canon string
		want  string
	}{
		{"https://dav.example.com/cal/b/./x.ics", "https://dav.example.com/cal/b/x.ics"},
		{"https://dav.example.com/cal//b/x.ics", "https://dav.example.com/cal/b/x.ics"},
		{"https://dav.example.com/cal/a/../b/x.ics", "https://dav.example.com/cal/b/x.ics"},
		{"https://dav.example.com/cal/b/x.ics", "https://dav.example.com/cal/b/x.ics"},
	}
	for _, tt := range tests {
		got, err := plannedHrefWith(func(string, string) (string, error) { return tt.canon, nil }, "/cal/b/", "x")
		if err != nil || got != tt.want {
			t.Errorf("plannedHrefWith(%q) = %q, %v; want %q", tt.canon, got, err, tt.want)
		}
	}
}

// createGatedEvent creates one unpushed event on calID that the user does
// not organize, so the organizer gate holds it back.
func createGatedEvent(t *testing.T, engine *Engine, calID int64) event.Event {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evt, err := engine.events.Create(ctx, event.CreateParams{
		CalendarID: calID,
		Title:      "Board meeting",
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := engine.events.ReplaceAttendeesForSync(ctx, evt.ID, []model.Attendee{
		{Email: "mailto:boss@example.com", Organizer: true, Role: "CHAIR"},
	}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync: %v", err)
	}
	return evt
}

// TestGatedRowIgnoresAMoveIntoAThirdCalendar holds a gated row of a UID in
// B. A tombstone of the UID in A names a third calendar C as the target.
// That move goes into C, so the gate of B must treat the row as a plain
// gated row: no move conflict, no push failure, and the tombstone of A
// keeps its target (issue #863).
func TestGatedRowIgnoresAMoveIntoAThirdCalendar(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()
	calC := linkThirdCalendar(t, engine, q, calA, "c")

	evt := createGatedEvent(t, engine, calB)
	if _, err := engine.db.ExecContext(ctx, `INSERT INTO tombstones (calendar_id, uid, remote_url, moved_to_calendar_id) VALUES (?, ?, ?, ?)`,
		calA, evt.UID, "/cal/a/"+evt.UID+".ics", calC); err != nil {
		t.Fatalf("insert the tombstone of A: %v", err)
	}

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || len(res.Errors) != 0 || res.Conflicts != 0 || res.Pushed != 0 {
		t.Fatalf("push of B = %+v, %v; want a plain gated skip", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 0 || sr.PushFailCount != 0 {
		t.Fatalf("sync resource B = %+v, %v; want it clean with no push failure", sr, err)
	}
	ts, err := q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calA, Uid: evt.UID})
	if err != nil || ts.MovedToCalendarID == nil || *ts.MovedToCalendarID != calC {
		t.Fatalf("tombstone of A = %+v, %v; want it to name C", ts, err)
	}
}

// TestGatedRowWaitsForAMoveRowWithoutATombstone holds a gated row of a UID
// in B with a move conflict in the local-picked state, and no tombstone
// names B. The early return of the gate must still see the move: the row
// stays dirty with a push failure, so the move stays visible (issue #863).
func TestGatedRowWaitsForAMoveRowWithoutATombstone(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createGatedEvent(t, engine, calB)
	if _, err := engine.db.ExecContext(ctx, `INSERT INTO sync_conflicts
		(calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag, resolved_at, resolution, move_source_calendar_id, move_state)
		VALUES (?, 'event', ?, ?, '', '', '', '2026-04-01T00:00:00Z', 'local', ?, 'local-picked')`,
		calB, evt.ID, evt.UID, calA); err != nil {
		t.Fatalf("insert the move conflict: %v", err)
	}

	res, err := engine.PushLocalEdits(ctx, calB)
	if err != nil || res.Pushed != 0 || res.SkippedConflicts != 1 {
		t.Fatalf("push of B = %+v, %v; want the row skipped", res, err)
	}
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calB, Uid: evt.UID})
	if err != nil || sr.Dirty != 1 || sr.PushFailCount != 1 {
		t.Fatalf("sync resource B = %+v, %v; want it dirty with one push failure", sr, err)
	}
}

// TestSettleUsesTheFreshTombstone bumps the version of the source
// tombstone after the push listed the move sources and before the settle
// checks the source. The settle must send the DELETE and clear the
// tombstone with the version it read in its check, so the create lands in
// the same pass (issue #863).
func TestSettleUsesTheFreshTombstone(t *testing.T) {
	t.Parallel()
	engine, q, srv, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evt := createPushedEvent(t, engine, calA)
	moveEvent(t, engine, evt, calB)
	engine.testHooks = &engineTestHooks{beforeSettleSource: func() {
		if _, err := engine.db.ExecContext(ctx, `UPDATE tombstones SET version = version + 1 WHERE calendar_id = ? AND uid = ?`, calA, evt.UID); err != nil {
			t.Errorf("bump the tombstone: %v", err)
		}
	}}
	res, err := engine.PushLocalEdits(ctx, calB)
	engine.testHooks = nil
	if err != nil || len(res.Errors) != 0 || res.Pushed != 1 {
		t.Fatalf("PushLocalEdits = %+v, %v; want one clean push", res, err)
	}
	wantOnlyServerObject(t, srv, "/cal/b/"+evt.UID+".ics")
	if ts, err := q.ListTombstonesByCalendar(ctx, calA); err != nil || len(ts) != 0 {
		t.Fatalf("tombstones of A = %v, %v; want none", ts, err)
	}
}

// TestOrganizerGateIsScopedToTheCalendar puts one UID in A and in B. The
// boss organizes the copy in A, and the copy in B names no organizer. The
// gate reads the copy of the calendar it checks, so it holds back only the
// copy in A. The push gate and targetPushGatedFor share the rule, so both
// give the same answer (issue #863).
func TestOrganizerGateIsScopedToTheCalendar(t *testing.T) {
	t.Parallel()
	engine, q, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	evtA := createGatedEvent(t, engine, calA)
	start := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	evtB, err := engine.events.Create(ctx, event.CreateParams{CalendarID: calB, Title: "Board meeting", StartTime: start, EndTime: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if _, err := engine.db.ExecContext(ctx, `UPDATE events SET uid = ? WHERE id = ?`, evtA.UID, evtB.ID); err != nil {
		t.Fatalf("share the UID: %v", err)
	}

	for _, tc := range []struct {
		calID int64
		want  bool
	}{{calA, true}, {calB, false}} {
		cal, err := q.GetCalendar(ctx, tc.calID)
		if err != nil {
			t.Fatalf("GetCalendar: %v", err)
		}
		account, err := q.GetAccount(ctx, *cal.AccountID)
		if err != nil {
			t.Fatalf("GetAccount: %v", err)
		}
		push, err := gatedByOrganizerOn(ctx, q, tc.calID, resolvePushIdentity(cal, account), ownerTypeEvent, evtA.UID)
		if err != nil || push != tc.want {
			t.Errorf("push gate of calendar %d = %v, %v; want %v", tc.calID, push, err, tc.want)
		}
		target, err := targetPushGatedFor(ctx, q, cal, ownerTypeEvent, evtA.UID)
		if err != nil || target != tc.want {
			t.Errorf("targetPushGatedFor of calendar %d = %v, %v; want %v", tc.calID, target, err, tc.want)
		}
	}
	// A todo and a local calendar are never gated.
	calA0, err := q.GetCalendar(ctx, calA)
	if err != nil {
		t.Fatalf("GetCalendar A: %v", err)
	}
	if gated, err := targetPushGatedFor(ctx, q, calA0, "todo", evtA.UID); err != nil || gated {
		t.Errorf("targetPushGatedFor of a todo = %v, %v; want false", gated, err)
	}
	calA0.AccountID = nil
	if gated, err := targetPushGatedFor(ctx, q, calA0, ownerTypeEvent, evtA.UID); err != nil || gated {
		t.Errorf("targetPushGatedFor of a local calendar = %v, %v; want false", gated, err)
	}
}

// TestResetOfTheSourceOnAGenericServerLeavesOneCopy moves an event from A to
// B on a server that keeps a UID unique per collection only, and resets A.
// The reset must keep the move tombstone, so the source DELETE still goes
// out and no pull brings the source object back. Each order of the passes
// must end with one object of the UID, in B (issue #866).
func TestResetOfTheSourceOnAGenericServerLeavesOneCopy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		landed bool
		order  string
	}{
		{"unpushed copy, A first", false, "ABA"},
		{"unpushed copy, B first", false, "BAB"},
		{"landed copy, A first", true, "ABA"},
		{"landed copy, B first", true, "BAB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine, q, srv, calA, calB := newICloudPairEngine(t)
			ctx := context.Background()
			svc := newICloudPairService(engine, q)
			evt := createPushedEvent(t, engine, calA)
			srv.setGeneric(true)
			moveEvent(t, engine, evt, calB)
			if tc.landed {
				if res, err := engine.PushLocalEdits(ctx, calB); err != nil || res.Pushed != 1 {
					t.Fatalf("push of B = %+v, %v; want the copy landed", res, err)
				}
			}
			if err := svc.ResetCalendar(ctx, calA); err != nil {
				t.Fatalf("ResetCalendar A: %v", err)
			}
			for _, c := range tc.order {
				cal := calA
				if c == 'B' {
					cal = calB
				}
				if _, err := engine.SyncCalendar(ctx, cal, ConflictPrompt); err != nil {
					t.Fatalf("pass of %c: %v", c, err)
				}
			}
			if srv.has("/cal/a/"+evt.UID+".ics") || !srv.has("/cal/b/"+evt.UID+".ics") {
				t.Fatalf("server objects: a=%v b=%v; want one object in B", srv.has("/cal/a/"+evt.UID+".ics"), srv.has("/cal/b/"+evt.UID+".ics"))
			}
			wantLive(t, engine, evt.ID, calB, evt.Title)
		})
	}
}
