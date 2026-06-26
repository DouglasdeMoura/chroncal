package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrSyncTokenInvalid signals that the server rejected the supplied sync
// token (RFC 6578 §3.6 valid-sync-token precondition). Callers should clear
// their stored token and retry with an empty token to receive a full
// snapshot plus a fresh token.
var ErrSyncTokenInvalid = errors.New("caldav: sync-token invalid, full resync required")

// ErrSyncCollectionUnsupported signals the server does not implement RFC 6578.
// Callers should fall back to a full QueryAll snapshot.
var ErrSyncCollectionUnsupported = errors.New("caldav: sync-collection not supported")

// SyncChange describes one changed or removed resource returned by a
// sync-collection REPORT. Deleted entries carry only Path; updated entries
// carry Path and ETag (no body — fetch with GetResources).
type SyncChange struct {
	Path    string
	ETag    string
	Deleted bool
}

// SyncCollectionResult holds the parsed multistatus from a sync-collection
// REPORT. SyncToken is the new token to store for the next incremental sync.
//
// Truncated reports the RFC 6578 §3.6 marker: the server limited the result
// set (Google pages large initial snapshots) and flagged it with a
// <response> for the collection itself carrying 507 Insufficient Storage.
// SyncToken then represents only the PARTIAL state — callers must repeat
// the REPORT with it to fetch the rest, and must never treat a truncated
// change list as a complete inventory (diffing local state against a
// partial page is how events get wrongly deleted).
type SyncCollectionResult struct {
	SyncToken string
	Changes   []SyncChange
	Truncated bool
}

// SyncCollection runs an RFC 6578 sync-collection REPORT against the calendar
// at calendarPath. Pass an empty syncToken on the very first call to receive
// a full snapshot of hrefs+etags plus a fresh token; subsequent calls return
// only resources changed since the supplied token.
//
// The body returned only contains hrefs + etags. Use GetResources for the
// updated paths to download bodies.
func (c *Client) SyncCollection(ctx context.Context, calendarPath string, syncToken string) (*SyncCollectionResult, error) {
	canonicalPath, err := c.CanonicalCollectionRef(calendarPath)
	if err != nil {
		return nil, err
	}

	body := buildSyncCollectionBody(syncToken)
	req, err := http.NewRequestWithContext(ctx, "REPORT", c.ResolveURL(canonicalPath), strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new REPORT request: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusMultiStatus, http.StatusOK:
		// happy path — fall through to parsing.
	case http.StatusForbidden, http.StatusConflict:
		// RFC 6578 §3.6: server returns 403 with a <DAV:valid-sync-token>
		// precondition when the supplied token is no longer recognized.
		// A few servers use 409 instead. Sniff the body for the precondition
		// element so we don't misclassify other 403s.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if bytes.Contains(raw, []byte("valid-sync-token")) {
			return nil, ErrSyncTokenInvalid
		}
		return nil, statusErrorf(resp.StatusCode, "REPORT sync-collection: HTTP %d", resp.StatusCode)
	case http.StatusBadRequest, http.StatusMethodNotAllowed,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity,
		http.StatusNotImplemented:
		// Server doesn't grok sync-collection — saw GMX (Cosmo-derived)
		// return 422 here; other servers return 400/405/415/501. Caller
		// should fall back to a full QueryAll.
		return nil, ErrSyncCollectionUnsupported
	default:
		return nil, fmt.Errorf("REPORT sync-collection: %w", httpError(resp))
	}

	var ms syncCollectionMultiStatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("decode sync-collection response: %w", err)
	}

	result := &SyncCollectionResult{SyncToken: strings.TrimSpace(ms.SyncToken)}
	for _, r := range ms.Responses {
		href := strings.TrimSpace(r.Href)
		if href == "" {
			continue
		}
		// Top-level <status>404</status> marks deletions; updates carry their
		// 200 status inside <propstat> alongside <getetag>.
		switch code := parseStatusCode(r.Status); {
		case code == http.StatusNotFound:
			result.Changes = append(result.Changes, SyncChange{Path: href, Deleted: true})
			continue
		case code == http.StatusInsufficientStorage:
			// RFC 6578 §3.6 truncation marker (the href is the collection
			// itself). Not a change — record and skip, or it pollutes the
			// multiget fetch list.
			result.Truncated = true
			continue
		case code != 0 && (code < 200 || code >= 300):
			// Any other non-2xx top-level status isn't a resource change.
			continue
		}
		var etag string
		for _, ps := range r.PropStats {
			code := parseStatusCode(ps.Status)
			if code >= 200 && code < 300 {
				etag = normalizeETag(ps.Prop.ETag)
				break
			}
		}
		result.Changes = append(result.Changes, SyncChange{Path: href, ETag: etag})
	}
	return result, nil
}

func buildSyncCollectionBody(syncToken string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:sync-collection xmlns:d="DAV:">
  <d:sync-token>%s</d:sync-token>
  <d:sync-level>1</d:sync-level>
  <d:prop>
    <d:getetag/>
  </d:prop>
</d:sync-collection>`, xmlEscape(syncToken))
}

type syncCollectionMultiStatus struct {
	XMLName   xml.Name                 `xml:"DAV: multistatus"`
	Responses []syncCollectionResponse `xml:"DAV: response"`
	SyncToken string                   `xml:"DAV: sync-token"`
}

type syncCollectionResponse struct {
	Href      string                   `xml:"DAV: href"`
	Status    string                   `xml:"DAV: status"`
	PropStats []syncCollectionPropStat `xml:"DAV: propstat"`
}

type syncCollectionPropStat struct {
	Status string `xml:"DAV: status"`
	Prop   struct {
		ETag string `xml:"DAV: getetag"`
	} `xml:"DAV: prop"`
}
