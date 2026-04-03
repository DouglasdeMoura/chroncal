package freebusy

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	goical "github.com/emersion/go-ical"
)

const productID = "-//chroncal//chroncal//EN"

// Export renders a single RFC 5545 VFREEBUSY result as a VCALENDAR document.
func Export(result Result, calName string) ([]byte, error) {
	cal := goical.NewCalendar()
	cal.Props.SetText(goical.PropVersion, "2.0")
	cal.Props.SetText(goical.PropProductID, productID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")
	if calName != "" {
		cal.Props.SetText("X-WR-CALNAME", calName)
	}

	component := &goical.Component{
		Name:  goical.CompFreeBusy,
		Props: make(goical.Props),
	}

	uid := result.UID
	if uid == "" {
		uid = "freebusy-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10) + "@chroncal"
	}
	component.Props.SetText(goical.PropUID, uid)

	dtstamp := result.DTStamp
	if dtstamp.IsZero() {
		dtstamp = time.Now().UTC()
	}
	component.Props.SetDateTime(goical.PropDateTimeStamp, dtstamp.UTC())

	if !result.Start.IsZero() {
		component.Props.SetDateTime(goical.PropDateTimeStart, result.Start.UTC())
	}
	if !result.End.IsZero() {
		component.Props.SetDateTime(goical.PropDateTimeEnd, result.End.UTC())
	}
	if result.Organizer != "" {
		prop := &goical.Prop{Name: goical.PropOrganizer}
		prop.Value = result.Organizer
		component.Props.Set(prop)
	}
	if result.URL != "" {
		prop := &goical.Prop{Name: goical.PropURL}
		prop.Value = result.URL
		component.Props.Set(prop)
	}

	for _, period := range result.Periods {
		prop := &goical.Prop{Name: goical.PropFreeBusy, Params: make(goical.Params)}
		prop.Value = fmt.Sprintf("%s/%s",
			period.Start.UTC().Format("20060102T150405Z"),
			period.End.UTC().Format("20060102T150405Z"),
		)
		if kind := normalizeType(period.Type); kind != Busy {
			prop.Params.Set(goical.ParamFreeBusyType, kind)
		}
		component.Props.Add(prop)
	}

	cal.Children = append(cal.Children, component)

	var buf bytes.Buffer
	enc := goical.NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, fmt.Errorf("encode ical: %w", err)
	}
	return buf.Bytes(), nil
}
