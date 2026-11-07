package sharefreebusy

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	goical "github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/freebusy"
)

const productID = "-//chroncal//chroncal//EN"

// ExportFree writes a VFREEBUSY document with free entries.
// It inverts the busy periods in the window.
// It gives a fresh UID and a current DTSTAMP.
// It adds ORGANIZER only for a non-empty value.
// One FREEBUSY property marks each free period with FBTYPE FREE.
// It returns an error when To does not exceed From.
func ExportFree(busy []freebusy.Period, from, to time.Time, organizer string) ([]byte, error) {
	return ExportFreeWork(busy, from, to, organizer, false)
}

// ExportFreeWork writes free entries with work hours.
// workHours true clamps free times to 09:00 to 17:00 local.
// It returns an error when To does not exceed From.
func ExportFreeWork(busy []freebusy.Period, from, to time.Time, organizer string, workHours bool) ([]byte, error) {
	if !to.After(from) {
		return nil, fmt.Errorf("invalid range: to must be after from")
	}
	free := Invert(busy, from, to)
	if workHours {
		free = ClampWorkHours(free)
	}

	cal := goical.NewCalendar()
	cal.Props.SetText(goical.PropVersion, "2.0")
	cal.Props.SetText(goical.PropProductID, productID)
	cal.Props.SetText("CALSCALE", "GREGORIAN")

	component := &goical.Component{
		Name:  goical.CompFreeBusy,
		Props: make(goical.Props),
	}

	uid := "sharefreebusy-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10) + "@chroncal"
	component.Props.SetText(goical.PropUID, uid)
	component.Props.SetDateTime(goical.PropDateTimeStamp, time.Now().UTC())
	component.Props.SetDateTime(goical.PropDateTimeStart, from.UTC())
	component.Props.SetDateTime(goical.PropDateTimeEnd, to.UTC())

	if organizer != "" {
		prop := &goical.Prop{Name: goical.PropOrganizer}
		prop.Value = organizer
		component.Props.Set(prop)
	}

	for _, period := range free {
		prop := &goical.Prop{Name: goical.PropFreeBusy, Params: make(goical.Params)}
		prop.Value = fmt.Sprintf("%s/%s",
			period.Start.UTC().Format("20060102T150405Z"),
			period.End.UTC().Format("20060102T150405Z"),
		)
		prop.Params.Set(goical.ParamFreeBusyType, Free)
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
