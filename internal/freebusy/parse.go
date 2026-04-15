package freebusy

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	goical "github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/duration"
)

// ParseCalendar extracts VFREEBUSY components from an iCalendar stream.
func ParseCalendar(r io.Reader) ([]Result, error) {
	dec := goical.NewDecoder(r)
	var results []Result

	for {
		cal, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			return results, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode ical: %w", err)
		}
		for _, child := range cal.Children {
			if child.Name != goical.CompFreeBusy {
				continue
			}
			result, err := ParseComponent(child)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
	}
}

// ParseComponent extracts a single VFREEBUSY component.
func ParseComponent(comp *goical.Component) (Result, error) {
	if comp == nil {
		return Result{}, fmt.Errorf("nil VFREEBUSY component")
	}
	if comp.Name != goical.CompFreeBusy {
		return Result{}, fmt.Errorf("unexpected component %q", comp.Name)
	}

	props := comp.Props
	result := Result{
		UID:       propValue(props, goical.PropUID),
		Organizer: propValue(props, goical.PropOrganizer),
		URL:       propValue(props, goical.PropURL),
	}

	if prop := props.Get(goical.PropDateTimeStamp); prop != nil {
		ts, err := parseDateTimeValue(prop.Value)
		if err != nil {
			return Result{}, fmt.Errorf("parse DTSTAMP: %w", err)
		}
		result.DTStamp = ts.UTC()
	}
	if prop := props.Get(goical.PropDateTimeStart); prop != nil {
		start, err := parseDateTimeValue(prop.Value)
		if err != nil {
			return Result{}, fmt.Errorf("parse DTSTART: %w", err)
		}
		result.Start = start.UTC()
	}
	if prop := props.Get(goical.PropDateTimeEnd); prop != nil {
		end, err := parseDateTimeValue(prop.Value)
		if err != nil {
			return Result{}, fmt.Errorf("parse DTEND: %w", err)
		}
		result.End = end.UTC()
	}

	for _, prop := range props.Values(goical.PropFreeBusy) {
		kind := normalizeType(prop.Params.Get(goical.ParamFreeBusyType))
		for _, raw := range strings.Split(prop.Value, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			period, err := parsePeriod(raw, kind)
			if err != nil {
				return Result{}, fmt.Errorf("parse FREEBUSY %q: %w", raw, err)
			}
			result.Periods = append(result.Periods, period)
		}
	}

	sort.Slice(result.Periods, func(i, j int) bool {
		if result.Periods[i].Start.Equal(result.Periods[j].Start) {
			return result.Periods[i].End.Before(result.Periods[j].End)
		}
		return result.Periods[i].Start.Before(result.Periods[j].Start)
	})

	return result, nil
}

func parsePeriod(raw, kind string) (Period, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 2 {
		return Period{}, fmt.Errorf("invalid PERIOD %q", raw)
	}

	start, err := parseDateTimeValue(parts[0])
	if err != nil {
		return Period{}, fmt.Errorf("parse period start: %w", err)
	}

	end, err := parseDateTimeValue(parts[1])
	if err != nil {
		if duration.Validate(parts[1]) != nil {
			return Period{}, fmt.Errorf("parse period end: %w", err)
		}
		end = duration.Add(start, parts[1])
		if end.IsZero() {
			return Period{}, fmt.Errorf("invalid duration %q", parts[1])
		}
	}

	return Period{
		Start: start.UTC(),
		End:   end.UTC(),
		Type:  normalizeType(kind),
	}, nil
}

func parseDateTimeValue(value string) (time.Time, error) {
	for _, layout := range []string{
		"20060102T150405Z",
		"20060102T150405",
		"20060102",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported datetime %q", value)
}

func propValue(props goical.Props, name string) string {
	if prop := props.Get(name); prop != nil {
		return prop.Value
	}
	return ""
}
