package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/storage"
	syncPkg "github.com/douglasdemoura/chroncal/internal/sync"
)

// syncDoctorCmd lists wedged sync resources and offers the confirmed escape
// push. The list also shows the rows that wait, the rows with a failed push,
// and the edits that no series owns (issue #862). A wedged resource fails export on a deterministic hydration error.
// Every sync then retries it and no edit under its UID reaches the server
// (issue #568). The push mode omits the unreadable relations from the PUT.
// The user accepts that loss with an explicit confirmation.
func syncDoctorCmd() *cobra.Command {
	var pushUID string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Find and recover wedged sync resources",
		Long: `List local resources whose unreadable data wedges the sync.

The list also shows three other kinds of resources:
  - a resource whose push waits for another step;
  - a resource whose last push failed;
  - an edit that stays local, because no series owns the edited row.

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
			svc, err := newSyncService(a, nil)
			if err != nil {
				return err
			}

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

// waitingEntry pairs one resource that waits with the calendar that holds it.
type waitingEntry struct {
	calendarName string
	waiting      syncPkg.WaitingResource
}

// waitingAll collects the resources that wait in the calendars cals, with
// one query. A wait is no wedge: the push of the row waits for another
// step, for example the resolution of a move conflict (issue #841).
func waitingAll(ctx context.Context, svc *syncPkg.Service, cals []storage.Calendar) ([]waitingEntry, error) {
	rows, err := svc.WaitingResources(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(cals))
	for _, cal := range cals {
		names[cal.ID] = cal.Name
	}
	var entries []waitingEntry
	for _, w := range rows {
		name, ok := names[w.CalendarID]
		if !ok {
			continue
		}
		entries = append(entries, waitingEntry{calendarName: name, waiting: w})
	}
	return entries, nil
}

// noteEntry is one resource that the doctor lists but cannot push: a push
// failure that is no wedge, or an edit that no series owns.
type noteEntry struct {
	state        string // "push_failure" or "unowned"
	calendarID   int64
	calendarName string
	uid          string
	ownerType    string
	count        int64  // push_failure only
	reason       string // the last push error, or storage.NoSeriesOwnerMsg
}

// notesAll collects the push failures and the unowned edits of the
// calendars cals. A push failure of a wedged resource stays out: the wedged
// line names its count already. A calendar with no account link cannot
// push, so its old push failures stay out, as in the sync status. Its
// unowned edits stay in: the edit of a legacy override goes to the calendar
// of a master, and that calendar can have a link.
func notesAll(ctx context.Context, svc *syncPkg.Service, cals []storage.Calendar, wedged []doctorEntry) ([]noteEntry, error) {
	type key struct {
		calendarID int64
		uid        string
	}
	isWedged := make(map[key]bool, len(wedged))
	for _, en := range wedged {
		isWedged[key{en.wedged.CalendarID, en.wedged.UID}] = true
	}
	var notes []noteEntry
	for _, cal := range cals {
		var failures []syncPkg.PushFailure
		if storage.CalendarLinked(cal) {
			var err error
			if failures, err = svc.PushFailures(ctx, cal.ID); err != nil {
				return nil, fmt.Errorf("calendar %q: %w", cal.Name, err)
			}
		}
		for _, f := range failures {
			if isWedged[key{cal.ID, f.UID}] {
				continue
			}
			notes = append(notes, noteEntry{
				state: "push_failure", calendarID: cal.ID, calendarName: cal.Name,
				uid: f.UID, ownerType: f.OwnerType, count: f.Count, reason: f.Error,
			})
		}
		unowned, err := svc.UnownedEdits(ctx, cal.ID)
		if err != nil {
			return nil, fmt.Errorf("calendar %q: %w", cal.Name, err)
		}
		for _, uid := range unowned {
			notes = append(notes, noteEntry{
				state: "unowned", calendarID: cal.ID, calendarName: cal.Name,
				uid: uid, ownerType: "event", reason: storage.NoSeriesOwnerMsg,
			})
		}
	}
	return notes, nil
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

// runDoctorList prints one line per resource that waits, per push failure,
// and per unowned edit, then one line per wedged resource across all
// calendars. JSON and YAML return an array, [] when no calendar holds any of
// them, like every sibling read command. The state key of each item is
// "wedged", "waiting", "push_failure", or "unowned".
func runDoctorList(cmd *cobra.Command, svc *syncPkg.Service, cals []storage.Calendar) error {
	entries, err := diagnoseAll(context.Background(), svc, cals)
	if err != nil {
		return err
	}
	waits, err := waitingAll(context.Background(), svc, cals)
	if err != nil {
		return err
	}
	notes, err := notesAll(context.Background(), svc, cals, entries)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	if outputFmt != "text" {
		items := make([]map[string]any, 0, len(entries)+len(waits)+len(notes))
		for _, en := range entries {
			items = append(items, map[string]any{
				"state":           "wedged",
				"calendar_id":     en.wedged.CalendarID,
				"calendar_name":   en.calendarName,
				"uid":             en.wedged.UID,
				"owner_type":      en.wedged.OwnerType,
				"relations":       en.wedged.Relations,
				"push_fail_count": en.wedged.PushFailCount,
			})
		}
		for _, wt := range waits {
			items = append(items, map[string]any{
				"state":         "waiting",
				"calendar_id":   wt.waiting.CalendarID,
				"calendar_name": wt.calendarName,
				"uid":           wt.waiting.UID,
				"owner_type":    wt.waiting.OwnerType,
				"reason":        wt.waiting.Reason,
			})
		}
		for _, n := range notes {
			item := map[string]any{
				"state":         n.state,
				"calendar_id":   n.calendarID,
				"calendar_name": n.calendarName,
				"uid":           n.uid,
				"owner_type":    n.ownerType,
				"reason":        n.reason,
			}
			if n.state == "push_failure" {
				item["push_fail_count"] = n.count
			}
			items = append(items, item)
		}
		return printOutput(out, items)
	}

	for _, wt := range waits {
		fmt.Fprintf(out, "calendar %q uid %s (%s): waiting on %s\n",
			safeText(wt.calendarName), safeText(wt.waiting.UID), safeText(wt.waiting.OwnerType), safeText(wt.waiting.Reason))
	}
	for _, n := range notes {
		if n.state == "push_failure" {
			fmt.Fprintf(out, "calendar %q uid %s (%s): %d failed push attempt(s); last error: %s\n",
				safeText(n.calendarName), safeText(n.uid), safeText(n.ownerType), n.count, safeText(n.reason))
			continue
		}
		fmt.Fprintf(out, "calendar %q uid %s (%s): the edit stays local: %s\n",
			safeText(n.calendarName), safeText(n.uid), safeText(n.ownerType), safeText(n.reason))
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
