package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/freebusy"
)

func freebusyCmd() *cobra.Command {
	var (
		fromStr      string
		toStr        string
		calendarName string
		remote       bool
		format       string
	)

	cmd := &cobra.Command{
		Use:   "freebusy",
		Short: "Compute or query busy time for a calendar range",
		Long: `Return busy periods for a time range.

By default this computes free/busy from local data. With --remote, it
queries the connected remote CalDAV calendar instead.`,
		Example: `  chroncal freebusy --from 2026-04-01 --to 2026-04-07
  chroncal freebusy --calendar Work --from 2026-04-01T09:00:00-03:00 --to 2026-04-01T18:00:00-03:00
  chroncal freebusy --calendar Work --remote --from 2026-04-01 --to 2026-04-07 --format ical`,
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := parseFreeBusyTime("from", fromStr, false)
			if err != nil {
				return err
			}
			to, err := parseFreeBusyTime("to", toStr, true)
			if err != nil {
				return err
			}
			if !to.After(from) {
				return errInvalidInputf("--to must be after --from")
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			var (
				result      freebusy.Result
				label       string
				calendarRef calendar.Calendar
			)

			if calendarName != "" {
				calID, err := resolveCalendarID(ctx, a, calendarName)
				if err != nil {
					return err
				}
				calendarRef, err = a.Calendars.Get(ctx, calID)
				if err != nil {
					return err
				}
				label = calendarRef.Name
			} else {
				label = "All Calendars"
			}

			if remote {
				if calendarName == "" {
					return errInvalidInputf("--calendar is required with --remote")
				}
				if calendarRef.AccountID == 0 || calendarRef.RemoteURL == "" {
					return errInvalidInputf("calendar %q is not connected to a remote calendar", calendarRef.Name)
				}

				credStore, err := auth.NewCredentialStore(a.AllowPlaintext)
				if err != nil {
					return fmt.Errorf("credential store: %w", err)
				}

				account, err := a.Queries.GetAccount(ctx, calendarRef.AccountID)
				if err != nil {
					return fmt.Errorf("get account: %w", err)
				}
				cred, err := credStore.Get(account.ID)
				if err != nil {
					return fmt.Errorf("get credentials: %w", err)
				}
				client, err := caldav.NewClientFromCredential(account.ServerUrl, cred, func(updated auth.Credential) error {
					return credStore.Set(updated)
				})
				if err != nil {
					return fmt.Errorf("create client: %w", err)
				}
				result, err = client.QueryFreeBusy(ctx, calendarRef.RemoteURL, from.UTC(), to.UTC())
				if err != nil {
					return err
				}
			} else {
				var calendarIDs []int64
				if calendarRef.ID != 0 {
					calendarIDs = []int64{calendarRef.ID}
				}
				result, err = freebusy.Compute(ctx, a.Recurrences, from.UTC(), to.UTC(), calendarIDs)
				if err != nil {
					return fmt.Errorf("compute freebusy: %w", err)
				}
			}

			if format == "ical" {
				data, err := freebusy.Export(result, label)
				if err != nil {
					return fmt.Errorf("export freebusy: %w", err)
				}
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}

			if outputFmt != "text" {
				return printOutput(cmd.OutOrStdout(), map[string]any{
					"calendar": label,
					"remote":   remote,
					"start":    result.Start.UTC().Format(time.RFC3339),
					"end":      result.End.UTC().Format(time.RFC3339),
					"periods":  toJSONFreeBusyPeriods(result.Periods),
				})
			}

			printFreeBusy(cmd.OutOrStdout(), label, remote, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromStr, "from", "", "range start (YYYY-MM-DD or RFC3339)")
	cmd.Flags().StringVar(&toStr, "to", "", "range end (YYYY-MM-DD or RFC3339)")
	cmd.Flags().StringVar(&calendarName, "calendar", "", "calendar to query")
	cmd.Flags().BoolVar(&remote, "remote", false, "query the linked remote calendar via CalDAV REPORT")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or ical")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// parseFreeBusyTime parses a free/busy range bound that may be a date-only
// (YYYY-MM-DD) value or a full RFC3339 timestamp. When inclusiveEnd is true and
// the input is date-only, the result is advanced by one day so the named day is
// fully covered by the half-open [from, to) range — matching the inclusive
// end-of-day semantics of parseDateRange used by `list`/`export` (issue #137).
func parseFreeBusyTime(flag, input string, inclusiveEnd bool) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02", input, time.Local); err == nil {
		if inclusiveEnd {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, input); err == nil {
		return t, nil
	}
	return time.Time{}, errInvalidInputf("--%s: invalid value %q (expected YYYY-MM-DD or RFC3339 timestamp)", flag, input)
}

func printFreeBusy(w io.Writer, label string, remote bool, result freebusy.Result) {
	scope := "local"
	if remote {
		scope = "remote"
	}
	fmt.Fprintf(w, "%s free/busy for %s\n", cases.Title(language.English).String(scope), label)
	fmt.Fprintf(w, "  Range: %s - %s\n", result.Start.Local().Format("2006-01-02 15:04"), result.End.Local().Format("2006-01-02 15:04"))
	if len(result.Periods) == 0 {
		fmt.Fprintln(w, "  No busy periods.")
		return
	}
	for _, period := range result.Periods {
		fmt.Fprintf(w, "  %s  %s - %s\n",
			period.Type,
			period.Start.Local().Format("2006-01-02 15:04"),
			period.End.Local().Format("2006-01-02 15:04"),
		)
	}
}

func toJSONFreeBusyPeriods(periods []freebusy.Period) []map[string]string {
	out := make([]map[string]string, len(periods))
	for i, period := range periods {
		out[i] = map[string]string{
			"start": period.Start.UTC().Format(time.RFC3339),
			"end":   period.End.UTC().Format(time.RFC3339),
			"type":  period.Type,
		}
	}
	return out
}
