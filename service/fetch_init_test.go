package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/laincat/veneracat-core/host"
)

type fakeServiceTransport struct {
	requests []*host.Request
	fn       func(ctx context.Context, req *host.Request) (*host.Response, error)
}

func (f *fakeServiceTransport) Do(ctx context.Context, req *host.Request) (*host.Response, error) {
	f.requests = append(f.requests, req)
	return f.fn(ctx, req)
}

func TestFetchInitForwardsMethodHeadersBodyAndStatus(t *testing.T) {
	var seen []FetchRequest
	h := Host{
		FetchRequest: func(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
			seen = append(seen, req)
			switch req.Method {
			case http.MethodPost:
				return &FetchResponse{
					Status:  http.StatusCreated,
					OK:      true,
					Body:    `{"created":true}`,
					Headers: map[string]string{"Content-Type": "application/json", "X-RateLimit": "42"},
				}, nil
			case http.MethodPatch:
				return &FetchResponse{
					Status:  http.StatusUnauthorized,
					OK:      false,
					Body:    `{"error":"expired"}`,
					Headers: map[string]string{"WWW-Authenticate": "Bearer"},
				}, nil
			case http.MethodPut:
				return &FetchResponse{
					Status:  http.StatusForbidden,
					OK:      false,
					Body:    `{"error":"denied"}`,
					Headers: map[string]string{},
				}, nil
			case http.MethodDelete:
				return &FetchResponse{
					Status:  http.StatusNotFound,
					OK:      false,
					Body:    `{"error":"missing"}`,
					Headers: map[string]string{},
				}, nil
			default:
				return &FetchResponse{Status: http.StatusOK, OK: true, Body: "ok"}, nil
			}
		},
	}

	const src = `
export function handle(method) {
  if (method === 'post') {
    return fetch('https://api.trakt.tv/scrobble/start', {
      method: 'POST',
      headers: {
        Authorization: 'Bearer tok-1',
        'User-Agent': 'VeneraCat/0.1.0',
        'trakt-api-key': 'cid-1',
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ progress: 50 }),
    });
  }
  if (method === 'patch') {
    return fetch('https://api.bgm.tv/v0/users/-/collections/1/episodes', {
      method: 'PATCH',
      headers: { Authorization: 'Bearer bad' },
      body: '{"type":2}',
    });
  }
  if (method === 'put') {
    return fetch('https://api.bgm.tv/v0/users/-/collections/-/episodes/9', {
      method: 'PUT',
      headers: { Authorization: 'Bearer nope' },
      body: '{"type":2}',
    });
  }
  if (method === 'delete') {
    return fetch('https://api.example.test/items/1', { method: 'DELETE' });
  }
  if (method === 'compat') {
    return { body: fetch('https://api.example.test/legacy') };
  }
  return null;
}
`
	r := NewRuntime(h, PermissionNetwork)
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t, PermissionNetwork), src); err != nil {
		t.Fatal(err)
	}

	callObj := func(method string) struct {
		Status  int               `json:"status"`
		OK      bool              `json:"ok"`
		Body    string            `json:"body"`
		Headers map[string]string `json:"headers"`
	} {
		t.Helper()
		raw, err := r.Call(context.Background(), method, nil)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var out struct {
			Status  int               `json:"status"`
			OK      bool              `json:"ok"`
			Body    string            `json:"body"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s unmarshal %s: %v", method, raw, err)
		}
		return out
	}

	postRes := callObj("post")
	if postRes.Status != 201 || !postRes.OK || postRes.Body != `{"created":true}` || postRes.Headers["x-ratelimit"] != "42" {
		t.Fatalf("post = %+v", postRes)
	}
	if len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].Headers["Authorization"] != "Bearer tok-1" ||
		seen[0].Headers["trakt-api-key"] != "cid-1" || seen[0].Headers["User-Agent"] != "VeneraCat/0.1.0" ||
		seen[0].Headers["Content-Type"] != "application/json" || seen[0].Body != `{"progress":50}` {
		t.Fatalf("captured post request = %+v", seen)
	}

	patchRes := callObj("patch")
	if patchRes.Status != 401 || patchRes.OK || patchRes.Body != `{"error":"expired"}` {
		t.Fatalf("patch = %+v", patchRes)
	}
	putRes := callObj("put")
	if putRes.Status != 403 || putRes.OK || putRes.Body != `{"error":"denied"}` {
		t.Fatalf("put = %+v", putRes)
	}
	delRes := callObj("delete")
	if delRes.Status != 404 || delRes.OK || delRes.Body != `{"error":"missing"}` {
		t.Fatalf("delete = %+v", delRes)
	}

	// Single-arg fetch(url) remains string-returning even when Host only sets FetchRequest.
	compatRaw, err := r.Call(context.Background(), "compat", nil)
	if err != nil || string(compatRaw) != `{"body":"ok"}` {
		t.Fatalf("compat = %s, %v", compatRaw, err)
	}
}

func TestFetchInitRejectsMissingFetchRequestCookieAndInvalidArgs(t *testing.T) {
	var legacyCalls int
	legacyHost := Host{
		Fetch: func(ctx context.Context, url string) (string, error) {
			legacyCalls++
			return "legacy:" + url, nil
		},
	}
	const src = `
export function handle(method) {
  if (method === 'single') return fetch('https://example.test/one');
  if (method === 'getInit') return fetch('https://example.test/two', { method: 'GET' });
  if (method === 'post') return fetch('https://example.test/p', { method: 'POST', body: '{}' });
  if (method === 'headers') return fetch('https://example.test/h', { method: 'GET', headers: { Authorization: 'Bearer x' } });
  if (method === 'cookie') return fetch('https://example.test/c', { method: 'GET', headers: { Cookie: 'sid=1' } });
  if (method === 'badMethod') return fetch('https://example.test/m', { method: 'TRACE' });
  if (method === 'badInit') return fetch('https://example.test/i', 'not-an-object');
  return null;
}
`
	r := NewRuntime(legacyHost, PermissionNetwork)
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t, PermissionNetwork), src); err != nil {
		t.Fatal(err)
	}

	if out, err := r.Call(context.Background(), "single", nil); err != nil || string(out) != `"legacy:https://example.test/one"` {
		t.Fatalf("single = %s, %v", out, err)
	}
	if out, err := r.Call(context.Background(), "getInit", nil); err != nil || !strings.Contains(string(out), `"status":200`) || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("getInit = %s, %v", out, err)
	}
	for _, tc := range []struct {
		method  string
		wantSub string
	}{
		{"post", "does not support custom fetch"},
		{"headers", "does not support custom fetch"},
		{"cookie", "Cookie"},
		{"badMethod", "unsupported fetch method"},
		{"badInit", "must be an object"},
	} {
		if _, err := r.Call(context.Background(), tc.method, nil); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("%s: err = %v, want substring %q", tc.method, err, tc.wantSub)
		}
	}
	if legacyCalls != 2 {
		t.Fatalf("legacy Fetch called %d times, want 2 (rejected calls must not downgrade)", legacyCalls)
	}
}

func TestFetchInitNetworkErrorStillThrows(t *testing.T) {
	h := Host{
		FetchRequest: func(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
			return nil, errors.New("connection reset")
		},
	}
	r := NewRuntime(h, PermissionNetwork)
	defer r.Close()
	if err := r.Load(context.Background(), manifestFor(t, PermissionNetwork), `
export function handle() {
  return fetch('https://example.test/x', { method: 'POST', body: '{}' });
}
`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "go", nil); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("network error = %v", err)
	}
}

func TestBridgeFetchRequestThroughHostTransportAndHTTPClient(t *testing.T) {
	// 1. Bridge backed by host.Transport (no internal http.Client).
	tr := &fakeServiceTransport{
		fn: func(ctx context.Context, req *host.Request) (*host.Response, error) {
			if req.Cookies {
				t.Errorf("expected Cookies=false, got true")
			}
			if req.Partition != "service:trakt" {
				t.Errorf("partition = %q", req.Partition)
			}
			if req.Method == http.MethodPost && string(req.Body) == `{"a":1}` && req.Header.Get("Authorization") == "Bearer secret" && req.Header.Get("trakt-api-key") == "key1" {
				return &host.Response{
					Status:   http.StatusUnauthorized,
					Header:   http.Header{"X-Test": []string{"yes"}},
					Body:     []byte(`{"error":"invalid_token"}`),
					FinalURL: req.URL,
				}, nil
			}
			return &host.Response{Status: http.StatusOK, Body: []byte("ok"), FinalURL: req.URL}, nil
		},
	}
	tb := NewBridge(BridgePolicy{
		Hosts:     []string{"api.trakt.tv"},
		Transport: tr,
		Partition: "service:trakt",
	})
	rt := NewRuntime(tb.Host(Host{}), PermissionNetwork)
	defer rt.Close()
	if err := rt.Load(context.Background(), manifestFor(t, PermissionNetwork), `
export function handle() {
  return fetch('https://api.trakt.tv/scrobble/start', {
    method: 'POST',
    headers: { Authorization: 'Bearer secret', 'trakt-api-key': 'key1', 'Content-Type': 'application/json' },
    body: '{"a":1}',
  });
}
`); err != nil {
		t.Fatal(err)
	}
	out, err := rt.Call(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("bridge transport call: %v", err)
	}
	var got struct {
		Status  int               `json:"status"`
		OK      bool              `json:"ok"`
		Body    string            `json:"body"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	if got.Status != 401 || got.OK || got.Body != `{"error":"invalid_token"}` || got.Headers["x-test"] != "yes" {
		t.Fatalf("got = %+v", got)
	}

	// Redirected finalURL outside allowlist must be blocked even on Transport path.
	tr.fn = func(ctx context.Context, req *host.Request) (*host.Response, error) {
		return &host.Response{Status: 200, Body: []byte("secret"), FinalURL: "https://evil.test/steal"}, nil
	}
	if _, err := tb.FetchRequest(context.Background(), FetchRequest{Method: http.MethodGet, URL: "https://api.trakt.tv/ok"}); err == nil || !strings.Contains(err.Error(), "allowed host") {
		t.Fatalf("disallowed FinalURL err = %v", err)
	}

	// 2. Standalone Bridge backed by httptest.Server forwards PATCH + headers + body + 404.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPatch || r.Header.Get("Authorization") != "Bearer bgm" || string(body) != `{"type":2}` {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Bgm", "v0")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"title":"Not Found"}`))
	}))
	defer srv.Close()

	srvHost := strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0]
	sb := NewBridge(BridgePolicy{Hosts: []string{srvHost}})
	resp, err := sb.FetchRequest(context.Background(), FetchRequest{
		Method:  http.MethodPatch,
		URL:     srv.URL + "/episodes",
		Headers: map[string]string{"Authorization": "Bearer bgm", "Content-Type": "application/json"},
		Body:    `{"type":2}`,
	})
	if err != nil {
		t.Fatalf("standalone FetchRequest: %v", err)
	}
	if resp.Status != 404 || resp.OK || resp.Body != `{"title":"Not Found"}` || resp.Headers["x-bgm"] != "v0" {
		t.Fatalf("standalone resp = %+v", resp)
	}
	if _, err := sb.FetchRequest(context.Background(), FetchRequest{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Headers: map[string]string{"Cookie": "a=b"},
	}); err == nil || !strings.Contains(err.Error(), "Cookie") {
		t.Fatalf("Cookie header err = %v", err)
	}
}
