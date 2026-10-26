package ical

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// The content form of an item holds the fields that a comparison of two
// copies of one item counts as content. It drops the fields that a store or
// a server writes on its own:
//   - the row ID, the calendar ID, and the SEQUENCE
//   - DTSTAMP, CREATED, and LAST-MODIFIED
//   - the order of the categories and of the attendees
//
// The import comparison of an unchanged item (issue #883) and the check of
// a refused create (issue #918) share these rules. The alarms have their
// own rules in each caller.

// ContentEvent returns the content form of e.
func ContentEvent(e event.Event) event.Event {
	e.ID, e.CalendarID, e.Sequence, e.DtStamp = 0, 0, 0, ""
	e.CreatedAt, e.UpdatedAt, e.DeletedAt = time.Time{}, time.Time{}, nil
	e.Categories = sortedCategories(e.Categories)
	e.Attendees = sortedAttendees(e.Attendees)
	return e
}

// ContentTodo returns the content form of t.
func ContentTodo(t todo.Todo) todo.Todo {
	t.ID, t.CalendarID, t.Sequence, t.DtStamp = 0, 0, 0, ""
	t.CreatedAt, t.UpdatedAt, t.DeletedAt = time.Time{}, time.Time{}, nil
	t.Categories = sortedCategories(t.Categories)
	t.Attendees = sortedAttendees(t.Attendees)
	return t
}

// ContentJournal returns the content form of j. The store keeps the joined
// Description only, so the split of the DESCRIPTION properties is no
// content.
func ContentJournal(j journal.Journal) journal.Journal {
	j.ID, j.CalendarID, j.Sequence, j.DtStamp = 0, 0, 0, ""
	j.CreatedAt, j.UpdatedAt, j.DeletedAt = time.Time{}, time.Time{}, nil
	j.Categories = sortedCategories(j.Categories)
	j.Attendees = sortedAttendees(j.Attendees)
	j.Descriptions = nil
	return j
}

// SameContent reports whether the body sent and the server calendar
// server hold the same items (issue #918). Both sides go through the
// import of a server body and the content form, and then through the
// export, so the comparison reads the same fields on each side. The alarms
// count without their UIDs and in a fixed order: the store gives a UID to
// an alarm without one, and it reads the alarms in the order of their rows.
// Each side takes the defaults of the store (WithStoreDefaults), because a
// stored row has them and a server body can omit them.
// A body that does not parse, or that holds no item, is not the same. A
// panic in the comparison counts as a difference, so the caller keeps its
// old path and the sync goes on.
func SameContent(sent []byte, server *ical.Calendar) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	a, err := ImportFileRemote(bytes.NewReader(sent))
	if err != nil || a.SkippedComponents > 0 {
		return false
	}
	b, err := ImportCalendarRemote(cloneCalendar(server))
	if err != nil || b.SkippedComponents > 0 {
		return false
	}
	fa, okA := contentBytes(a)
	fb, okB := contentBytes(b)
	return okA && okB && len(fa) > 0 && bytes.Equal(fa, fb)
}

// contentBytes returns the export of the content form of each item of r,
// in the order of the UID and the recurrence ID.
func contentBytes(r ImportResult) ([]byte, bool) {
	events := make([]event.Event, 0, len(r.Events))
	for _, e := range r.Events {
		e = ContentEvent(e.WithStoreDefaults())
		e.Alarms = contentAlarms(e.Alarms)
		events = append(events, e)
	}
	slices.SortStableFunc(events, func(x, y event.Event) int {
		return cmp.Or(cmp.Compare(x.UID, y.UID), cmp.Compare(x.RecurrenceID, y.RecurrenceID))
	})
	todos := make([]todo.Todo, 0, len(r.Todos))
	for _, t := range r.Todos {
		t = ContentTodo(t.WithStoreDefaults(t.CompletedAt))
		t.Alarms = contentAlarms(t.Alarms)
		todos = append(todos, t)
	}
	slices.SortStableFunc(todos, func(x, y todo.Todo) int {
		return cmp.Or(cmp.Compare(x.UID, y.UID), cmp.Compare(x.RecurrenceID, y.RecurrenceID))
	})
	journals := make([]journal.Journal, 0, len(r.Journals))
	for _, j := range r.Journals {
		journals = append(journals, ContentJournal(j.WithStoreDefaults()))
	}
	slices.SortStableFunc(journals, func(x, y journal.Journal) int {
		return cmp.Or(cmp.Compare(x.UID, y.UID), cmp.Compare(x.RecurrenceID, y.RecurrenceID))
	})

	var out bytes.Buffer
	for _, export := range []func() ([]byte, error){
		func() ([]byte, error) {
			return exportIfAny(len(events), func() ([]byte, error) { return ExportEvents(events, "") })
		},
		func() ([]byte, error) {
			return exportIfAny(len(todos), func() ([]byte, error) { return ExportTodos(todos, "") })
		},
		func() ([]byte, error) {
			return exportIfAny(len(journals), func() ([]byte, error) { return ExportJournals(journals, "") })
		},
	} {
		data, err := export()
		if err != nil {
			return nil, false
		}
		out.Write(data)
	}
	return out.Bytes(), true
}

// exportIfAny runs export only when the list has an item.
func exportIfAny(n int, export func() ([]byte, error)) ([]byte, error) {
	if n == 0 {
		return nil, nil
	}
	return export()
}

// contentAlarms returns copies of alarms with no IDs and no UIDs, in one
// fixed order.
func contentAlarms(alarms []model.Alarm) []model.Alarm {
	out := slices.Clone(alarms)
	for i := range out {
		out[i].ID, out[i].UID = 0, ""
	}
	slices.SortStableFunc(out, func(a, b model.Alarm) int {
		return cmp.Compare(fmt.Sprintf("%+v", a), fmt.Sprintf("%+v", b))
	})
	return out
}

// sortedCategories returns the categories of list in sorted order.
func sortedCategories(list string) string {
	cats := timeutil.ParseCategoryList(list)
	slices.Sort(cats)
	return timeutil.JoinCategoryList(cats)
}

// sortedAttendees returns a copy of attendees in one fixed order.
func sortedAttendees(attendees []model.Attendee) []model.Attendee {
	out := slices.Clone(attendees)
	for i := range out {
		out[i].ID, out[i].EventID = 0, 0
	}
	slices.SortStableFunc(out, func(a, b model.Attendee) int {
		return cmp.Compare(fmt.Sprintf("%+v", a), fmt.Sprintf("%+v", b))
	})
	return out
}

// cloneCalendar returns a deep copy of cal. ImportCalendarRemote changes
// the TZID parameters of its calendar in place, and the caller keeps the
// server calendar for the conflict record.
func cloneCalendar(cal *ical.Calendar) *ical.Calendar {
	if cal == nil {
		return nil
	}
	return &ical.Calendar{Component: cloneComponent(cal.Component)}
}

func cloneComponent(c *ical.Component) *ical.Component {
	if c == nil {
		return nil
	}
	out := &ical.Component{Name: c.Name, Props: make(ical.Props, len(c.Props))}
	for name, props := range c.Props {
		cp := make([]ical.Prop, len(props))
		for i, p := range props {
			params := make(ical.Params, len(p.Params))
			for k, v := range p.Params {
				params[k] = slices.Clone(v)
			}
			cp[i] = ical.Prop{Name: p.Name, Params: params, Value: p.Value}
		}
		out.Props[name] = cp
	}
	for _, child := range c.Children {
		out.Children = append(out.Children, cloneComponent(child))
	}
	return out
}
