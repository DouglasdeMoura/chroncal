package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// TestHealSteps_DeclaredOrder pins the declared heal list. The order is
// load-bearing, so a new step must land here on purpose, and a silent
// reorder must fail this test.
func TestHealSteps_DeclaredOrder(t *testing.T) {
	want := []string{
		"backfill alarm uids",
		"purge libical diagnostic x-props",
		"normalize alarm repeat pairs",
	}
	if len(healSteps) != len(want) {
		t.Fatalf("healSteps has %d steps, want %d (%v); add the step here and to want",
			len(healSteps), len(want), want)
	}
	for i, step := range healSteps {
		if step.name != want[i] {
			t.Errorf("healSteps[%d] = %q, want %q", i, step.name, want[i])
		}
	}
}

// TestHealSteps_Idempotent runs each declared heal step twice against the
// same database that holds damaged rows. The second run must succeed and
// must not change any row. Every heal runs on each startup, so a step that
// keeps rewriting healthy rows is a defect.
func TestHealSteps_Idempotent(t *testing.T) {
	for _, step := range healSteps {
		t.Run(step.name, func(t *testing.T) {
			db, q, err := Open(":memory:")
			if err != nil {
				t.Fatalf("Open error: %v", err)
			}
			defer db.Close()
			seedDamagedRows(t, db)

			ctx := context.Background()
			if err := step.run(ctx, db, q); err != nil {
				t.Fatalf("first run: %v", err)
			}
			before := dumpHealTables(t, db)
			if err := step.run(ctx, db, q); err != nil {
				t.Fatalf("second run: %v", err)
			}
			if after := dumpHealTables(t, db); after != before {
				t.Errorf("second run changed rows:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestHeal_ErrorNamesFailingStep checks that a heal failure reaches Open
// with the step name, as the inline calls in the old Open did.
func TestHeal_ErrorNamesFailingStep(t *testing.T) {
	db, _, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	db.Close()

	err = heal(db, New(db))
	if err == nil {
		t.Fatal("heal on a closed database returned no error")
	}
	if !strings.HasPrefix(err.Error(), "backfill alarm uids:") {
		t.Errorf("error %q does not name the failing step", err)
	}
}

// seedDamagedRows writes one row per heal concern, as an upgrade from an
// older build leaves them: an alarm without a UID, a libical diagnostic
// x-property, and an alarm with a REPEAT/DURATION pair the parsers reject
// today.
func seedDamagedRows(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	res, err := db.ExecContext(ctx,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, all_day, status, transp, sequence, priority)
		 VALUES ('heal-seed-event', 1, 'Heal seed', '2026-04-01T00:00:00Z', '2026-04-01T01:00:00Z', 0, 'CONFIRMED', 'OPAQUE', 0, 0)`)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("event LastInsertId: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO event_alarms (event_id, action, trigger_value, repeat, duration, uid)
		 VALUES (?, 'DISPLAY', '-PT15M', 150, 'not-a-duration', NULL)`,
		eventID); err != nil {
		t.Fatalf("insert event alarm: %v", err)
	}

	res, err = db.ExecContext(ctx,
		`INSERT INTO todos (uid, calendar_id, summary) VALUES ('heal-seed-todo', 1, 'heal me')`)
	if err != nil {
		t.Fatalf("insert todo: %v", err)
	}
	todoID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("todo LastInsertId: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO todo_alarms (todo_id, action, trigger_value, repeat, duration, uid)
		 VALUES (?, 'DISPLAY', '-PT30M', 150, 'not-a-duration', NULL)`,
		todoID); err != nil {
		t.Fatalf("insert todo alarm: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO x_properties (owner_type, owner_id, name, value)
		 VALUES ('event', ?, 'X-LIC-ERROR', 'parse error')`,
		eventID); err != nil {
		t.Fatalf("insert x-property: %v", err)
	}
}

// dumpHealTables renders every row of the tables the heals touch, so the
// idempotence test can diff two runs byte for byte.
func dumpHealTables(t *testing.T, db *sql.DB) string {
	t.Helper()
	tables := []string{"events", "event_alarms", "todos", "todo_alarms", "x_properties"}
	var b strings.Builder
	for _, table := range tables {
		fmt.Fprintf(&b, "%s:\n", table)
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("columns %s: %v", table, err)
		}
		for rows.Next() {
			values := make([]sql.RawBytes, len(cols))
			scan := make([]any, len(cols))
			for i := range values {
				scan[i] = &values[i]
			}
			if err := rows.Scan(scan...); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", table, err)
			}
			parts := make([]string, len(cols))
			for i, v := range values {
				parts[i] = cols[i] + "=" + string(v)
			}
			fmt.Fprintln(&b, strings.Join(parts, " "))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("dump %s: %v", table, err)
		}
		rows.Close()
	}
	return b.String()
}
