package ical

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
)

func journalFromVJournal(comp *ical.Component) (journal.Journal, []string, error) {
	props := comp.Props

	uid := propText(props, ical.PropUID)
	if uid == "" {
		return journal.Journal{}, nil, fmt.Errorf("missing UID")
	}

	summary := propText(props, ical.PropSummary)

	// VJOURNAL can have multiple DESCRIPTION properties (RFC 5545). Keep each
	// one in the Descriptions slice for round-trip fidelity, and join them into
	// the single DB-backed Description field used for display and search.
	var descriptions []string
	for _, prop := range props.Values(ical.PropDescription) {
		text, err := prop.Text()
		if err != nil {
			text = prop.Value
		}
		if text != "" {
			descriptions = append(descriptions, text)
		}
	}
	description := strings.Join(descriptions, "\n\n")

	startDate, startWarn := parseDateProp(props, "journal", ical.PropDateTimeStart, uid)
	var journalWarnings []string
	if startWarn != "" {
		journalWarnings = append(journalWarnings, startWarn)
	}

	status := propTextOr(props, ical.PropStatus, "FINAL")
	class := propTextOr(props, ical.PropClass, "PUBLIC")

	var sequence int64
	if prop := props.Get("SEQUENCE"); prop != nil {
		if v, err := strconv.ParseInt(prop.Value, 10, 64); err == nil {
			sequence = v
		}
	}

	url := propText(props, ical.PropURL)

	var timezone string
	var journalFloating bool
	if prop := props.Get(ical.PropDateTimeStart); prop != nil {
		if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
			timezone = tzid
		} else if len(prop.Value) > 8 && !strings.HasSuffix(prop.Value, "Z") {
			journalFloating = true
		}
	}

	categories := parseCategoriesFromProps(props)
	exdates := parseDateListFromProps(props, ical.PropExceptionDates, dtstartZone(props))
	rdates := parseDateListFromProps(props, ical.PropRecurrenceDates, dtstartZone(props))
	var rrule string
	if prop := props.Get(ical.PropRecurrenceRule); prop != nil {
		rrule = prop.Value
	}

	recurrenceID, err := parseRecurrenceID(props)
	if err != nil {
		return journal.Journal{}, nil, err
	}

	var dtstamp string
	if prop := props.Get(ical.PropDateTimeStamp); prop != nil {
		if t, err := prop.DateTime(nil); err == nil && !t.IsZero() {
			dtstamp = t.UTC().Format(time.RFC3339)
		}
	}

	// ATTENDEE + ORGANIZER
	attendees, attendeeWarnings := parseAttendeesFromProps(props, model.TaskAttendee)
	for _, w := range attendeeWarnings {
		// Name the owning record: the clamp leaves no other trace.
		journalWarnings = append(journalWarnings, fmt.Sprintf("journal %q: %s", uid, w))
	}

	// ATTACH, COMMENT, CONTACT, RELATED-TO
	attachments, err := parseAttachmentsFromProps(props)
	if err != nil {
		return journal.Journal{}, nil, err
	}
	comments := parseCommentsFromProps(props)
	contacts := parseContactsFromProps(props)
	relations := parseRelationsFromProps(props)

	return journal.Journal{
		UID:            uid,
		Summary:        summary,
		Description:    description,
		Descriptions:   descriptions,
		StartDate:      startDate,
		Status:         strings.ToUpper(status),
		Class:          strings.ToUpper(class),
		URL:            url,
		Categories:     categories,
		RecurrenceRule: rrule,
		Timezone:       floatingOrTZ(journalFloating, timezone),
		Sequence:       sequence,
		ExDates:        exdates,
		RDates:         rdates,
		RecurrenceID:   recurrenceID,
		DtStamp:        dtstamp,
		Attendees:      attendees,
		Attachments:    attachments,
		Comments:       comments,
		Contacts:       contacts,
		XProperties:    extractXPropertiesWithSet(props, handledJournalProps),
		Relations:      relations,
	}, journalWarnings, nil
}

// handledJournalProps is the set of property names explicitly parsed by journalFromVJournal.
var handledJournalProps = map[string]bool{
	ical.PropUID: true, ical.PropSummary: true, ical.PropDescription: true,
	ical.PropDateTimeStart: true, ical.PropRecurrenceRule: true, ical.PropStatus: true,
	"SEQUENCE": true, ical.PropClass: true, ical.PropURL: true,
	ical.PropCategories: true, ical.PropExceptionDates: true,
	ical.PropRecurrenceDates: true, ical.PropRecurrenceID: true,
	ical.PropDateTimeStamp: true, ical.PropCreated: true, ical.PropLastModified: true,
	ical.PropAttach: true, ical.PropComment: true, ical.PropContact: true,
	ical.PropRelatedTo: true,
	ical.PropAttendee:  true, ical.PropOrganizer: true,
}
