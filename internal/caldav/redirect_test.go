package caldav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDefaultHTTPClientStripsAuthOnCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var attackerAuth atomic.Value // string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerAuth.Store(r.Header.Get("Authorization"))
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
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got, _ := attackerAuth.Load().(string); got != "" {
		t.Fatalf("cross-host redirect leaked Authorization header = %q, want empty", got)
	}
}

func TestBearerAuthClientStripsAuthOnCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var attackerAuth atomic.Value // string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerAuth.Store(r.Header.Get("Authorization"))
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
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got, _ := attackerAuth.Load().(string); got != "" {
		t.Fatalf("cross-host redirect leaked bearer Authorization header = %q, want empty", got)
	}
}

func TestBasicAuthClientStripsAuthOnCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var attackerAuth atomic.Value // string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerAuth.Store(r.Header.Get("Authorization"))
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
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if got, _ := attackerAuth.Load().(string); got != "" {
		t.Fatalf("cross-host redirect leaked basic Authorization header = %q, want empty", got)
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
		t.Fatal("defaultHTTPClient.CheckRedirect = nil, want a policy that strips credentials on cross-host redirects")
	}
}
