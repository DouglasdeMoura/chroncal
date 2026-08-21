package caldav

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/auth"
)

const googleCalDAVBaseURL = "https://apidata.googleusercontent.com/caldav/v2"

var googleCalendarListURL = "https://www.googleapis.com/calendar/v3/users/me/calendarList"

type googleCalendarListResponse struct {
	NextPageToken string                   `json:"nextPageToken"`
	Items         []googleCalendarListItem `json:"items"`
}

type googleCalendarListItem struct {
	ID              string `json:"id"`
	Summary         string `json:"summary"`
	SummaryOverride string `json:"summaryOverride"`
	Description     string `json:"description"`
	BackgroundColor string `json:"backgroundColor"`
	AccessRole      string `json:"accessRole"`
	Deleted         bool   `json:"deleted"`
}

// IsGoogleCalendarEndpoint identifies Google's CalDAV host. Google exposes
// calendar resources over CalDAV but does not implement principal/home-set
// collection discovery there; collection inventory comes from CalendarList.
func IsGoogleCalendarEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(parsed.Hostname(), "apidata.googleusercontent.com")
}

// DiscoverGoogleCalendars lists every calendar in the authenticated user's
// Google CalendarList and maps each stable calendar ID to its CalDAV v2 event
// collection URL. The same OAuth credential and refresh path used by CalDAV
// requests backs the REST lookup, so a refresh is persisted once per account.
func DiscoverGoogleCalendars(ctx context.Context, cred auth.Credential, persist func(auth.Credential) error) ([]RemoteCalendar, error) {
	httpClient, err := httpClientFromCredential(cred, persist)
	if err != nil {
		return nil, err
	}

	calendars := make([]RemoteCalendar, 0)
	seenTokens := map[string]struct{}{"": {}}
	pageToken := ""
	for {
		page, err := fetchGoogleCalendarListPage(ctx, httpClient, pageToken)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.Deleted || strings.TrimSpace(item.ID) == "" {
				continue
			}
			name := strings.TrimSpace(item.SummaryOverride)
			if name == "" {
				name = strings.TrimSpace(item.Summary)
			}
			calendars = append(calendars, RemoteCalendar{
				Path:        googleCalDAVCollectionURL(item.ID),
				Name:        name,
				Description: strings.TrimSpace(item.Description),
				Color:       NormalizeCalendarColor(item.BackgroundColor),
				Access:      googleCalendarAccess(item.AccessRole),
				// A freeBusyReader can only query availability, never actual
				// events, so it must not be offered for VEVENT import.
				SupportedComponentSet: googleSupportedComponents(item.AccessRole),
			})
		}

		pageToken = strings.TrimSpace(page.NextPageToken)
		if pageToken == "" {
			return calendars, nil
		}
		if _, duplicate := seenTokens[pageToken]; duplicate {
			return nil, fmt.Errorf("google CalendarList repeated page token %q", pageToken)
		}
		seenTokens[pageToken] = struct{}{}
	}
}

type googleHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func fetchGoogleCalendarListPage(ctx context.Context, client googleHTTPClient, pageToken string) (googleCalendarListResponse, error) {
	return Retry(ctx, RetryOptions{MaxAttempts: 3}, func(ctx context.Context) (googleCalendarListResponse, error) {
		endpoint, err := url.Parse(googleCalendarListURL)
		if err != nil {
			return googleCalendarListResponse{}, fmt.Errorf("parse Google CalendarList URL: %w", err)
		}
		query := endpoint.Query()
		query.Set("maxResults", "250")
		query.Set("showHidden", "true")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		endpoint.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return googleCalendarListResponse{}, fmt.Errorf("create Google CalendarList request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return googleCalendarListResponse{}, fmt.Errorf("google CalendarList request: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return googleCalendarListResponse{}, fmt.Errorf("google CalendarList: %w", httpError(resp))
		}

		limited := &io.LimitedReader{R: resp.Body, N: maxHTTPResponseBytes + 1}
		body, err := io.ReadAll(limited)
		if err != nil {
			return googleCalendarListResponse{}, fmt.Errorf("read Google CalendarList response: %w", err)
		}
		if int64(len(body)) > maxHTTPResponseBytes {
			return googleCalendarListResponse{}, errResponseTooLarge
		}
		var page googleCalendarListResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return googleCalendarListResponse{}, fmt.Errorf("decode Google CalendarList response: %w", err)
		}
		return page, nil
	})
}

