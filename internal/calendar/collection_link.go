package calendar

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// ErrCollectionLinked reports a link of a remote collection that another
// local calendar links already (issue #874). Two links of one collection
// pull each item into two calendars, so each item shows twice. The user
// removes the other link first.
var ErrCollectionLinked = model.NewInputError(
	"another local calendar links this remote collection already; remove that link first")

// CollectionKey returns the identity of a remote collection. It resolves a
// relative remoteURL against serverURL. It changes the scheme and the host
// to lowercase, and it removes a default port and the user information. It
// cleans the path (dot segments, double slashes) and removes the trailing
// slash. It returns "" for an empty remoteURL. The account discovery and
// the calendar link use this one rule.
func CollectionKey(remoteURL, serverURL string) string {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return ""
	}
	ref, err := url.Parse(remoteURL)
	if err != nil {
		return strings.TrimRight(remoteURL, "/")
	}
	if base, baseErr := url.Parse(strings.TrimSpace(serverURL)); baseErr == nil && base.IsAbs() && !ref.IsAbs() {
		ref = base.ResolveReference(ref)
	}
	ref.Scheme = strings.ToLower(ref.Scheme)
	ref.Host = strings.ToLower(ref.Host)
	if port := ref.Port(); (ref.Scheme == "https" && port == "443") || (ref.Scheme == "http" && port == "80") {
		ref.Host = ref.Hostname()
	}
	ref.User = nil
	if ref.Path != "" {
		ref.Path = path.Clean(ref.Path)
	}
	ref.Path = strings.TrimRight(ref.Path, "/")
	ref.RawPath = ""
	return ref.String()
}

// CollectionLinkScope names the link that a check of a collection is for.
// The check skips the calendar CalendarID and the calendars of the account
// AccountID, because the caller links or relinks them. A zero ID skips
// nothing.
type CollectionLinkScope struct {
	CalendarID int64
	AccountID  int64
}

// collectionLink is one linked calendar of a CollectionLinks index.
type collectionLink struct {
	calendar  storage.Calendar
	accountID int64
	username  string
}

// CollectionLinks indexes the linked calendars by CollectionKey. A caller
// that checks many collections, such as the discovery, reads the calendars
// and the accounts one time.
type CollectionLinks struct {
	byKey map[string][]collectionLink
}

// LoadCollectionLinks reads the linked calendars and their accounts through q.
func LoadCollectionLinks(ctx context.Context, q *storage.Queries) (*CollectionLinks, error) {
	accounts, err := q.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	byID := make(map[int64]storage.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	rows, err := q.ListCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	links := &CollectionLinks{byKey: make(map[string][]collectionLink)}
	for _, row := range rows {
		if row.AccountID == nil {
			continue
		}
		account, ok := byID[*row.AccountID]
		if !ok {
			continue
		}
		key := CollectionKey(storage.NullableToString(row.RemoteUrl), account.ServerUrl)
		if key == "" {
			continue
		}
		links.byKey[key] = append(links.byKey[key], collectionLink{
			calendar: row, accountID: account.ID, username: strings.TrimSpace(account.Username),
		})
	}
	return links, nil
}

// Find returns a calendar of an account with the user username that links
// the collection remoteURL. A relative remoteURL resolves against serverURL.
// The second result is false when no such calendar exists. The usernames
// compare with no case.
//
// A different user can see the same collection with other rights, so the
// check names the user too.
func (l *CollectionLinks) Find(remoteURL, serverURL, username string, scope CollectionLinkScope) (storage.Calendar, bool) {
	username = strings.TrimSpace(username)
	for _, link := range l.byKey[CollectionKey(remoteURL, serverURL)] {
		if link.calendar.ID == scope.CalendarID || link.accountID == scope.AccountID {
			continue
		}
		if strings.EqualFold(link.username, username) {
			return link.calendar, true
		}
	}
	return storage.Calendar{}, false
}

// FindCollectionLink is LoadCollectionLinks and Find for one collection.
func FindCollectionLink(ctx context.Context, q *storage.Queries, remoteURL, serverURL, username string, scope CollectionLinkScope) (storage.Calendar, bool, error) {
	if CollectionKey(remoteURL, serverURL) == "" {
		return storage.Calendar{}, false, nil
	}
	links, err := LoadCollectionLinks(ctx, q)
	if err != nil {
		return storage.Calendar{}, false, err
	}
	other, linked := links.Find(remoteURL, serverURL, username, scope)
	return other, linked, nil
}

// collectionLinkedError is ErrCollectionLinked with the name of the other
// calendar.
type collectionLinkedError struct{ name string }

func (e *collectionLinkedError) Error() string {
	return fmt.Sprintf("the local calendar %q links this remote collection already; remove that link first", e.name)
}

func (e *collectionLinkedError) Is(target error) bool { return target == ErrCollectionLinked }

func (e *collectionLinkedError) InvalidInput() bool { return true }

// CheckCollectionFree returns ErrCollectionLinked, with the name of the other
// calendar, when FindCollectionLink finds a link of the collection.
func CheckCollectionFree(ctx context.Context, q *storage.Queries, remoteURL, serverURL, username string, scope CollectionLinkScope) error {
	other, linked, err := FindCollectionLink(ctx, q, remoteURL, serverURL, username, scope)
	if err != nil {
		return err
	}
	if linked {
		return &collectionLinkedError{name: other.Name}
	}
	return nil
}
