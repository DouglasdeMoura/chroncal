package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// syncDoctorCmd lists wedged sync resources and offers the confirmed escape
// push. A wedged resource fails export on a deterministic hydration error.
// Every sync then retries it and no edit under its UID reaches the server
// (issue #568). The push mode omits the unreadable relations from the PUT.
// The user accepts that loss with an explicit confirmation.
func syncDoctorCmd() *cobra.Command {
	var pushUID string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Find and recover wedged sync resources",
		Long: `List local resources whose unreadable data wedges the sync.

A wedged resource stays dirty forever. Every sync retries it and fails on
the same unreadable relation, and no edit under its UID reaches the server.
With --push, chroncal pushes the resource without the unreadable relations.
The server copy then loses exactly those fields. The command prints the loss
before the push and asks for confirmation.`,
		Example: `  chroncal sync doctor
  chroncal sync doctor --push 6f2c8e42-...@example.com --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()

			ctx := context.Background()
			credStore, _ := auth.NewCredentialStore(a.CredentialNamespace, a.PreviousCredentialNamespaces, a.MigrateLegacyCredentials, a.AllowPlaintext)
			svc := syncPkg.NewService(a.DB, a.Queries, credStore, a.Calendars, a.Events, a.Todos, a.Journals, nil)

			cals, err := a.Queries.ListCalendars(ctx)
			if err != nil {
				return fmt.Errorf("list calendars: %w", err)
			}

			if pushUID != "" {
				return runDoctorPush(cmd, svc, cals, pushUID)
			}
			return runDoctorList(cmd, svc, cals)
		},
	}
	cmd.Flags().StringVar(&pushUID, "push", "", "push this UID and drop its unreadable relations")
	addConfirmFlag(cmd)
	return cmd
}

// runDoctorList prints one line per wedged resource across all calendars.
func runDoctorList(cmd *cobra.Command, svc *syncPkg.Service, cals []storage.Calendar) error {
	out := cmd.OutOrStdout()
	total := 0
	for _, cal := range cals {
		wedged, err := svc.DiagnoseCalendar(context.Background(), cal.ID)
		if err != nil {
			return fmt.Errorf("diagnose calendar %q: %w", cal.Name, err)
		}
		for _, w := range wedged {
			total++
			fmt.Fprintf(out, "calendar %q uid %s (%s): unreadable relation(s) %s\n",
				cal.Name, w.UID, w.OwnerType, strings.Join(w.Relations, ", "))
		}
	}
	if total == 0 {
		fmt.Fprintln(out, "No wedged resources.")
		return nil
	}
	fmt.Fprintf(out, "\n%d wedged resource(s). Recover one with:\n", total)
	fmt.Fprintf(cmd.ErrOrStderr(), "  chroncal sync doctor --push <uid> --yes\n")
	fmt.Fprintf(cmd.ErrOrStderr(), "The push drops the unreadable relations from the server copy.\n")
	return nil
}

// runDoctorPush locates the UID among the wedged resources, announces the
// loss, asks for confirmation, and pushes the incomplete record.
func runDoctorPush(cmd *cobra.Command, svc *syncPkg.Service, cals []storage.Calendar, uid string) error {
	ctx := context.Background()
	for _, cal := range cals {
		wedged, err := svc.DiagnoseCalendar(ctx, cal.ID)
		if err != nil {
			return fmt.Errorf("diagnose calendar %q: %w", cal.Name, err)
		}
		for _, w := range wedged {
			if w.UID != uid {
				continue
			}
			question := fmt.Sprintf(
				"Push %s from calendar %q without relation(s) %s? The server copy loses those fields",
				uid, cal.Name, strings.Join(w.Relations, ", "))
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}
			dropped, err := svc.DoctorPush(ctx, cal.ID, uid)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pushed %s. Dropped relation(s): %s.\n",
				uid, strings.Join(dropped, ", "))
			fmt.Fprintf(cmd.OutOrStdout(), "The resource is no longer dirty. Other edits flow again.\n")
			return nil
		}
	}
	return &cliError{
		Code: "not-wedged",
		Msg:  fmt.Sprintf("no wedged resource with uid %q; run chroncal sync doctor for the list", uid),
	}
}
