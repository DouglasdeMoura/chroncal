package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/douglasdemoura/chroncal/internal/model"
)

func eventRsvpCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "rsvp <id|uid>",
		Short: "Set your RSVP for an event",
		Long: `Set the RSVP status of the calendar owner on an event.

The calendar must have an owner email. That email must match a
non-organizer attendee on the event.

--status accepts ACCEPTED, DECLINED, or TENTATIVE. The aliases yes/y,
no/n, and maybe/m also work.

This command writes the local PARTSTAT. It does not send an iTIP reply.`,
		Example: `  chroncal event rsvp 42 --status ACCEPTED
  chroncal event rsvp 42 --status yes
  chroncal event rsvp team-standup-uid --status maybe --output json`,
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			normalized, err := parseRsvpStatus(status)
			if err != nil {
				return err
			}

			a, err := initApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := context.Background()

			e, err := resolveEvent(ctx, a, args[0], "")
			if err != nil {
				return fmt.Errorf("get event: %w", err)
			}

			cal, err := a.Calendars.Get(ctx, e.CalendarID)
			if err != nil {
				return fmt.Errorf("get calendar: %w", err)
			}

			attendees, err := a.Events.ListAttendees(ctx, e.ID)
			if err != nil {
				return fmt.Errorf("list attendees: %w", err)
			}

			idx, err := findUserRsvpAttendee(attendees, cal.OwnerEmail)
			if err != nil {
				return err
			}

			attendees[idx].RSVPStatus = normalized
			if err := a.Events.ReplaceAttendees(ctx, e.ID, attendees); err != nil {
				return fmt.Errorf("update attendees: %w", err)
			}

			e.Attendees = attendees
			populateEventFields(ctx, a.Events, &e)

			w := cmd.OutOrStdout()
			if outputFmt != "text" {
				return printOutput(w, toJSONEvent(e))
			}
			fmt.Fprintf(w, "RSVP updated to %s.\n", normalized)
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "RSVP status (ACCEPTED, DECLINED, TENTATIVE; aliases: yes, no, maybe)")
	return cmd
}

func parseRsvpStatus(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "ACCEPTED", "YES", "Y":
		return "ACCEPTED", nil
	case "DECLINED", "NO", "N":
		return "DECLINED", nil
	case "TENTATIVE", "MAYBE", "M":
		return "TENTATIVE", nil
	case "":
		return "", errInvalidInputf("--status is required")
	default:
		return "", errInvalidInputf("--status must be ACCEPTED, DECLINED, or TENTATIVE")
	}
}

func findUserRsvpAttendee(attendees []model.Attendee, ownerEmail string) (int, error) {
	ownerEmail = strings.TrimSpace(ownerEmail)
	if ownerEmail == "" {
		return -1, errInvalidInputf("calendar has no owner email; set it with calendar update --email")
	}
	organizerOnly := false
	for i, att := range attendees {
		if !strings.EqualFold(att.Email, ownerEmail) {
			continue
		}
		if att.Organizer {
			organizerOnly = true
			continue
		}
		return i, nil
	}
	if organizerOnly {
		return -1, errInvalidInputf("you are the organizer of this event")
	}
	return -1, errInvalidInputf("you are not an invited attendee of this event")
}
