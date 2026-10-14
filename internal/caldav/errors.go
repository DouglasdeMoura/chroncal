package caldav

import (
	"bytes"
	"encoding/xml"
	"errors"

	"github.com/douglasdemoura/chroncal/internal/retry"
)

// IsTransient delegates to retry.IsTransient.
func IsTransient(err error) bool { return retry.IsTransient(err) }

// IsConflict delegates to retry.IsConflict.
func IsConflict(err error) bool { return retry.IsConflict(err) }

// IsNotFound reports whether err carries the HTTP status 404.
func IsNotFound(err error) bool { return retry.StatusCode(err) == 404 }

// IsAbsent reports whether err says that the resource does not exist: the
// HTTP status 404 Not Found or 410 Gone.
func IsAbsent(err error) bool {
	code := retry.StatusCode(err)
	return code == 404 || code == 410
}

// errUIDConflict marks a PUT that the server refused with the
// CALDAV:no-uid-conflict precondition. See IsUIDConflict.
var errUIDConflict = errors.New("caldav: the server holds another object with this UID")

// IsUIDConflict reports whether err is a PUT that the server refused
// because another object holds the same UID. RFC 4791 section 5.3.2.1
// answers that case with 403 and the CALDAV:no-uid-conflict precondition.
// A plain 403 does not count.
func IsUIDConflict(err error) bool { return errors.Is(err, errUIDConflict) }

// hasNoUIDConflict reports whether an error body holds the
// CALDAV:no-uid-conflict precondition element.
func hasNoUIDConflict(body []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if start, ok := tok.(xml.StartElement); ok &&
			start.Name.Space == "urn:ietf:params:xml:ns:caldav" && start.Name.Local == "no-uid-conflict" {
			return true
		}
	}
}
