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
  chroncal sync doctor --output json
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

// doctorEntry pairs one wedged resource with the calendar that holds it, so
// every view prints the calendar name next to the UID.
type doctorEntry struct {
	calendarName string
	wedged       syncPkg.WedgedResource
}

// diagnoseAll collects the wedged resources of every calendar.
func diagnoseAll(ctx context.Context, svc *syncPkg.Service, cals []storage.Calendar) ([]doctorEntry, error) {
	var entries []doctorEntry
	for _, cal := range cals {
		wedged, err := svc.DiagnoseCalendar(ctx, cal.ID)
		if err != nil {
			return nil, fmt.Errorf("diagnose calendar %q: %w", cal.Name, err)
		}
		for _, w := range wedged {
			entries = append(entries, doctorEntry{calendarName: cal.Name, wedged: w})
		}
	}
	return entries, nil
}

// runDoctorList prints one line per wedged resource across all calendars.
// JSON and YAML return an array, [] when no calendar holds a wedge, like
// every sibling read command.
func runDoctorList(cmd *cobra.Command, svc *syncPkg.Service, cals []storage.Calendar) error {
	entries, err := diagnoseAll(context.Background(), svc, cals)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	if outputFmt != "text" {
		items := make([]map[string]any, 0, len(entries))
		for _, en := range entries {
			items = append(items, map[string]any{
				"calendar_id":     en.wedged.CalendarID,
				"calendar_name":   en.calendarName,
				"uid":             en.wedged.UID,
				"owner_type":      en.wedged.OwnerType,
				"relations":       en.wedged.Relations,
				"push_fail_count": en.wedged.PushFailCount,
			})
		}
		return printOutput(out, items)
	}

	if len(entries) == 0 {
		fmt.Fprintln(out, "No wedged resources.")
		return nil
	}
	for _, en := range entries {
		fmt.Fprintf(out, "calendar %q uid %s (%s): unreadable relation(s) %s; %d failed push attempt(s)\n",
			safeText(en.calendarName), safeText(en.wedged.UID), safeText(en.wedged.OwnerType),
			safeText(strings.Join(en.wedged.Relations, ", ")), en.wedged.PushFailCount)
	}
	// The whole hint block goes to stdout. A consumer that captures stdout
	// then also captures the recovery command.
	fmt.Fprintf(out, "\n%d wedged resource(s). Recover one with:\n", len(entries))
	fmt.Fprintln(out, "  chroncal sync doctor --push <uid> --yes")
	fmt.Fprintln(out, "The push drops the unreadable relations from the server copy.")
	return nil
}

// runDoctorPush locates the UID among the wedged resources, announces the
// loss, asks for confirmation, and pushes the incomplete record.
func runDoctorPush(cmd *cobra.Command, svc *syncPkg.Service, cals []storage.Calendar, uid string) error {
	ctx := context.Background()
	entries, err := diagnoseAll(ctx, svc, cals)
	if err != nil {
		return err
	}
	for _, en := range entries {
		w := en.wedged
		if w.UID != uid {
			continue
		}
		question := fmt.Sprintf(
			"Push %s from calendar %q without relation(s) %s? The server copy loses those fields",
			safeText(uid), safeText(en.calendarName), safeText(strings.Join(w.Relations, ", ")))
		if err := confirmDestructive(cmd, question); err != nil {
			return err
		}
		// The confirmed relation set rides along. DoctorPush re-runs the
		// export under the lifecycle lock and refuses the push when the
		// actual drop set differs from what the user accepted.
		dropped, err := svc.DoctorPush(ctx, w.CalendarID, uid, w.Relations)
		if err != nil {
			return err
		}
		return renderDoctorPush(cmd, uid, dropped)
	}
	return &cliError{
		Code: "not_found",
		Msg:  fmt.Sprintf("no wedged resource with uid %q; run chroncal sync doctor for the list", uid),
	}
}

// renderDoctorPush confirms the escape push using the active --output
// format, like the sibling sync resolve command.
func renderDoctorPush(cmd *cobra.Command, uid string, dropped []string) error {
	if outputFmt != "text" {
		return printOutput(cmd.OutOrStdout(), map[string]any{
			"uid":     uid,
			"pushed":  true,
			"dropped": dropped,
		})
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Pushed %s. Dropped relation(s): %s.\n",
		safeText(uid), safeText(strings.Join(dropped, ", ")))
	fmt.Fprintln(out, "The resource is no longer dirty. Other edits flow again.")
	return nil
}
