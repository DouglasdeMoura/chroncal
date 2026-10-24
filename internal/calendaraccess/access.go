package calendaraccess

import (
	"context"
	"errors"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

var (
	ErrReadOnly             = errors.New("calendar is read-only")
	ErrUnsupportedComponent = errors.New("calendar does not support this component")
)

// IsReadOnly reports whether a calendars.remote_access value marks a
// read-only server collection.
func IsReadOnly(remoteAccess string) bool {
	return storage.CalendarReadOnly(remoteAccess)
}

// EnsureWritable rejects user-originated mutations that the linked remote
// collection cannot accept. Empty component metadata keeps legacy and direct
// links writable because those rows predate capability discovery.
func EnsureWritable(ctx context.Context, q *storage.Queries, calendarID int64, component string) error {
	cal, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		return err
	}
	if IsReadOnly(cal.RemoteAccess) {
		return fmt.Errorf("%w: %s", ErrReadOnly, cal.Name)
	}
	if storage.CalendarSupportsComponent(cal.RemoteComponents, component) {
		return nil
	}
	return fmt.Errorf("%w: %s does not advertise %s", ErrUnsupportedComponent, cal.Name, component)
}
