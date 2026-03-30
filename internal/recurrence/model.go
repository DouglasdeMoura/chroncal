package recurrence

import (
	"time"

	"github.com/douglasdemoura/tcal/internal/event"
	"github.com/douglasdemoura/tcal/internal/todo"
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

// ExpandedEvent wraps an event with its occurrence time for alarm checking
type ExpandedEvent struct {
	event.Event
	InstanceTime time.Time
	IsOverride   bool
}

// ExpandedTodo wraps a todo with its occurrence time for alarm checking
type ExpandedTodo struct {
	todo.Todo
	InstanceTime time.Time
	IsOverride   bool
}
