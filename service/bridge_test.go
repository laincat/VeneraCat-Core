package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBridgeRefusesSchemesAndUnknownHosts(t *testing.T) {
	b := NewBridge(BridgePolicy{Hosts: []string{"example.com"}})
	for _, raw := range []string{
		"file:///etc/passwd",
		"ftp://example.com/x",
		"http://127.0.0.1/x",
		"http://evilexample.com/x",
		"http://example.com.evil.test/x",
		"http:///no-host",
	} {
		if err := b.Allows(raw); err == nil {
			t.Errorf("%s was allowed", raw)
		}
	}
	for _, raw := range []string{"https://example.com/x", "https://api.example.com/x", "http://EXAMPLE.com/x"} {
		if err := b.Allows(raw); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
	}
}

func TestBridgeFetchesThroughThePolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie := r.Header.Get("Cookie"); cookie != "" {
			t.Errorf("the bridge sent cookies: %q", cookie)
		}
		w.Write([]byte("hello"))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	b := NewBridge(BridgePolicy{Hosts: []string{strings.Split(host, ":")[0]}})
	body, err := b.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if body != "hello" {
		t.Fatalf("body = %q", body)
	}
}

// A redirect is checked like the first request: a service cannot be bounced to
// a host the policy does not allow.
func TestBridgeChecksEveryRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer target.Close()

	// The redirect points at "localhost" while the policy allows only
	// "127.0.0.1": the same machine, a different host name, so the check is what
	// stops it.
	redirectTo := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTo, http.StatusFound)
	}))
	defer source.Close()

	sourceHost := strings.Split(strings.TrimPrefix(source.URL, "http://"), ":")[0]
	b := NewBridge(BridgePolicy{Hosts: []string{sourceHost}})
	if _, err := b.Fetch(context.Background(), source.URL); err == nil {
		t.Fatal("a redirect to a host outside the policy was followed")
	}
}

func TestBridgeBoundsTheResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer server.Close()

	b := NewBridge(BridgePolicy{
		Hosts:    []string{strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")[0]},
		MaxBytes: 1024,
	})
	if _, err := b.Fetch(context.Background(), server.URL); err == nil {
		t.Fatal("an oversized response was accepted")
	}
}

func TestBridgeReportsHTTPFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer server.Close()

	b := NewBridge(BridgePolicy{Hosts: []string{strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")[0]}})
	if _, err := b.Fetch(context.Background(), server.URL); err == nil {
		t.Fatal("a 403 was accepted")
	}
}

func TestBridgeStopsOnCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(700 * time.Millisecond)
		w.Write([]byte("late"))
	}))
	defer server.Close()

	b := NewBridge(BridgePolicy{Hosts: []string{strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")[0]}})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := b.Fetch(ctx, server.URL); err == nil {
		t.Fatal("a cancelled request returned a body")
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("took %s", elapsed)
	}
}

// The bridge is what a Runtime uses, so a service cannot go around it.
func TestBridgeHostDrivesTheRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("through-the-bridge"))
	}))
	defer server.Close()

	bridge := NewBridge(BridgePolicy{Hosts: []string{strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")[0]}})
	rt := NewRuntime(bridge.Host(Host{}), PermissionNetwork)
	defer rt.Close()
	source := `
export function handle() {
  if (fetch('` + server.URL + `').includes('through-the-bridge')) return { ok: true };
  return { ok: false };
}
`
	if err := rt.Load(context.Background(), manifestFor(t, PermissionNetwork), source); err != nil {
		t.Fatal(err)
	}
	out, err := rt.Call(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("result = %s", out)
	}
}

func TestBridgeHostRefusesAnUnlistedURLInsideAService(t *testing.T) {
	bridge := NewBridge(BridgePolicy{Hosts: []string{"example.com"}})
	rt := NewRuntime(bridge.Host(Host{}), PermissionNetwork)
	defer rt.Close()
	source := `export function handle() { return { body: fetch('http://127.0.0.1/secret') }; }`
	if err := rt.Load(context.Background(), manifestFor(t, PermissionNetwork), source); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Call(context.Background(), "go", nil); err == nil {
		t.Fatal("a service reached a host outside the policy")
	} else if !strings.Contains(err.Error(), "allowed host") {
		t.Fatalf("error = %v", err)
	}
}
