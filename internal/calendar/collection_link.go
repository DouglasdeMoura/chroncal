package calendar

import (
	"context"
	"fmt"
	"net/url"
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
// to lowercase, and it removes the trailing slash of the path. It returns ""
// for an empty remoteURL.
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

// FindCollectionLink returns a calendar of an account with the user username
// that links the collection remoteURL. A relative remoteURL resolves against
// serverURL. The second result is false when no such calendar exists. The
// usernames compare with no case.
//
// A different user can see the same collection with other rights, so the
// check names the user too.
func FindCollectionLink(ctx context.Context, q *storage.Queries, remoteURL, serverURL, username string, scope CollectionLinkScope) (storage.Calendar, bool, error) {
	key := CollectionKey(remoteURL, serverURL)
	if key == "" {
		return storage.Calendar{}, false, nil
	}
	accounts, err := q.ListAccounts(ctx)
	if err != nil {
		return storage.Calendar{}, false, fmt.Errorf("list accounts: %w", err)
	}
	byID := make(map[int64]storage.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	rows, err := q.ListCalendars(ctx)
	if err != nil {
		return storage.Calendar{}, false, fmt.Errorf("list calendars: %w", err)
	}
	username = strings.TrimSpace(username)
	for _, row := range rows {
		if row.AccountID == nil || row.ID == scope.CalendarID || *row.AccountID == scope.AccountID {
			continue
		}
		account, ok := byID[*row.AccountID]
		if !ok || !strings.EqualFold(strings.TrimSpace(account.Username), username) {
			continue
		}
		if CollectionKey(storage.NullableToString(row.RemoteUrl), account.ServerUrl) == key {
			return row, true, nil
		}
	}
	return storage.Calendar{}, false, nil
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
