package calendar

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// ErrLoginHasAccount reports a link of a local calendar with the server, the
// user, and the auth type of an account that the user added (issue #917).
// Such a link made a second account for the same login. The user adds the
// collection through that account instead. Connect does not reuse the
// account: a removal of the account would then unlink the calendar, and a
// change of its calendar selection could delete the calendar.
var ErrLoginHasAccount = model.NewInputError(
	"an account logs in to this server as this user already; add the calendar through that account")

// loginAccountError is ErrLoginHasAccount with the name of the account.
type loginAccountError struct{ name string }

func (e *loginAccountError) Error() string {
	return fmt.Sprintf("the account %q logs in to this server as this user already; "+
		"add the collection through it (chroncal account calendars add %q --calendar NAME)", e.name, e.name)
}

func (e *loginAccountError) Is(target error) bool { return target == ErrLoginHasAccount }

func (e *loginAccountError) InvalidInput() bool { return true }

// checkLoginFree returns ErrLoginHasAccount, with the name of the account,
// when an account that the user added has the login of link: the server
// origin of serverURL, the user, and the auth type. A hidden account of a
// calendar link does not count: each calendar link keeps its own. Connect
// never keeps an account that the user added, so the account of the
// calendar does not count as an exception.
func checkLoginFree(ctx context.Context, q *storage.Queries, link RemoteLink, serverURL string) error {
	if serverOrigin(serverURL) == "" {
		return nil
	}
	accounts, err := q.ListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	for _, a := range accounts {
		if !strings.HasPrefix(a.Name, hiddenAccountPrefix) && sameLogin(a, link, serverURL) {
			return &loginAccountError{name: a.Name}
		}
	}
	return nil
}

// checkRelinkLoginFree runs checkLoginFree for a relink that keeps the
// hidden account existing. A relink with the login of that account adds no
// login, for example a new credential, so it passes. A relink that changes
// the login gets the check of a new link.
func checkRelinkLoginFree(ctx context.Context, q *storage.Queries, existing storage.Account, link RemoteLink, serverURL string) error {
	if sameLogin(existing, link, serverURL) {
		return nil
	}
	return checkLoginFree(ctx, q, link, serverURL)
}

// sameLogin reports whether account a has the login of link: the server
// origin of serverURL, the user (case and outer spaces ignored), and the
// auth type.
func sameLogin(a storage.Account, link RemoteLink, serverURL string) bool {
	origin := serverOrigin(serverURL)
	return origin != "" && serverOrigin(a.ServerUrl) == origin &&
		strings.EqualFold(strings.TrimSpace(a.Username), strings.TrimSpace(link.Username)) &&
		NormalizeAuthType(a.AuthType) == NormalizeAuthType(link.AuthType)
}

// serverOrigin returns the scheme and the host of raw in lowercase, with no
// default port. It returns "" for a URL that does not parse.
func serverOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}
