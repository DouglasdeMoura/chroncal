package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// runSyncDoctorInProcess executes `sync doctor` against the database that
// CHRONCAL_DB points at. The confirm helper refuses a non-interactive shell
// without --yes, which is exactly the behavior under test.
func runSyncDoctorInProcess(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	prevFmt, prevPlaintext := outputFmt, allowPlaintext
	t.Cleanup(func() { outputFmt, allowPlaintext = prevFmt, prevPlaintext })

	root := &cobra.Command{
		Use: "chroncal-test",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			cfg, err = config.Load()
			return err
		},
	}
	root.PersistentFlags().StringVarP(&outputFmt, "output", "o", "text", "output format (text, json)")
	root.PersistentFlags().BoolVar(&allowPlaintext, "allow-plaintext", false, "permit storing credentials in plaintext when no OS keyring is available")
	root.AddCommand(syncCmd())
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

func TestSyncDoctorEmptyReportsNone(t *testing.T) {
	t.Setenv("CHRONCAL_DB", t.TempDir()+"/doctor.db")

	out, _, err := runSyncDoctorInProcess(t, "sync", "doctor")
	if err != nil {
		t.Fatalf("sync doctor: %v", err)
	}
	if !strings.Contains(out, "No wedged resources.") {
		t.Errorf("output %q misses the empty report", out)
	}
}

func TestSyncDoctorPushRefusesNonInteractiveWithoutYes(t *testing.T) {
	dbPath := t.TempDir() + "/doctor.db"
	t.Setenv("CHRONCAL_DB", dbPath)

	// Seed one wedged resource so the push flow reaches its confirmation
	// gate instead of stopping at the not-found check.
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	cals, err := a.Queries.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"wedge-cli@example.com", cals[0].ID, "Wedged",
		"2026-04-03T10:00:00Z", "2026-04-03T11:00:00Z", "CONFIRMED", "OPAQUE", "PUBLIC",
	); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if err := a.Queries.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   cals[0].ID,
		Uid:          "wedge-cli@example.com",
		OwnerType:    "event",
		Etag:         "",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	// Wedge the relations relation. app.New must stay able to open the
	// database afterwards, so the wedge cannot touch a table the startup
	// maintenance queries (event_alarms, x-properties). event_relations is
	// safe: only hydration reads it.
	if _, err := a.DB.ExecContext(ctx, `ALTER TABLE event_relations RENAME TO event_relations_broken`); err != nil {
		t.Fatalf("rename event_relations: %v", err)
	}

	_, _, err = runSyncDoctorInProcess(t, "sync", "doctor", "--push", "wedge-cli@example.com")
	if err == nil {
		t.Fatal("expected refusal without --yes in a non-interactive shell")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("refusal %q does not mention --yes", err.Error())
	}
}
