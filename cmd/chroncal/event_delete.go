package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func eventDeleteCmd() *cobra.Command {
	var (
		recurrenceID string
		following    string
		series       bool
	)
	cmd := &cobra.Command{
		Use:   "delete <id|uid>",
		Short: "Delete an event",
		Long: `Delete a single event, this occurrence, this and following
occurrences, or an entire recurring series.

--recurrence-id and --following take an RFC 3339 timestamp and address the
series by ID or UID. --recurrence-id excludes that one date (EXDATE, and
any override at that time). --following truncates the series at that date.`,
		Example: `  chroncal event delete 42
  chroncal event delete standup-uid --recurrence-id 2026-04-07T12:00:00Z
  chroncal event delete standup-uid --following 2026-04-07T12:00:00Z
  chroncal event delete standup-uid --series`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			if series && (recurrenceID != "" || following != "") {
				return errInvalidInputf("--series cannot be combined with --recurrence-id or --following")
			}
			if recurrenceID != "" && following != "" {
				return errInvalidInputf("--recurrence-id and --following are mutually exclusive")
			}

			var followingAt, occurrenceAt time.Time
			if following != "" {
				followingAt, err = parseRFC3339Flag("following", following)
				if err != nil {
					return err
				}
			}
			if recurrenceID != "" {
				occurrenceAt, err = parseRFC3339Flag("recurrence-id", recurrenceID)
				if err != nil {
					return err
				}
			}

			e, err := resolveEvent(ctx, a, args[0], "")
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}

			question := fmt.Sprintf("Delete event %q?", safeText(e.Title))
			switch {
			case series:
				question = fmt.Sprintf("Delete the entire recurring series %q (master + all overrides)?", safeText(e.Title))
			case following != "":
				question = fmt.Sprintf("Delete %q and following occurrences at %s?", safeText(e.Title), followingAt.Format(time.RFC3339))
			case recurrenceID != "":
				question = fmt.Sprintf("Delete this occurrence of %q at %s?", safeText(e.Title), occurrenceAt.Format(time.RFC3339))
			}
			if err := confirmDestructive(cmd, question); err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			if following != "" {
				at := followingAt
				if err := a.Events.DeleteFromInstance(ctx, e.UID, at); err != nil {
					return fmt.Errorf("delete this and following: %w", err)
				}
				if outputFmt != "text" {
					if err := printOutput(w, map[string]any{"deleted": true, "uid": e.UID, "following": at.UTC().Format(time.RFC3339)}); err != nil {
						return err
					}
					opportunisticPush(a, e.CalendarID, cmd)
					return nil
				}
				fmt.Fprintf(w, "Deleted %q and following occurrences.\n", safeText(e.Title))
				opportunisticPush(a, e.CalendarID, cmd)
				return nil
			}

			if recurrenceID != "" {
				at := occurrenceAt
				if err := a.Events.DeleteInstance(ctx, e.UID, at); err != nil {
					return fmt.Errorf("delete occurrence: %w", err)
				}
				if outputFmt != "text" {
					if err := printOutput(w, map[string]any{"deleted": true, "uid": e.UID, "recurrence_id": at.UTC().Format(time.RFC3339)}); err != nil {
						return err
					}
					opportunisticPush(a, e.CalendarID, cmd)
					return nil
				}
				fmt.Fprintf(w, "Deleted occurrence of %q at %s.\n", safeText(e.Title), at.UTC().Format(time.RFC3339))
				opportunisticPush(a, e.CalendarID, cmd)
				return nil
			}

			if series {
				if err := a.Events.DeleteSeries(ctx, e.UID); err != nil {
					return fmt.Errorf("delete series: %w", err)
				}
				if outputFmt != "text" {
					if err := printOutput(w, map[string]any{"deleted": true, "uid": e.UID, "series": true}); err != nil {
						return err
					}
					opportunisticPush(a, e.CalendarID, cmd)
					return nil
				}
				fmt.Fprintf(w, "Deleted event series %q.\n", safeText(e.UID))
				opportunisticPush(a, e.CalendarID, cmd)
				return nil
			}

			if err := a.Events.Delete(ctx, e.ID); err != nil {
				return fmt.Errorf("delete event: %w", err)
			}
			if outputFmt != "text" {
				if err := printOutput(w, map[string]any{"deleted": true, "id": e.ID}); err != nil {
					return err
				}
				opportunisticPush(a, e.CalendarID, cmd)
				return nil
			}
			fmt.Fprintf(w, "Deleted event %d.\n", e.ID)
			opportunisticPush(a, e.CalendarID, cmd)
			return nil
		},
	}
	cmd.Flags().StringVar(&recurrenceID, "recurrence-id", "", "delete this occurrence (RFC 3339 timestamp)")
	cmd.Flags().StringVar(&following, "following", "", "delete this occurrence and every following one (RFC 3339 timestamp)")
	cmd.Flags().BoolVar(&series, "series", false, "delete the entire recurring series (master + all overrides)")
	addConfirmFlag(cmd)
	return cmd
}
