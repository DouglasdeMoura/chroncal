package recurrence

import (
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// Instance represents a single occurrence of a recurring event
type Instance struct {
	ID         int64
	EventID    int64
	OriginalID int64
	InstanceAt time.Time
	IsOverride bool
	CreatedAt  time.Time
}

// ExpandedEvent wraps an event with its occurrence time for alarm checks
type ExpandedEvent struct {
	event.Event
	InstanceTime time.Time
	IsOverride   bool
}

// LocalInstanceTime returns the instance time as a local moment. An
// all-day instance starts at local midnight of its date
// (timeutil.FloatingLocal).
func (e ExpandedEvent) LocalInstanceTime() time.Time {
	if e.AllDay {
		return timeutil.FloatingLocal(e.InstanceTime)
	}
	return e.InstanceTime.Local()
}

// ExpandedTodo wraps a todo with its occurrence time for alarm checks
type ExpandedTodo struct {
	todo.Todo
	InstanceTime time.Time
	IsOverride   bool
}
