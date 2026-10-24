package calendar

import (
	"context"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestCollectionKeyNormalizesOneCollection gives one collection in several
// spellings. Each spelling must give the same key, or a second link of the
// collection passes the check (issue #874).
func TestCollectionKeyNormalizesOneCollection(t *testing.T) {
	want := CollectionKey("https://cal.example.com/dav/work/", "")
	for _, spelling := range []string{
		"https://CAL.example.com/dav/work",
		"HTTPS://cal.example.com:443/dav/work/",
		"https://alice@cal.example.com/dav/work/",
		"https://cal.example.com/dav/./x/../work/",
		"https://cal.example.com//dav//work/",
	} {
		if got := CollectionKey(spelling, ""); got != want {
			t.Errorf("CollectionKey(%q) = %q, want %q", spelling, got, want)
		}
	}
	if got := CollectionKey("/dav/work/", "http://cal.example.com:80/"); got != "http://cal.example.com/dav/work" {
		t.Errorf("relative key = %q; want the path on the server without the default port", got)
	}
	if got := CollectionKey("https://cal.example.com:8443/dav/work/", ""); got == want {
		t.Errorf("a key with another port = %q; want a different key", got)
	}
}

// TestConnect_RelinksACalendarOfAnOlderDoubleLink makes the double link of
// a collection that chroncal allowed before issue #874. A relink of one of
// the two calendars to its own collection changes only the credential, so
// it must still pass.
func TestConnect_RelinksACalendarOfAnOlderDoubleLink(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const url = "https://example.com/dav/calendars/work/"
	second, err := svc.Create(ctx, "Second", "#00ff00", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, id := range []int64{1, second.ID} {
		account, err := svc.q.CreateAccount(ctx, storage.CreateAccountParams{
			Name: hiddenAccountName(id), ServerUrl: "https://example.com", AuthType: "basic", Username: "user",
		})
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
		if err := svc.q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
			ID: id, AccountID: &account.ID, RemoteUrl: storage.StringToNullable(url),
		}); err != nil {
			t.Fatalf("LinkCalendarToAccount: %v", err)
		}
	}
	cal, err := svc.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	link := RemoteLink{RemoteURL: url, Username: "user", AuthType: "basic"}
	if err := svc.Connect(ctx, cal, link, auth.Credential{Username: "user", Password: "new"}, &memCredStore{}); err != nil {
		t.Fatalf("relink of the same collection = %v; want nil", err)
	}
	// A link of a third calendar still refuses.
	third, err := svc.Create(ctx, "Third", "#0000ff", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Connect(ctx, third, link, auth.Credential{Username: "user", Password: "pass"}, &memCredStore{}); !errors.Is(err, ErrCollectionLinked) {
		t.Fatalf("Connect third = %v; want ErrCollectionLinked", err)
	}
}
