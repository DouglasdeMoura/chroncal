package sync

// Calendar metadata (color/name) synchronization. This step is neither
// export, push, nor pull, so it lives outside those files.

import (
	"context"
	"fmt"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

func (e *Engine) syncCalendarMetadata(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) error {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("get calendar for metadata sync: %w", err)
	}

	// Google CalendarList already supplies the color at discovery. Apple
	// calendar-color is not a Google CalDAV property, so never PROPFIND
	// it. A dirty local color still has to be written: CalendarList PATCH
	// is the Google equivalent of Apple calendar-color PROPPATCH. Clearing
	// ColorDirty after a successful write lets later Discover rounds adopt
	// CalendarList again. A failed write must not fail event sync
	// (issue #628); the dirty latch then keeps the local override.
	if caldav.IsGoogleCalendarEndpoint(remoteURL) {
		if cal.ColorDirty == 0 {
			return nil
		}
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.SetGoogleCalendarListColor(ctx, remoteURL, cal.Color)
		}); err != nil {
			e.logger.Warn("set google calendar color failed", "calendar_id", calendarID, "error", err)
			return nil
		}
		if err := e.calendars.ClearColorDirty(ctx, calendarID, cal.Color); err != nil {
			return fmt.Errorf("clear calendar color dirty: %w", err)
		}
		return nil
	}

	// A dirty local color wins: push it and clear the flag. Skip the remote
	// fetch entirely — its value would be discarded, and a failed fetch must
	// not block the pending push or strand ColorDirty (issue #419).
	if cal.ColorDirty != 0 {
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.SetCalendarColor(ctx, remoteURL, cal.Color)
		}); err != nil {
			return fmt.Errorf("set remote calendar color: %w", err)
		}
		if err := e.calendars.ClearColorDirty(ctx, calendarID, cal.Color); err != nil {
			return fmt.Errorf("clear calendar color dirty: %w", err)
		}
		return nil
	}

	remoteColor, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
		return client.GetCalendarColor(ctx, remoteURL)
	})
	if err != nil {
		// Color is decorative. A server that refuses calendar-color must
		// not fail the rest of the calendar sync (issue #628).
		e.logger.Warn("get remote calendar color failed", "calendar_id", calendarID, "error", err)
		return nil
	}
	if remoteColor == "" {
		// The server does not advertise a color. Keep the color that
		// discovery or the user already set.
		return nil
	}

	if remoteColor != storage.NullableToString(cal.RemoteColor) {
		if err := e.calendars.UpdateColorFromSync(ctx, calendarID, remoteColor, remoteColor); err != nil {
			return fmt.Errorf("update calendar color from sync: %w", err)
		}
	}

	return nil
}
