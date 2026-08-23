package caldav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDefaultHTTPClientAbortsCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var attackerHits atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer attacker.Close()

	legit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/catch", http.StatusFound)
	}))
	defer legit.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, legit.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.SetBasicAuth("alice", "s3cr3t")

	resp, err := defaultHTTPClient.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("Do err = nil, want a cross-host redirect abort")
	}
	if !strings.Contains(err.Error(), "refuse redirect") {
		t.Fatalf("Do err = %v, want a refuse-redirect error", err)
	}
	if n := attackerHits.Load(); n != 0 {
		t.Fatalf("cross-host redirect sent %d requests to the other host, want 0", n)
	}
}

func TestBearerAuthClientAbortsCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var attackerHits atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer attacker.Close()

	legit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/catch", http.StatusFound)
	}))
	defer legit.Close()

	client, err := NewBearerAuthClient(legit.URL, "token")
	if err != nil {
		t.Fatalf("NewBearerAuthClient: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, legit.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := client.HTTPClient().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("Do err = nil, want a cross-host redirect abort")
	}
	if !strings.Contains(err.Error(), "refuse redirect") {
		t.Fatalf("Do err = %v, want a refuse-redirect error", err)
	}
	if n := attackerHits.Load(); n != 0 {
		t.Fatalf("cross-host redirect sent %d requests to the other host, want 0", n)
	}
}

func TestBasicAuthClientAbortsCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var attackerHits atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer attacker.Close()

	legit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/catch", http.StatusFound)
	}))
	defer legit.Close()

	client, err := NewBasicAuthClient(legit.URL, "alice", "s3cr3t")
	if err != nil {
		t.Fatalf("NewBasicAuthClient: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, legit.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := client.HTTPClient().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("Do err = nil, want a cross-host redirect abort")
	}
	if !strings.Contains(err.Error(), "refuse redirect") {
		t.Fatalf("Do err = %v, want a refuse-redirect error", err)
	}
	if n := attackerHits.Load(); n != 0 {
		t.Fatalf("cross-host redirect sent %d requests to the other host, want 0", n)
	}
}

func TestDefaultHTTPClientKeepsAuthOnSameHostRedirect(t *testing.T) {
	t.Parallel()

	var seenAuth atomic.Value // string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/step2", http.StatusFound)
			return
		}
		seenAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.SetBasicAuth("alice", "s3cr3t")

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got, _ := seenAuth.Load().(string); got == "" {
		t.Fatal("same-host redirect stripped Authorization header, want it preserved")
	}
}

func TestDefaultHTTPClientCapsRedirects(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.String(), http.StatusFound)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := defaultHTTPClient.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("Do err = nil, want redirect-cap error")
	}
}

func TestDefaultHTTPClientHasRedirectPolicy(t *testing.T) {
	t.Parallel()

	if defaultHTTPClient.CheckRedirect == nil {
		t.Fatal("defaultHTTPClient.CheckRedirect = nil, want a policy that aborts cross-host redirects")
	}
}

func TestSameRedirectHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"same host", "https://caldav.example.com/", "https://caldav.example.com/calendar/", true},
		{"host case differs", "https://CalDAV.example.com/", "https://caldav.example.com/", true},
		{"subdomain counts as different host", "https://caldav.example.com/", "https://evil.caldav.example.com/", false},
		{"different registrable domain", "https://caldav.example.com/", "https://caldav.example.org/", false},
		{"port differs", "https://caldav.example.com:8443/", "https://caldav.example.com/", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, err := url.Parse(tc.a)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.a, err)
			}
			b, err := url.Parse(tc.b)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.b, err)
			}
			if got := sameRedirectHost(a, b); got != tc.want {
				t.Fatalf("sameRedirectHost(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
