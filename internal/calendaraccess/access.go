package calendaraccess

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

var (
	ErrReadOnly             = errors.New("calendar is read-only")
	ErrUnsupportedComponent = errors.New("calendar does not support this component")
)

// EnsureWritable rejects user-originated mutations that the linked remote
// collection cannot accept. Empty component metadata keeps legacy and direct
// links writable because those rows predate capability discovery.
func EnsureWritable(ctx context.Context, q *storage.Queries, calendarID int64, component string) error {
	cal, err := q.GetCalendar(ctx, calendarID)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(cal.RemoteAccess), "read") {
		return fmt.Errorf("%w: %s", ErrReadOnly, cal.Name)
	}
	components := strings.TrimSpace(cal.RemoteComponents)
	if components == "" || strings.TrimSpace(component) == "" {
		return nil
	}
	for _, advertised := range strings.Split(components, ",") {
		if strings.EqualFold(strings.TrimSpace(advertised), component) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s does not advertise %s", ErrUnsupportedComponent, cal.Name, component)
}
