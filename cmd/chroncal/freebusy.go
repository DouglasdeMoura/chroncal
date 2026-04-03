package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := parseFreeBusyTime(fromStr)
			if err != nil {
				return fmt.Errorf("parse --from: %w", err)
			}
			to, err := parseFreeBusyTime(toStr)
			if err != nil {
				return fmt.Errorf("parse --to: %w", err)
			}
			if !to.After(from) {
				return fmt.Errorf("--to must be after --from")
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
					return fmt.Errorf("--calendar is required with --remote")
				}
				if calendarRef.AccountID == 0 || calendarRef.RemoteURL == "" {
					return fmt.Errorf("calendar %q is not linked to a remote account", calendarRef.Name)
				}

				credStore, err := auth.NewCredentialStore(true)
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
				client, err := caldav.NewClientFromCredential(account.ServerUrl, cred)
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
					"start":    result.Start.Local().Format(time.RFC3339),
					"end":      result.End.Local().Format(time.RFC3339),
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

func parseFreeBusyTime(input string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02",
		time.RFC3339,
	} {
		if layout == "2006-01-02" {
			if t, err := time.ParseInLocation(layout, input, time.Local); err == nil {
				return t, nil
			}
			continue
		}
		if t, err := time.Parse(layout, input); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time %q", input)
}

func printFreeBusy(w io.Writer, label string, remote bool, result freebusy.Result) {
	scope := "local"
	if remote {
		scope = "remote"
	}
	fmt.Fprintf(w, "%s free/busy for %s\n", strings.Title(scope), label)
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
			"start": period.Start.Local().Format(time.RFC3339),
			"end":   period.End.Local().Format(time.RFC3339),
			"type":  period.Type,
		}
	}
	return out
}