func googleCalDAVCollectionURL(calendarID string) string {
	return googleCalDAVBaseURL + "/" + url.PathEscape(strings.TrimSpace(calendarID)) + "/events"
}

// googleCalendarIDFromCollectionURL reverses googleCalDAVCollectionURL.
func googleCalendarIDFromCollectionURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if !IsGoogleCalendarEndpoint(raw) {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "caldav" || parts[1] != "v2" || parts[len(parts)-1] != "events" {
		return "", false
	}
	id := strings.TrimSpace(strings.Join(parts[2:len(parts)-1], "/"))
	if id == "" {
		return "", false
	}
	return id, true
}

type googleCalendarListColorPatch struct {
	BackgroundColor string `json:"backgroundColor"`
	ForegroundColor string `json:"foregroundColor"`
}

func googleCalendarListForeground(background string) string {
	hex := strings.TrimPrefix(NormalizeCalendarColor(background), "#")
	if len(hex) != 6 {
		return "#ffffff"
	}
	var r, g, b int
	if _, err := fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b); err != nil {
		return "#ffffff"
	}
	if 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b) > 140 {
		return "#000000"
	}
	return "#ffffff"
}

// SetGoogleCalendarListColor writes a calendar's display color through the
// Calendar JSON API. Google CalDAV does not implement Apple calendar-color.
func SetGoogleCalendarListColor(ctx context.Context, client googleHTTPClient, calendarURL, color string) error {
	calendarID, ok := googleCalendarIDFromCollectionURL(calendarURL)
	if !ok {
		return fmt.Errorf("google calendar id: not a Google CalDAV collection URL")
	}
	color = NormalizeCalendarColor(color)
	if color == "" {
		return fmt.Errorf("google calendar color is empty")
	}

	endpoint, err := url.Parse(googleCalendarListURL)
	if err != nil {
		return fmt.Errorf("parse Google CalendarList URL: %w", err)
	}
	if endpoint.Path == "" {
		endpoint.Path = "/"
	}
	endpoint = endpoint.JoinPath(calendarID)
	query := endpoint.Query()
	query.Set("colorRgbFormat", "true")
	endpoint.RawQuery = query.Encode()

	body, err := json.Marshal(googleCalendarListColorPatch{
		BackgroundColor: color,
		ForegroundColor: googleCalendarListForeground(color),
	})
	if err != nil {
		return fmt.Errorf("encode Google CalendarList color: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Google CalendarList color request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("google CalendarList color request: %w", err)
	}
	defer resp.Body.Close()
	if resp.Request == nil {
		resp.Request = req
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("google CalendarList color: %w", httpError(resp))
	}
	return nil
}

// SetGoogleCalendarListColor writes the calendar's display color through the
// Calendar JSON API using this client's credentials.
func (c *Client) SetGoogleCalendarListColor(ctx context.Context, calendarURL, color string) error {
	return SetGoogleCalendarListColor(ctx, c.httpClient, calendarURL, color)
}

func googleCalendarAccess(role string) CalendarAccess {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return CalendarAccessOwner
	case "writer":
		return CalendarAccessWrite
	case "reader", "freebusyreader":
		return CalendarAccessRead
	default:
		return CalendarAccessUnknown
	}
}

// googleSupportedComponents reports the iCalendar components a Google calendar
// exposes. VFREEBUSY is explicit rather than an empty set. Empty
// capability metadata means "unknown; allow legacy imports" to account
// discovery. freeBusyReader is known not to expose event resources.
func googleSupportedComponents(role string) []string {
	if strings.EqualFold(strings.TrimSpace(role), "freeBusyReader") {
		return []string{"VFREEBUSY"}
	}
	return []string{"VEVENT"}
}
