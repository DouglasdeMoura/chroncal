package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
)

func TestTodosSchemaRejectsDueDateAndDuration(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO todos (uid, calendar_id, summary, due_date, duration, status, priority, sequence)
		 VALUES ('todo-invalid-due-duration', ?, 'Invalid Todo', '2026-04-01', 'PT1H', 'NEEDS-ACTION', 0, 0)`,
		calID,
	)
	if err == nil {
		t.Fatal("expected due_date + duration insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTodosSchemaRejectsDurationWithoutStartDate(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO todos (uid, calendar_id, summary, duration, status, priority, sequence)
		 VALUES ('todo-invalid-duration-no-start', ?, 'Invalid Todo', 'PT1H', 'NEEDS-ACTION', 0, 0)`,
		calID,
	)
	if err == nil {
		t.Fatal("expected duration without start_date insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestXPropertiesRequireExistingOwner(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(
		`INSERT INTO x_properties (owner_type, owner_id, name, value, params)
		 VALUES ('event', 999, 'X-TEST', 'value', '{}')`,
	)
	if err == nil {
		t.Fatal("expected x_properties insert without owner to fail")
	}
}

func TestXPropertiesAreDeletedWithOwners(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	testCases := []struct {
		name      string
		ownerType string
		insertSQL string
		deleteSQL string
	}{
		{
			name:      "event",
			ownerType: "event",
			insertSQL: `INSERT INTO events (uid, calendar_id, title, start_time, end_time, all_day, status, transp, sequence, priority)
			           VALUES ('xprop-event', ?, 'Test', '2026-04-01T00:00:00Z', '2026-04-01T01:00:00Z', 0, 'CONFIRMED', 'OPAQUE', 0, 0)`,
			deleteSQL: `DELETE FROM events WHERE id = ?`,
		},
		{
			name:      "todo",
			ownerType: "todo",
			insertSQL: `INSERT INTO todos (uid, calendar_id, summary, status, priority, sequence)
			           VALUES ('xprop-todo', ?, 'Test', 'NEEDS-ACTION', 0, 0)`,
			deleteSQL: `DELETE FROM todos WHERE id = ?`,
		},
		{
			name:      "journal",
			ownerType: "journal",
			insertSQL: `INSERT INTO journals (uid, calendar_id, summary, status, sequence)
			           VALUES ('xprop-journal', ?, 'Test', 'FINAL', 0)`,
			deleteSQL: `DELETE FROM journals WHERE id = ?`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := db.Exec(tc.insertSQL, calID)
			if err != nil {
				t.Fatalf("insert owner: %v", err)
			}
			ownerID, err := res.LastInsertId()
			if err != nil {
				t.Fatalf("last insert id: %v", err)
			}

			if _, err := db.Exec(
				`INSERT INTO x_properties (owner_type, owner_id, name, value, params)
				 VALUES (?, ?, 'X-TEST', 'value', '{}')`,
				tc.ownerType, ownerID,
			); err != nil {
				t.Fatalf("insert x_property: %v", err)
			}

			if _, err := db.Exec(tc.deleteSQL, ownerID); err != nil {
				t.Fatalf("delete owner: %v", err)
			}

			var count int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM x_properties WHERE owner_type = ? AND owner_id = ?`,
				tc.ownerType, ownerID,
			).Scan(&count); err != nil {
				t.Fatalf("count x_properties: %v", err)
			}
			if count != 0 {
				t.Fatalf("x_properties count = %d, want 0", count)
			}
		})
	}
}

func TestSyncResourcesRejectInvalidOwnerType(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		 VALUES (?, 'sync-invalid-owner', 'note', '', '', 0, 'sync-token')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid owner_type insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncResourcesRejectInvalidDirtyValue(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		 VALUES (?, 'sync-invalid-dirty', 'event', '', '', 2, 'sync-token')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid dirty insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncResourcesRejectInvalidSyncStrategy(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
		 VALUES (?, 'sync-invalid-strategy', 'event', '', '', 0, 'manual')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid sync_strategy insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncConflictsRejectInvalidOwnerType(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	_, err = db.Exec(
		`INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag)
		 VALUES (?, 'note', 1, 'conflict-invalid-owner', 'BEGIN:VCALENDAR', 'BEGIN:VCALENDAR', 'etag')`,
		calID,
	)
	if err == nil {
		t.Fatal("expected invalid sync_conflicts owner_type insert to fail")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// calendars.name stays UNIQUE after migration 040. Collisions between remote
// collections that share a display name are resolved in code (unique local
// names with the pristine value in remote_name). The database must still
// reject a raw duplicate insert.
func TestCalendarsRejectDuplicateDisplayNames(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO calendars (name, color) VALUES ('Holidays in Brazil', '#7C3AED')`,
	); err != nil {
		t.Fatalf("insert first display name: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO calendars (name, color) VALUES ('Holidays in Brazil', '#7C3AED')`,
	); err == nil {
		t.Fatal("duplicate display name should be rejected by the UNIQUE constraint")
	}
}

func TestCalendarsScopeRemoteIdentityToAccount(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	for _, name := range []string{"Personal", "Family"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO accounts (name, server_url) VALUES (?, 'https://cal.example.test/')`,
			name,
		); err != nil {
			t.Fatalf("insert account %q: %v", name, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO calendars (name, account_id, remote_url)
		 VALUES ('Work', 1, '/calendars/work/')`,
	); err != nil {
		t.Fatalf("insert first remote calendar: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO calendars (name, account_id, remote_url)
		 VALUES ('Work (2)', 2, '/calendars/work/')`,
	); err != nil {
		t.Fatalf("same remote URL on another account should be allowed: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO calendars (name, account_id, remote_url)
		 VALUES ('Duplicate', 1, '/calendars/work/')`,
	); err == nil {
		t.Fatal("duplicate remote URL on one account should fail")
	}
}

func TestCalendarsRejectInvalidRemoteMetadata(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO calendars (name, remote_access) VALUES ('Bad access', 'admin')`,
	); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("invalid remote access error = %v, want CHECK constraint failure", err)
	}
	if _, err := db.Exec(
		`INSERT INTO calendars (name, remote_missing) VALUES ('Bad missing flag', 2)`,
	); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("invalid remote missing error = %v, want CHECK constraint failure", err)
	}
}

// Every value a model predicate accepts must also pass the alarm CHECK
// constraints. A value that passes the Go side but fails a constraint
// rolls back the whole resource transaction during sync (issue #575).
// This test probes both alarm tables with candidate values.
//
// The related column holds the two rules to the same verdict. The action
// column checks one direction only, because the two rules differ on
// purpose. The constraint rejects an empty action alone (issue #579 keeps
// a sync-only action such as NONE or X-APPLE-SOUND). The Go rule also
// rejects a whitespace-only action, which the constraint stores but
// export cannot represent (issue #595). Go therefore accepts a subset,
// which is the safe direction.
func TestAlarmConstraintsMatchModelValidators(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID

	res, err := db.Exec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time, all_day, status, transp, sequence, priority)
		 VALUES ('lockstep-event', ?, 'Test', '2026-04-01T00:00:00Z', '2026-04-01T01:00:00Z', 0, 'CONFIRMED', 'OPAQUE', 0, 0)`, calID)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	eventID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO todos (uid, calendar_id, summary, status, priority, sequence)
		 VALUES ('lockstep-todo', ?, 'Test', 'NEEDS-ACTION', 0, 0)`, calID)
	if err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	todoID, _ := res.LastInsertId()

	inserts := []struct {
		table string
		fk    string
		id    int64
	}{
		{"event_alarms", "event_id", eventID},
		{"todo_alarms", "todo_id", todoID},
	}
	checks := []struct {
		col    string
		values []string
		valid  func(string) bool
		// exact holds the constraint to the same verdict as the model
		// predicate. A column where the Go rule is stricter on purpose
		// sets it to false, and the test then checks only that every
		// value the Go rule accepts also reaches the table.
		exact bool
	}{
		// The valid candidates come from the model sets. The test then
		// probes a value added to the model but not to a migration
		// against the CHECK constraint, and the value fails here.
		{"action", append(model.AlarmActions(), "NONE", "PROCEDURE", "X-APPLE-SOUND", "display", " ", "\t", "NO NE", ""), model.StorableAlarmAction, false},
		{"related", append(model.AlarmRelatedValues(), "STARTS", "end", ""), model.ValidAlarmRelated, true},
	}

	for _, ins := range inserts {
		for _, c := range checks {
			for _, v := range c.values {
				_, err := db.Exec(
					`INSERT INTO `+ins.table+` (`+ins.fk+`, `+c.col+`, trigger_value) VALUES (?, ?, '-PT15M')`,
					ins.id, v,
				)
				if c.valid(v) && err != nil {
					t.Errorf("%s %s %q: the model predicate accepts it, but the insert failed: %v",
						ins.table, c.col, v, err)
				}
				if c.exact && !c.valid(v) && err == nil {
					t.Errorf("%s %s %q: the model predicate rejects it, but the insert passed",
						ins.table, c.col, v)
				}
				if err != nil && !strings.Contains(err.Error(), "CHECK constraint failed") {
					t.Errorf("%s %s %q: rejected by %v, not by the CHECK constraint", ins.table, c.col, v, err)
				}
			}
		}
	}
}

// The fireable-filtered queries and model.FireableAlarmAction must agree.
// The WHERE lists in the query files are copies of the Go set with only a
// comment as the tie. This test probes both with the same candidates, in
// the same way TestAlarmConstraintsMatchModelValidators pins the CHECK
// constraints.
func TestFireableAlarmQueriesMatchModelPredicate(t *testing.T) {
	db, q, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	calID := cals[0].ID
	res, err := db.Exec(`INSERT INTO events (uid, calendar_id, title, start_time, end_time, all_day, status, transp, sequence, priority)
		 VALUES ('fireable-q-event', ?, 'Test', '2026-04-01T00:00:00Z', '2026-04-01T01:00:00Z', 0, 'CONFIRMED', 'OPAQUE', 0, 0)`, calID)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	eventID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO todos (uid, calendar_id, summary, status, priority, sequence)
		 VALUES ('fireable-q-todo', ?, 'Test', 'NEEDS-ACTION', 0, 0)`, calID)
	if err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	todoID, _ := res.LastInsertId()

	// Derive the fireable candidates from the model set itself, so a value
	// added there gets a probe row without an edit here. A hardcoded list
	// would pass without covering the new value, and the drift it must
	// catch is exactly "the Go set grew, the SQL lists did not".
	// The non-fireable probes stay fixed: they cover the preserved actions
	// (issue #579) and a lowercase near-miss.
	candidates := map[string]string{}
	for i, action := range model.AlarmActions() {
		candidates[action] = fmt.Sprintf("-PT%dM", i+1)
	}
	for i, action := range []string{"NONE", "X-APPLE-SOUND", "display"} {
		candidates[action] = fmt.Sprintf("-PT%dH", i+1)
	}
	for action, trigger := range candidates {
		if _, err := db.Exec(
			`INSERT INTO event_alarms (event_id, action, trigger_value) VALUES (?, ?, ?)`,
			eventID, action, trigger); err != nil {
			t.Fatalf("insert event alarm %q: %v", action, err)
		}
		if _, err := db.Exec(
			`INSERT INTO todo_alarms (todo_id, action, trigger_value) VALUES (?, ?, ?)`,
			todoID, action, trigger); err != nil {
			t.Fatalf("insert todo alarm %q: %v", action, err)
		}
	}
	wantTriggers := map[string]bool{}
	for action, trigger := range candidates {
		if model.FireableAlarmAction(action) {
			wantTriggers[trigger] = true
		}
	}

	check := func(name string, got []string) {
		t.Helper()
		if len(got) != len(wantTriggers) {
			t.Errorf("%s returned %d triggers %v, want %d", name, len(got), got, len(wantTriggers))
			return
		}
		for _, trig := range got {
			if !wantTriggers[trig] {
				t.Errorf("%s returned trigger %q of a non-fireable action", name, trig)
			}
		}
	}

	evTrigs, err := q.ListDistinctAlarmTriggers(ctx)
	if err != nil {
		t.Fatalf("ListDistinctAlarmTriggers: %v", err)
	}
	check("ListDistinctAlarmTriggers", evTrigs)

	tdTrigs, err := q.ListDistinctTodoAlarmTriggers(ctx)
	if err != nil {
		t.Fatalf("ListDistinctTodoAlarmTriggers: %v", err)
	}
	check("ListDistinctTodoAlarmTriggers", tdTrigs)

	evRows, err := q.ListFireableAlarmsByEventIDs(ctx, []int64{eventID})
	if err != nil {
		t.Fatalf("ListFireableAlarmsByEventIDs: %v", err)
	}
	evGot := make([]string, len(evRows))
	for i, r := range evRows {
		if !model.FireableAlarmAction(r.Action) {
			t.Errorf("ListFireableAlarmsByEventIDs returned non-fireable action %q", r.Action)
		}
		evGot[i] = r.TriggerValue
	}
	check("ListFireableAlarmsByEventIDs", evGot)

	tdRows, err := q.ListFireableTodoAlarmsByTodoID(ctx, todoID)
	if err != nil {
		t.Fatalf("ListFireableTodoAlarmsByTodoID: %v", err)
	}
	tdGot := make([]string, len(tdRows))
	for i, r := range tdRows {
		if !model.FireableAlarmAction(r.Action) {
			t.Errorf("ListFireableTodoAlarmsByTodoID returned non-fireable action %q", r.Action)
		}
		tdGot[i] = r.TriggerValue
	}
	check("ListFireableTodoAlarmsByTodoID", tdGot)

	// The state queries filter on the same action list. Give every alarm a
	// fired state row, then check that only the fireable actions surface.
	fired := "2026-04-01T00:00:00Z"
	if _, err := db.Exec(
		`INSERT INTO alarm_state (alarm_id, event_id, trigger_at, fired_at)
		 SELECT id, ?, trigger_value, ? FROM event_alarms WHERE event_id = ?`,
		eventID, fired, eventID); err != nil {
		t.Fatalf("insert alarm states: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO todo_alarm_state (alarm_id, todo_id, trigger_at, fired_at)
		 SELECT id, ?, trigger_value, ? FROM todo_alarms WHERE todo_id = ?`,
		todoID, fired, todoID); err != nil {
		t.Fatalf("insert todo alarm states: %v", err)
	}

	evStates, err := q.ListPendingAlarmStates(ctx)
	if err != nil {
		t.Fatalf("ListPendingAlarmStates: %v", err)
	}
	evStateTrigs := make([]string, len(evStates))
	for i, s := range evStates {
		evStateTrigs[i] = s.TriggerAt
	}
	check("ListPendingAlarmStates", evStateTrigs)

	tdStates, err := q.ListPendingTodoAlarmStates(ctx)
	if err != nil {
		t.Fatalf("ListPendingTodoAlarmStates: %v", err)
	}
	tdStateTrigs := make([]string, len(tdStates))
	for i, s := range tdStates {
		tdStateTrigs[i] = s.TriggerAt
	}
	check("ListPendingTodoAlarmStates", tdStateTrigs)
}
