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
// when an account that the user added has the server origin of serverURL,
// the user of link, and the auth type of link. A hidden account of a
// calendar link does not count: each calendar link keeps its own. When the
// account of cal has the login already, the link adds no login, and the
// check passes.
func checkLoginFree(ctx context.Context, q *storage.Queries, cal Calendar, link RemoteLink, serverURL string) error {
	origin := serverOrigin(serverURL)
	if origin == "" {
		return nil
	}
	accounts, err := q.ListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	user := strings.TrimSpace(link.Username)
	authType := NormalizeAuthType(link.AuthType)
	sameLogin := func(a storage.Account) bool {
		return serverOrigin(a.ServerUrl) == origin &&
			strings.EqualFold(strings.TrimSpace(a.Username), user) &&
			NormalizeAuthType(a.AuthType) == authType
	}
	for _, a := range accounts {
		if a.ID == cal.AccountID && sameLogin(a) {
			return nil
		}
	}
	for _, a := range accounts {
		if !strings.HasPrefix(a.Name, hiddenAccountPrefix) && sameLogin(a) {
			return &loginAccountError{name: a.Name}
		}
	}
	return nil
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
