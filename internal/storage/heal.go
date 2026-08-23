package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// healStep is one startup repair pass. Every step is idempotent: a step
// that runs twice changes nothing on the second run. name identifies the
// step in error messages and in tests.
type healStep struct {
	name string
	run  func(ctx context.Context, conn *sql.DB, q *Queries) error
}

// healSteps declares every startup heal, in run order. The order is
// load-bearing. Keep this slice the single source of the order, so a new
// heal shows up here and not as an extra call hidden in a function body.
var healSteps = []healStep{
	{
		name: "backfill alarm uids",
		run: func(ctx context.Context, conn *sql.DB, q *Queries) error {
			return backfillAlarmUIDs(conn, q)
		},
	},
	{
		name: "purge libical diagnostic x-props",
		run: func(ctx context.Context, conn *sql.DB, q *Queries) error {
			return purgeLibicalDiagnosticXProps(conn)
		},
	},
	{
		name: "normalize alarm repeat pairs",
		run: func(ctx context.Context, conn *sql.DB, q *Queries) error {
			return normalizeAlarmRepeatPairs(conn)
		},
	},
}

// heal is the heal phase of Open. It runs every declared heal step, in
// order. A failure in any step is fatal to Open, and the returned error
// carries the step name.
func heal(conn *sql.DB, q *Queries) error {
	ctx := context.Background()
	for _, step := range healSteps {
		if err := step.run(ctx, conn, q); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}
